//go:build !linux

package main

import (
	"fmt"
	"net/netip"
)

func runNative(options, netip.Prefix) error {
	return fmt.Errorf("native TUN transport requires Linux")
}
