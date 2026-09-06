//go:build purego && linux

package purego

import (
	"encoding/binary"
	"image/color"
	"net"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	_ "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

type recButton struct {
	*widget.Button
	in, out int
}

func (b *recButton) MouseIn(e *desktop.MouseEvent) {
	b.in++
	b.Button.MouseIn(e)
}

func (b *recButton) MouseOut() {
	b.out++
	b.Button.MouseOut()
}

func (b *recButton) MouseMoved(e *desktop.MouseEvent) {
	b.Button.MouseMoved(e)
}

func TestHarnessXTestHoverInOutOnButton(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-hover").(*window)
	btn := &recButton{Button: widget.NewButton("hover me", nil)}
	w.SetContent(btn)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	pos := btn.Position()
	size := btn.Size()
	clickX := int16(pos.X + size.Width/2)
	clickY := int16(pos.Y + size.Height/2)
	root := sync.rootWindow
	const motionAbsolute = 0
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput MotionNotify onto button: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	if btn.in == 0 {
		t.Fatalf("MouseIn was not received after XTEST motion onto widget.Button at (%d,%d)", clickX, clickY)
	}

	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, 0, 0); err != nil {
		t.Fatalf("fakeInput MotionNotify off button: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	if btn.out == 0 {
		t.Fatal("MouseOut was not received after XTEST motion off widget.Button")
	}
}

func TestHarnessXTestCursorableSetsXCursor(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-cursor").(*window)
	entry := widget.NewEntry()
	w.SetContent(entry)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)

	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatalf("surface is %T, want *x11Surface", w.surface)
	}

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	pos := entry.Position()
	size := entry.Size()
	x := int16(pos.X + size.Width/2)
	y := int16(pos.Y + size.Height/2)
	const motionAbsolute = 0
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, sync.rootWindow, x, y); err != nil {
		t.Fatalf("fakeInput MotionNotify onto entry: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	if got := appliedCursor(xs); got != desktop.TextCursor {
		t.Fatalf("applied cursor = %v, want TextCursor", got)
	}
}

type xtestPointerRec struct {
	*widget.Label
	log []string
	btn []desktop.MouseButton
}

func (p *xtestPointerRec) MouseDown(e *desktop.MouseEvent) {
	p.log = append(p.log, "MouseDown")
	p.btn = append(p.btn, e.Button)
}

func (p *xtestPointerRec) MouseUp(e *desktop.MouseEvent) {
	p.log = append(p.log, "MouseUp")
	p.btn = append(p.btn, e.Button)
}

func (p *xtestPointerRec) Tapped(*fyne.PointEvent) {
	p.log = append(p.log, "Tapped")
}

func (p *xtestPointerRec) TappedSecondary(*fyne.PointEvent) {
	p.log = append(p.log, "TappedSecondary")
}

