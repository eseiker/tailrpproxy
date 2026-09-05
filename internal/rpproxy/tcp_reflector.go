package rpproxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// TCPReflector handles subnet-routed TCP flows without adding an application
// framing protocol. It dials the source Tailscale peer on the original
// destination port and then proxies the stream in both directions.
type TCPReflector struct {
	dial           DialContextFunc
	target         netip.Addr
	requireTailnet bool
	config         Config
	logf           func(string, ...any)
	metrics        Metrics

	closing       bool
	workerCtx     context.Context
	cancelWorkers context.CancelFunc
	workers       sync.WaitGroup

	mu          sync.Mutex
	connections map[net.Conn]struct{}
	peerCounts  map[string]int
	totalActive int
}

func NewTCPReflector(
	dial DialContextFunc,
	route netip.Prefix,
	requireTailnet bool,
	config Config,
	logf func(string, ...any),
) (*TCPReflector, error) {
	if dial == nil {
		return nil, fmt.Errorf("dialer is required")
	}
	if !route.IsValid() || !route.IsSingleIP() {
		return nil, fmt.Errorf("reflector route must be a single IP prefix, got %q", route)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	return &TCPReflector{
		dial:           dial,
		target:         route.Masked().Addr(),
		requireTailnet: requireTailnet,
		config:         config.withDefaults(),
		logf:           logf,
		workerCtx:      workerCtx,
		cancelWorkers:  cancelWorkers,
		connections:    make(map[net.Conn]struct{}),
		peerCounts:     make(map[string]int),
	}, nil
}

func (reflector *TCPReflector) Metrics() *Metrics {
	return &reflector.metrics
}

// HandleTCPFlow selects flows addressed to the synthetic route for reflection.
func (reflector *TCPReflector) HandleTCPFlow(src, dst netip.AddrPort) (func(net.Conn), bool) {
	if dst.Addr() != reflector.target {
		return nil, false
	}
	if reflector.requireTailnet && !IsTailnetAddress(src.Addr()) {
		reflector.metrics.rejected.Add(1)
		reflector.logf("rejected non-tailnet source %s for %s", src, dst)
		return nil, true
	}
	if src.Port() == 0 || dst.Port() == 0 {
		reflector.metrics.rejected.Add(1)
		return nil, true
	}
	return func(connection net.Conn) {
		reflector.handle(connection, src, dst)
	}, true
}

func (reflector *TCPReflector) handle(client net.Conn, src, dst netip.AddrPort) {
	peer := src.Addr().String()
	if !reflector.acquire(peer, client) {
		reflector.metrics.rejected.Add(1)
		_ = client.Close()
		return
	}
	defer reflector.workers.Done()
	defer reflector.release(peer, client)
	defer client.Close()

	reflector.metrics.total.Add(1)
	reflector.metrics.active.Add(1)
	defer reflector.metrics.active.Add(-1)

	target := net.JoinHostPort(src.Addr().String(), strconv.Itoa(int(dst.Port())))
	dialContext, cancel := context.WithTimeout(reflector.workerCtx, reflector.config.DialTimeout)
	device, err := reflector.dial(dialContext, "tcp", target)
	cancel()
	if err != nil {
		reflector.metrics.dialFailures.Add(1)
		reflector.logf("reflector dial failed for %s -> %s: %v", src, target, err)
		return
	}
	reflector.track(device, true)
	defer reflector.track(device, false)
	defer device.Close()

	if reflector.config.StreamTimeout > 0 {
		deadline := time.Now().Add(reflector.config.StreamTimeout)
		_ = client.SetDeadline(deadline)
		_ = device.SetDeadline(deadline)
	}

	started := time.Now()
	result := proxyBidirectional(device, client)
	reflector.metrics.bytesToDevice.Add(result.toDevice.bytes)
	reflector.metrics.bytesToClient.Add(result.toClient.bytes)
	reflector.logf(
		"stream closed after %s: src=%s dst=%s %s",
		time.Since(started).Round(time.Millisecond),
		src,
		dst,
		streamResultSummary(result, reflector.config.Verbose),
	)
}

func (reflector *TCPReflector) Shutdown(ctx context.Context) error {
	reflector.mu.Lock()
	reflector.closing = true
	reflector.mu.Unlock()
	reflector.metrics.SetReady(false)

	drained := make(chan struct{})
	go func() {
		reflector.workers.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		reflector.cancelWorkers()
		return nil
	case <-ctx.Done():
		reflector.cancelWorkers()
		reflector.closeConnections()
		return ctx.Err()
	}
}

func (reflector *TCPReflector) acquire(peer string, connection net.Conn) bool {
	reflector.mu.Lock()
	defer reflector.mu.Unlock()
	if reflector.closing || reflector.totalActive >= reflector.config.MaxConnections {
		return false
	}
	if reflector.peerCounts[peer] >= reflector.config.MaxConnectionsPerPeer {
		return false
	}
	reflector.totalActive++
	reflector.peerCounts[peer]++
	reflector.connections[connection] = struct{}{}
	reflector.workers.Add(1)
	return true
}

func (reflector *TCPReflector) release(peer string, connection net.Conn) {
	reflector.mu.Lock()
	defer reflector.mu.Unlock()
	delete(reflector.connections, connection)
	reflector.totalActive--
	reflector.peerCounts[peer]--
	if reflector.peerCounts[peer] == 0 {
		delete(reflector.peerCounts, peer)
	}
}

func (reflector *TCPReflector) track(connection net.Conn, add bool) {
	reflector.mu.Lock()
	defer reflector.mu.Unlock()
	if add {
		reflector.connections[connection] = struct{}{}
	} else {
		delete(reflector.connections, connection)
	}
}

func (reflector *TCPReflector) closeConnections() {
	reflector.mu.Lock()
	connections := make([]net.Conn, 0, len(reflector.connections))
	for connection := range reflector.connections {
		connections = append(connections, connection)
	}
	reflector.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}
