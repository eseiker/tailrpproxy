package rpproxy

import "net/netip"

var (
	tailscaleIPv4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleIPv6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

func IsTailnetAddress(host string) bool {
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	address = address.Unmap().WithZone("")
	return tailscaleIPv4.Contains(address) || tailscaleIPv6.Contains(address)
}
