//go:build purego

package purego

import (
	"image"
	"testing"
	"time"
)

// TestShowLeavesNoOrphanServerError pins the RandR subscription: a
// mis-numbered RRSelectInput once drew a BadLength error nobody waited for
// on every window shown, and the screen-change watch was silently inactive.
func TestShowLeavesNoOrphanServerError(t *testing.T) {
	s := openX11ForTest(t, 200, 100)
	t.Cleanup(func() { _ = s.Close() })
	subscribeRandRScreenChange(s)
	if err := s.Present(image.NewNRGBA(image.Rect(0, 0, 200, 100))); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if _, err := s.internAtom("PUREGO_RANDR_SYNC"); err != nil {
		t.Fatalf("InternAtom: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := s.droppedErrors.Load(); n != 0 {
		t.Fatalf("%d server error(s) answered a fire-and-forget request after Show", n)
	}
}
