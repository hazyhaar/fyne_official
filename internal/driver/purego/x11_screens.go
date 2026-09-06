//go:build purego && linux

package purego

import (
	"encoding/binary"
	"fmt"
)

// RandR (RANDR X11 extension) request minor-opcodes and reply layout offsets
// used to enumerate monitors without Xlib/XCB. Extension requests are
// dispatched under the major opcode QueryExtension returns for "RANDR"
// (opQueryExtension is already defined in x11.go and reused here); the
// minor-opcode goes in the second request byte, exactly like a core request
// puts its opcode in the first.
const (
	randrExtensionName = "RANDR"

	randrMinorQueryVersion                   = 0
	randrMinorGetScreenResourcesCur          = 25
	randrMinorGetCrtcInfo                    = 20
	randrMinorSelectInput                    = 4 // X_RRSelectInput; 6 is RRGetScreenSizeRange
	randrRequestedMajor, randrRequestedMinor = 1, 2
	randrScreenChangeNotifyMask              = 1

	queryExtensionFirstEventByte = 10

	randrSelectInputReqLen, randrSelectInputReqWords = 12, 3

	offRRWidthPx  = 24
	offRRHeightPx = 26
	offRRWidthMM  = 28
	offRRHeightMM = 30

	queryExtensionReqHeaderLen = 8
	queryExtensionPresentByte  = 8
	queryExtensionMajorOpByte  = 9

	getCrtcInfoReqLen, getCrtcInfoReqWords = 12, 3

	getScreenResourcesReqLen, getScreenResourcesReqWords = 8, 2

	rrQueryVersionReqLen, rrQueryVersionReqWords = 12, 3

	screenResourcesHeaderLen = 32
	// Offsets within a GetScreenResourcesCurrent reply header.
	offNumCrtcs   = 16
	offConfigTime = 12

	// Offsets within a GetCrtcInfo reply header.
	offCrtcX      = 12
	offCrtcY      = 14
	offCrtcWidth  = 16
	offCrtcHeight = 18
)

var _ screenSurface = (*x11Surface)(nil)

func (s *x11Surface) queryExtension(name string) (majorOpcode, firstEvent byte, ok bool, err error) {
	pad := (setupWordSize - len(name)%setupWordSize) % setupWordSize
	reqWords := (queryExtensionReqHeaderLen + len(name) + pad) / setupWordSize
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opQueryExtension
	reqWords16, err := card16FromInt(reqWords)
	if err != nil {
		return 0, 0, false, err
	}
	nameLen16, err := card16FromInt(len(name))
	if err != nil {
		return 0, 0, false, err
	}
	binary.LittleEndian.PutUint16(req[2:4], reqWords16)
	binary.LittleEndian.PutUint16(req[4:6], nameLen16)
	copy(req[8:], name)
	reply, err := s.request(req)
	if err != nil {
		return 0, 0, false, err
	}
	if len(reply) <= queryExtensionFirstEventByte || reply[queryExtensionPresentByte] == 0 {
		return 0, 0, false, nil
	}
	return reply[queryExtensionMajorOpByte], reply[queryExtensionFirstEventByte], true, nil
}

// queryRandR resolves the RANDR extension's major opcode on this
// connection, or returns ok=false when the server has no RandR support.
func (s *x11Surface) queryRandR() (majorOpcode, firstEvent byte, ok bool, err error) {
	majorOpcode, firstEvent, ok, err = s.queryExtension(randrExtensionName)
	if err != nil || !ok {
		return 0, 0, false, err
	}
	verReq := make([]byte, rrQueryVersionReqLen)
	verReq[0] = majorOpcode
	verReq[1] = randrMinorQueryVersion
	binary.LittleEndian.PutUint16(verReq[2:4], rrQueryVersionReqWords)
	binary.LittleEndian.PutUint32(verReq[4:8], randrRequestedMajor)
	binary.LittleEndian.PutUint32(verReq[8:12], randrRequestedMinor)
	if _, err := s.request(verReq); err != nil {
		return 0, 0, false, err
	}
	return majorOpcode, firstEvent, true, nil
}

