//go:build purego && linux

package purego

import (
	"image"
	"image/color"
	"os"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestPresentRegionUpdatesOnlyRect(t *testing.T) {
	const width, height = 64, 48
	s, err := openX11(os.Getenv("DISPLAY"), width, height)
	if err != nil {
		t.Skipf("openX11: %v", err)
	}
	defer s.Close()

	red := color.NRGBA{R: 200, G: 10, B: 10, A: 255}
	blue := color.NRGBA{R: 10, G: 10, B: 200, A: 255}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, red)
		}
	}
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}

	region := image.Rect(8, 6, 24, 22)
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			img.SetNRGBA(x, y, blue)
		}
	}
	if err := s.PresentRegion(img, region); err != nil {
		t.Fatalf("PresentRegion: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	inside, err := getImagePixel(s, 12, 10)
	if err != nil {
		t.Fatalf("inside pixel: %v", err)
	}
	if inside != blue {
		t.Fatalf("inside region = %+v, want %+v", inside, blue)
	}
	outside, err := getImagePixel(s, 40, 30)
	if err != nil {
		t.Fatalf("outside pixel: %v", err)
	}
	if outside != red {
		t.Fatalf("outside region = %+v, want %+v", outside, red)
	}
}

func TestHarnessRegionParityAfterLocalMutations(t *testing.T) {
	test.NewApp()
	const width, height = 240, 180

	d := newTestDriver()
	w := d.CreateWindow("harness-region").(*window)
	label := widget.NewLabel("one")
	rect := fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 40, B: 40, A: 255})
	rect.SetMinSize(fyne.NewSize(60, 40))
	w.SetContent(container.NewVBox(label, rect))
	w.Resize(fyne.NewSize(width, height))
	w.Show()
	t.Cleanup(w.Close)

	w.canvas.EnsureMinSize()
	w.canvas.SetDirty()
	w.redrawIfDirty()

	label.SetText("two")
	w.canvas.Refresh(label)
	w.redrawIfDirty()
	rect.FillColor = color.NRGBA{R: 40, G: 200, B: 40, A: 255}
	w.canvas.Refresh(rect)
	w.redrawIfDirty()
	time.Sleep(1 * time.Second)

	sync := dialSyncX11(t)
	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatalf("surface is %T, want *x11Surface", w.surface)
	}
	got, err := getImageFull(sync.conn, xs.winID, width, height)
	if err != nil {
		t.Fatalf("getImageFull: %v", err)
	}
	want := toNRGBA(w.canvas.Capture())
	if want == nil {
		t.Fatal("Capture returned nil")
	}
	mismatches := 0
	var firstX, firstY int
	var firstGot, firstWant color.NRGBA
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			g := got.NRGBAAt(x, y)
			wpix := want.NRGBAAt(x, y)
			if g.R != wpix.R || g.G != wpix.G || g.B != wpix.B {
				if mismatches == 0 {
					firstX, firstY, firstGot, firstWant = x, y, g, wpix
				}
				mismatches++
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d/%d pixels differ after local mutations; first at (%d,%d): got %+v want %+v",
			mismatches, width*height, firstX, firstY, firstGot, firstWant)
	}
}
