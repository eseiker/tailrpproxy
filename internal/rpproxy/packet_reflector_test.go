package rpproxy

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestPacketReflectorSwapsConfiguredIPv4Route(t *testing.T) {
	reflector, err := NewPacketReflector(netip.MustParsePrefix("10.7.0.1/32"), true)
	if err != nil {
		t.Fatal(err)
	}
	packet := testIPv4Packet([4]byte{100, 100, 4, 3}, [4]byte{10, 7, 0, 1}, []byte("rppairing"))
	originalChecksum := binary.BigEndian.Uint16(packet[10:12])

	if !reflector.Reflect(packet) {
		t.Fatal("packet was not reflected")
	}
	if got := netip.AddrFrom4([4]byte(packet[12:16])); got.String() != "10.7.0.1" {
		t.Fatalf("source = %s, want 10.7.0.1", got)
	}
	if got := netip.AddrFrom4([4]byte(packet[16:20])); got.String() != "100.100.4.3" {
		t.Fatalf("destination = %s, want 100.100.4.3", got)
	}
	if got := binary.BigEndian.Uint16(packet[10:12]); got != originalChecksum {
		t.Fatalf("checksum changed from %#x to %#x", originalChecksum, got)
	}
	snapshot := reflector.Metrics().Snapshot()
	if snapshot.PacketsReflected != 1 || snapshot.BytesReflected != uint64(len(packet)) {
		t.Fatalf("unexpected metrics: %+v", snapshot)
	}
}

func TestPacketReflectorDropsOtherAndMalformedPackets(t *testing.T) {
	reflector, err := NewPacketReflector(netip.MustParsePrefix("10.7.0.1/32"), true)
	if err != nil {
		t.Fatal(err)
	}
	other := testIPv4Packet([4]byte{100, 64, 0, 2}, [4]byte{10, 7, 0, 2}, nil)
	nonTailnet := testIPv4Packet([4]byte{192, 0, 2, 2}, [4]byte{10, 7, 0, 1}, nil)
	for _, packet := range [][]byte{nil, {0x60}, other, nonTailnet} {
		if reflector.Reflect(packet) {
			t.Fatalf("unexpected reflection for %x", packet)
		}
	}
	if got := reflector.Metrics().Snapshot().Rejected; got != 4 {
		t.Fatalf("rejected = %d, want 4", got)
	}
}

func testIPv4Packet(source, destination [4]byte, payload []byte) []byte {
	packet := make([]byte, 20+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = 6
	copy(packet[12:16], source[:])
	copy(packet[16:20], destination[:])
	copy(packet[20:], payload)
	binary.BigEndian.PutUint16(packet[10:12], ipv4HeaderChecksum(packet[:20]))
	return packet
}

func ipv4HeaderChecksum(header []byte) uint16 {
	var sum uint32
	for index := 0; index < len(header); index += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[index : index+2]))
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}
