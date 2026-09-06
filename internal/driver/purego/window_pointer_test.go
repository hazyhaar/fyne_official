//go:build purego

package purego

import (
	"image"
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

type stubSurface struct {
	events  chan surfaceEvent
	cursors []desktop.Cursor
}

func (*stubSurface) Present(*image.NRGBA) error { return nil }
func (s *stubSurface) Events() <-chan surfaceEvent {
	return s.events
}
func (*stubSurface) Resize(int, int) error { return nil }
func (*stubSurface) SetTitle(string) error { return nil }
func (*stubSurface) Close() error          { return nil }
func (s *stubSurface) SetCursor(c desktop.Cursor) error {
	s.cursors = append(s.cursors, c)
	return nil
}

type hoverRec struct {
	*fynecanvas.Rectangle
	in, out, moved int
}

func (h *hoverRec) MouseIn(*desktop.MouseEvent)    { h.in++ }
func (h *hoverRec) MouseMoved(*desktop.MouseEvent) { h.moved++ }
func (h *hoverRec) MouseOut()                      { h.out++ }

type textCursorHover struct {
	*fynecanvas.Rectangle
}

func (*textCursorHover) Cursor() desktop.Cursor         { return desktop.TextCursor }
func (*textCursorHover) MouseIn(*desktop.MouseEvent)    {}
func (*textCursorHover) MouseMoved(*desktop.MouseEvent) {}
func (*textCursorHover) MouseOut()                      {}

func TestHandlePointerHoverInThenOut(t *testing.T) {
	test.NewApp()
	w := newWindow("hover")
	surf := &stubSurface{events: make(chan surfaceEvent, 4)}
	w.surface = surf
	h := &hoverRec{Rectangle: fynecanvas.NewRectangle(color.White)}
	w.SetContent(h)
	w.canvas.Resize(fyne.NewSize(200, 200))

	w.handlePointer(pointerEvent{X: 50, Y: 50, Moved: true})
	if h.in != 1 {
		t.Fatalf("MouseIn count = %d, want 1", h.in)
	}
	w.handlePointer(pointerEvent{X: 50, Y: 50, Moved: true})
	if h.moved != 1 {
		t.Fatalf("MouseMoved count = %d, want 1", h.moved)
	}
	w.handlePointer(pointerEvent{X: 0, Y: 0, Left: true})
	if h.out != 1 {
		t.Fatalf("MouseOut count = %d, want 1", h.out)
	}
}

func TestHandlePointerCursorableSetsCursor(t *testing.T) {
	test.NewApp()
	w := newWindow("cursor")
	surf := &stubSurface{events: make(chan surfaceEvent, 4)}
	w.surface = surf
	obj := &textCursorHover{Rectangle: fynecanvas.NewRectangle(color.White)}
	w.SetContent(obj)
	w.canvas.Resize(fyne.NewSize(200, 200))

	w.handlePointer(pointerEvent{X: 50, Y: 50, Moved: true})
	if len(surf.cursors) == 0 {
		t.Fatal("SetCursor was not called on the surface")
	}
	if surf.cursors[len(surf.cursors)-1] != desktop.TextCursor {
		t.Fatalf("SetCursor got %v, want TextCursor", surf.cursors[len(surf.cursors)-1])
	}
}

func TestHandlePointerButtonStillTaps(t *testing.T) {
	test.NewApp()
	w := newWindow("tap")
	tapped := false
	btn := widget.NewButton("x", func() { tapped = true })
	w.SetContent(btn)
	w.canvas.Resize(fyne.NewSize(200, 200))
	pos := btn.Position()
	size := btn.Size()
	click := pointerEvent{
		X:      int(pos.X + size.Width/2),
		Y:      int(pos.Y + size.Height/2),
		Button: 1,
	}
	click.Pressed = true
	w.handlePointer(click)
	click.Pressed = false
	w.handlePointer(click)
	if !tapped {
		t.Fatal("button tap was lost after hover dispatch")
	}
}

type pointerRec struct {
	*fynecanvas.Rectangle
	log []string
	btn []desktop.MouseButton
}

func (p *pointerRec) MouseDown(e *desktop.MouseEvent) {
	p.log = append(p.log, "MouseDown")
	p.btn = append(p.btn, e.Button)
}

func (p *pointerRec) MouseUp(e *desktop.MouseEvent) {
	p.log = append(p.log, "MouseUp")
	p.btn = append(p.btn, e.Button)
}

func (p *pointerRec) Tapped(*fyne.PointEvent) {
	p.log = append(p.log, "Tapped")
}

func (p *pointerRec) TappedSecondary(*fyne.PointEvent) {
	p.log = append(p.log, "TappedSecondary")
}

func newPointerRec() *pointerRec {
	r := fynecanvas.NewRectangle(color.White)
	r.SetMinSize(fyne.NewSize(200, 200))
	return &pointerRec{Rectangle: r}
}

func TestHandlePointerMouseableDownUp(t *testing.T) {
	test.NewApp()
	w := newWindow("mouseable")
	p := newPointerRec()
	w.SetContent(p)
	w.canvas.Resize(fyne.NewSize(200, 200))

	w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 1, Pressed: true})
	w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 1, Pressed: false})
	want := []string{"MouseDown", "MouseUp", "Tapped"}
	if len(p.log) != len(want) {
		t.Fatalf("events = %v, want %v", p.log, want)
	}
	for i := range want {
		if p.log[i] != want[i] {
			t.Fatalf("events = %v, want %v", p.log, want)
		}
	}
	if p.btn[0] != desktop.MouseButtonPrimary || p.btn[1] != desktop.MouseButtonPrimary {
		t.Fatalf("buttons = %v, want Primary, Primary", p.btn)
	}
}

