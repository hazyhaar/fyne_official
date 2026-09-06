//go:build purego && linux

package purego

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/driver/desktop"
)

// openX11ForTest wraps openX11 with a few short retries on "connection reset
// by peer" during the initial dial/handshake: this sandbox's Xvfb
// occasionally resets a brand-new Unix-domain connection outright (observed
// on roughly 1 in 4-6 back-to-back connections across every test file in
// this package, unrelated to which request is sent — the reset happens
// before the handshake reply is even read), which is an environment
// transient, not a wire-protocol bug in this package.
func openX11ForTest(t *testing.T, width, height int, opts ...x11Option) *x11Surface {
	t.Helper()
	const attempts = 5
	var lastErr error
	for i := 0; i < attempts; i++ {
		s, err := openX11("", width, height, opts...)
		if err == nil {
			return s
		}
		lastErr = err
		if !strings.Contains(err.Error(), "connection reset by peer") {
			t.Fatalf("openX11: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("openX11: %v (after %d retries on connection reset)", lastErr, attempts)
	return nil
}

// TestSetCursorStandardCursors checks that every desktop.StandardCursor maps
// to a glyph the "cursor" font actually has and that setting it against a
// real xvfb server does not error, for both a first use (font/glyph
// creation) and a repeat use (cached glyph). This test failed before
// x11_cursor.go existed, since x11Surface had no SetCursor method to call.
func TestSetCursorStandardCursors(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })
	t.Cleanup(func() { forgetCursorState(s) })

	cursors := []desktop.StandardCursor{
		desktop.DefaultCursor,
		desktop.TextCursor,
		desktop.CrosshairCursor,
		desktop.PointerCursor,
		desktop.HResizeCursor,
		desktop.VResizeCursor,
		desktop.NESWResizeCursor,
		desktop.NWSEResizeCursor,
		desktop.HiddenCursor,
	}
	for _, c := range cursors {
		if err := s.SetCursor(c); err != nil {
			t.Fatalf("SetCursor(%v): %v", c, err)
		}
	}
	// Repeat pass: exercises the cached-glyph path in cursorFontState.
	for _, c := range cursors {
		if err := s.SetCursor(c); err != nil {
			t.Fatalf("SetCursor(%v) second call: %v", c, err)
		}
	}
}
