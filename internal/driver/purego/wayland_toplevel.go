//go:build purego && linux

package purego

import (
	"sync"

	"fyne.io/fyne/v2"
)

const (
	xdgToplevelOpSetMaxSize      uint16 = 7
	xdgToplevelOpSetMinSize      uint16 = 8
	xdgToplevelOpSetFullscreen   uint16 = 11
	xdgToplevelOpUnsetFullscreen uint16 = 12
	xdgToplevelStateFullscreen   uint32 = 2
	wlNullBufferID               uint32 = 0
)

var (
	_ fullscreenSurface = (*waylandSurface)(nil)
	_ iconSurface       = (*waylandSurface)(nil)
	_ hideSurface       = (*waylandSurface)(nil)
	_ fixedSizeSurface  = (*waylandSurface)(nil)
)

// waylandSurface does not implement positionSurface: xdg-shell has no
// request that lets a client place a toplevel, so CenterOnScreen is an
// explicit no-op on this backend (the compositor chooses the position).

type wlToplevelState struct {
	mu         sync.Mutex
	fullscreen bool
	appID      string
}

func waylandToplevelOf(s *waylandSurface) *wlToplevelState {
	return &s.state.top
}

func waylandToplevelConfigure(s *waylandSurface, opcode uint16, p []byte) {
	if opcode != xdgToplevelEventConfigure {
		return
	}
	r := wlParser{p: p}
	if _, err := r.int32(); err != nil {
		return
	}
	if _, err := r.int32(); err != nil {
		return
	}
	n, err := r.uint32()
	if err != nil {
		return
	}
	fullscreen := false
	for n >= 4 {
		st, err := r.uint32()
		if err != nil {
			return
		}
		if st == xdgToplevelStateFullscreen {
			fullscreen = true
		}
		n -= 4
	}
	ts := waylandToplevelOf(s)
	ts.mu.Lock()
	ts.fullscreen = fullscreen
	ts.mu.Unlock()
}

func waylandToplevelIsFullscreen(s *waylandSurface) bool {
	ts := waylandToplevelOf(s)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.fullscreen
}

func (s *waylandSurface) attachBuffer(bufferID uint32) error {
	w := wlMsgWriter{}
	w.putUint32(bufferID)
	w.putInt32(0)
	w.putInt32(0)
	return s.send(s.surfaceID, wlSurfaceOpAttach, w)
}

// Hide implements hideSurface: wl_surface.attach(nil) followed by commit
// unmaps the role surface. The wl_shm buffers and the Fyne canvas stay
// allocated so Show can re-attach.
func (s *waylandSurface) Hide() error {
	s.mu.Lock()
	s.hidden = true
	s.mu.Unlock()
	if err := s.attachBuffer(wlNullBufferID); err != nil {
		return err
	}
	return s.send(s.surfaceID, wlSurfaceOpCommit, wlMsgWriter{})
}

// Show implements hideSurface: re-attach the last wl_shm buffer (if any)
// and commit so the compositor maps the toplevel again and sends
// xdg_surface.configure. A roundtrip waits for that configure without
// spinning.
func (s *waylandSurface) Show() error {
	s.mu.Lock()
	s.hidden = false
	var bufID uint32
	var bw, bh int
	for i := range s.buffers {
		if s.buffers[i].bufferID != 0 && s.buffers[i].mem != nil {
			bufID = s.buffers[i].bufferID
			bw = s.buffers[i].width
			bh = s.buffers[i].height
			break
		}
	}
	s.mu.Unlock()

	if bufID != 0 {
		if err := s.attachBuffer(bufID); err != nil {
			return err
		}
		bw32, err := int32FromInt(bw)
		if err != nil {
			return err
		}
		bh32, err := int32FromInt(bh)
		if err != nil {
			return err
		}
		if err := s.send(s.surfaceID, wlSurfaceOpDamageBuffer, func() wlMsgWriter {
			wm := wlMsgWriter{}
			wm.putInt32(0)
			wm.putInt32(0)
			wm.putInt32(bw32)
			wm.putInt32(bh32)
			return wm
		}()); err != nil {
			return err
		}
	}
	if err := s.send(s.surfaceID, wlSurfaceOpCommit, wlMsgWriter{}); err != nil {
		return err
	}
	return s.roundtrip()
}

func (s *waylandSurface) sendToplevelSize(opcode uint16, width, height int) error {
	w := wlMsgWriter{}
	width32, err := int32FromInt(width)
	if err != nil {
		return err
	}
	height32, err := int32FromInt(height)
	if err != nil {
		return err
	}
	w.putInt32(width32)
	w.putInt32(height32)
	return s.send(s.toplevelID, opcode, w)
}

// SetFixedSize implements fixedSizeSurface: xdg_toplevel.set_min_size and
// set_max_size are set to the current size when fixed, or to 0,0 (no
// constraint) when not.
func (s *waylandSurface) SetFixedSize(fixed bool) error {
	s.mu.Lock()
	w, h := s.width, s.height
	s.mu.Unlock()
	if !fixed {
		w, h = 0, 0
	}
	if err := s.sendToplevelSize(xdgToplevelOpSetMinSize, w, h); err != nil {
		return err
	}
	return s.sendToplevelSize(xdgToplevelOpSetMaxSize, w, h)
}

func (s *waylandSurface) SetFullscreen(full bool) error {
	if full {
		return s.send(s.toplevelID, xdgToplevelOpSetFullscreen, wlMsgWriter{}.putU(0))
	}
	return s.send(s.toplevelID, xdgToplevelOpUnsetFullscreen, wlMsgWriter{})
}

func (s *waylandSurface) SetWindowIcon(icon fyne.Resource) error {
	s.mu.Lock()
	mgr := s.iconMgrID
	s.mu.Unlock()
	if mgr != 0 && icon != nil {
		if err := waylandSetToplevelIcon(s, icon); err == nil {
			return nil
		}
	}
	// xdg-shell has no pixel icon request. Without
	// xdg_toplevel_icon_manager_v1 the resource name is published as
	// app_id so the compositor can still match a .desktop file.
	id := "fyne"
	if icon != nil && icon.Name() != "" {
		id = icon.Name()
	}
	w := wlMsgWriter{}
	w.putString(id)
	if err := s.send(s.toplevelID, xdgToplevelOpSetAppID, w); err != nil {
		return err
	}
	st := waylandToplevelOf(s)
	st.mu.Lock()
	st.appID = id
	st.mu.Unlock()
	return nil
}

func waylandToplevelAppID(s *waylandSurface) string {
	st := waylandToplevelOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.appID
}
