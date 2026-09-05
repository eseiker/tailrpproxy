package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestNewTSNetServerConfiguresOptionalPort(t *testing.T) {
	server := newTSNetServer(options{}, "")
	if server.Port != 0 {
		t.Fatalf("default port = %d, want 0", server.Port)
	}

	server = newTSNetServer(options{tsnetPort: 41641}, "")
	if server.Port != 41641 {
		t.Fatalf("fixed port = %d, want 41641", server.Port)
	}
}

func TestLogAuthURLOnce(t *testing.T) {
	var messages []string
	logf := logAuthURLOnce(func(format string, args ...any) {
		messages = append(messages, fmt.Sprintf(format, args...))
	})
	logf(authURLLogPrefix+" %s", "https://login.example/one")
	logf(authURLLogPrefix+" %s", "https://login.example/one")
	logf("state=%s", "Running")

	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want one auth URL and one status", messages)
	}
}

func TestTSNetUpContextWaitsForInteractiveLogin(t *testing.T) {
	ctx, cancel := tsnetUpContext(context.Background(), time.Nanosecond, true)
	defer cancel()
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		t.Fatal("interactive login context has a deadline")
	}

	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("interactive login context did not cancel")
	}
}

func TestTSNetUpContextBoundsCredentialedStartup(t *testing.T) {
	ctx, cancel := tsnetUpContext(context.Background(), time.Minute, false)
	defer cancel()
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		t.Fatal("credentialed startup context has no deadline")
	}
}
