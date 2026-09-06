//go:build purego && linux

package purego

import (
	"encoding/binary"
	"os"
	"testing"
	"time"
)

func TestSetDisableScreenBlankingSuspendsXScreenSaver(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("X11 blanking test runs in the X11 suite only")
	}
	peer := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = peer.Close() })

	major, _, ok, err := peer.queryExtension("MIT-SCREEN-SAVER")
	if err != nil {
		t.Fatalf("queryExtension(MIT-SCREEN-SAVER): %v", err)
	}
	if !ok {
		t.Fatal("MIT-SCREEN-SAVER is not available on this Xvfb")
	}
	if err := screenSaverQueryVersion(peer, major); err != nil {
		t.Fatalf("ScreenSaverQueryVersion: %v", err)
	}

	d := NewDriver().(*driverImpl)
	installScaleApp(t, d)
	w := d.CreateWindow("blanking").(*window)
	w.Show()
	if w.surface == nil {
		t.Skip("purego/x11: no display server reachable")
	}
	t.Cleanup(func() { w.Close() })

	before := countX11ClientSockets(t)
	d.SetDisableScreenBlanking(true)
	t.Cleanup(func() { d.SetDisableScreenBlanking(false) })
	after := countX11ClientSockets(t)
	if after != before {
		t.Fatalf("SetDisableScreenBlanking opened an X11 socket: before=%d after=%d", before, after)
	}

	if !x11BlankingHeld() {
		t.Fatal("SetDisableScreenBlanking(true) did not suspend MIT-SCREEN-SAVER")
	}
	state, err := x11BlankingQueryInfoState()
	if err != nil {
		t.Fatalf("ScreenSaverQueryInfo after suspend: %v", err)
	}
	const saverDisabled = 3
	if state != saverDisabled && state != 0 {
		t.Fatalf("screensaver state = %d after suspend, want Disabled (%d) or Off (0) on Xvfb", state, saverDisabled)
	}

	d.SetDisableScreenBlanking(false)
	if x11BlankingHeld() {
		t.Fatal("blanking still held after SetDisableScreenBlanking(false)")
	}
	state, err = screenSaverQueryInfoState(peer, major)
	if err != nil {
		t.Fatalf("ScreenSaverQueryInfo after resume: %v", err)
	}
	if state == saverDisabled {
		t.Fatal("screensaver still Disabled after SetDisableScreenBlanking(false)")
	}
}

func TestX11BlankingSocketClosedOnQuit(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("X11 blanking test runs in the X11 suite only")
	}
	d := NewDriver().(*driverImpl)
	installScaleApp(t, d)
	w := d.CreateWindow("blanking-quit").(*window)
	w.Show()
	if w.surface == nil {
		t.Skip("purego/x11: no display server reachable")
	}
	d.SetDisableScreenBlanking(true)
	if !x11BlankingHeld() {
		t.Fatal("SetDisableScreenBlanking(true) did not suspend screen blanking")
	}
	d.Quit()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !x11BlankingHeld() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("blanking still held after Quit")
}

func screenSaverQueryVersion(s *x11Surface, major byte) error {
	req := make([]byte, 8)
	req[0] = major
	req[1] = 0
	binary.LittleEndian.PutUint16(req[2:4], 2)
	req[4] = 1
	req[5] = 1
	_, err := s.request(req)
	return err
}

func screenSaverQueryInfoState(s *x11Surface, major byte) (byte, error) {
	req := make([]byte, 8)
	req[0] = major
	req[1] = 1
	binary.LittleEndian.PutUint16(req[2:4], 2)
	binary.LittleEndian.PutUint32(req[4:8], s.rootWindow)
	reply, err := s.request(req)
	if err != nil {
		return 0, err
	}
	return reply[1], nil
}
