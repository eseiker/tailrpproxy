package rpproxy

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestReflectorProxiesToSourceOnOriginalDestinationPort(t *testing.T) {
	var dialed string
	reflector, err := NewTCPReflector(
		func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = address
			proxySide, deviceSide := net.Pipe()
			go func() {
				defer deviceSide.Close()
				buffer := make([]byte, 64)
				count, readError := deviceSide.Read(buffer)
				if readError == nil {
					_, _ = deviceSide.Write(buffer[:count])
				}
			}()
			return proxySide, nil
		},
		netip.MustParsePrefix("10.7.0.1/32"),
		true,
		Config{},
		func(string, ...any) {},
	)
	if err != nil {
		t.Fatal(err)
	}

	src := netip.MustParseAddrPort("100.100.4.3:54321")
	dst := netip.MustParseAddrPort("10.7.0.1:49152")
	handler, intercept := reflector.HandleTCPFlow(src, dst)
	if !intercept || handler == nil {
		t.Fatal("expected reflector to intercept configured route")
	}

	proxySide, clientSide := net.Pipe()
	go handler(proxySide)
	payload := []byte("rppairing")
	if _, err := clientSide.Write(payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(clientSide, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != string(payload) {
		t.Fatalf("response = %q, want %q", response, payload)
	}
	if dialed != "100.100.4.3:49152" {
		t.Fatalf("dialed = %q, want source peer on destination port", dialed)
	}
	_ = clientSide.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := reflector.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	metrics := reflector.Metrics().Snapshot()
	if metrics.Total != 1 || metrics.BytesToDevice == 0 || metrics.BytesToClient == 0 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestReflectorOnlyInterceptsConfiguredRoute(t *testing.T) {
	reflector, err := NewTCPReflector(
		func(context.Context, string, string) (net.Conn, error) { return nil, nil },
		netip.MustParsePrefix("10.7.0.1/32"),
		true,
		Config{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		src       string
		dst       string
		intercept bool
		handler   bool
	}{
		{name: "route", src: "100.64.0.2:1234", dst: "10.7.0.1:49152", intercept: true, handler: true},
		{name: "other destination", src: "100.64.0.2:1234", dst: "10.7.0.2:49152"},
		{name: "non-tailnet source", src: "192.0.2.10:1234", dst: "10.7.0.1:49152", intercept: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, intercept := reflector.HandleTCPFlow(
				netip.MustParseAddrPort(test.src),
				netip.MustParseAddrPort(test.dst),
			)
			if intercept != test.intercept || (handler != nil) != test.handler {
				t.Fatalf("handler=%t intercept=%t, want handler=%t intercept=%t", handler != nil, intercept, test.handler, test.intercept)
			}
		})
	}
}
