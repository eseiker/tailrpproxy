package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"tailscale.com/ipn"
	"tailscale.com/ipn/conffile"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

type operatorConfig struct {
	Path   string
	Config *conffile.Config
}

func loadOperatorConfig(directory string) (*operatorConfig, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR is required in operator mode")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read Operator config directory %q: %w", directory, err)
	}

	bestVersion := -1
	bestPath := ""
	for _, entry := range entries {
		version, ok := capabilityVersionFromName(entry.Name())
		if !ok || version > int(tailcfg.CurrentCapabilityVersion) || version <= bestVersion {
			continue
		}
		bestVersion = version
		bestPath = filepath.Join(directory, entry.Name())
	}
	if bestPath == "" {
		return nil, fmt.Errorf("no compatible cap-<version>.hujson found in %q for capability version %d", directory, tailcfg.CurrentCapabilityVersion)
	}

	config, err := conffile.Load(bestPath)
	if err != nil {
		return nil, fmt.Errorf("load Operator config %q: %w", bestPath, err)
	}
	return &operatorConfig{Path: bestPath, Config: config}, nil
}

func capabilityVersionFromName(name string) (int, bool) {
	if !strings.HasPrefix(name, "cap-") || !strings.HasSuffix(name, ".hujson") {
		return 0, false
	}
	text := strings.TrimSuffix(strings.TrimPrefix(name, "cap-"), ".hujson")
	version, err := strconv.Atoi(text)
	return version, err == nil && version >= 0
}

func (config *operatorConfig) authKey() (string, error) {
	if config.Config.Parsed.AuthKey == nil {
		return "", nil
	}
	value := strings.TrimSpace(*config.Config.Parsed.AuthKey)
	if !strings.HasPrefix(value, "file:") {
		return value, nil
	}
	path := strings.TrimPrefix(value, "file:")
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(config.Path), path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read auth key file %q: %w", path, err)
	}
	return strings.TrimSpace(string(contents)), nil
}

func (config *operatorConfig) hostname() string {
	if config.Config.Parsed.Hostname == nil {
		return ""
	}
	return *config.Config.Parsed.Hostname
}

func (config *operatorConfig) controlURL() string {
	if config.Config.Parsed.ServerURL == nil {
		return ""
	}
	return *config.Config.Parsed.ServerURL
}

func newOperatorTSNetServer(configuration options, config *operatorConfig, store ipn.StateStore, authKey string) *tsnet.Server {
	return &tsnet.Server{
		Dir:        configuration.tsnetStateDir,
		Store:      store,
		Hostname:   config.hostname(),
		AuthKey:    authKey,
		ControlURL: config.controlURL(),
		UserLogf:   log.Printf,
		// The Operator already encoded Connector.spec.tags, or its defaultTags
		// fallback, into this one-time auth key. Standalone tag settings must not
		// replace that choice.
		AdvertiseTags: nil,
	}
}
