package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Every is the whole scheduler: a job that fails to start at boot, or keeps
// running after shutdown, is invisible until someone notices an email that
// never came or a process that will not exit.
func TestEveryRunsImmediatelyThenRepeatsAndStops(t *testing.T) {
	var runs int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		Every(ctx, 10*time.Millisecond, func(context.Context) { atomic.AddInt32(&runs, 1) })
		close(done)
	}()

	// Immediately: a restart must not wait a whole interval before it checks.
	deadline := time.Now().Add(5 * time.Millisecond)
	for atomic.LoadInt32(&runs) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if atomic.LoadInt32(&runs) < 1 {
		t.Fatal("fn did not run immediately at start")
	}

	// Repeats.
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got < 3 {
		t.Fatalf("fn ran %d times in ~80ms at a 10ms interval, want at least 3", got)
	}

	// Stops, and returns.
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Every did not return after the context was cancelled")
	}
	after := atomic.LoadInt32(&runs)
	time.Sleep(40 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != after {
		t.Fatalf("fn kept running after cancel: %d -> %d", after, got)
	}
}
