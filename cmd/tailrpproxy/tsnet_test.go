package main

import (
	"context"
	"testing"
	"time"
)

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
