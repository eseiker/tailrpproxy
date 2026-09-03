package rpproxy

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// PacketReflector rewrites packets sent to one synthetic IPv4 address so the
// kernel routes them back to their original source.
type PacketReflector struct {
	target         netip.Addr
	requireTailnet bool
	metrics        Metrics
}

func NewPacketReflector(route netip.Prefix, requireTailnet bool) (*PacketReflector, error) {
	if !route.IsValid() || !route.IsSingleIP() || !route.Addr().Is4() {
		return nil, fmt.Errorf("packet reflector route must be a single IPv4 prefix, got %q", route)
	}
	return &PacketReflector{target: route.Masked().Addr(), requireTailnet: requireTailnet}, nil
}

func (reflector *PacketReflector) Metrics() *Metrics {
	return &reflector.metrics
}

// Reflect swaps the IPv4 source and destination in place. IP and transport
// checksums remain valid because both checksum inputs are order-independent.
func (reflector *PacketReflector) Reflect(packet []byte) bool {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		reflector.metrics.rejected.Add(1)
		return false
	}
	headerLength := int(packet[0]&0x0f) * 4
	totalLength := int(binary.BigEndian.Uint16(packet[2:4]))
	if headerLength < 20 || totalLength < headerLength || totalLength > len(packet) {
		reflector.metrics.rejected.Add(1)
		return false
	}
	destination := netip.AddrFrom4([4]byte(packet[16:20]))
	if destination != reflector.target {
		reflector.metrics.rejected.Add(1)
		return false
	}
	source := netip.AddrFrom4([4]byte(packet[12:16]))
	if reflector.requireTailnet && !IsTailnetAddress(source.String()) {
		reflector.metrics.rejected.Add(1)
		return false
	}

	var sourceBytes [4]byte
	copy(sourceBytes[:], packet[12:16])
	copy(packet[12:16], packet[16:20])
	copy(packet[16:20], sourceBytes[:])
	reflector.metrics.packetsReflected.Add(1)
	reflector.metrics.bytesReflected.Add(uint64(totalLength))
	return true
}
