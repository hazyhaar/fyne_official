//go:build purego && linux

package purego

import (
	"runtime"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

// TestHarnessWindowShowResizeClose exercises the fyne.Window lifecycle this
// backend implements (Show/Resize/Close) directly against a real X server
// named by $DISPLAY, and checks that Close releases the underlying
// connection so a further write on it fails. This test fails before
// window.go/x11.go exist, and fails again if Close stops actually closing
// the socket (surface.conn.Close returns nil error without the connection
// having been shut down would leave a later Write silently succeed).
func TestHarnessWindowShowResizeClose(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-lifecycle").(*window)

	w.Show()
	if w.surface == nil {
		t.Fatal("Show did not open a surface")
	}

	if err := w.surface.SetTitle("resized"); err != nil {
		t.Fatalf("SetTitle before resize: %v", err)
	}

	w.Resize(fyne.NewSize(320, 240))
	if got := w.canvas.Size(); got.Width != 320 || got.Height != 240 {
		t.Fatalf("canvas size after Resize = %+v, want 320x240", got)
	}

	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatalf("surface is %T, want *x11Surface", w.surface)
	}
	conn := xs.conn
	w.Close()
	if !w.closed {
		t.Fatal("window not marked closed after Close")
	}
	if _, err := conn.Write([]byte{0}); err == nil {
		t.Fatal("Write on surface connection succeeded after Close, want error")
	}
}

// TestHarnessDriverQuitOnLastWindowClosed checks that closing the only open
// window makes the driver quit: driverImpl.removeWindow calls quitLocked
// once the window list is empty, which closes d.done, the channel Run's
// select loop (driver.go) waits on to return. This test does not call
// Run() itself: async.IsMainGoroutine (internal/async/goroutine_desktop.go)
// gates it to the goroutine recorded at process init, and go test does not
// guarantee a test function - let alone a goroutine spawned from one - runs
// on that goroutine (calling d.Run() from a spawned goroutine panics with
// "Run() or ShowAndRun() must be called from main goroutine"). Observing d.done
// instead exercises exactly the wiring Run() depends on without that
// restriction, and fails before quitLocked existed (d.done would never
// close and this would time out) or if removeWindow stopped calling it.
func TestHarnessDriverQuitOnLastWindowClosed(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-quit").(*window)
	w.Show()

	w.Close()

	select {
	case <-d.done:
	case <-time.After(2 * time.Second):
		t.Fatal("d.done did not close within 2s of the last window closing")
	}
}

// TestHarnessMasterWindowCloseQuitsDriver checks that closing a window
// marked SetMaster quits the driver even when another window is still
// open, matching glfw's window.destroy. d.done closing is the signal
// driverImpl.Run waits on. This failed before removeWindow read w.master.
func TestHarnessMasterWindowCloseQuitsDriver(t *testing.T) {
	d := newTestDriver()
	master := d.CreateWindow("master").(*window)
	_ = d.CreateWindow("other")
	master.SetMaster()

	master.Close()

	select {
	case <-d.done:
	case <-time.After(2 * time.Second):
		t.Fatal("d.done did not close within 2s of the master window closing")
	}
}

// TestHarnessNonMasterCloseLeavesDriverRunning is the counterpart: closing
// a non-master window while another remains must not quit.
func TestHarnessNonMasterCloseLeavesDriverRunning(t *testing.T) {
	d := newTestDriver()
	master := d.CreateWindow("master").(*window)
	other := d.CreateWindow("other").(*window)
	master.SetMaster()

	other.Close()

	select {
	case <-d.done:
		t.Fatal("d.done closed after a non-master window closed with the master still open")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestHarnessNoGoroutineLeakAfterClose opens and closes a window several
// times and checks that the X11 surface's background read goroutine
// (x11Surface.readLoop) does not accumulate. goleak is not a Fyne
// dependency, so this counts
// runtime.NumGoroutine before and after with a documented tolerance: GC and
// the Go runtime's own bookkeeping goroutines can shift the count by a
// handful even with zero leaks, so the tolerance below (5) is chosen to stay
// well under one leaked readLoop per iteration (5 iterations) while still
// catching a leak of the size this backend would actually produce.
func TestHarnessNoGoroutineLeakAfterClose(t *testing.T) {
	const iterations = 5
	const tolerance = 5

	settle := func() int {
		runtime.GC() //nolint:revive // deliberate settling point before sampling NumGoroutine, not leftover debug code
		runtime.Gosched()
		time.Sleep(50 * time.Millisecond)
		return runtime.NumGoroutine()
	}

	before := settle()
	for i := 0; i < iterations; i++ {
		d := newTestDriver()
		w := d.CreateWindow("harness-leak").(*window)
		w.Show()
		if w.surface == nil {
			// Connecting many short-lived clients back to back has been
			// observed to trip a transient "connection reset by peer" on
			// this X server; one retry after a short pause is enough to
			// distinguish that from an actual open failure.
			time.Sleep(100 * time.Millisecond)
			w.Show()
		}
		if w.surface == nil {
			t.Fatalf("iteration %d: Show did not open a surface even after one retry", i)
		}
		w.Close()
	}
	after := settle()

	if delta := after - before; delta > tolerance {
		t.Fatalf("goroutine count grew by %d (before=%d after=%d), want <= %d after %d open/close cycles",
			delta, before, after, tolerance, iterations)
	}
}
