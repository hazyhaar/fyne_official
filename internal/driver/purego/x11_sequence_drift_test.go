//go:build purego && linux

package purego

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// TestSequenceStaysInStepAfterFireAndForgetRequests: every request written to
// the server advances the client's sequence counter, or the reply to the next
// synchronous request is routed to a number nobody waits for and that call
// blocks forever. Fullscreen, icon and cursor changes are fire-and-forget
// requests that used to skip the counter.
func TestSequenceStaysInStepAfterFireAndForgetRequests(t *testing.T) {
	s, err := openX11(os.Getenv("DISPLAY"), 64, 48)
	if err != nil {
		t.Skipf("openX11: %v", err)
	}
	defer s.Close()
	_ = s.SetFullscreen(true)
	_ = s.SetFullscreen(false)
	_ = s.SetCursor(desktop.TextCursor)
	icon := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := range icon.Pix {
		icon.Pix[i] = 0xff
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, icon); err != nil {
		t.Fatal(err)
	}
	_ = s.SetWindowIcon(fyne.NewStaticResource("icon.png", buf.Bytes()))
	_ = s.Present(image.NewNRGBA(image.Rect(0, 0, 64, 48)))

	done := make(chan error, 1)
	go func() { _, err := s.internAtom("WM_NAME"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InternAtom after fire-and-forget requests: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("InternAtom reply never routed: the sequence counter drifted from the server's")
	}
}
