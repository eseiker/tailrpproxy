package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eseiker/tailrpproxy/internal/rpproxy"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

func runTSNet(configuration options, route netip.Prefix) error {
	authKey := firstNonEmpty(os.Getenv("TS_AUTHKEY"), os.Getenv("TS_AUTH_KEY"))
	server := &tsnet.Server{
		Dir:           configuration.tsnetStateDir,
		Hostname:      configuration.tsnetHostname,
		AuthKey:       authKey,
		AdvertiseTags: splitCommaList(configuration.tsnetTags),
		UserLogf:      log.Printf,
	}
	reflector, err := rpproxy.NewTCPReflector(
		server.Dial,
		route,
		!configuration.allowNonTailnetSource,
		streamConfig(configuration),
		log.Printf,
	)
	if err != nil {
		return err
	}
	deregister := registerSubnetTCPReflector(server, reflector)
	defer deregister()
	defer server.Close()

	healthServer, err := startHealthServer(configuration.healthListen, reflector.Metrics())
	if err != nil {
		return err
	}
	defer healthServer.Close()

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	interactiveLogin := !hasTSNetCredentials(os.Getenv)
	startupContext, cancelStartup := tsnetUpContext(
		signalContext,
		configuration.tsnetStartupTimeout,
		interactiveLogin,
	)
	defer cancelStartup()
	if interactiveLogin {
		log.Printf("No tsnet credentials configured; waiting for interactive login. Open the Tailscale auth URL printed below or press Ctrl+C to cancel.")
	}

	localClient, err := server.LocalClient()
	if err != nil {
		return fmt.Errorf("start tsnet: %w", err)
	}
	status, err := server.Up(startupContext)
	cancelStartup()
	if err != nil {
		if signalContext.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("bring up tsnet node: %w", err)
	}
	if status.Self == nil {
		return errors.New("tsnet reached Running without self status")
	}
	configureContext, cancelConfigure := context.WithTimeout(signalContext, configuration.tsnetStartupTimeout)
	defer cancelConfigure()
	if _, err := localClient.EditPrefs(configureContext, &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			AdvertiseRoutes: []netip.Prefix{route},
		},
		AdvertiseRoutesSet: true,
	}); err != nil {
		return fmt.Errorf("advertise tsnet route %s: %w", route, err)
	}

	reflector.Metrics().SetReady(true)
	log.Printf(
		"RPPairing tsnet subnet reflector ready: hostname=%s ips=%v route=%s",
		configuration.tsnetHostname,
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
		log.Printf("RPPairing TCP reflector drain: %v", err)
	}
	shutdownHealthServer(healthServer)
	return nil
}

func tsnetUpContext(parent context.Context, timeout time.Duration, interactive bool) (context.Context, context.CancelFunc) {
	if interactive {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

// registerSubnetTCPReflector isolates the tsnet interception API from the
// transport-neutral reflector API used by this project.
func registerSubnetTCPReflector(server *tsnet.Server, reflector *rpproxy.TCPReflector) func() {
	deregister := server.RegisterFallbackTCPHandler(reflector.HandleTCPFlow)
	var once sync.Once
	return func() { once.Do(deregister) }
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
