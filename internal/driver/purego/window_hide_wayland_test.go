//go:build purego && linux

package purego

import (
	"image/color"
	"os"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

// TestWindowHideShowWaylandUnderCage checks Hide then Show against a real
// compositor: neither call errors or blocks, and an xdg_surface.configure
// is observed after Show. This failed while Hide was a no-op and Show
// returned immediately for an already-opened surface.
func TestWindowHideShowWaylandUnderCage(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("purego/wayland: WAYLAND_DISPLAY unset, Wayland tests run in the Wayland suite only")
	}
	test.NewApp()
	d := newTestDriver()
	w := d.CreateWindow("hide-show-wl").(*window)
	w.SetContent(fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 10, B: 20, A: 255}))
	w.Resize(fyne.NewSize(64, 48))
	w.Show()
	t.Cleanup(w.Close)
	ws, ok := w.surface.(*waylandSurface)
	if !ok {
		t.Fatalf("surface is %T, want *waylandSurface", w.surface)
	}

	done := make(chan error, 1)
	go func() {
		w.Hide()
		w.Show()
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Hide/Show: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Hide then Show blocked for 5s")
	}

	ws.mu.Lock()
	configured := ws.configured
	ws.mu.Unlock()
	if !configured {
		t.Fatal("surface was not configured after Hide then Show")
	}
}
