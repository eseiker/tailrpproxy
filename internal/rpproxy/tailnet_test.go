package rpproxy

import (
	"net/netip"
	"testing"
)

func TestIsTailnetAddress(t *testing.T) {
	tests := map[string]bool{
		"100.64.0.1":           true,
		"100.127.255.254":      true,
		"fd7a:115c:a1e0::1234": true,
		"192.168.1.1":          false,
		"fd00::1":              false,
	}
	for text, expected := range tests {
		address := netip.MustParseAddr(text)
		if got := IsTailnetAddress(address); got != expected {
			t.Errorf("IsTailnetAddress(%q) = %t, want %t", address, got, expected)
		}
	}
}
