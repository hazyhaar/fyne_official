//go:build purego && linux

package purego

import (
	"bytes"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"fyne.io/fyne/v2"
)

// This file covers wl_seat/wl_pointer/wl_keyboard: pointer motion and button
// press/release, and keyboard press/release. Wheel motion is reported as
// scrollEvent from wl_pointer.axis and axis_discrete. Keyboard events
// resolve the compositor's xkb_v1 keymap (wl_keyboard.keymap) through
// xkbkeymap.go: the evdev code plus eight is looked up by xkb name, the
// level follows Shift/Lock/AltGr and the key type, and keysym.go produces
// fyne.KeyName and rune.

// linuxButton mirrors the button codes wl_pointer.button reports, taken
// from the kernel's linux/input-event-codes.h, not from any implementation
// this package was built from.
const (
	linuxBtnLeft   uint32 = 0x110
	linuxBtnRight  uint32 = 0x111
	linuxBtnMiddle uint32 = 0x112

	wlPointerButtonStateReleased uint32 = 0

	wlPointerEventEnter        uint16 = 0
	wlPointerEventLeave        uint16 = 1
	wlPointerEventAxis         uint16 = 4
	wlPointerEventAxisDiscrete uint16 = 8
	wlPointerEventAxisValue120 uint16 = 9
	wlPointerAxisVertical      uint32 = 0
	wlPointerAxisHorizontal    uint32 = 1
	wlPointerAxisValue120Unit  int32  = 120

	wlKeyboardEventLeave        uint16 = 2
	wlKeyboardEventRepeatInfo   uint16 = 5
	wlKeyboardKeymapFormatXKBV1 uint32 = 1
)

type wlKeyRepeat struct {
	mu          sync.Mutex
	delay, rate int32
	stop        chan struct{}
}

func (s *waylandSurface) handleSeatCapabilities(p []byte) {
	r := wlParser{p: p}
	caps, err := r.uint32()
	if err != nil {
		return
	}
	s.mu.Lock()
	needPointer := caps&wlSeatCapPointer != 0 && s.pointerID == 0
	needKeyboard := caps&wlSeatCapKeyboard != 0 && s.keyboardID == 0
	if needPointer {
		s.pointerID = s.nextID
		s.nextID++
	}
	if needKeyboard {
		s.keyboardID = s.nextID
		s.nextID++
	}
	pointerID, keyboardID := s.pointerID, s.keyboardID
	s.mu.Unlock()
	if needPointer {
		logErr("purego/wayland: get_pointer", s.send(s.seatID, wlSeatOpGetPointer, wlMsgWriter{}.putU(pointerID)))
	}
	if needKeyboard {
		logErr("purego/wayland: get_keyboard", s.send(s.seatID, wlSeatOpGetKeyboard, wlMsgWriter{}.putU(keyboardID)))
	}
}

