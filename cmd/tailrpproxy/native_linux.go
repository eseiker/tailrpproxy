//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"

	"github.com/eseiker/tailrpproxy/internal/rpproxy"
	"github.com/jsimonetti/rtnetlink"
	"github.com/jsimonetti/rtnetlink/rtnl"
	"github.com/tailscale/wireguard-go/tun"
	"golang.org/x/sys/unix"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/types/opt"
)

func runNative(ctx context.Context, configuration options, route netip.Prefix) error {
	if err := requireIPv4Forwarding(); err != nil {
		return err
	}
	reflector, err := rpproxy.NewPacketReflector(route, !configuration.allowNonTailnetSource)
	if err != nil {
		return err
	}
	device, interfaceName, localAddress, err := createNativeTUN(
		configuration.nativeTUNName,
		configuration.nativeTUNMTU,
		route,
	)
	if err != nil {
		return err
	}
	defer device.Close()

	startupContext, cancelStartup := context.WithTimeout(context.Background(), configuration.tsnetStartupTimeout)
	defer cancelStartup()
	if err := configureHostTailscale(startupContext, configuration.tailscaledSocket, route); err != nil {
		return err
	}

	healthServer, err := startHealthServer(configuration.healthListen, reflector.Metrics())
	if err != nil {
		return err
	}
	defer shutdownHealthServer(healthServer)

	serveError := make(chan error, 1)
	go func() { serveError <- servePacketReflector(device, reflector) }()
	metrics := reflector.Metrics()
	metrics.SetReady(true)
	defer metrics.SetReady(false)
	log.Printf(
		"RPPairing native TUN reflector ready: interface=%s local=%s peer=%s route=%s",
		interfaceName,
		localAddress,
		route.Addr(),
		route,
	)

	select {
	case err := <-serveError:
		return err
	case <-ctx.Done():
	}

	_ = device.Close()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), configuration.shutdownTimeout)
	defer cancelShutdown()
	select {
	case <-serveError:
	case <-shutdownContext.Done():
		log.Printf("native TUN reflector shutdown timed out: %v", shutdownContext.Err())
	}
	return nil
}

func requireIPv4Forwarding() error {
	value, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return fmt.Errorf("read net.ipv4.ip_forward: %w", err)
	}
	if strings.TrimSpace(string(value)) != "1" {
		return errors.New("native mode requires net.ipv4.ip_forward=1")
	}
	return nil
}

func createNativeTUN(name string, mtu int, route netip.Prefix) (tun.Device, string, netip.Addr, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", netip.Addr{}, errors.New("native TUN name is empty")
	}
	if mtu < 576 || mtu > 65535 {
		return nil, "", netip.Addr{}, fmt.Errorf("native TUN MTU must be between 576 and 65535, got %d", mtu)
	}
	localAddress, err := nativeTUNLocalAddress(route)
	if err != nil {
		return nil, "", netip.Addr{}, err
	}

	device, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, "", netip.Addr{}, fmt.Errorf("create TUN %q: %w", name, err)
	}
	closeOnError := func(err error) (tun.Device, string, netip.Addr, error) {
		_ = device.Close()
		return nil, "", netip.Addr{}, err
	}
	actualName, err := device.Name()
	if err != nil {
		return closeOnError(fmt.Errorf("read TUN name: %w", err))
	}
	if err := configureNativeInterface(actualName, localAddress, route.Addr()); err != nil {
		return closeOnError(err)
	}
	return device, actualName, localAddress, nil
}

func nativeTUNLocalAddress(route netip.Prefix) (netip.Addr, error) {
	if address := route.Addr().Prev(); address.Is4() {
		return address, nil
	}
	return netip.Addr{}, fmt.Errorf("cannot derive native TUN local address from %s", route)
}

