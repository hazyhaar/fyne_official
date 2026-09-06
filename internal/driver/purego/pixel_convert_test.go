//go:build purego && linux

package purego

import (
	"math/rand"
	"testing"
)

// convertRowRGBAToBGRXScalar is the old byte-by-byte reference
// implementation, kept here only as a test oracle
// to prove convertRowRGBAToBGRX's word-based version produces bit-identical
// output.
func convertRowRGBAToBGRXScalar(dst, src []byte, w int) {
	for x := 0; x < w; x++ {
		r, g, b, a := src[x*4], src[x*4+1], src[x*4+2], src[x*4+3]
		dst[x*4+0] = b
		dst[x*4+1] = g
		dst[x*4+2] = r
		dst[x*4+3] = a
	}
}

// TestConvertRowRGBAToBGRXMatchesScalar is the pixel-parity test the
// performance deliverable requires: the word-based conversion
// (convertRowRGBAToBGRX) must produce byte-identical output to the old
// scalar path for every pixel, across random data and both even and odd
// pixel counts (word operations are a classic place for an off-by-one at
// the row's last pixel).
func TestConvertRowRGBAToBGRXMatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // fixed seed for reproducible pixel-parity data, not a secret
	for _, w := range []int{0, 1, 2, 3, 4, 7, 16, 17, 3840} {
		src := make([]byte, w*4)
		_, _ = rng.Read(src)
		want := make([]byte, w*4)
		got := make([]byte, w*4)
		convertRowRGBAToBGRXScalar(want, src, w)
		convertRowRGBAToBGRX(got, src, w)
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("w=%d: byte %d differs: word-based=%#x, scalar=%#x", w, i, got[i], want[i])
			}
		}
	}
}

// BenchmarkConvertRowRGBAToBGRX_4K and its Scalar counterpart measure the
// per-frame conversion cost at F4 4K resolution (3840x2160), the scenario
// the strata bench's F4 cell (15ms budget) both target, letting
// `go test -bench` show the before/after of the word-wise conversion.
const (
	benchFrameWidth  = 3840
	benchFrameHeight = 2160
)

func BenchmarkConvertRowRGBAToBGRX_4K(b *testing.B) {
	src := make([]byte, benchFrameWidth*4)
	dst := make([]byte, benchFrameWidth*4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for y := 0; y < benchFrameHeight; y++ {
			convertRowRGBAToBGRX(dst, src, benchFrameWidth)
		}
	}
}

func BenchmarkConvertRowRGBAToBGRXScalar_4K(b *testing.B) {
	src := make([]byte, benchFrameWidth*4)
	dst := make([]byte, benchFrameWidth*4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for y := 0; y < benchFrameHeight; y++ {
			convertRowRGBAToBGRXScalar(dst, src, benchFrameWidth)
		}
	}
}
