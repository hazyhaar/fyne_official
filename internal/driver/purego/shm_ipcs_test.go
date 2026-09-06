//go:build purego && linux

package purego

import (
	"image"
	"image/color"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestShmSegmentLeavesNoResidueAfterClose guards against a regression once seen in this code:
// server had actually attached it, and shmctl/shmdt return codes discarded,
// left a real risk of a residual segment surviving this process's exit. It
// drives a real Present (forcing the MIT-SHM path, allocating a genuine SysV
// segment through shmAttachAndConfirm/markShmForDestruction), records the
// segment's ID, closes the surface, and shells out to ipcs -m — the actual
// kernel-visible state, not this process's own bookkeeping — to confirm the
// ID is no longer listed.
func TestShmSegmentLeavesNoResidueAfterClose(t *testing.T) {
	if _, err := exec.LookPath("ipcs"); err != nil {
		t.Skip("ipcs not available in this environment")
	}

	const w, h = 64, 48
	s, err := openX11("", w, h)
	if err != nil {
		t.Fatalf("openX11: %v", err)
	}
	if s.shm == nil || !s.shm.available {
		t.Skip("MIT-SHM not available against this X server")
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fill := color.NRGBA{R: 9, G: 8, B: 7, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill)
		}
	}
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if s.shmSeg == nil {
		t.Fatal("Present used the MIT-SHM path but recorded no segment")
	}
	shmid := strconv.Itoa(s.shmSeg.ShmID)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The X server detaches its side when it processes ShmDetach, which on a
	// loaded host can take well over 100 ms: poll until the id is gone,
	// bounded by a deadline.
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, err := exec.Command("ipcs", "-m").CombinedOutput()
		if err != nil {
			t.Fatalf("ipcs -m: %v (%s)", err, out)
		}
		listed := ""
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[1] == shmid {
				listed = line
			}
		}
		if listed == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("shmid %s still listed by ipcs -m 2s after Close:\n%s\nfull output:\n%s", shmid, listed, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