func (s *waylandSurface) handlePointer(opcode uint16, p []byte) {
	switch opcode {
	case wlPointerEventEnter:
		r := wlParser{p: p}
		if _, err := r.uint32(); err != nil { // serial
			return
		}
		if _, err := r.uint32(); err != nil { // surface
			return
		}
		x, err1 := r.fixed()
		y, err2 := r.fixed()
		if err1 != nil || err2 != nil {
			return
		}
		s.mu.Lock()
		s.lastPointerX, s.lastPointerY = x, y
		mods := s.modifiers
		s.mu.Unlock()
		s.emit(pointerEvent{X: x, Y: y, Moved: true, Modifiers: mods})
	case wlPointerEventLeave:
		s.mu.Lock()
		x, y := s.lastPointerX, s.lastPointerY
		mods := s.modifiers
		s.mu.Unlock()
		s.emit(pointerEvent{X: x, Y: y, Left: true, Modifiers: mods})
	case wlPointerEventMotion:
		r := wlParser{p: p}
		if _, err := r.uint32(); err != nil { // time
			return
		}
		x, err1 := r.fixed()
		y, err2 := r.fixed()
		if err1 != nil || err2 != nil {
			return
		}
		s.mu.Lock()
		s.lastPointerX, s.lastPointerY = x, y
		mods := s.modifiers
		s.mu.Unlock()
		s.emit(pointerEvent{X: x, Y: y, Moved: true, Modifiers: mods})
	case wlPointerEventButton:
		r := wlParser{p: p}
		if _, err := r.uint32(); err != nil { // serial
			return
		}
		if _, err := r.uint32(); err != nil { // time
			return
		}
		code, err1 := r.uint32()
		state, err2 := r.uint32()
		if err1 != nil || err2 != nil {
			return
		}
		btn := 1
		switch code {
		case linuxBtnRight:
			btn = 3
		case linuxBtnMiddle:
			btn = 2
		}
		s.mu.Lock()
		x, y := s.lastPointerX, s.lastPointerY
		mods := s.modifiers
		s.mu.Unlock()
		s.emit(pointerEvent{X: x, Y: y, Button: btn, Pressed: state != wlPointerButtonStateReleased, Modifiers: mods})
	case wlPointerEventAxis:
		r := wlParser{p: p}
		if _, err := r.uint32(); err != nil { // time
			return
		}
		axis, err1 := r.uint32()
		value, err2 := r.fixed()
		if err1 != nil || err2 != nil {
			return
		}
		s.emit(waylandScroll(s, axis, float32(value), true))
	case wlPointerEventAxisDiscrete:
		r := wlParser{p: p}
		axis, err1 := r.uint32()
		steps, err2 := r.int32()
		if err1 != nil || err2 != nil {
			return
		}
		s.emit(waylandScroll(s, axis, float32(steps), false))
	case wlPointerEventAxisValue120:
		r := wlParser{p: p}
		axis, err1 := r.uint32()
		value120, err2 := r.int32()
		if err1 != nil || err2 != nil {
			return
		}
		s.emit(waylandScroll(s, axis, float32(value120)/float32(wlPointerAxisValue120Unit), false))
	}
}

func waylandScroll(s *waylandSurface, axis uint32, delta float32, continuous bool) surfaceEvent {
	s.mu.Lock()
	x, y := s.lastPointerX, s.lastPointerY
	s.mu.Unlock()
	ev := scrollEvent{X: x, Y: y, Continuous: continuous}
	if axis == wlPointerAxisHorizontal {
		ev.DX = delta
	} else {
		ev.DY = delta
	}
	return ev
}

func (s *waylandSurface) handleKeyboard(opcode uint16, p []byte) {
	switch opcode {
	case wlKeyboardEventKeymap:
		s.handleKeyboardKeymap(p)
	case wlKeyboardEventEnter:
		s.emit(focusEvent{Gained: true})
	case wlKeyboardEventLeave:
		s.emit(focusEvent{Gained: false})
	case wlKeyboardEventKey:
		s.handleKeyboardKey(p)
	case wlKeyboardEventModifiers:
		s.handleKeyboardModifiers(p)
	case wlKeyboardEventRepeatInfo:
		r := wlParser{p: p}
		rate, err1 := r.int32()
		delay, err2 := r.int32()
		if err1 != nil || err2 != nil {
			return
		}
		st := &s.state.repeat
		st.mu.Lock()
		st.rate, st.delay = rate, delay
		st.mu.Unlock()
	}
}

func (s *waylandSurface) handleKeyboardKeymap(p []byte) {
	r := wlParser{p: p}
	format, err1 := r.uint32()
	size, err2 := r.uint32()
	fd, haveFD := s.takePendingFD()
	if haveFD {
		defer unix.Close(fd)
	}
	if err1 != nil || err2 != nil {
		return
	}
	if format != wlKeyboardKeymapFormatXKBV1 {
		return
	}
	if err := checkWireAllocBytes(uint64(size), maxKeymapBytes, "wl_keyboard.keymap"); err != nil {
		fyne.LogError("purego/wayland: closing connection", err)
		s.fail()
		return
	}
	if haveFD {
		s.loadKeymapFD(fd, size)
	}
}