func (s *x11Surface) randrCrtcs(majorOpcode byte) ([]uint32, error) {
	req := make([]byte, getScreenResourcesReqLen)
	req[0] = majorOpcode
	req[1] = randrMinorGetScreenResourcesCur
	binary.LittleEndian.PutUint16(req[2:4], getScreenResourcesReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.rootWindow)
	reply, err := s.request(req)
	if err != nil {
		return nil, err
	}
	if len(reply) < screenResourcesHeaderLen || reply[0] != replyStatusReply {
		return nil, fmt.Errorf("purego/x11: RandR GetScreenResourcesCurrent error status %d", reply[0])
	}
	extra := reply[screenResourcesHeaderLen:]
	numCrtcs := int(binary.LittleEndian.Uint16(reply[offNumCrtcs : offNumCrtcs+2]))
	crtcs := make([]uint32, 0, numCrtcs)
	for i := 0; i < numCrtcs && (i+1)*4 <= len(extra); i++ {
		crtcs = append(crtcs, binary.LittleEndian.Uint32(extra[i*4:i*4+4]))
	}
	return crtcs, nil
}

func (s *x11Surface) randrCrtcGeometry(majorOpcode byte, crtc uint32) (x, y, width, height int, ok bool, err error) {
	req := make([]byte, getCrtcInfoReqLen)
	req[0] = majorOpcode
	req[1] = randrMinorGetCrtcInfo
	binary.LittleEndian.PutUint16(req[2:4], getCrtcInfoReqWords)
	binary.LittleEndian.PutUint32(req[4:8], crtc)
	binary.LittleEndian.PutUint32(req[8:12], 0)
	reply, err := s.request(req)
	if err != nil {
		return 0, 0, 0, 0, false, err
	}
	if len(reply) < screenResourcesHeaderLen || reply[0] != replyStatusReply {
		return 0, 0, 0, 0, false, nil
	}
	width = int(binary.LittleEndian.Uint16(reply[offCrtcWidth : offCrtcWidth+2]))
	height = int(binary.LittleEndian.Uint16(reply[offCrtcHeight : offCrtcHeight+2]))
	if width == 0 || height == 0 {
		return 0, 0, 0, 0, false, nil
	}
	x = intFromWireINT16(binary.LittleEndian.Uint16(reply[offCrtcX : offCrtcX+2]))
	y = intFromWireINT16(binary.LittleEndian.Uint16(reply[offCrtcY : offCrtcY+2]))
	return x, y, width, height, true, nil
}

type randrWatch struct {
	unsubID uint64
	done    chan struct{}
}

// subscribeRandRScreenChange selects RandR ScreenChangeNotify on the
// primary connection and forwards each notify as a scaleEvent.
func subscribeRandRScreenChange(s *x11Surface) {
	major, firstEvent, ok, err := s.queryRandR()
	if err != nil || !ok || firstEvent == 0 {
		return
	}
	if err := s.randrSelectInput(major); err != nil {
		return
	}
	s.screensMu.Lock()
	s.screensRandRChecked = true
	s.screensRandRMajor = major
	s.screensRandRFirstEvent = firstEvent
	s.screensRandROK = true
	s.screensMu.Unlock()
	id, ch := s.subscribeX11Event(firstEvent, s.rootWindow)
	watch := &randrWatch{unsubID: id, done: make(chan struct{})}
	s.state.mu.Lock()
	if old := s.state.randr; old != nil {
		close(old.done)
		s.unsubscribeX11Event(old.unsubID)
	}
	s.state.randr = watch
	s.state.mu.Unlock()
	go watch.forward(s, ch)
}

