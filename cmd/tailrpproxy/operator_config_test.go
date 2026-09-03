package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tailscale.com/tailcfg"
)

func TestLoadOperatorConfigSelectsHighestCompatibleVersion(t *testing.T) {
	directory := t.TempDir()
	writeOperatorConfig(t, directory, 95, "older")
	writeOperatorConfig(t, directory, int(tailcfg.CurrentCapabilityVersion), "current")
	writeOperatorConfig(t, directory, int(tailcfg.CurrentCapabilityVersion)+1, "future")

	config, err := loadOperatorConfig(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.hostname(); got != "current" {
		t.Fatalf("hostname = %q, want current", got)
	}
}

func TestLoadOperatorConfigRequiresVersionedFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte(`{"version":"alpha0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOperatorConfig(directory); err == nil {
		t.Fatal("expected missing compatible config error")
	}
}

func TestOperatorConfigReadsRelativeAuthKeyFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "authkey"), []byte("tskey-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := `{"version":"alpha0","authKey":"file:authkey"}`
	path := filepath.Join(directory, "cap-95.hujson")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadOperatorConfig(directory)
	if err != nil {
		t.Fatal(err)
	}
	authKey, err := config.authKey()
	if err != nil {
		t.Fatal(err)
	}
	if authKey != "tskey-test" {
		t.Fatalf("auth key = %q", authKey)
	}
}

func TestOperatorTSNetServerDoesNotOverrideIssuedAuthKeyTags(t *testing.T) {
	directory := t.TempDir()
	writeOperatorConfig(t, directory, int(tailcfg.CurrentCapabilityVersion), "operator-node")
	config, err := loadOperatorConfig(directory)
	if err != nil {
		t.Fatal(err)
	}

	server := newOperatorTSNetServer(options{
		tsnetStateDir: directory,
		tsnetTags:     "tag:standalone-must-not-override-operator",
	}, config, nil, "tskey-operator-issued")
	if len(server.AdvertiseTags) != 0 {
		t.Fatalf("AdvertiseTags = %#v, want none so the Operator-issued auth key controls tags", server.AdvertiseTags)
	}
	if server.AuthKey != "tskey-operator-issued" {
		t.Fatalf("AuthKey = %q, want Operator-issued key", server.AuthKey)
	}
}

func writeOperatorConfig(t *testing.T, directory string, version int, hostname string) {
	t.Helper()
	contents := fmt.Sprintf(`{"version":"alpha0","hostname":%q}`, hostname)
	path := filepath.Join(directory, fmt.Sprintf("cap-%d.hujson", version))
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
