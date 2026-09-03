package rpproxy

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

type Config struct {
	DialTimeout           time.Duration
	StreamTimeout         time.Duration
	MaxConnections        int
	MaxConnectionsPerPeer int
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

func proxyBidirectional(device, client net.Conn) (toDevice uint64, toClient uint64) {
	var wait sync.WaitGroup
	wait.Add(2)

	copyOneWay := func(destination, source net.Conn, counter *uint64) {
		defer wait.Done()
		count, _ := io.Copy(destination, source)
		*counter = uint64(count)
		if closer, ok := destination.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
	}

	go copyOneWay(device, client, &toDevice)
	go copyOneWay(client, device, &toClient)
	wait.Wait()
	return toDevice, toClient
}
