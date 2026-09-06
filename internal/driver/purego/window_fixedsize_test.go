//go:build purego && linux

package purego

import (
	"encoding/binary"
	"fmt"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

const (
	sizeHintPMinSizeTest      = 16
	sizeHintPMaxSizeTest      = 32
	sizeHintMinWidthIndex     = 5
	sizeHintMinHeightIndex    = 6
	sizeHintMaxWidthIndex     = 7
	sizeHintMaxHeightIndex    = 8
	sizeHintWantFlags         = sizeHintPMinSizeTest | sizeHintPMaxSizeTest
	atomWMSizeHintsPredefined = 41
)

type wmSizeHints struct {
	flags               uint32
	minWidth, minHeight int
	maxWidth, maxHeight int
}

func readWMNormalHints(s *x11Surface, window, property uint32) (wmSizeHints, error) {
	req := make([]byte, getPropertyReqLen)
	req[0] = opGetProperty
	binary.LittleEndian.PutUint16(req[2:4], getPropertyReqWords)
	binary.LittleEndian.PutUint32(req[4:8], window)
	binary.LittleEndian.PutUint32(req[8:12], property)
	binary.LittleEndian.PutUint32(req[12:16], atomWMSizeHintsPredefined)
	binary.LittleEndian.PutUint32(req[16:20], 0)
	binary.LittleEndian.PutUint32(req[20:24], getPropertyMaxWords)

	reply, err := s.request(req)
	if err != nil {
		return wmSizeHints{}, err
	}
	if reply[0] != replyStatusReply {
		return wmSizeHints{}, fmt.Errorf("GetProperty status %d", reply[0])
	}
	_, extra, err := parseGetPropertyReply(reply)
	if err != nil {
		return wmSizeHints{}, err
	}
	need := (sizeHintMaxHeightIndex + 1) * 4
	if len(extra) < need {
		return wmSizeHints{}, fmt.Errorf("WM_NORMAL_HINTS too short: %d bytes", len(extra))
	}
	u32 := func(i int) uint32 {
		return binary.LittleEndian.Uint32(extra[i*4 : i*4+4])
	}
	return wmSizeHints{
		flags:     u32(0),
		minWidth:  int(u32(sizeHintMinWidthIndex)),
		minHeight: int(u32(sizeHintMinHeightIndex)),
		maxWidth:  int(u32(sizeHintMaxWidthIndex)),
		maxHeight: int(u32(sizeHintMaxHeightIndex)),
	}, nil
}

// TestSetFixedSizeWritesWMNormalHints checks that SetFixedSize(true) publishes
// ICCCM WM_NORMAL_HINTS with PMinSize|PMaxSize equal to the current size, and
// that a later Resize while still fixed updates both extents. This failed
// while SetFixedSize only stored a Go bool.
func TestSetFixedSizeWritesWMNormalHints(t *testing.T) {
	test.NewApp()
	d := newTestDriver()
	w := d.CreateWindow("fixed-size").(*window)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)
	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatalf("surface is %T, want *x11Surface", w.surface)
	}

	w.SetFixedSize(true)

	peer := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = peer.Close() })
	atom, err := peer.internAtom("WM_NORMAL_HINTS")
	if err != nil {
		t.Fatalf("internAtom(WM_NORMAL_HINTS): %v", err)
	}

	hints, err := readWMNormalHints(peer, xs.winID, atom)
	if err != nil {
		t.Fatalf("reading WM_NORMAL_HINTS: %v", err)
	}
	if hints.flags&sizeHintWantFlags != sizeHintWantFlags {
		t.Fatalf("WM_NORMAL_HINTS flags = %#x, want PMinSize|PMaxSize (%#x)", hints.flags, sizeHintWantFlags)
	}
	if hints.minWidth != 200 || hints.minHeight != 100 || hints.maxWidth != 200 || hints.maxHeight != 100 {
		t.Fatalf("WM_NORMAL_HINTS min=%dx%d max=%dx%d, want 200x100 for both",
			hints.minWidth, hints.minHeight, hints.maxWidth, hints.maxHeight)
	}

	w.Resize(fyne.NewSize(240, 120))
	hints, err = readWMNormalHints(peer, xs.winID, atom)
	if err != nil {
		t.Fatalf("reading WM_NORMAL_HINTS after Resize: %v", err)
	}
	if hints.minWidth != 240 || hints.minHeight != 120 || hints.maxWidth != 240 || hints.maxHeight != 120 {
		t.Fatalf("WM_NORMAL_HINTS after Resize min=%dx%d max=%dx%d, want 240x120 for both",
			hints.minWidth, hints.minHeight, hints.maxWidth, hints.maxHeight)
	}
}
