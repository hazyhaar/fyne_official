//go:build purego && linux

package purego

import (
	"encoding/binary"
	"errors"
	"fmt"

	"fyne.io/fyne/v2"
)

const (
	opGetWindowAttributes = 3
	opGetGeometry         = 14
	opQueryPointer        = 38

	getWindowAttributesReqLen, getWindowAttributesReqWords = 8, 2
	getWindowAttributesOverrideOffset                      = 27

	getGeometryReqLen, getGeometryReqWords = 8, 2
	getGeometryXOffset                     = 12
	getGeometryYOffset                     = 14

	queryPointerReqLen, queryPointerReqWords = 8, 2
	queryPointerMaskOffset                   = 24
)

func (s *x11Surface) windowOverrideRedirect() (bool, error) {
	req := make([]byte, getWindowAttributesReqLen)
	req[0] = opGetWindowAttributes
	binary.LittleEndian.PutUint16(req[2:4], getWindowAttributesReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	reply, err := s.request(req)
	if err != nil {
		return false, err
	}
	if len(reply) <= getWindowAttributesOverrideOffset {
		return false, errors.New("purego/x11: GetWindowAttributes reply too short")
	}
	return reply[getWindowAttributesOverrideOffset] != 0, nil
}

func (s *x11Surface) windowGeometry() (x, y int, err error) {
	req := make([]byte, getGeometryReqLen)
	req[0] = opGetGeometry
	binary.LittleEndian.PutUint16(req[2:4], getGeometryReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	reply, err := s.request(req)
	if err != nil {
		return 0, 0, err
	}
	if len(reply) < getGeometryYOffset+2 {
		return 0, 0, errors.New("purego/x11: GetGeometry reply too short")
	}
	return intFromWireINT16(binary.LittleEndian.Uint16(reply[getGeometryXOffset : getGeometryXOffset+2])),
		intFromWireINT16(binary.LittleEndian.Uint16(reply[getGeometryYOffset : getGeometryYOffset+2])), nil
}

func (s *x11Surface) centerOnScreen() error {
	screens, err := s.Screens()
	if err != nil {
		return err
	}
	if len(screens) == 0 {
		return errors.New("purego/x11: no screen geometry to center on")
	}
	scr := screens[0]
	for i := range screens {
		if screens[i].Primary {
			scr = screens[i]
			break
		}
	}
	s.mu.Lock()
	width, height := s.width, s.height
	s.mu.Unlock()
	x := scr.X + (scr.Width-width)/2
	y := scr.Y + (scr.Height-height)/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	const configureReqLen, configureReqWords = 20, 5
	req := make([]byte, configureReqLen)
	req[0] = opConfigureWindow
	binary.LittleEndian.PutUint16(req[2:4], configureReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], configureValueX|configureValueY)
	x32, err := uint32FromInt(x)
	if err != nil {
		return err
	}
	y32, err := uint32FromInt(y)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(req[12:16], x32)
	binary.LittleEndian.PutUint32(req[16:20], y32)
	return s.writeRequest(req)
}

func (s *x11Surface) queryKeyModifiers() keyModifiers {
	req := make([]byte, queryPointerReqLen)
	req[0] = opQueryPointer
	binary.LittleEndian.PutUint16(req[2:4], queryPointerReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	reply, err := s.request(req)
	if err != nil || len(reply) < queryPointerMaskOffset+2 {
		return 0
	}
	state := binary.LittleEndian.Uint16(reply[queryPointerMaskOffset : queryPointerMaskOffset+2])
	return decodeModifiers(state)
}

const pc105ShiftLKeycode = 50

func shiftLeftKeycode(s *x11Surface) byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.minKeycode == 0 || s.maxKeycode < s.minKeycode {
		return pc105ShiftLKeycode
	}
	for code := s.minKeycode; code <= s.maxKeycode; code++ {
		if s.lookupKeysymLocked(code, 0) == xkShiftL {
			return code
		}
	}
	return pc105ShiftLKeycode
}

func hasSecondaryScreen(screens []ScreenGeometry) bool {
	if len(screens) < 2 {
		return false
	}
	px, py := screens[0].X, screens[0].Y
	for _, scr := range screens[1:] {
		if scr.X != px || scr.Y != py {
			return true
		}
	}
	return false
}

func currentKeyModifiers(d *driverImpl) fyne.KeyModifier {
	d.mu.Lock()
	windows := append([]*window(nil), d.windows...)
	d.mu.Unlock()
	for _, w := range windows {
		if w.surface == nil {
			continue
		}
		switch s := w.surface.(type) {
		case *x11Surface:
			return fyneKeyModifier(s.queryKeyModifiers())
		case *waylandSurface:
			s.mu.Lock()
			m := s.modifiers
			s.mu.Unlock()
			return fyneKeyModifier(m)
		}
	}
	return 0
}

func hasSecondaryDisplay(d *driverImpl) bool {
	d.mu.Lock()
	windows := append([]*window(nil), d.windows...)
	d.mu.Unlock()
	for _, w := range windows {
		if w.surface == nil {
			continue
		}
		ss, ok := w.surface.(screenSurface)
		if !ok {
			continue
		}
		screens, err := ss.Screens()
		if err != nil {
			fyne.LogError(fmt.Sprintf("purego: listing screens: %v", err), nil)
			continue
		}
		return hasSecondaryScreen(screens)
	}
	return false
}
