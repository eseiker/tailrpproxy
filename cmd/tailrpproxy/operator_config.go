package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"tailscale.com/ipn/conffile"
	"tailscale.com/tailcfg"
)

func loadOperatorConfig(directory string) (*conffile.Config, error) {
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
	return config, nil
}

func capabilityVersionFromName(name string) (int, bool) {
	if !strings.HasPrefix(name, "cap-") || !strings.HasSuffix(name, ".hujson") {
		return 0, false
	}
	text := strings.TrimSuffix(strings.TrimPrefix(name, "cap-"), ".hujson")
	version, err := strconv.Atoi(text)
	return version, err == nil && version >= 0
}

func operatorAuthKey(config *conffile.Config) (string, error) {
	if config.Parsed.AuthKey == nil {
		return "", nil
	}
	value := strings.TrimSpace(*config.Parsed.AuthKey)
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

func optionalString(value *string) string {
	if value != nil {
		return *value
	}
	return ""
}
