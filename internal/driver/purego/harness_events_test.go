//go:build purego && linux

package purego

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	_ "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// pumpFor drains and handles surface events for w for the given duration,
// simulating the relevant slice of driverImpl.Run's loop (drainEvents) so
// event-injection tests do not depend on the full Run loop.
func pumpFor(w *window, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		w.drainEvents()
		time.Sleep(2 * time.Millisecond)
	}
}

// TestHarnessXTestClickTriggersOnTapped sends a synthetic XTEST button click
// at the button's on-screen position and checks that OnTapped fires. The
// click is injected through the XTEST extension (XTestFakeInput), not by
// calling window.handlePointer directly, so the test exercises the real X11
// wire path end to end: fake input -> X server -> ButtonPress/ButtonRelease
// events read back over this backend's own connection -> window.handleEvent
// -> window.handlePointer -> widget.Button.Tapped.
func TestHarnessXTestClickTriggersOnTapped(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-click").(*window)

	tapped := make(chan struct{}, 1)
	btn := widget.NewButton("tap me", func() { tapped <- struct{}{} })
	w.SetContent(btn)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)

	// XTEST is queried and fired over a dedicated second connection
	// (dialSyncX11), not w.surface.conn: see harness_x11_client_test.go for
	// why issuing a synchronous request on the window's own connection races
	// x11Surface.readLoop once it is running (it is, since w.Show() above
	// already started it).
	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	// The button occupies the padded content area; its center is a safe
	// click target regardless of the exact label metrics.
	pos := btn.Position()
	size := btn.Size()
	clickX := int16(pos.X + size.Width/2)
	clickY := int16(pos.Y + size.Height/2)

	root := sync.rootWindow
	const leftButton = 1
	// XTestFakeInput ignores rootX/rootY for ButtonPress/ButtonRelease (per
	// the XTEST protocol, only MotionNotify's detail/rootX/rootY warp the
	// pointer); the button events fire wherever the pointer already is, so
	// the pointer is warped there first with a MotionNotify (detail 0 =
	// absolute positioning).
	const motionAbsolute = 0
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput MotionNotify: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonPress, leftButton, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput ButtonPress: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, leftButton, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput ButtonRelease: %v", err)
	}

	pumpFor(w, 500*time.Millisecond)

	select {
	case <-tapped:
	default:
		t.Fatalf("OnTapped did not fire after synthetic XTEST click at (%d,%d)", clickX, clickY)
	}
}

// TestHarnessXTestKeyReachesFocusedEntry sends a synthetic XTEST key press
// for the 'a' key and checks that a focused widget.Entry receives it as
// typed text. This is expected to fail against the driver as committed at
// the snapshot this worktree was created from: window.handleKey (window.go)
// only calls w.canvas.OnTypedRune()/OnTypedKey(), it never consults
// w.canvas.Focused(), so no canvas-level handler is registered here and a
// focused Entry never sees the keystroke. The core package needs
// handleKey to consult canvas.Focused() for this to pass.
func TestHarnessXTestKeyReachesFocusedEntry(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-key").(*window)

	entry := widget.NewEntry()
	w.SetContent(entry)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)

	w.Canvas().Focus(entry)

	// See TestHarnessXTestClickTriggersOnTapped for why XTEST goes over a
	// dedicated connection rather than w.surface.conn.
	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	const keycodeQ = 24

	root := sync.rootWindow
	if err := xt.fakeInput(sync.conn, xtestKeyPress, keycodeQ, root, 0, 0); err != nil {
		t.Fatalf("fakeInput KeyPress: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestKeyRelease, keycodeQ, root, 0, 0); err != nil {
		t.Fatalf("fakeInput KeyRelease: %v", err)
	}

	pumpFor(w, 500*time.Millisecond)

	if got := entry.Text; got != "q" {
		t.Fatalf("entry.Text = %q after synthetic 'q' keypress, want \"q\"", got)
	}
}
