package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/eseiker/tailrpproxy/internal/rpproxy"
	"tailscale.com/paths"
	tsversion "tailscale.com/version"
)

const nativeTUNPath = "/dev/net/tun"

type nativePrerequisiteProbe func(string) (bool, string)
type fileModeProbe func(string) (os.FileMode, error)

type options struct {
	transport             string
	allowNonTailnetSource bool
	dialTimeout           time.Duration
	streamTimeout         time.Duration
	shutdownTimeout       time.Duration
	maxConnections        int
	maxPerPeer            int
	healthListen          string
	syntheticRoute        string
	tsnetStateDir         string
	tsnetHostname         string
	tsnetTags             string
	tsnetStartupTimeout   time.Duration
	nativeTUNName         string
	nativeTUNMTU          int
	tailscaledSocket      string
	printVersion          bool
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	configuration := parseFlags()
	if configuration.printVersion {
		fmt.Printf("tailrpproxy %s\n", tsversion.Long())
		return nil
	}
	selectedTransport, selectionReason, err := resolveTransport(
		configuration.transport,
		configuration.tsnetStateDir,
		configuration.tailscaledSocket,
		os.Getenv,
		nativePrerequisites,
	)
	if err != nil {
		return err
	}
	configuration.transport = selectedTransport
	log.Printf("transport selected: %s (%s)", selectedTransport, selectionReason)

	route, err := parseSyntheticRoute(configuration.syntheticRoute)
	if err != nil {
		return err
	}
	switch configuration.transport {
	case "native":
		return runNative(configuration, route)
	case "tsnet":
		return runTSNet(configuration, route)
	case "operator":
		return runOperator(configuration, route)
	default:
		return fmt.Errorf("unsupported -transport %q", configuration.transport)
	}
}

func parseFlags() options {
	var configuration options
	flag.StringVar(&configuration.transport, "transport", envOrDefault("RPPROXY_TRANSPORT", "auto"), "tailnet transport: auto, native, tsnet, or operator")
	flag.BoolVar(&configuration.allowNonTailnetSource, "allow-non-tailnet-source", false, "allow reflection for source addresses outside the Tailscale ranges")
	flag.DurationVar(&configuration.dialTimeout, "dial-timeout", 10*time.Second, "reflected TCP dial timeout")
	flag.DurationVar(&configuration.streamTimeout, "stream-timeout", 0, "optional reflected TCP stream lifetime; zero disables it")
	flag.DurationVar(&configuration.shutdownTimeout, "shutdown-timeout", 15*time.Second, "graceful shutdown timeout")
	flag.IntVar(&configuration.maxConnections, "max-connections", 64, "maximum concurrent reflected TCP streams")
	flag.IntVar(&configuration.maxPerPeer, "max-connections-per-peer", 8, "maximum concurrent reflected TCP streams per source peer")
	flag.StringVar(&configuration.healthListen, "health-listen", envOrDefault("RPPROXY_HEALTH_LISTEN", "127.0.0.1:9090"), "health and metrics listen address; empty disables it")
	flag.StringVar(&configuration.syntheticRoute, "synthetic-route", envOrDefault("RPPROXY_SYNTHETIC_ROUTE", "10.7.0.1/32"), "single IPv4 route reflected to the source peer")
	flag.StringVar(&configuration.tsnetStateDir, "tsnet-state-dir", strings.TrimSpace(os.Getenv("RPPROXY_TSNET_STATE_DIR")), "persistent tsnet state directory")
	flag.StringVar(&configuration.tsnetHostname, "tsnet-hostname", envOrDefault("RPPROXY_TSNET_HOSTNAME", "tailrpproxy"), "tsnet machine hostname")
	flag.StringVar(&configuration.tsnetTags, "tsnet-tags", strings.TrimSpace(os.Getenv("RPPROXY_TSNET_TAGS")), "comma-separated tsnet tags")
	flag.DurationVar(&configuration.tsnetStartupTimeout, "tsnet-startup-timeout", 45*time.Second, "Tailscale startup and configuration timeout")
	flag.StringVar(&configuration.nativeTUNName, "native-tun-name", envOrDefault("RPPROXY_NATIVE_TUN_NAME", "tailrpproxy"), "Linux TUN interface name for native mode")
	flag.IntVar(&configuration.nativeTUNMTU, "native-tun-mtu", 1500, "Linux TUN interface MTU for native mode")
	flag.StringVar(&configuration.tailscaledSocket, "tailscaled-socket", strings.TrimSpace(os.Getenv("RPPROXY_TAILSCALED_SOCKET")), "host tailscaled LocalAPI socket path")
	flag.BoolVar(&configuration.printVersion, "version", false, "print version information and exit")
	flag.Parse()
	return configuration
}

