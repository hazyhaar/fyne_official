//go:build purego && linux

package purego

import (
	"image"
	"image/color"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPresentDoesNotRaceClose guards against a regression once seen in this code:
// when presentShm read and mutated s.shmSeg/s.shmXID/
// s.shmSize, and wrote shm requests on s.conn, all without holding s.mu,
// while Close read and tore down the same fields under a different,
// inconsistent locking discipline. Driving concurrent Present and Close
// calls under the race detector reproduces the data race directly; both
// functions now hold s.mu for their entire body (see their doc comments in
// x11.go), so this test is expected to pass under -race.
// countShmSegments returns the number of SysV SHM segments ipcs lists, so a
// test can assert that a surface lifecycle leaves none behind.
func countShmSegments(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("ipcs", "-m").Output()
	if err != nil {
		t.Skipf("ipcs -m: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 6 {
			if _, err := strconv.Atoi(f[1]); err == nil {
				n++
			}
		}
	}
	return n
}

func TestPresentDoesNotRaceClose(t *testing.T) {
	segmentsBefore := countShmSegments(t)
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for {
			after := countShmSegments(t)
			if after <= segmentsBefore {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d SysV SHM segments leaked by racing Present against Close", after-segmentsBefore)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	const w, h = 48, 32
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fill := color.NRGBA{R: 1, G: 2, B: 3, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill)
		}
	}

	const rounds = 20
	for i := 0; i < rounds; i++ {
		s, err := openX11("", w, h)
		if err != nil {
			t.Fatalf("openX11: %v", err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = s.Present(img)
			}
		}()
		go func() {
			defer wg.Done()
			_ = s.Close()
		}()
		wg.Wait()
	}
}
