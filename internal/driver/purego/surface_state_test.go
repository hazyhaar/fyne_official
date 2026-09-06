//go:build purego && linux

package purego

import (
	"runtime"
	"testing"
	"time"
)

func settleGoroutines() int {
	runtime.GC() //nolint:revive // settle before sampling NumGoroutine
	runtime.Gosched()
	time.Sleep(20 * time.Millisecond)
	return runtime.NumGoroutine()
}

// TestAttachedStateHelpersSpawnNoCleanupGoroutine fails while per-surface
// extras live in package maps whose accessors start a goroutine that only
// waits on s.done to delete the map entry.
func TestAttachedStateHelpersSpawnNoCleanupGoroutine(t *testing.T) {
	s := &waylandSurface{done: make(chan struct{})}
	before := settleGoroutines()
	_ = waylandCursorOf(s)
	_ = waylandClipboardOf(s)
	_ = waylandOutputsOf(s)
	_ = waylandIdleOf(s)
	_ = waylandDndOf(s)
	_ = waylandToplevelOf(s)
	after := settleGoroutines()
	if after > before {
		t.Fatalf("attached-state helpers left %d extra goroutine(s) (before=%d after=%d)", after-before, before, after)
	}
}

// TestX11CloseDropsAttachedState fails while Close leaves a clipboard owner
// or cursor cache keyed by the surface pointer in a package map.
func TestX11CloseDropsAttachedState(t *testing.T) {
	s := openX11ForTest(t, 16, 16)
	_ = cursorStateFor(s)
	if err := s.SetClipboardText("drop-attached"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	if s.state.clip == nil {
		t.Fatal("clipboard owner missing after SetClipboardText")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitClipboardOwnerGone(t, s)
	if s.state.clip != nil {
		t.Fatal("clipboard owner still referenced after Close")
	}
}

func TestX11CloseLeavesNoExtraGoroutines(t *testing.T) {
	before := settleGoroutines()
	s := openX11ForTest(t, 16, 16)
	if err := s.SetClipboardText("close-goroutines"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	subscribeRandRScreenChange(s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitClipboardOwnerGone(t, s)
	after := settleGoroutines()
	if after > before {
		t.Fatalf("Close left %d extra goroutine(s) (before=%d after=%d)", after-before, before, after)
	}
}
