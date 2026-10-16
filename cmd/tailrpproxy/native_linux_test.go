//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"testing"

	"github.com/eseiker/tailrpproxy/internal/rpproxy"
	"github.com/tailscale/wireguard-go/tun"
	"tailscale.com/ipn"
	"tailscale.com/types/opt"
)

type packetReflectorTUN struct {
	tun.Device
	batchSize int
	read      func([]byte, []tun.ReadPacket) (int, error)
	write     func([][]byte, int) (int, error)
}

func (device *packetReflectorTUN) BatchSize() int { return device.batchSize }

func (device *packetReflectorTUN) Read(slab []byte, packets []tun.ReadPacket) (int, error) {
	return device.read(slab, packets)
}

func (device *packetReflectorTUN) Write(buffers [][]byte, offset int) (int, error) {
	return device.write(buffers, offset)
}

func TestServePacketReflector(t *testing.T) {
	for _, batchSize := range []int{0, 1, 3} {
		for _, readErr := range []error{nil, io.EOF} {
			t.Run(fmt.Sprintf("batch=%d/readErr=%v", batchSize, readErr), func(t *testing.T) {
				reflector, err := rpproxy.NewPacketReflector(netip.MustParsePrefix("10.7.0.1/32"), true)
				if err != nil {
					t.Fatal(err)
				}
				packet := make([]byte, 24)
				packet[0] = 0x45
				binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
				copy(packet[12:16], []byte{100, 64, 0, 2})
				copy(packet[16:20], []byte{10, 7, 0, 1})
				expected := bytes.Clone(packet)
				copy(expected[12:16], packet[16:20])
				copy(expected[16:20], packet[12:16])
				reads, writes := 0, 0
				device := &packetReflectorTUN{
					batchSize: batchSize,
					read: func(slab []byte, packets []tun.ReadPacket) (int, error) {
						reads++
						if reads > 1 {
							return 0, io.EOF
						}
						if len(packets) < max(batchSize, 1) || len(slab) < max(batchSize, 1)*(65535+tun.ReadPacketSpacing)+tun.ReadPacketSpacing {
							t.Fatal("insufficient read capacity")
						}
						offset := tun.ReadPacketSpacing
						for index := range packets {
							if index == 1 {
								copy(slab[offset:], []byte{0x60})
								packets[index] = tun.ReadPacket{Offset: offset, Size: 1}
								offset += 1 + tun.ReadPacketSpacing
								continue
							}
							copy(slab[offset:], packet)
							packets[index] = tun.ReadPacket{Offset: offset, Size: len(packet)}
							offset += len(packet) + tun.ReadPacketSpacing
						}
						return len(packets), readErr
					},
					write: func(buffers [][]byte, offset int) (int, error) {
						writes++
						wantCount := max(batchSize, 1)
						if batchSize == 3 {
							wantCount--
						}
						if len(buffers) != wantCount || offset != tun.ReadPacketSpacing {
							t.Fatalf("write count=%d offset=%d", len(buffers), offset)
						}
						for _, buffer := range buffers {
							if cap(buffer) != len(buffer) {
								t.Fatal("write buffer capacity overlaps subsequent packets")
							}
							if !bytes.Equal(buffer[offset:], expected) {
								t.Fatalf("reflected packet = %x, want %x", buffer[offset:], expected)
							}
							clear(buffer[:offset])
						}
						return len(buffers), nil
					},
				}
				if err := servePacketReflector(device, reflector); !errors.Is(err, io.EOF) {
					t.Fatalf("serve error = %v, want EOF", err)
				}
				if writes != 1 {
					t.Fatalf("writes = %d, want 1", writes)
				}
			})
		}
	}
}

func TestServePacketReflectorErrors(t *testing.T) {
	for _, writeError := range []bool{false, true} {
		reflector, err := rpproxy.NewPacketReflector(netip.MustParsePrefix("10.7.0.1/32"), true)
		if err != nil {
			t.Fatal(err)
		}
		failure := errors.New("TUN failure")
		device := &packetReflectorTUN{
			batchSize: 1,
			read: func(slab []byte, packets []tun.ReadPacket) (int, error) {
				if !writeError {
					return 0, failure
				}
				packet := slab[tun.ReadPacketSpacing:][:20]
				packet[0] = 0x45
				binary.BigEndian.PutUint16(packet[2:4], 20)
				copy(packet[12:16], []byte{100, 64, 0, 2})
				copy(packet[16:20], []byte{10, 7, 0, 1})
				packets[0] = tun.ReadPacket{Offset: tun.ReadPacketSpacing, Size: 20}
				return 1, nil
			},
			write: func([][]byte, int) (int, error) {
				if !writeError {
					t.Fatal("unexpected write")
				}
				return 0, failure
			},
		}
		err = servePacketReflector(device, reflector)
		operation := "read native TUN"
		if writeError {
			operation = "write reflected packet to native TUN"
		}
		if !errors.Is(err, failure) || !strings.Contains(err.Error(), operation) {
			t.Fatalf("serve error = %v, want %s failure", err, operation)
		}
	}
}

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
