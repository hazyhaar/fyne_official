//go:build purego && linux

package purego

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"

	"fyne.io/fyne/v2"
)

const (
	wlTestEvdevAD01 = 16
	wlTestEvdevAE05 = 6
	wlKeymapArgsLen = 8
	wlModsArgsLen   = 20
	wlKeyArgsLen    = 16
	wlModsDepressed = 4
	wlModsLatched   = 8
	wlModsLocked    = 12
	wlModsGroup     = 16
	wlKeyCodeOff    = 8
	wlKeyStateOff   = 12
)

func TestWaylandKeyboardWithoutKeymapHasNoUSTable(t *testing.T) {
	s := newWlKeyboardSurface(t)
	s.handleKeyboard(wlKeyboardEventKey, wlKeyPayload(wlTestEvdevAD01, true))
	k := recvKey(t, s)
	if k.Rune != 0 || k.KeySym != 0 || k.Name != "" {
		t.Fatalf("without keymap, evdev %d = rune %q keysym 0x%X name %q; US table must not apply", wlTestEvdevAD01, k.Rune, k.KeySym, k.Name)
	}
}

func TestWaylandKeyboardKeymapShiftCapsAltGr(t *testing.T) {
	s := newWlKeyboardSurface(t)
	feedKeymap(t, s, frenchSyntheticKeymap)

	s.handleKeyboard(wlKeyboardEventModifiers, wlModsPayload(0, 0, 0, 0))
	s.handleKeyboard(wlKeyboardEventKey, wlKeyPayload(wlTestEvdevAD01, true))
	k := recvKey(t, s)
	if k.Rune != 'a' || k.KeySym != 'a' || k.Name != fyne.KeyA {
		t.Fatalf("a: rune=%q keysym=0x%X name=%q", k.Rune, k.KeySym, k.Name)
	}

	s.handleKeyboard(wlKeyboardEventModifiers, wlModsPayload(xkbModShiftBit, 0, 0, 0))
	s.handleKeyboard(wlKeyboardEventKey, wlKeyPayload(wlTestEvdevAD01, true))
	k = recvKey(t, s)
	if k.Rune != 'A' || k.KeySym != 'A' || k.Name != fyne.KeyA || k.Modifiers&modShift == 0 {
		t.Fatalf("Shift+a: rune=%q keysym=0x%X name=%q mods=%v", k.Rune, k.KeySym, k.Name, k.Modifiers)
	}

	s.handleKeyboard(wlKeyboardEventModifiers, wlModsPayload(0, 0, xkbModLockBit, 0))
	s.handleKeyboard(wlKeyboardEventKey, wlKeyPayload(wlTestEvdevAD01, true))
	k = recvKey(t, s)
	if k.Rune != 'A' || k.KeySym != 'A' || k.Name != fyne.KeyA || k.Modifiers&modLock == 0 {
		t.Fatalf("Caps+a: rune=%q keysym=0x%X name=%q mods=%v", k.Rune, k.KeySym, k.Name, k.Modifiers)
	}

	s.handleKeyboard(wlKeyboardEventModifiers, wlModsPayload(xkbMod5Bit, 0, 0, 0))
	s.handleKeyboard(wlKeyboardEventKey, wlKeyPayload(wlTestEvdevAE05, true))
	k = recvKey(t, s)
	if k.Rune != '€' || k.KeySym != xkEuroSign {
		t.Fatalf("AltGr+AE05: rune=%q keysym=0x%X, want € / EuroSign", k.Rune, k.KeySym)
	}
}

func newWlKeyboardSurface(t *testing.T) *waylandSurface {
	t.Helper()
	s := &waylandSurface{
		events: make(chan surfaceEvent, 8),
		done:   make(chan struct{}),
	}
	t.Cleanup(func() {
		wlStopKeyRepeat(s)
		close(s.done)
	})
	return s
}

func feedKeymap(t *testing.T, s *waylandSurface, text string) {
	t.Helper()
	fd, err := unix.MemfdCreate("purego-keymap-test", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatalf("memfd_create: %v", err)
	}
	raw := append([]byte(text), 0)
	if _, err := unix.Write(fd, raw); err != nil {
		unix.Close(fd)
		t.Fatalf("write keymap: %v", err)
	}
	if len(raw) > maxKeymapBytes {
		unix.Close(fd)
		t.Fatalf("keymap too large: %d", len(raw))
	}
	s.enqueuePendingFDs([]int{fd})
	p := make([]byte, wlKeymapArgsLen)
	binary.LittleEndian.PutUint32(p[0:4], wlKeyboardKeymapFormatXKBV1)
	n, err := uint32FromInt(len(raw))
	if err != nil {
		unix.Close(fd)
		t.Fatalf("keymap length: %v", err)
	}
	binary.LittleEndian.PutUint32(p[4:8], n)
	s.handleKeyboard(wlKeyboardEventKeymap, p)
}

func wlModsPayload(depressed, latched, locked, group uint32) []byte {
	p := make([]byte, wlModsArgsLen)
	binary.LittleEndian.PutUint32(p[wlModsDepressed:wlModsDepressed+4], depressed)
	binary.LittleEndian.PutUint32(p[wlModsLatched:wlModsLatched+4], latched)
	binary.LittleEndian.PutUint32(p[wlModsLocked:wlModsLocked+4], locked)
	binary.LittleEndian.PutUint32(p[wlModsGroup:wlModsGroup+4], group)
	return p
}

func wlKeyPayload(code uint32, pressed bool) []byte {
	p := make([]byte, wlKeyArgsLen)
	binary.LittleEndian.PutUint32(p[wlKeyCodeOff:wlKeyCodeOff+4], code)
	if pressed {
		binary.LittleEndian.PutUint32(p[wlKeyStateOff:wlKeyStateOff+4], 1)
	}
	return p
}

func recvKey(t *testing.T, s *waylandSurface) keyEvent {
	t.Helper()
	ev := recvEvent(t, s.events)
	k, ok := ev.(keyEvent)
	if !ok {
		t.Fatalf("got %T, want keyEvent", ev)
	}
	return k
}
