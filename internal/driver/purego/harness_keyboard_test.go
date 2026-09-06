//go:build purego && linux

package purego

import (
	"net"
	"os/exec"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	_ "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

const (
	xKeycodeA        = 38
	xKeycode1        = 10
	xKeycode2        = 11
	xKeycodeShiftL   = 50
	xKeycodeCapsLock = 66
)

func TestKeyboardMappingLoaded(t *testing.T) {
	s, err := openX11("", 64, 48)
	if err != nil {
		t.Fatalf("openX11: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.mu.Lock()
	ks := s.lookupKeysymLocked(xKeycodeA, 0)
	s.mu.Unlock()
	if ks != 0x61 {
		t.Fatalf("lookup of keycode %d = 0x%X, want 0x61 ('a')", xKeycodeA, ks)
	}
}

func TestHarnessXTestKeymapShiftCapsAndLayout(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-keymap").(*window)

	entry := widget.NewEntry()
	w.SetContent(entry)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)
	w.Canvas().Focus(entry)

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatal("expected x11Surface")
	}

	root := sync.rootWindow

	xtestTap(t, xt, sync.conn, root, xKeycodeA)
	pumpUntilText(t, w, entry, "a")

	entry.SetText("")
	xtestChord(t, xt, sync.conn, root, xKeycodeShiftL, xKeycodeA)
	pumpUntilText(t, w, entry, "A")

	entry.SetText("")
	xtestTap(t, xt, sync.conn, root, xKeycodeCapsLock)
	pumpFor(w, 200*time.Millisecond)
	xtestTap(t, xt, sync.conn, root, xKeycodeA)
	pumpUntilText(t, w, entry, "A")
	xtestTap(t, xt, sync.conn, root, xKeycodeCapsLock)
	pumpFor(w, 200*time.Millisecond)

	entry.SetText("")
	xtestTap(t, xt, sync.conn, root, xKeycode1)
	pumpUntilText(t, w, entry, "1")

	entry.SetText("")
	xtestChord(t, xt, sync.conn, root, xKeycodeShiftL, xKeycode1)
	pumpUntilText(t, w, entry, "!")

	if _, lookErr := exec.LookPath("setxkbmap"); lookErr != nil {
		t.Log("setxkbmap not found; skipping French é injection")
		return
	}
	t.Cleanup(func() { _ = exec.Command("setxkbmap", "us").Run() })
	if err := exec.Command("setxkbmap", "fr").Run(); err != nil {
		t.Fatalf("setxkbmap fr: %v", err)
	}
	waitKeysymColumn(t, xs, xKeycode2, 0, 0xe9)

	entry.SetText("")
	xtestTap(t, xt, sync.conn, root, xKeycode2)
	pumpUntilText(t, w, entry, "é")
}

func xtestTap(t *testing.T, xt *xtestExtension, conn net.Conn, root uint32, keycode byte) {
	t.Helper()
	if err := xt.fakeInput(conn, xtestKeyPress, keycode, root, 0, 0); err != nil {
		t.Fatalf("fakeInput KeyPress: %v", err)
	}
	if err := xt.fakeInput(conn, xtestKeyRelease, keycode, root, 0, 0); err != nil {
		t.Fatalf("fakeInput KeyRelease: %v", err)
	}
}

func xtestChord(t *testing.T, xt *xtestExtension, conn net.Conn, root uint32, mod, key byte) {
	t.Helper()
	if err := xt.fakeInput(conn, xtestKeyPress, mod, root, 0, 0); err != nil {
		t.Fatalf("fakeInput mod press: %v", err)
	}
	if err := xt.fakeInput(conn, xtestKeyPress, key, root, 0, 0); err != nil {
		t.Fatalf("fakeInput key press: %v", err)
	}
	if err := xt.fakeInput(conn, xtestKeyRelease, key, root, 0, 0); err != nil {
		t.Fatalf("fakeInput key release: %v", err)
	}
	if err := xt.fakeInput(conn, xtestKeyRelease, mod, root, 0, 0); err != nil {
		t.Fatalf("fakeInput mod release: %v", err)
	}
}

func pumpUntilText(t *testing.T, w *window, entry *widget.Entry, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.drainEvents()
		if entry.Text == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("entry.Text = %q, want %q", entry.Text, want)
}

func waitKeysymColumn(t *testing.T, xs *x11Surface, keycode byte, col int, want uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		xs.mu.Lock()
		syms := keysymsOf(xs.minKeycode, xs.keysymsPer, xs.keysyms, keycode)
		xs.mu.Unlock()
		if col >= 0 && col < coreKeysymCols && syms[col] == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("keycode %d column %d did not become 0x%X after layout change", keycode, col, want)
}

func TestHarnessXTestKeyableDownTypedUp(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-keyable").(*window)

	rec := &keyableRecorder{}
	w.SetContent(rec)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)
	w.Canvas().Focus(rec)

	if w.canvas.Focused() != rec {
		t.Fatal("keyable widget did not become focused")
	}

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	xtestTap(t, xt, sync.conn, sync.rootWindow, xKeycodeA)
	deadline := time.Now().Add(2 * time.Second)
	want := []string{"KeyDown", "TypedKey", "KeyUp"}
	for time.Now().Before(deadline) {
		w.drainEvents()
		if len(rec.seq) >= len(want) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(rec.seq) != len(want) {
		t.Fatalf("event sequence = %v, want %v", rec.seq, want)
	}
	for i := range want {
		if rec.seq[i] != want[i] {
			t.Fatalf("event sequence = %v, want %v", rec.seq, want)
		}
	}
}
