//go:build purego && linux

package purego

import "sync"

const (
	wlOutputEventGeometry uint16 = 0
	wlOutputEventMode     uint16 = 1
	wlOutputEventScale    uint16 = 3
	wlOutputModeCurrent   uint32 = 1

	wlSurfaceEventEnter                uint16 = 0
	wlSurfaceEventLeave                uint16 = 1
	wlSurfaceEventPreferredBufferScale uint16 = 2
	wlSurfaceOpSetBufferScale          uint16 = 8

	wlCompositorBindVersion uint32 = 6
)

var _ screenSurface = (*waylandSurface)(nil)

type wlOutputInfo struct {
	x, y         int
	widthMM      int
	heightMM     int
	width        int
	height       int
	scale        int
	haveGeometry bool
	haveMode     bool
}

type wlOutputs struct {
	mu             sync.Mutex
	byID           map[uint32]*wlOutputInfo
	order          []uint32
	entered        []uint32
	preferredScale int
	havePreferred  bool
	bufferWidth    int
	bufferHeight   int
}

func waylandOutputsOf(s *waylandSurface) *wlOutputs {
	st := &s.state.outs
	st.mu.Lock()
	if st.byID == nil {
		st.byID = make(map[uint32]*wlOutputInfo)
		st.preferredScale = 1
	}
	st.mu.Unlock()
	return st
}

func waylandRegisterOutput(s *waylandSurface, id uint32) {
	st := waylandOutputsOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.byID[id]; ok {
		return
	}
	st.byID[id] = &wlOutputInfo{scale: 1}
	st.order = append(st.order, id)
}

func waylandOutputDispatch(s *waylandSurface, objectID uint32, opcode uint16, p []byte) bool {
	st := waylandOutputsOf(s)
	st.mu.Lock()
	out, ok := st.byID[objectID]
	st.mu.Unlock()
	if !ok {
		return false
	}
	r := wlParser{p: p}
	switch opcode {
	case wlOutputEventGeometry:
		x, err1 := r.int32()
		y, err2 := r.int32()
		wmm, err3 := r.int32()
		hmm, err4 := r.int32()
		if _, err := r.int32(); err != nil || err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return true
		}
		if _, err := r.str(); err != nil {
			return true
		}
		if _, err := r.str(); err != nil {
			return true
		}
		st.mu.Lock()
		out.x, out.y = int(x), int(y)
		out.widthMM, out.heightMM = int(wmm), int(hmm)
		out.haveGeometry = true
		st.mu.Unlock()
	case wlOutputEventMode:
		flags, err1 := r.uint32()
		w, err2 := r.int32()
		h, err3 := r.int32()
		if err1 != nil || err2 != nil || err3 != nil {
			return true
		}
		if flags&wlOutputModeCurrent == 0 && out.haveMode {
			return true
		}
		st.mu.Lock()
		out.width, out.height = int(w), int(h)
		out.haveMode = true
		st.mu.Unlock()
	case wlOutputEventScale:
		factor, err := r.int32()
		if err != nil {
			return true
		}
		if factor < 1 {
			factor = 1
		}
		st.mu.Lock()
		out.scale = int(factor)
		st.mu.Unlock()
	}
	return true
}

func waylandSurfaceScaleDispatch(s *waylandSurface, objectID uint32, opcode uint16, p []byte) bool {
	s.mu.Lock()
	sid := s.surfaceID
	s.mu.Unlock()
	if objectID != sid || sid == 0 {
		return false
	}
	r := wlParser{p: p}
	switch opcode {
	case wlSurfaceEventEnter:
		outID, err := r.uint32()
		if err != nil {
			return true
		}
		waylandNoteEnteredOutput(s, outID, true)
		waylandApplyOutputScale(s)
		return true
	case wlSurfaceEventLeave:
		outID, err := r.uint32()
		if err != nil {
			return true
		}
		waylandNoteEnteredOutput(s, outID, false)
		waylandApplyOutputScale(s)
		return true
	case wlSurfaceEventPreferredBufferScale:
		factor, err := r.int32()
		if err != nil {
			return true
		}
		if factor < 1 {
			factor = 1
		}
		st := waylandOutputsOf(s)
		st.mu.Lock()
		st.preferredScale = int(factor)
		st.havePreferred = true
		st.mu.Unlock()
		waylandApplyOutputScale(s)
		return true
	default:
		return false
	}
}

func waylandNoteEnteredOutput(s *waylandSurface, outID uint32, enter bool) {
	st := waylandOutputsOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if enter {
		for _, id := range st.entered {
			if id == outID {
				return
			}
		}
		st.entered = append(st.entered, outID)
		return
	}
	kept := st.entered[:0]
	for _, id := range st.entered {
		if id != outID {
			kept = append(kept, id)
		}
	}
	st.entered = kept
}

func waylandSurfaceScaleFactor(s *waylandSurface) int {
	st := waylandOutputsOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.havePreferred && st.preferredScale >= 1 {
		return st.preferredScale
	}
	highest := 1
	for _, id := range st.entered {
		out := st.byID[id]
		if out != nil && out.scale > highest {
			highest = out.scale
		}
	}
	return highest
}

func waylandApplyOutputScale(s *waylandSurface) {
	factor := waylandSurfaceScaleFactor(s)
	s.mu.Lock()
	sw, sh := s.width, s.height
	s.mu.Unlock()
	st := waylandOutputsOf(s)
	st.mu.Lock()
	if sw > 0 && sh > 0 && factor > 0 {
		st.bufferWidth = sw * factor
		st.bufferHeight = sh * factor
	}
	st.mu.Unlock()
	if s.events != nil {
		s.emit(scaleEvent{Factor: factor})
	}
	waylandReallocForScale(s, factor)
}

func waylandReallocForScale(s *waylandSurface, factor int) {
	if s.conn == nil || factor < 1 {
		return
	}
	s.mu.Lock()
	shm := s.shmID
	w, h := s.width, s.height
	s.mu.Unlock()
	if shm == 0 || w < 1 || h < 1 {
		return
	}
	pw, ph := w*factor, h*factor
	logErr("purego/wayland: reallocate buffers for scale", s.allocateBuffers(pw, ph, pw*bytesPerPixelWl))
	wm := wlMsgWriter{}
	f32, err := int32FromInt(factor)
	if err != nil {
		logErr("purego/wayland: set_buffer_scale", err)
		return
	}
	wm.putInt32(f32)
	logErr("purego/wayland: set_buffer_scale", s.send(s.surfaceID, wlSurfaceOpSetBufferScale, wm))
}

func waylandPreferredBufferScale(s *waylandSurface) int {
	st := waylandOutputsOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.preferredScale < 1 {
		return 1
	}
	return st.preferredScale
}

func waylandBufferPixelSize(s *waylandSurface) (w, h int) {
	st := waylandOutputsOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.bufferWidth, st.bufferHeight
}

func (s *waylandSurface) Screens() ([]ScreenGeometry, error) {
	st := waylandOutputsOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.order) == 0 {
		return nil, nil
	}
	screens := make([]ScreenGeometry, 0, len(st.order))
	for i, id := range st.order {
		out := st.byID[id]
		if out == nil {
			continue
		}
		screens = append(screens, ScreenGeometry{
			X: out.x, Y: out.y,
			Width: out.width, Height: out.height,
			WidthMM: out.widthMM, HeightMM: out.heightMM,
			Primary: i == 0,
		})
	}
	return screens, nil
}
