//go:build purego

package purego

import (
	"image"
	"image/color"
	"runtime"
	"sync"
	"testing"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/internal/async"
	"fyne.io/fyne/v2/internal/cache"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

// TestCaptureFillsThemeBackground is the regression test for when
// canvas.Capture() called the software rasterizer
// directly and returned its output unmodified. internal/painter/software.
// Painter.Paint starts from a zero-valued (fully transparent) image and
// never paints a background itself — driver/software/canvas.go's own
// Capture does that explicitly before rasterizing — so a captured frame
// with no content set was fully transparent/black instead of
// theme.ColorNameBackground, breaking pixel parity with driver/software for
// the same scene. This test fails if Capture stops painting that
// background before compositing the object tree.
func TestCaptureFillsThemeBackground(t *testing.T) {
	test.NewApp() // theme.Color needs a current fyne.App for its variant lookup
	c := newCanvas()
	c.Resize(fyne.NewSize(64, 64))
	c.SetContent(fynecanvas.NewRectangle(theme.Color(theme.ColorNameBackground)))

	img := c.Capture()
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		t.Fatalf("Capture() returned %T, want *image.NRGBA", img)
	}

	// A corner pixel is outside the rectangle content only if padding is
	// applied; check the very first pixel, which the canvas background must
	// cover regardless of content layout.
	want := theme.Color(theme.ColorNameBackground)
	wr, wg, wb, wa := want.RGBA()
	gr, gg, gb, ga := nrgba.At(0, 0).RGBA()
	if gr != wr || gg != wg || gb != wb || ga != wa {
		t.Fatalf("corner pixel = %v, want theme background %v (Capture is not painting the background before rasterizing)", nrgba.At(0, 0), want)
	}
}

func TestEnsureMinSizeRegistersCanvas(t *testing.T) {
	test.NewApp()
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("canvas-for-object").(*window)
	rect := fynecanvas.NewRectangle(theme.Color(theme.ColorNameBackground))
	w.SetContent(rect)
	w.canvas.Resize(fyne.NewSize(64, 64))
	w.canvas.EnsureMinSize()
	if got := d.CanvasForObject(rect); got != w.canvas {
		t.Fatalf("CanvasForObject = %v, want the window canvas", got)
	}
	if got := cache.GetCanvasForObject(rect); got != w.canvas {
		t.Fatalf("cache canvas = %v, want the window canvas", got)
	}
}

func TestCanvasRefreshConcurrentWithRedraw(_ *testing.T) {
	test.NewApp()
	async.SetMainGoroutine()
	c := newCanvas()
	left := fynecanvas.NewRectangle(color.NRGBA{R: 200, A: 255})
	right := fynecanvas.NewRectangle(color.NRGBA{B: 200, A: 255})
	c.SetContent(container.NewWithoutLayout(left, right))
	c.Resize(fyne.NewSize(160, 80))
	c.EnsureMinSize()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					c.Refresh(left)
					c.Refresh(right)
					runtime.Gosched()
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		c.EnsureMinSize()
		if c.CheckDirtyAndClear() {
			c.renderDirty()
		}
	}
	close(stop)
	wg.Wait()
}

func TestCanvasCachesDropRemovedObjects(t *testing.T) {
	test.NewApp()
	async.SetMainGoroutine()
	c := newCanvas()
	objs := make([]fyne.CanvasObject, 1000)
	for i := range objs {
		r := fynecanvas.NewRectangle(color.NRGBA{A: 255})
		r.Resize(fyne.NewSize(10, 10))
		objs[i] = r
	}
	c.SetContent(container.NewWithoutLayout(objs...))
	c.Resize(fyne.NewSize(200, 200))
	c.EnsureMinSize()
	for _, obj := range objs {
		c.Refresh(obj)
	}
	c.drainRefreshQueue()
	if len(c.lastBounds) == 0 {
		t.Fatal("lastBounds empty after refreshing 1000 objects")
	}
	if len(c.minSizes) == 0 {
		t.Fatal("minSizes empty after EnsureMinSize on 1000 objects")
	}

	c.SetContent(fynecanvas.NewRectangle(color.NRGBA{A: 255}))
	if len(c.lastBounds) != 0 {
		t.Fatalf("lastBounds still holds %d entries after SetContent replaced the tree", len(c.lastBounds))
	}
	if len(c.minSizes) != 0 {
		t.Fatalf("minSizes still holds %d entries after SetContent replaced the tree", len(c.minSizes))
	}

	overlay := fynecanvas.NewRectangle(color.NRGBA{G: 200, A: 255})
	overlay.Resize(fyne.NewSize(20, 20))
	c.Overlays().Add(overlay)
	c.Refresh(overlay)
	c.drainRefreshQueue()
	if len(c.lastBounds) == 0 {
		t.Fatal("lastBounds empty after refreshing an overlay")
	}
	c.Overlays().Remove(overlay)
	if len(c.lastBounds) != 0 {
		t.Fatalf("lastBounds still holds %d entries after the overlay was removed", len(c.lastBounds))
	}

	c.SetContent(container.NewWithoutLayout(objs...))
	c.EnsureMinSize()
	for _, obj := range objs {
		c.Refresh(obj)
	}
	c.drainRefreshQueue()
	c.releaseCaches()
	if len(c.lastBounds) != 0 || len(c.minSizes) != 0 {
		t.Fatalf("caches after Close path: lastBounds=%d minSizes=%d, want empty", len(c.lastBounds), len(c.minSizes))
	}
}

func TestSetPaddedRelayoutsContent(t *testing.T) {
	test.NewApp()
	w := newWindow("padded")
	rect := fynecanvas.NewRectangle(color.NRGBA{A: 255})
	w.SetContent(rect)
	w.canvas.Resize(fyne.NewSize(100, 80))
	if rect.Position() == fyne.NewPos(0, 0) {
		t.Fatal("padded content started at (0,0); padding should offset it")
	}
	oldSize := rect.Size()
	w.SetPadded(false)
	if got := rect.Position(); got != fyne.NewPos(0, 0) {
		t.Fatalf("content position after SetPadded(false) = %v, want (0,0)", got)
	}
	if rect.Size() == oldSize {
		t.Fatalf("content size stayed %v after SetPadded(false); expected a relayout", oldSize)
	}
}

func TestMenuPaintCanvasContentNoAlloc(t *testing.T) {
	test.NewApp()
	c := newCanvas()
	c.SetContent(fynecanvas.NewRectangle(color.NRGBA{A: 255}))
	c.menu = fynecanvas.NewRectangle(color.NRGBA{R: 200, A: 255})
	c.Resize(fyne.NewSize(120, 80))
	m := &menuPaintCanvas{canvas: c}
	_ = m.Content()
	allocs := testing.AllocsPerRun(1000, func() {
		_ = m.Content()
	})
	if allocs != 0 {
		t.Fatalf("menuPaintCanvas.Content AllocsPerRun = %v, want 0", allocs)
	}
	runtime.KeepAlive(m)
}
