package software_test

import (
	"image"
	"image/color"
	"image/draw"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/internal/painter/software"
	"fyne.io/fyne/v2/test"
)

func regionParityScene(t *testing.T) (fyne.Canvas, *software.Painter) {
	t.Helper()
	test.ApplyTheme(t, test.Theme())
	left := canvas.NewRectangle(color.NRGBA{R: 200, G: 40, B: 40, A: 255})
	left.Resize(fyne.NewSize(100, 100))
	left.Move(fyne.NewPos(0, 0))
	right := canvas.NewRectangle(color.NRGBA{R: 40, G: 40, B: 200, A: 255})
	right.Resize(fyne.NewSize(100, 100))
	right.Move(fyne.NewPos(100, 0))
	label := canvas.NewText("region", color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	label.Resize(fyne.NewSize(80, 20))
	label.Move(fyne.NewPos(10, 10))
	img := canvas.NewImageFromImage(makeTestImage(8, 8))
	img.Resize(fyne.NewSize(40, 40))
	img.Move(fyne.NewPos(120, 30))
	c := test.NewCanvas()
	c.SetPadded(false)
	c.SetContent(container.NewWithoutLayout(left, right, label, img))
	c.Resize(fyne.NewSize(200, 100))
	return c, software.NewPainter()
}

func copyNRGBA(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Rect)
	draw.Draw(dst, dst.Rect, src, src.Rect.Min, draw.Src)
	return dst
}

func asNRGBA(t *testing.T, img image.Image) *image.NRGBA {
	t.Helper()
	n, ok := img.(*image.NRGBA)
	if !ok {
		t.Fatalf("Paint returned %T, want *image.NRGBA", img)
	}
	return n
}

func assertRegionParity(t *testing.T, full, got *image.NRGBA, region image.Rectangle, original *image.NRGBA) {
	t.Helper()
	bounds := full.Bounds()
	region = region.Intersect(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			g := got.NRGBAAt(x, y)
			if image.Pt(x, y).In(region) {
				w := full.NRGBAAt(x, y)
				if g != w {
					t.Fatalf("pixel (%d,%d) in region: got %+v want %+v", x, y, g, w)
				}
				continue
			}
			w := original.NRGBAAt(x, y)
			if g != w {
				t.Fatalf("pixel (%d,%d) outside region changed: got %+v want %+v", x, y, g, w)
			}
		}
	}
}

func TestPaintRegionParity(t *testing.T) {
	c, p := regionParityScene(t)
	full := asNRGBA(t, p.Paint(c))

	regions := map[string]image.Rectangle{
		"corner": full.Bounds().Intersect(image.Rect(0, 0, 40, 30)),
		"band":   full.Bounds().Intersect(image.Rect(0, 40, full.Bounds().Dx(), 60)),
		"full":   full.Bounds(),
		"empty":  image.Rect(0, 0, 0, 0),
	}
	for name, region := range regions {
		t.Run(name, func(t *testing.T) {
			base := copyNRGBA(full)
			draw.Draw(base, region, image.NewUniform(color.Transparent), image.Point{}, draw.Src)
			original := copyNRGBA(base)
			p.PaintRegion(c, base, region)
			assertRegionParity(t, full, base, region, original)
		})
	}
}

func TestPaintRegionEmptyLeavesPixels(t *testing.T) {
	c, p := regionParityScene(t)
	full := asNRGBA(t, p.Paint(c))
	base := copyNRGBA(full)
	base.SetNRGBA(10, 10, color.NRGBA{R: 9, G: 8, B: 7, A: 255})
	p.PaintRegion(c, base, image.Rectangle{})
	if got := base.NRGBAAt(10, 10); got != (color.NRGBA{R: 9, G: 8, B: 7, A: 255}) {
		t.Fatalf("empty region mutated pixel: %+v", got)
	}
}

func regionBenchCanvas() (fyne.Canvas, *software.Painter) {
	const width, height float32 = 1280, 800
	objs := make([]fyne.CanvasObject, 0, 41)
	cellW, cellH := width/8, height/5
	for row := 0; row < 5; row++ {
		for col := 0; col < 8; col++ {
			r := canvas.NewRectangle(color.NRGBA{R: uint8(col * 20), G: uint8(row * 40), B: 80, A: 255})
			r.Resize(fyne.NewSize(cellW, cellH))
			r.Move(fyne.NewPos(float32(col)*cellW, float32(row)*cellH))
			objs = append(objs, r)
		}
	}
	txt := canvas.NewText("bench-region", color.White)
	txt.TextSize = 18
	txt.Move(fyne.NewPos(20, 20))
	txt.Resize(fyne.NewSize(200, 30))
	objs = append(objs, txt)
	c := test.NewCanvas()
	c.SetPadded(false)
	c.SetContent(container.NewWithoutLayout(objs...))
	c.Resize(fyne.NewSize(width, height))
	return c, software.NewPainter()
}

func BenchmarkPaintFull_1280x800(b *testing.B) {
	c, p := regionBenchCanvas()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Paint(c)
	}
}

func BenchmarkPaintRegion10pct_1280x800(b *testing.B) {
	c, p := regionBenchCanvas()
	full := p.Paint(c).(*image.NRGBA)
	region := image.Rect(0, 0, 1280, 80) // 10 % of 1280×800
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.PaintRegion(c, full, region)
	}
}
