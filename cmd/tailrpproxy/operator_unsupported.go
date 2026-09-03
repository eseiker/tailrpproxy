//go:build !linux

package main

import (
	"fmt"
	"net/netip"
)

func runOperator(options, netip.Prefix) error {
	return fmt.Errorf("operator transport requires Linux")
}
