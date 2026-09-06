//go:build purego

package purego

import (
	"image"
	"testing"
)

// BenchmarkToNRGBAPresentPath exercises image.go's toNRGBA, the smallest
// of the three functions probeStage instruments. toNRGBA is a handful of
// bounds checks and, on the fast path, a single type assertion, so any
// per-call probeStage overhead shows up here in relative terms first.
func BenchmarkToNRGBAPresentPath(b *testing.B) {
	img := image.NewNRGBA(image.Rect(0, 0, 1280, 800))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = toNRGBA(img)
	}
}
