//go:build purego && linux

package purego

import (
	"encoding/binary"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

func TestWaylandPointerEnterLeave(t *testing.T) {
	s := &waylandSurface{
		events: make(chan surfaceEvent, 4),
		done:   make(chan struct{}),
	}

	enter := make([]byte, 16)
	binary.LittleEndian.PutUint32(enter[0:4], 1)
	binary.LittleEndian.PutUint32(enter[4:8], 2)
	binary.LittleEndian.PutUint32(enter[8:12], uint32(10*wlFixedShift))
	binary.LittleEndian.PutUint32(enter[12:16], uint32(20*wlFixedShift))
	s.handlePointer(wlPointerEventEnter, enter)

	ev := recvEvent(t, s.events)
	p, ok := ev.(pointerEvent)
	if !ok || !p.Moved || p.X != 10 || p.Y != 20 {
		t.Fatalf("wl_pointer.enter decoded as %#v", ev)
	}

	s.handlePointer(wlPointerEventLeave, make([]byte, 8))
	ev = recvEvent(t, s.events)
	p, ok = ev.(pointerEvent)
	if !ok || !p.Left || p.X != 10 || p.Y != 20 {
		t.Fatalf("wl_pointer.leave decoded as %#v", ev)
	}
}

func TestWaylandPointerAxisAndDiscrete(t *testing.T) {
	s := &waylandSurface{
		events:       make(chan surfaceEvent, 4),
		done:         make(chan struct{}),
		lastPointerX: 3,
		lastPointerY: 4,
	}

	axis := make([]byte, 12)
	binary.LittleEndian.PutUint32(axis[0:4], 0)
	binary.LittleEndian.PutUint32(axis[4:8], wlPointerAxisVertical)
	binary.LittleEndian.PutUint32(axis[8:12], uint32(2*wlFixedShift))
	s.handlePointer(wlPointerEventAxis, axis)
	ev := recvEvent(t, s.events)
	sc, ok := ev.(scrollEvent)
	if !ok || sc.DY != 2 || sc.DX != 0 || sc.X != 3 || sc.Y != 4 {
		t.Fatalf("wl_pointer.axis decoded as %#v", ev)
	}

	disc := make([]byte, 8)
	binary.LittleEndian.PutUint32(disc[0:4], wlPointerAxisHorizontal)
	binary.LittleEndian.PutUint32(disc[4:8], uint32(1))
	s.handlePointer(wlPointerEventAxisDiscrete, disc)
	ev = recvEvent(t, s.events)
	sc, ok = ev.(scrollEvent)
	if !ok || sc.DX != 1 || sc.DY != 0 {
		t.Fatalf("wl_pointer.axis_discrete decoded as %#v", ev)
	}
}

type scrollDeltaRec struct {
	*fynecanvas.Rectangle
	dx, dy float32
}

func (s *scrollDeltaRec) Scrolled(e *fyne.ScrollEvent) {
	s.dx, s.dy = e.Scrolled.DX, e.Scrolled.DY
}

func TestWaylandScrollUnitsNotchVsContinuous(t *testing.T) {
	test.NewApp()
	w := newWindow("scroll-units")
	rec := &scrollDeltaRec{Rectangle: fynecanvas.NewRectangle(color.White)}
	w.SetContent(rec)
	w.canvas.Resize(fyne.NewSize(200, 200))

	s := &waylandSurface{
		events:       make(chan surfaceEvent, 4),
		done:         make(chan struct{}),
		lastPointerX: 50,
		lastPointerY: 50,
	}

	disc := make([]byte, 8)
	binary.LittleEndian.PutUint32(disc[0:4], wlPointerAxisVertical)
	binary.LittleEndian.PutUint32(disc[4:8], 1)
	s.handlePointer(wlPointerEventAxisDiscrete, disc)
	ev := recvEvent(t, s.events)
	sc, ok := ev.(scrollEvent)
	if !ok {
		t.Fatalf("axis_discrete decoded as %#v", ev)
	}
	w.handleScroll(sc)
	if rec.dy != 25 {
		t.Fatalf("vertical notch DY = %v, want 25", rec.dy)
	}

	rec.dx, rec.dy = 0, 0
	axis := make([]byte, 12)
	binary.LittleEndian.PutUint32(axis[0:4], 0)
	binary.LittleEndian.PutUint32(axis[4:8], wlPointerAxisVertical)
	binary.LittleEndian.PutUint32(axis[8:12], 2560)
	s.handlePointer(wlPointerEventAxis, axis)
	ev = recvEvent(t, s.events)
	sc, ok = ev.(scrollEvent)
	if !ok {
		t.Fatalf("axis decoded as %#v", ev)
	}
	w.handleScroll(sc)
	if rec.dy != 10 {
		t.Fatalf("continuous axis 2560 (10 px) DY = %v, want 10", rec.dy)
	}
}
