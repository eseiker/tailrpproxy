package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/eseiker/tailrpproxy/internal/rpproxy"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

const authURLLogPrefix = "To start this tsnet server, restart with TS_AUTHKEY set, or go to:"

type tsnetMode struct {
	name        string
	server      *tsnet.Server
	routes      []netip.Prefix
	interactive bool
	onUpError   func(*local.Client)
	afterUp     func(context.Context, *ipnstate.Status) error
}

func runTSNet(ctx context.Context, configuration options, route netip.Prefix) error {
	authKey := strings.TrimSpace(os.Getenv("TS_AUTHKEY"))
	if authKey == "" {
		authKey = strings.TrimSpace(os.Getenv("TS_AUTH_KEY"))
	}
	server := newTSNetServer(configuration, authKey)
	server.AdvertiseTags = strings.FieldsFunc(configuration.tsnetTags, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	return runTSNetReflector(ctx, configuration, route, tsnetMode{
		name:        "tsnet subnet",
		server:      server,
		routes:      []netip.Prefix{route},
		interactive: !hasTSNetCredentials(os.Getenv),
	})
}

// newTSNetServer applies settings shared by standalone and Operator nodes.
// Callers opt into mode-specific tags, state stores, and control servers.
func newTSNetServer(configuration options, authKey string) *tsnet.Server {
	return &tsnet.Server{
		Dir:      configuration.tsnetStateDir,
		Hostname: configuration.tsnetHostname,
		AuthKey:  authKey,
	}
}

func runTSNetReflector(ctx context.Context, configuration options, route netip.Prefix, mode tsnetMode) error {
	if !slices.Contains(mode.routes, route) {
		return fmt.Errorf("%s does not advertise required route %s", mode.name, route)
	}
	mode.server.UserLogf = logAuthURLOnce(mode.server.UserLogf)
	reflector, err := rpproxy.NewTCPReflector(
		mode.server.Dial,
		route,
		!configuration.allowNonTailnetSource,
		streamConfig(configuration),
		log.Printf,
	)
	if err != nil {
		return err
	}
	deregister := registerSubnetTCPReflector(mode.server, reflector)
	defer deregister()
	defer mode.server.Close()

	healthServer, err := startHealthServer(configuration.healthListen, reflector.Metrics())
	if err != nil {
		return err
	}
	defer shutdownHealthServer(healthServer)

	startupContext, cancelStartup := tsnetUpContext(
		ctx,
		configuration.tsnetStartupTimeout,
		mode.interactive,
	)
	defer cancelStartup()
	if mode.interactive {
		log.Printf("No tsnet credentials configured; waiting for interactive login. Open the Tailscale auth URL printed below or press Ctrl+C to cancel.")
	}

	localClient, err := mode.server.LocalClient()
	if err != nil {
		return fmt.Errorf("start tsnet: %w", err)
	}
	status, err := mode.server.Up(startupContext)
	cancelStartup()
	if err != nil {
		if mode.onUpError != nil {
			mode.onUpError(localClient)
		}
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("bring up %s node: %w", mode.name, err)
	}
	if status.Self == nil {
		return errors.New("tsnet reached Running without self status")
	}
	configureContext, cancelConfigure := context.WithTimeout(ctx, configuration.tsnetStartupTimeout)
	defer cancelConfigure()
	if _, err := localClient.EditPrefs(configureContext, &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			AdvertiseRoutes: mode.routes,
		},
		AdvertiseRoutesSet: true,
	}); err != nil {
		return fmt.Errorf("advertise %s routes: %w", mode.name, err)
	}
	if mode.afterUp != nil {
		if err := mode.afterUp(configureContext, status); err != nil {
			return err
		}
	}

	reflector.Metrics().SetReady(true)
	defer reflector.Metrics().SetReady(false)
	log.Printf(
		"RPPairing %s reflector ready: hostname=%s ips=%v route=%s",
		mode.name,
		mode.server.Hostname,
		status.TailscaleIPs,
		route,
	)
	<-ctx.Done()
	deregister()
	_ = mode.server.Close()

	drainContext, cancelDrain := context.WithTimeout(context.Background(), configuration.shutdownTimeout)
	defer cancelDrain()
	if err := reflector.Shutdown(drainContext); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("RPPairing %s reflector drain: %v", mode.name, err)
	}
	return nil
}

func logAuthURLOnce(logf func(string, ...any)) func(string, ...any) {
	if logf == nil {
		logf = log.Printf
	}
	var once sync.Once
	return func(format string, args ...any) {
		if strings.HasPrefix(format, authURLLogPrefix) {
			once.Do(func() { logf(format, args...) })
			return
		}
		logf(format, args...)
	}
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