func resolveTransport(
	requested,
	tsnetStateDir,
	tailscaledSocket string,
	getenv func(string) string,
	probeNative nativePrerequisiteProbe,
) (string, string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		requested = "auto"
	}

	switch requested {
	case "native", "tsnet", "operator":
		return requested, "explicit configuration", nil
	case "auto":
	default:
		return "", "", fmt.Errorf("unsupported -transport %q", requested)
	}

	operatorConfig := strings.TrimSpace(getenv("TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR")) != ""
	operatorSecret := strings.TrimSpace(getenv("TS_KUBE_SECRET")) != ""
	if operatorConfig != operatorSecret {
		return "", "", errors.New("incomplete Tailscale Operator environment: TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR and TS_KUBE_SECRET must be set together")
	}
	if operatorConfig {
		return "operator", "Tailscale Operator config and state Secret detected", nil
	}

	if hasTSNetCredentials(getenv) {
		return "tsnet", "tsnet credentials detected", nil
	}
	stateExists, err := tsnetStateExists(tsnetStateDir)
	if err != nil {
		return "", "", err
	}
	if stateExists {
		return "tsnet", "persisted tsnet state detected", nil
	}

	if available, reason := probeNative(tailscaledSocket); !available {
		return "tsnet", "native transport unavailable: " + reason, nil
	}
	return "native", "native TUN and tailscaled socket detected", nil
}

func nativePrerequisites(tailscaledSocket string) (bool, string) {
	return probeNativePrerequisites(
		runtime.GOOS,
		nativeTUNPath,
		effectiveTailscaledSocket(tailscaledSocket),
		func(path string) (os.FileMode, error) {
			info, err := os.Stat(path)
			if err != nil {
				return 0, err
			}
			return info.Mode(), nil
		},
	)
}

func probeNativePrerequisites(
	goos,
	tunPath,
	tailscaledSocket string,
	statMode fileModeProbe,
) (bool, string) {
	if goos != "linux" {
		return false, "native transport requires Linux"
	}
	tunMode, err := statMode(tunPath)
	if err != nil {
		return false, fmt.Sprintf("TUN device %s is unavailable: %v", tunPath, err)
	}
	if tunMode&os.ModeDevice == 0 || tunMode&os.ModeCharDevice == 0 {
		return false, fmt.Sprintf("TUN path %s is not a character device", tunPath)
	}

	tailscaledSocket = strings.TrimSpace(tailscaledSocket)
	if tailscaledSocket == "" {
		return false, "tailscaled has no default LocalAPI socket path"
	}
	socketMode, err := statMode(tailscaledSocket)
	if err != nil {
		return false, fmt.Sprintf("tailscaled socket %s is unavailable: %v", tailscaledSocket, err)
	}
	if socketMode&os.ModeSocket == 0 {
		return false, fmt.Sprintf("tailscaled path %s is not a Unix socket", tailscaledSocket)
	}
	return true, "native prerequisites detected"
}

func effectiveTailscaledSocket(configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	return paths.DefaultTailscaledSocket()
}

func hasTSNetCredentials(getenv func(string) string) bool {
	for _, name := range []string{
		"TS_AUTHKEY",
		"TS_AUTH_KEY",
		"TS_CLIENT_SECRET",
	} {
		if strings.TrimSpace(getenv(name)) != "" {
			return true
		}
	}
	return false
}

func tsnetStateExists(directory string) (bool, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return false, nil
	}
	_, err := os.Stat(filepath.Join(directory, "tailscaled.state"))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect tsnet state directory %q: %w", directory, err)
}

func parseSyntheticRoute(value string) (netip.Prefix, error) {
	route, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !route.IsSingleIP() || !route.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("-synthetic-route must be a single IPv4 prefix: %q", value)
	}
	return route.Masked(), nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func streamConfig(configuration options) rpproxy.Config {
	return rpproxy.Config{
		DialTimeout:           configuration.dialTimeout,
		StreamTimeout:         configuration.streamTimeout,
		MaxConnections:        configuration.maxConnections,
		MaxConnectionsPerPeer: configuration.maxPerPeer,
	}
}

func startHealthServer(address string, metrics *rpproxy.Metrics) (*http.Server, error) {
	server := &http.Server{Addr: address, ReadHeaderTimeout: 2 * time.Second}
	if strings.TrimSpace(address) == "" {
		return server, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		if !metrics.Snapshot().Ready {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ready\n"))
	})
	mux.HandleFunc("/metrics", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = writer.Write([]byte(metrics.Prometheus()))
	})
	server.Handler = mux

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("health listen: %w", err)
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("health server: %v", err)
		}
	}()
	return server, nil
}

func shutdownHealthServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func splitCommaList(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			values = append(values, item)
		}
	}
	return values
}
