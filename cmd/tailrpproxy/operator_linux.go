//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/eseiker/tailrpproxy/internal/rpproxy"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/ipn/store/kubestore"
	"tailscale.com/kube/authkey"
	"tailscale.com/kube/kubeapi"
	"tailscale.com/kube/kubeclient"
	"tailscale.com/kube/kubetypes"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

const operatorFieldManager = "tailrpproxy"

func runOperator(configuration options, route netip.Prefix) error {
	config, err := loadOperatorConfig(os.Getenv("TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR"))
	if err != nil {
		return err
	}
	if !slices.Contains(config.Config.Parsed.AdvertiseRoutes, route) {
		return fmt.Errorf("Operator config %q does not advertise required route %s", config.Path, route)
	}
	authKey, err := config.authKey()
	if err != nil {
		return err
	}
	stateSecret := strings.TrimSpace(os.Getenv("TS_KUBE_SECRET"))
	if stateSecret == "" {
		return errors.New("TS_KUBE_SECRET is required in operator mode")
	}

	kubeClient, err := kubeclient.New(operatorFieldManager)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}
	if err := resetForReissuedAuthKey(context.Background(), kubeClient, stateSecret, authKey); err != nil {
		return err
	}
	stateStore, err := kubestore.New(log.Printf, stateSecret)
	if err != nil {
		return fmt.Errorf("create Kubernetes state store: %w", err)
	}

	server := &tsnet.Server{
		Dir:        configuration.tsnetStateDir,
		Store:      stateStore,
		Hostname:   config.hostname(),
		AuthKey:    authKey,
		ControlURL: config.controlURL(),
		UserLogf:   log.Printf,
	}
	reflector, err := rpproxy.NewTCPReflector(
		server.Dial,
		route,
		!configuration.allowNonTailnetSource,
		rpproxy.Config{
			DialTimeout:           configuration.dialTimeout,
			StreamTimeout:         configuration.streamTimeout,
			MaxConnections:        configuration.maxConnections,
			MaxConnectionsPerPeer: configuration.maxPerPeer,
		},
		log.Printf,
	)
	if err != nil {
		return err
	}
	deregister := registerSubnetTCPReflector(server, reflector)

	healthServer, err := startHealthServer(configuration.healthListen, reflector.Metrics())
	if err != nil {
		return err
	}
	defer healthServer.Close()

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	startupContext, cancelStartup := context.WithTimeout(signalContext, configuration.tsnetStartupTimeout)
	defer cancelStartup()

	localClient, err := server.LocalClient()
	if err != nil {
		return fmt.Errorf("start tsnet: %w", err)
	}
	status, err := server.Up(startupContext)
	if err != nil {
		requestAuthKeyReissueIfNeeded(kubeClient, stateSecret, authKey, localClient)
		_ = server.Close()
		return fmt.Errorf("bring up Operator tsnet node: %w", err)
	}
	if status.Self == nil {
		_ = server.Close()
		return errors.New("tsnet reached Running without self status")
	}

	if err := storeDeviceID(startupContext, kubeClient, stateSecret, status.Self.ID); err != nil {
		_ = server.Close()
		return err
	}
	maskedPrefs := &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			AdvertiseRoutes: config.Config.Parsed.AdvertiseRoutes,
		},
		AdvertiseRoutesSet: true,
	}
	if _, err := localClient.EditPrefs(startupContext, maskedPrefs); err != nil {
		_ = server.Close()
		return fmt.Errorf("advertise Operator routes: %w", err)
	}
	if err := storeDeviceEndpoints(startupContext, kubeClient, stateSecret, status); err != nil {
		_ = server.Close()
		return err
	}

	reflector.Metrics().SetReady(true)
	log.Printf(
		"RPPairing Operator reflector ready: hostname=%s ips=%v route=%s mode=source-same-port",
		config.hostname(),
		status.TailscaleIPs,
		route,
	)
	<-signalContext.Done()
	reflector.Metrics().SetReady(false)
	deregister()
	_ = server.Close()

	drainContext, cancelDrain := context.WithTimeout(context.Background(), configuration.shutdownTimeout)
	defer cancelDrain()
	if err := reflector.Shutdown(drainContext); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("RPPairing reflector drain: %v", err)
	}
	healthContext, cancelHealth := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelHealth()
	_ = healthServer.Shutdown(healthContext)
	return nil
}

func resetForReissuedAuthKey(ctx context.Context, client kubeclient.Client, secretName, currentAuthKey string) error {
	secret, err := client.GetSecret(ctx, secretName)
	if err != nil {
		return fmt.Errorf("read state Secret %q: %w", secretName, err)
	}
	brokenAuthKey, requested := secret.Data[kubetypes.KeyReissueAuthkey]
	if !requested || currentAuthKey == "" || string(brokenAuthKey) == currentAuthKey {
		return nil
	}
	if err := authkey.ClearReissueAuthKey(ctx, client, secretName, operatorFieldManager); err != nil {
		return fmt.Errorf("accept reissued auth key: %w", err)
	}
	log.Printf("Accepted reissued Operator auth key and cleared stale tsnet identity")
	return nil
}

func requestAuthKeyReissueIfNeeded(client kubeclient.Client, secretName, currentAuthKey string, localClient *local.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := localClient.Status(ctx)
	if err != nil || status.BackendState != ipn.NeedsLogin.String() {
		return
	}
	failedKey := currentAuthKey
	if failedKey == "" {
		failedKey = "no-authkey"
	}
	if err := authkey.SetReissueAuthKey(ctx, client, secretName, failedKey, operatorFieldManager); err != nil {
		log.Printf("Unable to request an Operator auth key reissue: %v", err)
		return
	}
	log.Printf("Requested a new auth key from the Tailscale Operator")
}

func storeDeviceID(ctx context.Context, client kubeclient.Client, secretName string, deviceID tailcfg.StableNodeID) error {
	secret := &kubeapi.Secret{Data: map[string][]byte{
		kubetypes.KeyDeviceID: []byte(deviceID),
		kubetypes.KeyCapVer:   fmt.Appendf(nil, "%d", tailcfg.CurrentCapabilityVersion),
	}}
	if podUID := os.Getenv("POD_UID"); podUID != "" {
		secret.Data[kubetypes.KeyPodUID] = []byte(podUID)
	}
	if err := client.StrategicMergePatchSecret(ctx, secretName, secret, operatorFieldManager); err != nil {
		return fmt.Errorf("store device identity in Secret %q: %w", secretName, err)
	}
	return nil
}

func storeDeviceEndpoints(ctx context.Context, client kubeclient.Client, secretName string, status *ipnstate.Status) error {
	ips := make([]string, 0, len(status.TailscaleIPs))
	for _, address := range status.TailscaleIPs {
		ips = append(ips, address.String())
	}
	encodedIPs, err := json.Marshal(ips)
	if err != nil {
		return fmt.Errorf("encode device IPs: %w", err)
	}
	secret := &kubeapi.Secret{Data: map[string][]byte{
		kubetypes.KeyDeviceFQDN: []byte(status.Self.DNSName),
		kubetypes.KeyDeviceIPs:  encodedIPs,
	}}
	if err := client.StrategicMergePatchSecret(ctx, secretName, secret, operatorFieldManager); err != nil {
		return fmt.Errorf("store device endpoints in Secret %q: %w", secretName, err)
	}
	return nil
}
