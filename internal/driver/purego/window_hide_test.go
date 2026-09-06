//go:build purego && linux

package purego

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

const (
	opGetWindowAttributesTest = 3
	getWindowAttributesWords  = 2
	mapStateUnmappedTest      = 0
	mapStateViewableTest      = 2
	mapStateByteOffset        = 26
)

func readMapState(s *x11Surface, window uint32) (byte, error) {
	req := make([]byte, getWindowAttributesReqLen)
	req[0] = opGetWindowAttributesTest
	binary.LittleEndian.PutUint16(req[2:4], getWindowAttributesWords)
	binary.LittleEndian.PutUint32(req[4:8], window)

	reply, err := s.request(req)
	if err != nil {
		return 0, err
	}
	if len(reply) <= mapStateByteOffset {
		return 0, errors.New("GetWindowAttributes reply too short")
	}
	if reply[0] != replyStatusReply {
		return 0, fmt.Errorf("GetWindowAttributes status %d", reply[0])
	}
	return reply[mapStateByteOffset], nil
}

func waitMapState(t *testing.T, s *x11Surface, window uint32, want byte, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var last byte
	var err error
	for time.Now().Before(deadline) {
		last, err = readMapState(s, window)
		if err == nil && last == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GetWindowAttributes: %v (last map_state=%d, want %d)", err, last, want)
	}
	t.Fatalf("map_state = %d, want %d", last, want)
}

// TestWindowHideShowRemapX11 checks Hide unmaps the native window (map_state
// IsUnmapped) and a later Show maps it again (IsViewable) and re-presents
// the canvas frame. This failed while Hide was a documented no-op and Show
// returned early once w.shown was set.
func TestWindowHideShowRemapX11(t *testing.T) {
	test.NewApp()
	d := newTestDriver()
	w := d.CreateWindow("hide-show").(*window)
	red := color.NRGBA{R: 200, G: 10, B: 20, A: 255}
	rect := fynecanvas.NewRectangle(red)
	w.SetContent(rect)
	w.Resize(fyne.NewSize(64, 48))
	w.Show()
	t.Cleanup(w.Close)
	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatalf("surface is %T, want *x11Surface", w.surface)
	}
	w.canvas.SetDirty()
	w.redrawIfDirty()

	peer := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = peer.Close() })

	waitMapState(t, peer, xs.winID, mapStateViewableTest, 2*time.Second)

	got, err := getImagePixel(xs, 32, 24)
	if err != nil {
		t.Fatalf("pixel after first present: %v", err)
	}
	if got.R < 100 {
		t.Fatalf("center pixel after first present = %+v, want a red canvas frame", got)
	}

	w.Hide()
	waitMapState(t, peer, xs.winID, mapStateUnmappedTest, 2*time.Second)

	blue := color.NRGBA{R: 10, G: 20, B: 200, A: 255}
	img := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.SetNRGBA(x, y, blue)
		}
	}
	if err := xs.Present(img); err != nil {
		t.Fatalf("Present while hidden: %v", err)
	}

	w.Show()
	waitMapState(t, peer, xs.winID, mapStateViewableTest, 2*time.Second)

	got, err = getImagePixel(xs, 32, 24)
	if err != nil {
		t.Fatalf("pixel after Show: %v", err)
	}
	if got.B > got.R {
		t.Fatalf("center pixel after Show = %+v, want the canvas frame re-presented (red), not the blue Present issued while hidden", got)
	}
	if got.R < 100 {
		t.Fatalf("center pixel after Show = %+v, want a re-presented red canvas frame", got)
	}
}
