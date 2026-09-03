//go:build !linux

package main

import (
	"context"
	"fmt"
	"net/netip"
)

func runOperator(context.Context, options, netip.Prefix) error {
	return fmt.Errorf("operator transport requires Linux")
}
