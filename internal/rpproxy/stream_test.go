package rpproxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
)

func TestProxyBidirectionalReportsBytesAndTermination(t *testing.T) {
	proxyDevice, remoteDevice := net.Pipe()
	proxyClient, remoteClient := net.Pipe()
	resultChannel := make(chan streamResult, 1)
	go func() { resultChannel <- proxyBidirectional(proxyDevice, proxyClient) }()

	request := []byte("request")
	response := []byte("response")
	clientDone := make(chan error, 1)
	go func() {
		defer remoteClient.Close()
		if _, err := remoteClient.Write(request); err != nil {
			clientDone <- err
			return
		}
		responseBuffer := make([]byte, len(response))
		_, err := io.ReadFull(remoteClient, responseBuffer)
		if err == nil && !bytes.Equal(responseBuffer, response) {
			err = fmt.Errorf("response = %q, want %q", responseBuffer, response)
		}
		clientDone <- err
	}()

	requestBuffer := make([]byte, len(request))
	if _, err := io.ReadFull(remoteDevice, requestBuffer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(requestBuffer, request) {
		t.Fatalf("request = %q, want %q", requestBuffer, request)
	}
	if _, err := remoteDevice.Write(response); err != nil {
		t.Fatal(err)
	}
	_ = remoteDevice.Close()
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	result := <-resultChannel
	if result.firstClosed == "" {
		t.Fatal("firstClosed is empty")
	}
	if result.toDevice.bytes != uint64(len(request)) {
		t.Fatalf("to-device bytes = %d, want %d", result.toDevice.bytes, len(request))
	}
	if result.toClient.bytes != uint64(len(response)) {
		t.Fatalf("to-client bytes = %d, want %d", result.toClient.bytes, len(response))
	}
}

func TestStreamTermination(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "eof", want: "eof"},
		{name: "closed", err: net.ErrClosed, want: "closed"},
		{name: "reset", err: syscall.ECONNRESET, want: "reset"},
		{name: "broken pipe", err: syscall.EPIPE, want: "broken-pipe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := streamTermination(test.err); got != test.want {
				t.Fatalf("streamTermination(%v) = %q, want %q", test.err, got, test.want)
			}
		})
	}
}

func TestStreamResultSummary(t *testing.T) {
	result := streamResult{
		firstClosed: "to-device",
		toDevice:    streamCopyResult{bytes: 1054},
		toClient:    streamCopyResult{bytes: 1899, err: syscall.ECONNRESET},
	}
	if got, want := streamResultSummary(result, false), "to-device=1054 to-client=1899"; got != want {
		t.Fatalf("default summary = %q, want %q", got, want)
	}
	if got, want := streamResultSummary(result, true), "first=to-device to-device=1054/eof to-client=1899/reset"; got != want {
		t.Fatalf("verbose summary = %q, want %q", got, want)
	}
}
