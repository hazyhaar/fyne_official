//go:build purego && linux

package purego

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

func x11KeyPacket(code byte, pressed bool, stamp uint32) []byte {
	buf := make([]byte, coreEventSize)
	if pressed {
		buf[0] = eventCodeKeyPress
	} else {
		buf[0] = eventCodeKeyRelease
	}
	buf[1] = code
	binary.LittleEndian.PutUint32(buf[4:8], stamp)
	return buf
}

func TestX11KeyIsAutoRepeat(t *testing.T) {
	rel := x11KeyPacket(38, false, 100)
	press := x11KeyPacket(38, true, 100)
	if !x11KeyIsAutoRepeat(rel, press) {
		t.Fatal("same keycode and timestamp should be auto-repeat")
	}
	pressOther := x11KeyPacket(39, true, 100)
	if x11KeyIsAutoRepeat(rel, pressOther) {
		t.Fatal("different keycode must not be auto-repeat")
	}
	pressLater := x11KeyPacket(38, true, 101)
	if x11KeyIsAutoRepeat(rel, pressLater) {
		t.Fatal("different timestamp must not be auto-repeat")
	}
}

func TestReadLoopAutoRepeatDropsRelease(t *testing.T) {
	client, server := unixSocketPair(t)
	s := &x11Surface{
		conn:   server,
		events: make(chan surfaceEvent, 8),
		done:   make(chan struct{}),
	}
	go s.readLoop()
	t.Cleanup(func() {
		close(s.done)
		_ = client.Close()
		_ = server.Close()
	})

	const keycode = 38
	if _, err := client.Write(x11KeyPacket(keycode, false, 50)); err != nil {
		t.Fatalf("write KeyRelease: %v", err)
	}
	if _, err := client.Write(x11KeyPacket(keycode, true, 50)); err != nil {
		t.Fatalf("write KeyPress: %v", err)
	}

	ev := recvEvent(t, s.events)
	k, ok := ev.(keyEvent)
	if !ok || !k.Pressed || k.Scancode != keycode {
		t.Fatalf("auto-repeat delivered %#v, want a single KeyPress", ev)
	}
	if !k.Repeat {
		t.Fatal("auto-repeat KeyPress did not set Repeat")
	}
	select {
	case extra := <-s.events:
		t.Fatalf("unexpected extra event after auto-repeat: %#v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHarnessXTestAutoRepeatSameTimestamp(t *testing.T) {
	d := newTestDriver()
	w := d.CreateWindow("harness-xtest-repeat").(*window)
	w.Resize(fyne.NewSize(120, 80))
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

	// Drain lifecycle events so the auto-repeat pair is the next key traffic.
	pumpFor(w, 100*time.Millisecond)

	const keycode = byte(38) // X11 keycode of the physical A key (evdev KEY_A 30 + 8)
	const stamp = uint32(7)
	if err := sendX11CoreEvent(sync.conn, xs.winID, x11KeyPacket(keycode, false, stamp)); err != nil {
		t.Fatalf("SendEvent KeyRelease: %v", err)
	}
	if err := sendX11CoreEvent(sync.conn, xs.winID, x11KeyPacket(keycode, true, stamp)); err != nil {
		t.Fatalf("SendEvent KeyPress: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	releases := 0
	presses := 0
	for time.Now().Before(deadline) {
		select {
		case ev := <-xs.Events():
			k, ok := ev.(keyEvent)
			if !ok {
				continue
			}
			if k.Pressed {
				presses++
			} else {
				releases++
			}
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	if releases != 0 {
		t.Fatalf("auto-repeat Release reached the canvas %d times, want 0", releases)
	}
	if presses != 1 {
		t.Fatalf("auto-repeat Press count = %d, want 1", presses)
	}
}

func sendX11CoreEvent(conn net.Conn, dest uint32, ev []byte) error {
	const sendEventReqLen, sendEventReqWords = 44, 11
	req := make([]byte, sendEventReqLen)
	req[0] = opSendEvent
	binary.LittleEndian.PutUint16(req[2:4], sendEventReqWords)
	binary.LittleEndian.PutUint32(req[4:8], dest)
	binary.LittleEndian.PutUint32(req[8:12], eventMaskKeyPress|eventMaskKeyRelease)
	if len(ev) >= 16 {
		binary.LittleEndian.PutUint32(ev[12:16], dest)
	}
	if len(ev) > 0 {
		ev[0] |= 0x80
	}
	copy(req[12:], ev)
	_, err := conn.Write(req)
	return err
}

func TestWaylandKeyboardRepeatTimer(t *testing.T) {
	s := &waylandSurface{
		events: make(chan surfaceEvent, 32),
		done:   make(chan struct{}),
	}
	t.Cleanup(func() {
		wlStopKeyRepeat(s)
		close(s.done)
	})

	info := make([]byte, 8)
	binary.LittleEndian.PutUint32(info[0:4], 50)
	binary.LittleEndian.PutUint32(info[4:8], 20)
	s.handleKeyboard(wlKeyboardEventRepeatInfo, info)

	press := make([]byte, 16)
	binary.LittleEndian.PutUint32(press[8:12], 30)
	binary.LittleEndian.PutUint32(press[12:16], 1)
	s.handleKeyboard(wlKeyboardEventKey, press)

	time.Sleep(90 * time.Millisecond)
	presses := 0
	for {
		select {
		case ev := <-s.events:
			k, ok := ev.(keyEvent)
			if ok && k.Pressed {
				presses++
			}
		default:
			goto counted
		}
	}
counted:
	if presses < 2 {
		t.Fatalf("repeat timer delivered %d presses, want at least the original plus one repeat", presses)
	}

	release := make([]byte, 16)
	binary.LittleEndian.PutUint32(release[8:12], 30)
	s.handleKeyboard(wlKeyboardEventKey, release)
	time.Sleep(5 * time.Millisecond)
	for {
		select {
		case <-s.events:
		default:
			goto drained
		}
	}
drained:
	time.Sleep(50 * time.Millisecond)
	extra := 0
	for {
		select {
		case ev := <-s.events:
			k, ok := ev.(keyEvent)
			if ok && k.Pressed {
				extra++
			}
		default:
			goto done
		}
	}
done:
	if extra != 0 {
		t.Fatalf("repeat timer kept firing after release: %d extra presses", extra)
	}
}