func configureNativeInterface(name string, localAddress, peerAddress netip.Addr) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("find TUN %q: %w", name, err)
	}
	connection, err := rtnl.Dial(nil)
	if err != nil {
		return fmt.Errorf("open route netlink: %w", err)
	}
	defer connection.Close()

	localIP := net.IP(localAddress.AsSlice())
	peerIP := net.IP(peerAddress.AsSlice())
	if err := connection.Conn.Address.New(&rtnetlink.AddressMessage{
		Family:       unix.AF_INET,
		PrefixLength: 32,
		Scope:        unix.RT_SCOPE_UNIVERSE,
		Index:        uint32(iface.Index),
		Attributes: &rtnetlink.AddressAttributes{
			Local:   localIP,
			Address: peerIP,
		},
	}); err != nil {
		return fmt.Errorf("configure TUN addresses %s peer %s: %w", localAddress, peerAddress, err)
	}
	if err := connection.LinkUp(iface); err != nil {
		return fmt.Errorf("bring TUN %q up: %w", name, err)
	}
	destination := net.IPNet{IP: peerIP, Mask: net.CIDRMask(32, 32)}
	if err := connection.RouteReplace(iface, destination, nil); err != nil {
		return fmt.Errorf("route %s through TUN %q: %w", destination.String(), name, err)
	}
	return nil
}

func configureHostTailscale(ctx context.Context, socket string, route netip.Prefix) error {
	client := &local.Client{Socket: strings.TrimSpace(socket)}
	status, err := client.Status(ctx)
	if err != nil {
		return fmt.Errorf("connect to host tailscaled LocalAPI: %w", err)
	}
	if status.BackendState != ipn.Running.String() {
		return fmt.Errorf("host tailscaled state is %q, want %q", status.BackendState, ipn.Running.String())
	}
	prefs, err := client.GetPrefs(ctx)
	if err != nil {
		return fmt.Errorf("read host tailscaled preferences: %w", err)
	}
	maskedPrefs, err := nativeRoutePrefs(prefs, route)
	if err != nil {
		return err
	}
	if _, err := client.EditPrefs(ctx, maskedPrefs); err != nil {
		return fmt.Errorf("configure host tailscaled route %s: %w", route, err)
	}
	log.Printf("host tailscaled advertises %s with subnet-route SNAT and stateful filtering disabled", route)
	return nil
}

func nativeRoutePrefs(prefs *ipn.Prefs, route netip.Prefix) (*ipn.MaskedPrefs, error) {
	if prefs == nil {
		return nil, errors.New("host tailscaled returned nil preferences")
	}
	routes := slices.Clone(prefs.AdvertiseRoutes)
	if !slices.Contains(routes, route) {
		routes = append(routes, route)
	}
	hasOtherRoutes := slices.ContainsFunc(routes, func(advertised netip.Prefix) bool { return advertised != route })
	if hasOtherRoutes && (!prefs.NoSNAT || prefs.NoStatefulFiltering.EqualBool(false)) {
		return nil, errors.New("native mode cannot change subnet routing policy while host tailscaled advertises other routes; configure --snat-subnet-routes=false and --stateful-filtering=false explicitly")
	}
	return &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			AdvertiseRoutes:     routes,
			NoSNAT:              true,
			NoStatefulFiltering: opt.True,
		},
		AdvertiseRoutesSet:     true,
		NoSNATSet:              true,
		NoStatefulFilteringSet: true,
	}, nil
}

func servePacketReflector(device tun.Device, reflector *rpproxy.PacketReflector) error {
	batchSize := max(device.BatchSize(), 1)
	slab := make([]byte, batchSize*(65535+tun.ReadPacketSpacing)+tun.ReadPacketSpacing)
	packets := make([]tun.ReadPacket, batchSize)
	reflected := make([][]byte, 0, batchSize)
	for {
		count, readErr := device.Read(slab, packets)
		reflected = reflected[:0]
		for _, packet := range packets[:count] {
			if packet.Offset < tun.ReadPacketSpacing || packet.Offset > len(slab) || packet.Size <= 0 || packet.Size > len(slab)-packet.Offset {
				continue
			}
			buffer := slab[packet.Offset : packet.Offset+packet.Size]
			if reflector.Reflect(buffer) {
				end := packet.Offset + packet.Size
				reflected = append(reflected, slab[packet.Offset-tun.ReadPacketSpacing:end:end])
			}
		}
		if len(reflected) > 0 {
			if _, err := device.Write(reflected, tun.ReadPacketSpacing); err != nil {
				return fmt.Errorf("write reflected packet to native TUN: %w", err)
			}
		}
		if readErr != nil {
			return fmt.Errorf("read native TUN: %w", readErr)
		}
	}
}
