//go:build !linux

package main

import (
	"context"
	"fmt"
	"net/netip"
)

func runNative(context.Context, options, netip.Prefix) error {
	return fmt.Errorf("native TUN transport requires Linux")
}