func stopRandRWatch(s *x11Surface) {
	s.state.mu.Lock()
	watch := s.state.randr
	s.state.randr = nil
	s.state.mu.Unlock()
	if watch == nil {
		return
	}
	close(watch.done)
	s.unsubscribeX11Event(watch.unsubID)
}

func (s *x11Surface) randrSelectInput(majorOpcode byte) error {
	req := make([]byte, randrSelectInputReqLen)
	req[0] = majorOpcode
	req[1] = randrMinorSelectInput
	binary.LittleEndian.PutUint16(req[2:4], randrSelectInputReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.rootWindow)
	binary.LittleEndian.PutUint16(req[8:10], randrScreenChangeNotifyMask)
	return s.writeRequest(req)
}

func decodeRandRScreenChange(buf []byte) scaleEvent {
	if len(buf) < coreEventSize {
		return scaleEvent{}
	}
	return scaleEvent{
		Width:    int(binary.LittleEndian.Uint16(buf[offRRWidthPx : offRRWidthPx+2])),
		Height:   int(binary.LittleEndian.Uint16(buf[offRRHeightPx : offRRHeightPx+2])),
		WidthMM:  int(binary.LittleEndian.Uint16(buf[offRRWidthMM : offRRWidthMM+2])),
		HeightMM: int(binary.LittleEndian.Uint16(buf[offRRHeightMM : offRRHeightMM+2])),
	}
}

func (w *randrWatch) forward(s *x11Surface, ch <-chan []byte) {
	defer s.unsubscribeX11Event(w.unsubID)
	for {
		select {
		case buf, ok := <-ch:
			if !ok {
				return
			}
			s.enqueueEvent(decodeRandRScreenChange(buf))
		case <-w.done:
			return
		case <-s.done:
			return
		}
	}
}

// Screens implements screenSurface (optional.go, core-owned). It reports
// physical geometry only (position, size, size in mm, which one is
// primary); computing scale from WidthMM/Width is the core's job per
// optional.go's doc-comment on screenSurface. When RandR is unavailable it
// falls back to a single screen built from the root screen geometry
// captured at connection setup, rather than returning an empty list.
func (s *x11Surface) Screens() ([]ScreenGeometry, error) {
	s.screensMu.Lock()
	defer s.screensMu.Unlock()

	if !s.screensRandRChecked {
		majorOpcode, firstEvent, ok, err := s.queryRandR()
		if err != nil {
			return nil, err
		}
		s.screensRandRChecked = true
		s.screensRandRMajor = majorOpcode
		s.screensRandRFirstEvent = firstEvent
		s.screensRandROK = ok
	}
	majorOpcode, ok := s.screensRandRMajor, s.screensRandROK
	if !ok {
		return []ScreenGeometry{{
			Width: s.rootWidthPx, Height: s.rootHeightPx,
			WidthMM: s.rootWidthMM, HeightMM: s.rootHeightMM,
			Primary: true,
		}}, nil
	}

	crtcs, err := s.randrCrtcs(majorOpcode)
	if err != nil {
		s.screensRandRChecked = false
		return nil, err
	}
	var screens []ScreenGeometry
	for _, crtc := range crtcs {
		x, y, w, h, enabled, err := s.randrCrtcGeometry(majorOpcode, crtc)
		if err != nil {
			s.screensRandRChecked = false
			return nil, err
		}
		if !enabled {
			continue
		}
		screens = append(screens, ScreenGeometry{
			X: x, Y: y, Width: w, Height: h,
			WidthMM: s.rootWidthMM, HeightMM: s.rootHeightMM,
			Primary: len(screens) == 0,
		})
	}
	if len(screens) == 0 {
		screens = append(screens, ScreenGeometry{
			Width: s.rootWidthPx, Height: s.rootHeightPx,
			WidthMM: s.rootWidthMM, HeightMM: s.rootHeightMM,
			Primary: true,
		})
	}
	return screens, nil
}