func (s *waylandSurface) loadKeymapFD(fd int, size uint32) {
	if size == 0 {
		return
	}
	n, err := intFromUint32(size)
	if err != nil {
		return
	}
	data, err := unix.Mmap(fd, 0, n, unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return
	}
	text := mmapCString(data)
	km, err := parseXkbKeymap(text)
	_ = unix.Munmap(data)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.xkb = km
	s.mu.Unlock()
}

func mmapCString(data []byte) string {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return string(data[:i])
	}
	return string(data)
}

func (s *waylandSurface) handleKeyboardKey(p []byte) {
	r := wlParser{p: p}
	if _, err := r.uint32(); err != nil {
		return
	}
	if _, err := r.uint32(); err != nil {
		return
	}
	key, err1 := r.uint32()
	state, err2 := r.uint32()
	if err1 != nil || err2 != nil {
		return
	}
	s.mu.Lock()
	mods := s.modifiers
	s.mu.Unlock()
	ks, name, ru := s.resolveWlKey(key)
	pressed := state != wlKeyboardKeyStateReleased
	if pressed {
		wlStartKeyRepeat(s, key, mods, ks, name, ru)
	} else {
		wlStopKeyRepeat(s)
	}
	s.emit(keyEvent{
		Pressed:   pressed,
		Scancode:  key,
		Rune:      ru,
		Modifiers: mods,
		KeySym:    ks,
		Name:      name,
	})
}

func (s *waylandSurface) handleKeyboardModifiers(p []byte) {
	r := wlParser{p: p}
	if _, err := r.uint32(); err != nil {
		return
	}
	depressed, err1 := r.uint32()
	latched, err2 := r.uint32()
	locked, err3 := r.uint32()
	group, err4 := r.uint32()
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return
	}
	s.mu.Lock()
	s.wlModsDepressed = depressed
	s.wlModsLatched = latched
	s.wlModsLocked = locked
	s.wlGroup = group
	s.modifiers = decodeWlModifiers(depressed | latched | locked)
	s.mu.Unlock()
}

func (s *waylandSurface) resolveWlKey(evdev uint32) (uint32, fyne.KeyName, rune) {
	s.mu.Lock()
	km := s.xkb
	mods := s.wlModsDepressed | s.wlModsLatched | s.wlModsLocked
	group := s.wlGroup
	s.mu.Unlock()
	ks := km.lookupEvdev(evdev, mods, group)
	name, ru := keysymToKey(ks)
	return ks, name, ru
}

func wlStopKeyRepeat(s *waylandSurface) {
	st := &s.state.repeat
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.stop == nil {
		return
	}
	close(st.stop)
	st.stop = nil
}

func wlStartKeyRepeat(s *waylandSurface, key uint32, mods keyModifiers, ks uint32, name fyne.KeyName, ru rune) {
	wlStopKeyRepeat(s)
	st := &s.state.repeat
	st.mu.Lock()
	if st.rate <= 0 {
		st.mu.Unlock()
		return
	}
	delay := st.delay
	rate := st.rate
	stop := make(chan struct{})
	st.stop = stop
	st.mu.Unlock()

	interval := time.Second / time.Duration(rate)
	wait := time.Duration(delay) * time.Millisecond
	go func() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-s.done:
				return
			case <-timer.C:
				s.emit(keyEvent{
					Pressed:   true,
					Repeat:    true,
					Scancode:  key,
					Rune:      ru,
					Modifiers: mods,
					KeySym:    ks,
					Name:      name,
				})
				timer.Reset(interval)
			}
		}
	}()
}

func decodeWlModifiers(mods uint32) keyModifiers {
	var m keyModifiers
	if mods&xkbModShiftBit != 0 {
		m |= modShift
	}
	if mods&xkbModLockBit != 0 {
		m |= modLock
	}
	if mods&xkbModControlBit != 0 {
		m |= modControl
	}
	if mods&xkbMod1Bit != 0 {
		m |= modAlt
	}
	if mods&xkbMod4Bit != 0 {
		m |= modSuper
	}
	return m
}