func TestHandlePointerTappedSecondary(t *testing.T) {
	test.NewApp()
	w := newWindow("secondary")
	p := newPointerRec()
	w.SetContent(p)
	w.canvas.Resize(fyne.NewSize(200, 200))

	w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 3, Pressed: true})
	w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 3, Pressed: false})
	want := []string{"MouseDown", "MouseUp", "TappedSecondary"}
	if len(p.log) != len(want) {
		t.Fatalf("events = %v, want %v", p.log, want)
	}
	for i := range want {
		if p.log[i] != want[i] {
			t.Fatalf("events = %v, want %v", p.log, want)
		}
	}
	if p.btn[0] != desktop.MouseButtonSecondary || p.btn[1] != desktop.MouseButtonSecondary {
		t.Fatalf("buttons = %v, want Secondary, Secondary", p.btn)
	}
}

type dragRec struct {
	*fynecanvas.Rectangle
	deltas []fyne.Delta
	ends   int
}

func (d *dragRec) Dragged(e *fyne.DragEvent) {
	d.deltas = append(d.deltas, e.Dragged)
}

func (d *dragRec) DragEnd() { d.ends++ }

type doubleRec struct {
	*fynecanvas.Rectangle
	tapped chan struct{}
	double chan struct{}
}

func (d *doubleRec) Tapped(*fyne.PointEvent) {
	select {
	case d.tapped <- struct{}{}:
	default:
	}
}

func (d *doubleRec) DoubleTapped(*fyne.PointEvent) {
	select {
	case d.double <- struct{}{}:
	default:
	}
}

func TestHandlePointerDraggedAndDragEnd(t *testing.T) {
	test.NewApp()
	w := newWindow("drag")
	d := &dragRec{Rectangle: fynecanvas.NewRectangle(color.White)}
	d.Resize(fyne.NewSize(80, 80))
	d.Move(fyne.NewPos(10, 10))
	w.SetContent(container.NewWithoutLayout(d))
	w.canvas.Resize(fyne.NewSize(200, 200))

	w.handlePointer(pointerEvent{X: 40, Y: 40, Button: 1, Pressed: true})
	w.handlePointer(pointerEvent{X: 40, Y: 50, Moved: true})
	if len(d.deltas) == 0 {
		t.Fatal("Dragged was not called after moving past the drag threshold")
	}
	if d.deltas[0] != fyne.NewDelta(0, 10) {
		t.Fatalf("first Dragged delta = %v, want (0, 10)", d.deltas[0])
	}
	w.handlePointer(pointerEvent{X: 150, Y: 150, Moved: true})
	if len(d.deltas) < 2 {
		t.Fatal("Dragged stopped after the pointer left the object")
	}
	w.handlePointer(pointerEvent{X: 150, Y: 150, Button: 1, Pressed: false})
	if d.ends != 1 {
		t.Fatalf("DragEnd count = %d, want 1", d.ends)
	}
}

func TestHandlePointerDoubleTapAndSingleTap(t *testing.T) {
	test.NewApp()
	w := newWindow("double")
	d := &doubleRec{
		Rectangle: fynecanvas.NewRectangle(color.White),
		tapped:    make(chan struct{}, 1),
		double:    make(chan struct{}, 1),
	}
	d.SetMinSize(fyne.NewSize(200, 200))
	w.SetContent(d)
	w.canvas.Resize(fyne.NewSize(200, 200))

	click := func() {
		w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 1, Pressed: true})
		w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 1, Pressed: false})
	}

	click()
	select {
	case <-d.tapped:
	case <-time.After(time.Second):
		t.Fatal("single click did not emit Tapped after DoubleTapDelay")
	}
	select {
	case <-d.double:
		t.Fatal("single click emitted DoubleTapped")
	default:
	}

	click()
	time.Sleep(100 * time.Millisecond)
	click()
	select {
	case <-d.double:
	case <-time.After(time.Second):
		t.Fatal("two clicks 100ms apart did not emit DoubleTapped")
	}
	select {
	case <-d.tapped:
		t.Fatal("double click also emitted Tapped")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleScrollMovesContainer(t *testing.T) {
	test.NewApp()
	w := newWindow("scroll")
	content := fynecanvas.NewRectangle(color.White)
	content.SetMinSize(fyne.NewSize(800, 800))
	scr := container.NewScroll(content)
	w.SetContent(scr)
	w.canvas.Resize(fyne.NewSize(200, 200))
	scr.ScrollToOffset(fyne.NewPos(100, 100))

	w.handleScroll(scrollEvent{X: 50, Y: 50, DY: 1})
	if scr.Offset.Y == 100 {
		t.Fatal("vertical wheel did not change Offset.Y")
	}
	gotY := scr.Offset.Y
	w.handleScroll(scrollEvent{X: 50, Y: 50, DX: 1})
	if scr.Offset.X == 100 {
		t.Fatal("horizontal wheel did not change Offset.X")
	}
	if scr.Offset.Y != gotY {
		t.Fatalf("horizontal wheel changed Offset.Y from %v to %v", gotY, scr.Offset.Y)
	}
}
