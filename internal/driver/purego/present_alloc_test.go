//go:build purego && linux

package purego

import (
	"image"
	"os"
	"runtime"
	"testing"
)

// TestPresentReusesStagingBuffer checks that two Present calls of the same
// size do not allocate a new backing buffer each time: the MIT-SHM segment
// when it is available (Present converts directly into it — see
// presentShm's doc comment in x11.go, avoiding a double
// copy), or the PutImage fallback's staging buffer (s.pixels) otherwise.
func TestPresentReusesStagingBuffer(t *testing.T) {
	s, err := openX11(os.Getenv("DISPLAY"), 64, 48)
	if err != nil {
		t.Skipf("openX11: %v", err)
	}
	defer s.Close()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	if err := s.Present(img); err != nil {
		t.Fatalf("first Present: %v", err)
	}

	usingShm := s.shm != nil && s.shm.available
	var firstShmAddr *byte
	var firstPixelsAddr *byte
	if usingShm {
		if sl := s.shmSeg.Slice(); len(sl) > 0 {
			firstShmAddr = &sl[0]
		}
	} else if len(s.pixels) > 0 {
		firstPixelsAddr = &s.pixels[0]
	}

	// The property under test is "no frame-sized buffer per Present": count
	// bytes, not objects, since the wire path legitimately allocates a few
	// small request buffers per call.
	const runs = 20
	runtime.GC() //nolint:revive // call-to-gc: settle the heap before measuring allocation deltas
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		if err := s.Present(img); err != nil {
			t.Fatalf("Present: %v", err)
		}
	}
	runtime.ReadMemStats(&after)
	bytesPerCall := float64(after.TotalAlloc-before.TotalAlloc) / runs
	frameBytes := float64(img.Rect.Dx() * img.Rect.Dy() * 4)

	if usingShm {
		if sl := s.shmSeg.Slice(); firstShmAddr != nil && (len(sl) == 0 || &sl[0] != firstShmAddr) {
			t.Fatal("MIT-SHM segment was reallocated between same-size frames")
		}
	} else if firstPixelsAddr != nil && (len(s.pixels) == 0 || &s.pixels[0] != firstPixelsAddr) {
		t.Fatal("staging buffer was reallocated between same-size frames")
	}
	if bytesPerCall > frameBytes/2 {
		t.Fatalf("Present allocates %.0f bytes per call, more than half a %.0f-byte frame: a frame buffer is being reallocated", bytesPerCall, frameBytes)
	}
}
