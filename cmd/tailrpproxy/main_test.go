package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTransportAutoDetectsOperator(t *testing.T) {
	transport, _, err := resolveTransport("auto", "", mapEnvironment(map[string]string{
		"TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR": "/etc/tsconfig",
		"TS_KUBE_SECRET":                       "tailrpproxy-0",
		"TS_AUTHKEY":                           "tskey-auth-ignored",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if transport != "operator" {
		t.Fatalf("transport = %q, want operator", transport)
	}
}

func TestResolveTransportRejectsPartialOperatorEnvironment(t *testing.T) {
	for _, environment := range []map[string]string{
		{"TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR": "/etc/tsconfig"},
		{"TS_KUBE_SECRET": "tailrpproxy-0"},
	} {
		if _, _, err := resolveTransport("auto", "", mapEnvironment(environment)); err == nil {
			t.Fatalf("environment %#v did not return an error", environment)
		}
	}
}

func TestResolveTransportAutoDetectsTSNetCredentials(t *testing.T) {
	for _, variable := range []string{
		"TS_AUTHKEY",
		"TS_AUTH_KEY",
		"TS_CLIENT_SECRET",
	} {
		t.Run(variable, func(t *testing.T) {
			transport, _, err := resolveTransport("auto", "", mapEnvironment(map[string]string{
				variable: "tskey-auth-test",
			}))
			if err != nil {
				t.Fatal(err)
			}
			if transport != "tsnet" {
				t.Fatalf("transport = %q, want tsnet", transport)
			}
		})
	}
}

func TestResolveTransportAutoDetectsPersistedTSNetState(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "tailscaled.state"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}

	transport, _, err := resolveTransport("auto", directory, mapEnvironment(nil))
	if err != nil {
		t.Fatal(err)
	}
	if transport != "tsnet" {
		t.Fatalf("transport = %q, want tsnet", transport)
	}
}

func TestResolveTransportDefaultsToNative(t *testing.T) {
	transport, _, err := resolveTransport("auto", filepath.Join(t.TempDir(), "missing"), mapEnvironment(nil))
	if err != nil {
		t.Fatal(err)
	}
	if transport != "native" {
		t.Fatalf("transport = %q, want native", transport)
	}
}

func TestResolveTransportExplicitOverrideWins(t *testing.T) {
	for _, requested := range []string{"native", "tsnet", "operator"} {
		t.Run(requested, func(t *testing.T) {
			transport, _, err := resolveTransport(requested, "", mapEnvironment(map[string]string{
				"TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR": "/partial/operator/environment",
			}))
			if err != nil {
				t.Fatal(err)
			}
			if transport != requested {
				t.Fatalf("transport = %q, want %q", transport, requested)
			}
		})
	}
}

func TestResolveTransportRejectsUnknownValue(t *testing.T) {
	if _, _, err := resolveTransport("invalid", "", mapEnvironment(nil)); err == nil {
		t.Fatal("expected unsupported transport error")
	}
}

func TestSplitCommaList(t *testing.T) {
	values := splitCommaList("tag:one, tag:two,,")
	if len(values) != 2 || values[0] != "tag:one" || values[1] != "tag:two" {
		t.Fatalf("values = %#v", values)
	}
}

func TestParseSyntheticRoute(t *testing.T) {
	route, err := parseSyntheticRoute("10.7.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	if got := route.String(); got != "10.7.0.1/32" {
		t.Fatalf("route = %s", got)
	}
	for _, value := range []string{"10.7.0.0/24", "fd7a:115c:a1e0::1/128", "invalid"} {
		if _, err := parseSyntheticRoute(value); err == nil {
			t.Fatalf("route %q did not return an error", value)
		}
	}
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}
