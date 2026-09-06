//go:build purego && linux

package purego

import (
	"runtime"
	"testing"
	"time"
)

// TestWaitConfiguredDoesNotSpawnAGoroutine is the regression test for
// when waitConfigured used to start a new
// goroutine, on every call, that spun in a tight loop re-locking s.mu and
// polling s.configured until the compositor answered — no runtime.Gosched,
// no blocking receive, saturating a CPU core for however long that took.
// The wait is a blocking receive on configuredCh (plus a timer); it must
// not increase the live goroutine count beyond the caller, and it must
// actually block until configuredCh is closed.
func TestWaitConfiguredDoesNotSpawnAGoroutine(t *testing.T) {
	s := &waylandSurface{done: make(chan struct{}), configuredCh: make(chan struct{})}
	old := waylandRequestTimeout
	waylandRequestTimeout = time.Second
	t.Cleanup(func() { waylandRequestTimeout = old })

	before := runtime.NumGoroutine()
	errCh := make(chan error, 1)
	go func() { errCh <- s.waitConfigured() }()
	runtime.Gosched()
	time.Sleep(10 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 { // caller goroutine + timer slack
		t.Fatalf("waitConfigured left %d new goroutines running (want ~1): it is spawning a polling goroutine again", after-before)
	}

	select {
	case err := <-errCh:
		t.Fatalf("waitConfigured returned %v before configuredCh was closed", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(s.configuredCh)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("waitConfigured: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waitConfigured did not unblock after configuredCh was closed")
	}
}

func TestRoundtripTimesOutOnSilentCompositor(t *testing.T) {
	client, server := unixSocketPair(t)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	s := &waylandSurface{
		conn:   client,
		done:   make(chan struct{}),
		events: make(chan surfaceEvent, 4),
		nextID: 2,
	}
	t.Cleanup(func() { _ = s.Close() })
	go s.readLoop()

	old := waylandRequestTimeout
	waylandRequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { waylandRequestTimeout = old })

	start := time.Now()
	err := s.roundtrip()
	if err == nil {
		t.Fatal("roundtrip to a silent compositor returned no error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s, want around %s", elapsed, waylandRequestTimeout)
	}
}

func TestWaitConfiguredTimesOutOnSilentCompositor(t *testing.T) {
	client, server := unixSocketPair(t)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	s := &waylandSurface{
		conn:         client,
		done:         make(chan struct{}),
		events:       make(chan surfaceEvent, 4),
		configuredCh: make(chan struct{}),
	}
	t.Cleanup(func() { _ = s.Close() })
	go s.readLoop()

	old := waylandRequestTimeout
	waylandRequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { waylandRequestTimeout = old })

	start := time.Now()
	err := s.waitConfigured()
	if err == nil {
		t.Fatal("waitConfigured on a silent compositor returned no error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s, want around %s", elapsed, waylandRequestTimeout)
	}
}
