//go:build purego

package purego

import (
	"image"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
)

func twoRectCanvas() (c *canvas, left, right *fynecanvas.Rectangle) {
	c = newCanvas()
	c.padded = false
	left = fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 20, B: 20, A: 255})
	left.Resize(fyne.NewSize(80, 80))
	left.Move(fyne.NewPos(0, 0))
	right = fynecanvas.NewRectangle(color.NRGBA{R: 20, G: 20, B: 200, A: 255})
	right.Resize(fyne.NewSize(80, 80))
	right.Move(fyne.NewPos(80, 0))
	c.SetContent(container.NewWithoutLayout(left, right))
	c.Resize(fyne.NewSize(160, 80))
	return c, left, right
}

func TestRefreshAccumulatesObjectBounds(t *testing.T) {
	test.NewApp()
	c, left, _ := twoRectCanvas()
	c.CheckDirtyAndClear()
	c.takeDirty(c.pixelBounds())

	c.Refresh(left)
	c.drainRefreshQueue()
	if c.dirtyAll.Load() {
		t.Fatal("Refresh(obj) marked the whole canvas dirty")
	}
	if c.dirtyR.Empty() {
		t.Fatal("Refresh(obj) did not accumulate a dirty rectangle")
	}
	if c.dirtyR.Dx() > 90 || c.dirtyR.Dy() > 90 {
		t.Fatalf("dirty rectangle %v is far larger than the 80x80 object", c.dirtyR)
	}
}

func TestSetDirtyMarksWholeCanvas(t *testing.T) {
	test.NewApp()
	c, left, _ := twoRectCanvas()
	c.CheckDirtyAndClear()
	c.takeDirty(c.pixelBounds())
	c.Refresh(left)
	c.SetDirty()
	if !c.dirtyAll.Load() {
		t.Fatal("SetDirty did not mark the whole canvas")
	}
	_, full := c.takeDirty(c.pixelBounds())
	if !full {
		t.Fatal("SetDirty did not consume as a full-canvas dirty region")
	}
}

func TestRenderDirtyRegionMatchesFullCapture(t *testing.T) {
	test.NewApp()
	c, left, _ := twoRectCanvas()
	if !c.CheckDirtyAndClear() {
		t.Fatal("new content should be dirty")
	}
	if _, _, full := c.renderDirty(); !full {
		t.Fatal("first paint should be a full capture")
	}

	left.FillColor = color.NRGBA{R: 20, G: 200, B: 20, A: 255}
	c.Refresh(left)
	if !c.CheckDirtyAndClear() {
		t.Fatal("Refresh should mark dirty")
	}
	got, region, full := c.renderDirty()
	if full {
		t.Fatal("local Refresh should take the region path")
	}
	if region.Empty() {
		t.Fatal("region path returned an empty rectangle")
	}
	want := c.Capture()
	gn, ok := got.(*image.NRGBA)
	if !ok {
		t.Fatalf("renderDirty returned %T", got)
	}
	wn, ok := want.(*image.NRGBA)
	if !ok {
		t.Fatalf("Capture returned %T", want)
	}
	if gn.Rect != wn.Rect {
		t.Fatalf("size %v vs %v", gn.Rect, wn.Rect)
	}
	for y := gn.Rect.Min.Y; y < gn.Rect.Max.Y; y++ {
		for x := gn.Rect.Min.X; x < gn.Rect.Max.X; x++ {
			if gn.NRGBAAt(x, y) != wn.NRGBAAt(x, y) {
				t.Fatalf("pixel (%d,%d): got %+v want %+v", x, y, gn.NRGBAAt(x, y), wn.NRGBAAt(x, y))
			}
		}
	}
}

type recordSurface struct {
	full    int
	regions []image.Rectangle
}

func (r *recordSurface) Present(*image.NRGBA) error { r.full++; return nil }
func (r *recordSurface) PresentRegion(_ *image.NRGBA, rect image.Rectangle) error {
	r.regions = append(r.regions, rect)
	return nil
}
func (*recordSurface) Events() <-chan surfaceEvent { return nil }
func (*recordSurface) Resize(int, int) error       { return nil }
func (*recordSurface) SetTitle(string) error       { return nil }
func (*recordSurface) Close() error                { return nil }

func TestRedrawIfDirtyPresentsRegionAfterLocalMutation(t *testing.T) {
	test.NewApp()
	rec := &recordSurface{}
	w := newWindow("region")
	w.surface = rec
	w.canvas.padded = false
	left := fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 20, B: 20, A: 255})
	left.Resize(fyne.NewSize(80, 80))
	left.Move(fyne.NewPos(0, 0))
	right := fynecanvas.NewRectangle(color.NRGBA{R: 20, G: 20, B: 200, A: 255})
	right.Resize(fyne.NewSize(80, 80))
	right.Move(fyne.NewPos(80, 0))
	w.canvas.SetContent(container.NewWithoutLayout(left, right))
	w.canvas.Resize(fyne.NewSize(160, 80))
	w.redrawIfDirty()
	if rec.full != 1 {
		t.Fatalf("first redraw Present count = %d, want 1", rec.full)
	}

	left.FillColor = color.NRGBA{R: 20, G: 200, B: 20, A: 255}
	w.canvas.Refresh(left)
	w.redrawIfDirty()
	if rec.full != 1 {
		t.Fatalf("local mutation triggered a full Present (count %d)", rec.full)
	}
	if len(rec.regions) != 1 {
		t.Fatalf("PresentRegion calls = %d, want 1", len(rec.regions))
	}
	if rec.regions[0].Empty() {
		t.Fatal("PresentRegion was called with an empty rectangle")
	}
}
