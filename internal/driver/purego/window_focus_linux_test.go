//go:build purego && linux

package purego

import (
	"encoding/binary"
	"image/color"
	"net"
	"os"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

func TestReadLoopFocusInOut(t *testing.T) {
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

	out := make([]byte, coreEventSize)
	out[0] = eventCodeFocusOut
	if _, err := client.Write(out); err != nil {
		t.Fatalf("write FocusOut: %v", err)
	}
	ev := recvEvent(t, s.events)
	f, ok := ev.(focusEvent)
	if !ok || f.Gained {
		t.Fatalf("FocusOut decoded as %#v, want focusEvent Gained=false", ev)
	}

	in := make([]byte, coreEventSize)
	in[0] = eventCodeFocusIn
	if _, err := client.Write(in); err != nil {
		t.Fatalf("write FocusIn: %v", err)
	}
	ev = recvEvent(t, s.events)
	f, ok = ev.(focusEvent)
	if !ok || !f.Gained {
		t.Fatalf("FocusIn decoded as %#v, want focusEvent Gained=true", ev)
	}
}

func TestWaylandKeyboardEnterLeaveFocus(t *testing.T) {
	s := &waylandSurface{
		events: make(chan surfaceEvent, 4),
		done:   make(chan struct{}),
	}
	s.handleKeyboard(wlKeyboardEventEnter, make([]byte, 8))
	ev := recvEvent(t, s.events)
	f, ok := ev.(focusEvent)
	if !ok || !f.Gained {
		t.Fatalf("wl_keyboard.enter decoded as %#v, want focusEvent Gained=true", ev)
	}
	s.handleKeyboard(wlKeyboardEventLeave, make([]byte, 8))
	ev = recvEvent(t, s.events)
	f, ok = ev.(focusEvent)
	if !ok || f.Gained {
		t.Fatalf("wl_keyboard.leave decoded as %#v, want focusEvent Gained=false", ev)
	}
}

func TestHarnessXTestFocusLostThenGained(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("XTEST focus test runs in the X11 suite only")
	}
	test.NewApp()
	d := newTestDriver()
	w := d.CreateWindow("focus-xtest").(*window)
	h := &focusHook{Rectangle: fynecanvas.NewRectangle(color.White)}
	h.SetMinSize(fyne.NewSize(200, 200))
	w.SetContent(h)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)
	w.Canvas().Focus(h)
	pumpFor(w, 200*time.Millisecond)
	h.gained, h.lost = 0, 0

	sync := dialSyncX11(t)
	if err := sendSetInputFocus(sync.conn, x11FocusPointerRoot); err != nil {
		t.Fatalf("SetInputFocus PointerRoot: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.lost == 0 {
		w.drainEvents()
		time.Sleep(2 * time.Millisecond)
	}
	if h.lost == 0 {
		t.Fatal("FocusLost was not received after SetInputFocus left the window")
	}

	xs := w.surface.(*x11Surface)
	if err := sendSetInputFocus(sync.conn, xs.winID); err != nil {
		t.Fatalf("SetInputFocus window: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.gained == 0 {
		w.drainEvents()
		time.Sleep(2 * time.Millisecond)
	}
	if h.gained == 0 {
		t.Fatal("FocusGained was not received after SetInputFocus returned to the window")
	}
}

func TestHarnessXTestControlClickModifier(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("XTEST modifier click test runs in the X11 suite only")
	}
	test.NewApp()
	d := newTestDriver()
	w := d.CreateWindow("ctrl-click-xtest").(*window)
	rec := &modifierClickRec{Rectangle: fynecanvas.NewRectangle(color.White)}
	rec.SetMinSize(fyne.NewSize(200, 200))
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

	code := controlLeftKeycode(w.surface.(*x11Surface))
	root := sync.rootWindow
	if err := xt.fakeInput(sync.conn, xtestKeyPress, code, root, 0, 0); err != nil {
		t.Fatalf("fakeInput Control press: %v", err)
	}
	t.Cleanup(func() {
		_ = xt.fakeInput(sync.conn, xtestKeyRelease, code, root, 0, 0)
	})

	pos := rec.Position()
	size := rec.Size()
	clickX := int16(pos.X + size.Width/2)
	clickY := int16(pos.Y + size.Height/2)
	const motionAbsolute = 0
	const leftButton = 1
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput MotionNotify: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonPress, leftButton, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput ButtonPress: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, leftButton, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput ButtonRelease: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(rec.mods) == 0 {
		w.drainEvents()
		time.Sleep(2 * time.Millisecond)
	}
	if len(rec.mods) == 0 {
		t.Fatal("MouseDown was not delivered after Ctrl+click")
	}
	if rec.mods[0] != fyne.KeyModifierControl {
		t.Fatalf("MouseEvent.Modifier = %v, want ControlModifier", rec.mods[0])
	}
}

const (
	x11FocusPointerRoot   = 1
	pc105ControlLKeycode  = 37
	setInputFocusReqLen   = 12
	setInputFocusReqWords = 3
)

func sendSetInputFocus(conn net.Conn, winID uint32) error {
	req := make([]byte, setInputFocusReqLen)
	req[0] = opSetInputFocus
	req[1] = revertToParent
	binary.LittleEndian.PutUint16(req[2:4], setInputFocusReqWords)
	binary.LittleEndian.PutUint32(req[4:8], winID)
	_, err := conn.Write(req)
	return err
}

func controlLeftKeycode(s *x11Surface) byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.minKeycode == 0 || s.maxKeycode < s.minKeycode {
		return pc105ControlLKeycode
	}
	for code := s.minKeycode; code <= s.maxKeycode; code++ {
		if s.lookupKeysymLocked(code, 0) == xkControlL {
			return code
		}
	}
	return pc105ControlLKeycode
}
