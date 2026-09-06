//go:build purego && linux

package purego

import (
	"encoding/binary"
	"image"
	"testing"

	"fyne.io/fyne/v2/driver/desktop"
)

func TestWaylandCursorShapeValues(t *testing.T) {
	cases := map[desktop.StandardCursor]uint32{
		desktop.DefaultCursor:    wpCursorShapeDefault,
		desktop.TextCursor:       wpCursorShapeText,
		desktop.CrosshairCursor:  wpCursorShapeCrosshair,
		desktop.PointerCursor:    wpCursorShapePointer,
		desktop.HResizeCursor:    wpCursorShapeEwResize,
		desktop.VResizeCursor:    wpCursorShapeNsResize,
		desktop.NESWResizeCursor: wpCursorShapeNeswResize,
		desktop.NWSEResizeCursor: wpCursorShapeNwseResize,
	}
	for c, want := range cases {
		if got := waylandCursorShape(c); got != want {
			t.Fatalf("waylandCursorShape(%v) = %d, want %d", c, got, want)
		}
	}
}

func TestWaylandSetCursorShapeSendsRequest(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:             client,
		done:             make(chan struct{}),
		nextID:           30,
		pointerID:        9,
		cursorShapeMgrID: 8,
	}
	t.Cleanup(func() { close(s.done) })
	st := waylandClipboardOf(s)
	st.mu.Lock()
	st.pointerSerial = 7
	st.mu.Unlock()

	if err := s.SetCursor(desktop.TextCursor); err != nil {
		t.Fatalf("SetCursor(TextCursor): %v", err)
	}

	obj, op, payload := readWlMessage(t, server)
	if obj != 8 || op != wpCursorShapeMgrOpGetPointer {
		t.Fatalf("first request object=%d opcode=%d, want get_pointer on manager 8", obj, op)
	}
	if len(payload) < 8 {
		t.Fatal("get_pointer missing new_id/pointer")
	}
	dev := binary.LittleEndian.Uint32(payload[0:4])
	ptr := binary.LittleEndian.Uint32(payload[4:8])
	if ptr != 9 {
		t.Fatalf("get_pointer pointer=%d, want 9", ptr)
	}

	obj, op, payload = readWlMessage(t, server)
	if obj != dev || op != wpCursorShapeDevOpSetShape {
		t.Fatalf("second request object=%d opcode=%d, want set_shape on %d", obj, op, dev)
	}
	if len(payload) < 8 {
		t.Fatal("set_shape missing serial/shape")
	}
	serial := binary.LittleEndian.Uint32(payload[0:4])
	shape := binary.LittleEndian.Uint32(payload[4:8])
	if serial != 7 || shape != wpCursorShapeText {
		t.Fatalf("set_shape serial=%d shape=%d, want 7/%d", serial, shape, wpCursorShapeText)
	}
}

func TestWaylandSetCursorHiddenNullSurface(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:      client,
		done:      make(chan struct{}),
		pointerID: 9,
	}
	t.Cleanup(func() { close(s.done) })
	st := waylandClipboardOf(s)
	st.mu.Lock()
	st.pointerSerial = 11
	st.mu.Unlock()

	if err := s.SetCursor(desktop.HiddenCursor); err != nil {
		t.Fatalf("SetCursor(HiddenCursor): %v", err)
	}
	obj, op, payload := readWlMessage(t, server)
	if obj != 9 || op != wlPointerOpSetCursor {
		t.Fatalf("hidden cursor sent object=%d opcode=%d, want set_cursor on pointer 9", obj, op)
	}
	if len(payload) < 16 {
		t.Fatalf("set_cursor payload len %d, want 16", len(payload))
	}
	serial := binary.LittleEndian.Uint32(payload[0:4])
	surface := binary.LittleEndian.Uint32(payload[4:8])
	if serial != 11 || surface != 0 {
		t.Fatalf("set_cursor serial=%d surface=%d, want 11/null", serial, surface)
	}
}

func TestWaylandSetCursorUnderCage(t *testing.T) {
	s := openWaylandForTest(t)
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}
	cursors := []desktop.StandardCursor{
		desktop.DefaultCursor,
		desktop.TextCursor,
		desktop.PointerCursor,
		desktop.HResizeCursor,
		desktop.VResizeCursor,
		desktop.HiddenCursor,
	}
	for _, c := range cursors {
		if err := s.SetCursor(c); err != nil {
			t.Fatalf("SetCursor(%v): %v", c, err)
		}
	}
}
