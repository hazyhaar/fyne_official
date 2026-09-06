//go:build purego && linux

package purego

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

// dialX11Socket duplicates the small "which unix socket does $DISPLAY name"
// resolution openX11 (x11.go) does, without any of the window-creation or
// readLoop side effects that make openX11 unsuitable for a second,
// synchronous-only connection.
func dialX11Socket() (net.Conn, error) {
	dpyName := os.Getenv("DISPLAY")
	if dpyName == "" {
		dpyName = ":0"
	}
	displayNum := "0"
	if strings.HasPrefix(dpyName, ":") {
		displayNum = strings.SplitN(strings.TrimPrefix(dpyName, ":"), ".", 2)[0]
	}
	sockPath := "/tmp/.X11-unix/X" + displayNum
	conn, err := net.Dial("unix", sockPath) //nolint:gosec // sockPath is derived from DISPLAY, a local display selector
	if err != nil {
		return nil, fmt.Errorf("purego/x11 harness: cannot reach X server at %s: %w", sockPath, err)
	}
	return conn, nil
}

// dialSyncX11 opens a second, dedicated connection to the same X server a
// purego window is displayed on, for issuing requests that need a
// synchronous reply (GetImage, QueryExtension) after the window's own
// surface is already running.
//
// This is needed because x11Surface has exactly one goroutine reading its
// connection once open (x11.go's readLoop, started at the end of openX11),
// and readLoop unconditionally treats every 32 bytes it reads as a
// fixed-size core event: for a reply with a status byte of replyStatusReply
// it takes the "continue" branch and drops the 32-byte header without
// reading the variable-length data a reply like GetImage appends after it.
// Issuing a synchronous request on that same connection after readLoop has
// started races the two readers over the same byte stream: whichever side
// wins the race for the header may leave the other blocked forever on
// io.ReadFull for bytes that already went to the wrong reader, and for a
// reply with trailing data a readLoop "win" permanently desynchronizes the
// stream (every subsequent read is misaligned). This was observed directly:
// the pre-existing TestOpenX11CreatesAndPresentsWindow (x11_wire_test.go),
// which does exactly this (Present, then a synchronous GetImage on the same
// surface connection after readLoop is already running), hangs
// deterministically under a real Xvfb server. It is not a flake: the
// goroutine dump shows the test goroutine parked in getImagePixel's
// io.ReadFull while x11Surface.readLoop holds the other io.ReadFull on the
// same fd.
//
// A second connection sidesteps the race entirely: it has no readLoop of
// its own, so a synchronous write-then-read on it never competes with
// anything. X11 extension major opcodes and drawable IDs (including another
// client's window ID) are valid across connections to the same server, so
// this client can still issue GetImage against the window surface's real
// winID, and XTestFakeInput against the real root window, using majorOpcode
// values it queries for itself.
//
// The core package needs a way to issue a synchronous request against a
// *running* x11Surface without racing readLoop, e.g. a
// func (s *x11Surface) syncRequest(req []byte, replyLen int) (header, body
// []byte, error) that hands off through a pending-reply channel readLoop
// selects on before falling into its own continue/drop branch.
func dialSyncX11(t *testing.T) *x11Surface {
	t.Helper()
	conn, err := dialX11Socket()
	if err != nil {
		t.Skipf("dialSyncX11: no X server: %v", err)
	}
	s := &x11Surface{conn: conn}
	displayNum := "0"
	if dpy := os.Getenv("DISPLAY"); strings.HasPrefix(dpy, ":") {
		displayNum = strings.SplitN(strings.TrimPrefix(dpy, ":"), ".", 2)[0]
	}
	if err := s.handshake(displayNum); err != nil {
		_ = conn.Close()
		t.Fatalf("dialSyncX11: handshake: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return s
}

// TestAppClipboardSetContentVisibleToSecondX11Client checks that
// fyne.CurrentApp().Clipboard().SetContent publishes to the X11 CLIPBOARD
// selection of a shown window, so a second client can read it back. Before
// NewClipboard was wired to clipboardSurface this test failed: SetContent
// only updated process-local memory.
func TestAppClipboardSetContentVisibleToSecondX11Client(t *testing.T) {
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("app-clipboard").(*window)
	w.Show()
	if w.surface == nil {
		t.Skip("purego/x11: no display server reachable")
	}
	clip := NewClipboard()
	installClipboardApp(t, d, clip)
	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Skipf("surface is %T, want *x11Surface", w.surface)
	}
	t.Cleanup(func() {
		w.Close()
		waitClipboardOwnerGone(t, xs)
	})

	const want = "app clipboard second client"
	fyne.CurrentApp().Clipboard().SetContent(want)
	time.Sleep(50 * time.Millisecond)

	got := readUTF8ClipboardAsSecondClient(t)
	if got != want {
		t.Fatalf("second X11 client read %q, want %q", got, want)
	}
}

func readUTF8ClipboardAsSecondClient(t *testing.T) string {
	t.Helper()
	client := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = client.Close() })
	got, err := client.ClipboardText()
	if err != nil {
		t.Fatalf("second client ClipboardText: %v", err)
	}
	return got
}
