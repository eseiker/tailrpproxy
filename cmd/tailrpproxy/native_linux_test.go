//go:build linux

package main

import (
	"net/netip"
	"testing"

	"tailscale.com/ipn"
	"tailscale.com/types/opt"
)

func TestNativeRoutePrefsConfiguresDedicatedRoute(t *testing.T) {
	configured, err := nativeRoutePrefs(&ipn.Prefs{}, netip.MustParsePrefix("10.7.0.1/32"))
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.AdvertiseRoutes) != 1 || configured.AdvertiseRoutes[0].String() != "10.7.0.1/32" {
		t.Fatalf("routes = %v", configured.AdvertiseRoutes)
	}
	if !configured.NoSNAT || configured.NoStatefulFiltering != opt.True {
		t.Fatalf("unsafe subnet preferences: %+v", configured.Prefs)
	}
}

func TestNativeTUNLocalAddress(t *testing.T) {
	address, err := nativeTUNLocalAddress(netip.MustParsePrefix("10.7.0.1/32"))
	if err != nil {
		t.Fatal(err)
	}
	if got := address.String(); got != "10.7.0.0" {
		t.Fatalf("local address = %s, want 10.7.0.0", got)
	}
	if _, err := nativeTUNLocalAddress(netip.MustParsePrefix("0.0.0.0/32")); err == nil {
		t.Fatal("zero route did not return an error")
	}
}

func TestNativeRoutePrefsPreservesCompatibleRoutes(t *testing.T) {
	configured, err := nativeRoutePrefs(&ipn.Prefs{
		AdvertiseRoutes:     []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		NoSNAT:              true,
		NoStatefulFiltering: opt.True,
	}, netip.MustParsePrefix("10.7.0.1/32"))
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.AdvertiseRoutes) != 2 {
		t.Fatalf("routes = %v, want existing and synthetic routes", configured.AdvertiseRoutes)
	}
}

func TestNativeRoutePrefsRejectsGlobalPolicyChanges(t *testing.T) {
	route := netip.MustParsePrefix("10.7.0.1/32")
	otherRoute := netip.MustParsePrefix("192.0.2.0/24")
	for _, prefs := range []*ipn.Prefs{
		{AdvertiseRoutes: []netip.Prefix{otherRoute}},
		{AdvertiseRoutes: []netip.Prefix{otherRoute}, NoSNAT: true, NoStatefulFiltering: opt.False},
	} {
		if _, err := nativeRoutePrefs(prefs, route); err == nil {
			t.Fatalf("preferences %+v did not return an error", prefs)
		}
	}
}
