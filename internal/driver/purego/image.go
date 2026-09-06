//go:build purego

package purego

import (
	"image"
	"image/draw"
)

// toNRGBA converts the software rasterizer's output to *image.NRGBA, the
// format x11Surface.Present expects. internal/painter/software already
// produces *image.NRGBA directly (see internal/painter/software/painter.go),
// so the common case is a type assertion; the draw fallback only runs for a
// painter.Painter implementation that returns a different concrete type.
func toNRGBA(img image.Image) *image.NRGBA {
	probeStage(StageF3Start)
	defer probeStage(StageF3End)
	if img == nil {
		return nil
	}
	if n, ok := img.(*image.NRGBA); ok {
		return n
	}
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	draw.Draw(out, bounds, img, bounds.Min, draw.Src)
	return out
}
