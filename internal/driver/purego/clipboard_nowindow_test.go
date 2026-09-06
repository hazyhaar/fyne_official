//go:build purego && linux

package purego

import (
	"os"
	"testing"

	"fyne.io/fyne/v2"
)

// TestAppClipboardWithoutWindowSecondClient is the no-window system
// clipboard path: App.Clipboard().SetContent must publish CLIPBOARD on the
// X server even when no Fyne window has been shown, so a second X11 client
// can read the text. It failed while clipboard.go stored content only in
// process memory whenever no shown window implemented clipboardSurface.
func TestAppClipboardWithoutWindowSecondClient(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("purego/x11: Wayland session, X11 hidden clipboard is not used")
	}
	if os.Getenv("DISPLAY") == "" {
		t.Skip("purego/x11: DISPLAY unset")
	}

	d := NewDriver()
	clip := NewClipboard()
	installClipboardApp(t, d, clip)

	forgetHiddenClipboard()
	before := countX11ClientSockets(t)
	const want = "purego app clipboard without window"
	fyne.CurrentApp().Clipboard().SetContent(want)
	afterSet := countX11ClientSockets(t)
	if afterSet != before+1 {
		t.Fatalf("hidden CLIPBOARD owner sockets: before=%d after SetContent=%d, want exactly one extra (one per surface)", before, afterSet)
	}

	client := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = client.Close() })

	got, err := client.ClipboardText()
	if err != nil {
		t.Fatalf("second client ClipboardText: %v", err)
	}
	if got != want {
		t.Fatalf("second client read %q, want %q", got, want)
	}
}