func TestHarnessXTestMouseableAndSecondary(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-mouseable").(*window)
	rec := &xtestPointerRec{Label: widget.NewLabel("pointer target")}
	w.SetContent(rec)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	pos := rec.Position()
	size := rec.Size()
	x := int16(pos.X + size.Width/2)
	y := int16(pos.Y + size.Height/2)
	root := sync.rootWindow
	const motionAbsolute = 0
	const leftButton = 1
	const rightButton = 3

	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, x, y); err != nil {
		t.Fatalf("fakeInput MotionNotify: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonPress, leftButton, root, x, y); err != nil {
		t.Fatalf("fakeInput ButtonPress left: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, leftButton, root, x, y); err != nil {
		t.Fatalf("fakeInput ButtonRelease left: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	wantLeft := []string{"MouseDown", "MouseUp", "Tapped"}
	if len(rec.log) != len(wantLeft) {
		t.Fatalf("left click events = %v, want %v", rec.log, wantLeft)
	}
	for i := range wantLeft {
		if rec.log[i] != wantLeft[i] {
			t.Fatalf("left click events = %v, want %v", rec.log, wantLeft)
		}
	}
	if rec.btn[0] != desktop.MouseButtonPrimary || rec.btn[1] != desktop.MouseButtonPrimary {
		t.Fatalf("left click buttons = %v, want Primary, Primary", rec.btn)
	}

	rec.log = nil
	rec.btn = nil
	if err := xt.fakeInput(sync.conn, xtestButtonPress, rightButton, root, x, y); err != nil {
		t.Fatalf("fakeInput ButtonPress right: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, rightButton, root, x, y); err != nil {
		t.Fatalf("fakeInput ButtonRelease right: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	wantRight := []string{"MouseDown", "MouseUp", "TappedSecondary"}
	if len(rec.log) != len(wantRight) {
		t.Fatalf("right click events = %v, want %v", rec.log, wantRight)
	}
	for i := range wantRight {
		if rec.log[i] != wantRight[i] {
			t.Fatalf("right click events = %v, want %v", rec.log, wantRight)
		}
	}
	if rec.btn[0] != desktop.MouseButtonSecondary || rec.btn[1] != desktop.MouseButtonSecondary {
		t.Fatalf("right click buttons = %v, want Secondary, Secondary", rec.btn)
	}
}

type xtestDoubleRec struct {
	*widget.Label
	tapped chan struct{}
	double chan struct{}
}

func (p *xtestDoubleRec) Tapped(*fyne.PointEvent) {
	select {
	case p.tapped <- struct{}{}:
	default:
	}
}

func (p *xtestDoubleRec) DoubleTapped(*fyne.PointEvent) {
	select {
	case p.double <- struct{}{}:
	default:
	}
}

func TestHarnessXTestScrollDragChangesOffset(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-scroll-drag").(*window)
	content := fynecanvas.NewRectangle(color.White)
	content.SetMinSize(fyne.NewSize(800, 800))
	scr := container.NewScroll(content)
	w.SetContent(scr)
	w.Resize(fyne.NewSize(200, 200))
	w.Show()
	t.Cleanup(w.Close)
	start := scr.Offset

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	pos := scr.Position()
	size := scr.Size()
	x := int16(pos.X + size.Width - 3)
	y := int16(pos.Y + 20)
	root := sync.rootWindow
	const motionAbsolute = 0
	const leftButton = 1
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, x, y); err != nil {
		t.Fatalf("fakeInput MotionNotify onto scrollbar: %v", err)
	}
	pumpFor(w, 200*time.Millisecond)
	if err := xt.fakeInput(sync.conn, xtestButtonPress, leftButton, root, x, y); err != nil {
		t.Fatalf("fakeInput ButtonPress: %v", err)
	}
	pumpFor(w, 50*time.Millisecond)
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, x, y+40); err != nil {
		t.Fatalf("fakeInput MotionNotify drag: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, leftButton, root, x, y+40); err != nil {
		t.Fatalf("fakeInput ButtonRelease: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	if scr.Offset == start {
		t.Fatalf("container.Scroll Offset still %v after dragging the scrollbar", scr.Offset)
	}
}

func TestHarnessXTestDoubleTapAndSingleTap(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-double").(*window)
	rec := &xtestDoubleRec{
		Label:  widget.NewLabel("double target"),
		tapped: make(chan struct{}, 2),
		double: make(chan struct{}, 2),
	}
	w.SetContent(rec)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	pos := rec.Position()
	size := rec.Size()
	x := int16(pos.X + size.Width/2)
	y := int16(pos.Y + size.Height/2)
	root := sync.rootWindow
	const motionAbsolute = 0
	const leftButton = 1
	click := func() {
		if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, x, y); err != nil {
			t.Fatalf("fakeInput MotionNotify: %v", err)
		}
		if err := xt.fakeInput(sync.conn, xtestButtonPress, leftButton, root, x, y); err != nil {
			t.Fatalf("fakeInput ButtonPress: %v", err)
		}
		if err := xt.fakeInput(sync.conn, xtestButtonRelease, leftButton, root, x, y); err != nil {
			t.Fatalf("fakeInput ButtonRelease: %v", err)
		}
	}

	click()
	pumpFor(w, 500*time.Millisecond)
	select {
	case <-rec.tapped:
	default:
		t.Fatal("single XTEST click did not emit Tapped after DoubleTapDelay")
	}
	select {
	case <-rec.double:
		t.Fatal("single XTEST click emitted DoubleTapped")
	default:
	}

	click()
	pumpFor(w, 50*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	click()
	pumpFor(w, 200*time.Millisecond)
	select {
	case <-rec.double:
	default:
		t.Fatal("two XTEST clicks 100ms apart did not emit DoubleTapped")
	}
	select {
	case <-rec.tapped:
		t.Fatal("double XTEST click also emitted Tapped")
	default:
	}
}

func TestReadLoopEnterLeaveNotify(t *testing.T) {
	client, server := net.Pipe()
	s := &x11Surface{
		conn:   server,
		events: make(chan surfaceEvent, 4),
		done:   make(chan struct{}),
	}
	go s.readLoop()
	t.Cleanup(func() {
		close(s.done)
		_ = client.Close()
		_ = server.Close()
	})

	enter := make([]byte, coreEventSize)
	enter[0] = eventCodeEnterNotify
	enter[30] = notifyModeNormal
	binary.LittleEndian.PutUint16(enter[24:26], 12)
	binary.LittleEndian.PutUint16(enter[26:28], 34)
	if _, err := client.Write(enter); err != nil {
		t.Fatalf("write EnterNotify: %v", err)
	}
	ev := recvEvent(t, s.events)
	p, ok := ev.(pointerEvent)
	if !ok || !p.Moved || p.Left || p.X != 12 || p.Y != 34 {
		t.Fatalf("EnterNotify decoded as %#v", ev)
	}

	leave := make([]byte, coreEventSize)
	leave[0] = eventCodeLeaveNotify
	leave[30] = notifyModeNormal
	binary.LittleEndian.PutUint16(leave[24:26], 12)
	binary.LittleEndian.PutUint16(leave[26:28], 34)
	if _, err := client.Write(leave); err != nil {
		t.Fatalf("write LeaveNotify: %v", err)
	}
	ev = recvEvent(t, s.events)
	p, ok = ev.(pointerEvent)
	if !ok || !p.Left || p.Moved || p.X != 12 || p.Y != 34 {
		t.Fatalf("LeaveNotify decoded as %#v", ev)
	}
}

func TestReadLoopWheelButtonsAreScrollEvents(t *testing.T) {
	client, server := net.Pipe()
	s := &x11Surface{
		conn:   server,
		events: make(chan surfaceEvent, 4),
		done:   make(chan struct{}),
	}
	go s.readLoop()
	t.Cleanup(func() {
		close(s.done)
		_ = client.Close()
		_ = server.Close()
	})

	writeButton := func(button byte) {
		buf := make([]byte, coreEventSize)
		buf[0] = eventCodeButtonPress
		buf[1] = button
		binary.LittleEndian.PutUint16(buf[24:26], 8)
		binary.LittleEndian.PutUint16(buf[26:28], 9)
		if _, err := client.Write(buf); err != nil {
			t.Fatalf("write ButtonPress %d: %v", button, err)
		}
	}
	writeButton(x11ButtonWheelUp)
	ev := recvEvent(t, s.events)
	sc, ok := ev.(scrollEvent)
	if !ok || sc.DY != 1 || sc.DX != 0 {
		t.Fatalf("button 4 decoded as %#v, want scrollEvent DY=1", ev)
	}
	writeButton(x11ButtonWheelLeft)
	ev = recvEvent(t, s.events)
	sc, ok = ev.(scrollEvent)
	if !ok || sc.DX != 1 || sc.DY != 0 {
		t.Fatalf("button 6 decoded as %#v, want scrollEvent DX=1", ev)
	}
}

func TestHarnessXTestScrollBothAxes(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-scroll").(*window)
	content := fynecanvas.NewRectangle(color.White)
	content.SetMinSize(fyne.NewSize(800, 800))
	scr := container.NewScroll(content)
	w.SetContent(scr)
	w.Resize(fyne.NewSize(200, 200))
	w.Show()
	t.Cleanup(w.Close)
	scr.ScrollToOffset(fyne.NewPos(100, 100))

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	pos := scr.Position()
	size := scr.Size()
	x := int16(pos.X + size.Width/2)
	y := int16(pos.Y + size.Height/2)
	root := sync.rootWindow
	const motionAbsolute = 0
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, x, y); err != nil {
		t.Fatalf("fakeInput MotionNotify: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonPress, x11ButtonWheelUp, root, x, y); err != nil {
		t.Fatalf("fakeInput button 4: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, x11ButtonWheelUp, root, x, y); err != nil {
		t.Fatalf("fakeInput button 4 release: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	if scr.Offset.Y == 100 {
		t.Fatal("XTEST button 4 did not change container.Scroll Offset.Y")
	}
	gotY := scr.Offset.Y
	if err := xt.fakeInput(sync.conn, xtestButtonPress, x11ButtonWheelLeft, root, x, y); err != nil {
		t.Fatalf("fakeInput button 6: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, x11ButtonWheelLeft, root, x, y); err != nil {
		t.Fatalf("fakeInput button 6 release: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	if scr.Offset.X == 100 {
		t.Fatal("XTEST button 6 did not change container.Scroll Offset.X")
	}
	if scr.Offset.Y != gotY {
		t.Fatalf("XTEST button 6 changed Offset.Y from %v to %v", gotY, scr.Offset.Y)
	}
}

func recvEvent(t *testing.T, ch <-chan surfaceEvent) surfaceEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for surface event")
		return nil
	}
}
