//go:build purego && linux

package purego

import (
	"sync"

	"fyne.io/fyne/v2"
)

const (
	zwpIdleInhibitMgrOpCreateInhibitor uint16 = 1
	zwpIdleInhibitorOpDestroy          uint16 = 0
)

type wlIdleState struct {
	mu          sync.Mutex
	inhibitorID uint32
}

var (
	wlIdleMu      sync.Mutex
	wlIdleLogged  bool
	wlIdleLogOnce sync.Once
)

func waylandIdleOf(s *waylandSurface) *wlIdleState {
	return &s.state.idle
}

func waylandSetDisableScreenBlanking(d *driverImpl, disable bool) {
	d.mu.Lock()
	windows := append([]*window(nil), d.windows...)
	d.mu.Unlock()
	for _, w := range windows {
		ws, ok := w.surface.(*waylandSurface)
		if !ok || ws == nil {
			continue
		}
		waylandSetIdleInhibit(ws, disable)
		return
	}
	if disable {
		waylandLogIdleInhibitMissing()
	}
}

func waylandSetIdleInhibit(s *waylandSurface, disable bool) {
	s.mu.Lock()
	mgr := s.idleInhibitMgrID
	surfaceID := s.surfaceID
	s.mu.Unlock()
	if mgr == 0 {
		// Only an attempt to inhibit is worth a log line; releasing a
		// never-created inhibitor at close time is not.
		if disable {
			waylandLogIdleInhibitMissing()
		}
		return
	}
	st := waylandIdleOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if disable {
		if st.inhibitorID != 0 {
			return
		}
		id := s.allocID()
		w := wlMsgWriter{}
		w.putUint32(id)
		w.putUint32(surfaceID)
		if err := s.send(mgr, zwpIdleInhibitMgrOpCreateInhibitor, w); err != nil {
			fyne.LogError("purego/wayland: idle_inhibit_manager.create_inhibitor", err)
			return
		}
		st.inhibitorID = id
		return
	}
	if st.inhibitorID == 0 {
		return
	}
	if err := s.send(st.inhibitorID, zwpIdleInhibitorOpDestroy, wlMsgWriter{}); err != nil {
		fyne.LogError("purego/wayland: idle_inhibitor.destroy", err)
		return
	}
	st.inhibitorID = 0
}

func waylandLogIdleInhibitMissing() {
	wlIdleLogOnce.Do(func() {
		wlIdleMu.Lock()
		wlIdleLogged = true
		wlIdleMu.Unlock()
		fyne.LogError("purego/wayland: zwp_idle_inhibit_manager_v1 is not advertised; SetDisableScreenBlanking is a no-op", nil)
	})
}

func waylandIdleInhibitLogged() bool {
	wlIdleMu.Lock()
	defer wlIdleMu.Unlock()
	return wlIdleLogged
}
