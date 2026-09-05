package rpproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

type Config struct {
	DialTimeout           time.Duration
	StreamTimeout         time.Duration
	MaxConnections        int
	MaxConnectionsPerPeer int
	Verbose               bool
}

func (config Config) withDefaults() Config {
	if config.DialTimeout <= 0 {
		config.DialTimeout = 10 * time.Second
	}
	if config.MaxConnections <= 0 {
		config.MaxConnections = 64
	}
	if config.MaxConnectionsPerPeer <= 0 {
		config.MaxConnectionsPerPeer = 8
	}
	return config
}

type streamCopyResult struct {
	direction string
	bytes     uint64
	err       error
}

type streamResult struct {
	firstClosed string
	toDevice    streamCopyResult
	toClient    streamCopyResult
}

func proxyBidirectional(device, client net.Conn) streamResult {
	results := make(chan streamCopyResult, 2)
	copyOneWay := func(direction string, destination, source net.Conn) {
		count, err := io.Copy(destination, source)
		if closer, ok := destination.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		results <- streamCopyResult{direction: direction, bytes: uint64(count), err: err}
	}

	go copyOneWay("to-device", device, client)
	go copyOneWay("to-client", client, device)
	first, second := <-results, <-results
	result := streamResult{firstClosed: first.direction}
	for _, copied := range []streamCopyResult{first, second} {
		switch copied.direction {
		case "to-device":
			result.toDevice = copied
		case "to-client":
			result.toClient = copied
		}
	}
	return result
}

func streamTermination(err error) string {
	switch {
	case err == nil:
		return "eof"
	case errors.Is(err, os.ErrDeadlineExceeded):
		return "timeout"
	case errors.Is(err, syscall.ECONNRESET):
		return "reset"
	case errors.Is(err, syscall.EPIPE):
		return "broken-pipe"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	default:
		return err.Error()
	}
}

func streamResultSummary(result streamResult, verbose bool) string {
	if !verbose {
		return fmt.Sprintf("to-device=%d to-client=%d", result.toDevice.bytes, result.toClient.bytes)
	}
	return fmt.Sprintf(
		"first=%s to-device=%d/%s to-client=%d/%s",
		result.firstClosed,
		result.toDevice.bytes,
		streamTermination(result.toDevice.err),
		result.toClient.bytes,
		streamTermination(result.toClient.err),
	)
}
