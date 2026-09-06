//go:build purego && linux

package purego

import (
	"image"
	"image/color"
	"os"
	"testing"
	"time"
)

// TestWaylandOpenPresentConfigure opens a surface against the compositor
// named by $WAYLAND_DISPLAY, presents one solid-color frame and waits for
// the resizeEvent a real compositor sends through xdg_toplevel.configure.
// It assumes no particular window size: the fullscreen-forcing compositor
// used to run this test (cage) grants whatever size it wants.
func TestWaylandOpenPresentConfigure(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("purego/wayland: WAYLAND_DISPLAY unset, Wayland tests run in the Wayland suite only")
	}
	s, err := newWaylandSurface(320, 240)
	if err != nil {
		t.Skipf("purego/wayland: no compositor reachable: %v", err)
	}
	defer s.Close()

	img := image.NewNRGBA(image.Rect(0, 0, 320, 240))
	fill := color.NRGBA{R: 0x20, G: 0x80, B: 0xc0, A: 0xff}
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, fill)
		}
	}
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if r, ok := ev.(resizeEvent); ok {
				if r.Width <= 0 || r.Height <= 0 {
					t.Fatalf("resizeEvent reported non-positive size %dx%d", r.Width, r.Height)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for a configure/resizeEvent from the compositor")
		}
	}
}
