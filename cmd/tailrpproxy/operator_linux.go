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
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/ipn/store/kubestore"
	"tailscale.com/kube/authkey"
	"tailscale.com/kube/kubeapi"
	"tailscale.com/kube/kubeclient"
	"tailscale.com/kube/kubetypes"
	"tailscale.com/tailcfg"
)

const operatorFieldManager = "tailrpproxy"

func runOperator(ctx context.Context, configuration options, route netip.Prefix) error {
	config, err := loadOperatorConfig(os.Getenv("TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR"))
	if err != nil {
		return err
	}
	authKey, err := operatorAuthKey(config)
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
	if err := resetForReissuedAuthKey(ctx, kubeClient, stateSecret, authKey); err != nil {
		return err
	}
	stateStore, err := kubestore.New(log.Printf, stateSecret)
	if err != nil {
		return fmt.Errorf("create Kubernetes state store: %w", err)
	}

	server := newTSNetServer(configuration, authKey)
	server.Store = stateStore
	server.Hostname = optionalString(config.Parsed.Hostname)
	server.ControlURL = optionalString(config.Parsed.ServerURL)
	return runTSNetReflector(ctx, configuration, route, tsnetMode{
		name:   "Operator",
		server: server,
		routes: config.Parsed.AdvertiseRoutes,
		onUpError: func(client *local.Client) {
			requestAuthKeyReissueIfNeeded(kubeClient, stateSecret, authKey, client)
		},
		afterUp: func(ctx context.Context, status *ipnstate.Status) error {
			if err := storeDeviceID(ctx, kubeClient, stateSecret, status.Self.ID); err != nil {
				return err
			}
			return storeDeviceEndpoints(ctx, kubeClient, stateSecret, status)
		},
	})
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
