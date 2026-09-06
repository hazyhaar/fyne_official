//go:build purego && linux

package purego

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"

	"fyne.io/fyne/v2"
)

var errX11BlankingInactive = errors.New("purego/x11: screen blanking is not active")

const (
	screenSaverName = "MIT-SCREEN-SAVER"
	dpmsName        = "DPMS"

	ssMinorQueryVersion = 0
	ssMinorQueryInfo    = 1
	ssMinorSuspend      = 5

	ssQueryVersionReqLen, ssQueryVersionReqWords = 8, 2
	ssQueryInfoReqLen, ssQueryInfoReqWords       = 8, 2
	ssSuspendReqLen, ssSuspendReqWords           = 8, 2

	dpmsMinorGetVersion = 0
	dpmsMinorEnable     = 4
	dpmsMinorDisable    = 5

	dpmsGetVersionReqLen, dpmsGetVersionReqWords = 8, 2
	dpmsEnableReqLen, dpmsEnableReqWords         = 4, 1
	dpmsInfoReqLen, dpmsInfoReqWords             = 4, 1
)

var (
	x11BlankMu        sync.Mutex
	x11BlankDisabled  bool
	x11BlankLogged    bool
	x11BlankSurf      *x11Surface
	x11BlankSSMajor   byte
	x11BlankDPMSMajor byte
	x11BlankUseSS     bool
)

func x11SurfaceFromDriver(d *driverImpl) *x11Surface {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	windows := append([]*window(nil), d.windows...)
	d.mu.Unlock()
	for _, w := range windows {
		if w == nil {
			continue
		}
		w.mu.Lock()
		surf := w.surface
		w.mu.Unlock()
		if xs, ok := surf.(*x11Surface); ok {
			return xs
		}
	}
	return nil
}

func setDisableScreenBlanking(d *driverImpl, disable bool) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		waylandSetDisableScreenBlanking(d, disable)
		return
	}
	x11SetDisableScreenBlanking(d, disable)
}

func x11WatchDriverQuit(d *driverImpl) {
	if d == nil || d.done == nil {
		return
	}
	done := d.done
	go func() {
		<-done
		x11BlankMu.Lock()
		x11BlankResumeLocked()
		x11BlankMu.Unlock()
	}()
}

func x11SetDisableScreenBlanking(d *driverImpl, disable bool) {
	x11BlankMu.Lock()
	defer x11BlankMu.Unlock()
	if x11BlankDisabled == disable {
		return
	}
	if !disable {
		x11BlankResumeLocked()
		return
	}
	s := x11SurfaceFromDriver(d)
	if s == nil {
		if !x11BlankLogged {
			x11BlankLogged = true
			fyne.LogError("purego/x11: SetDisableScreenBlanking needs an open window", nil)
		}
		return
	}
	if major, _, ok, err := s.queryExtension(screenSaverName); err == nil && ok {
		if err := x11ScreenSaverQueryVersion(s, major); err != nil {
			fyne.LogError("purego/x11: ScreenSaverQueryVersion", err)
			return
		}
		if err := x11ScreenSaverSuspend(s, major, true); err != nil {
			fyne.LogError("purego/x11: ScreenSaverSuspend", err)
			return
		}
		if _, err := x11ScreenSaverQueryInfo(s, major); err != nil {
			fyne.LogError("purego/x11: ScreenSaverQueryInfo after Suspend", err)
			return
		}
		x11BlankSurf = s
		x11BlankSSMajor = major
		x11BlankUseSS = true
		x11BlankDisabled = true
		x11WatchDriverQuit(d)
		return
	}
	if major, _, ok, err := s.queryExtension(dpmsName); err == nil && ok {
		if err := x11DPMSGetVersion(s, major); err != nil {
			fyne.LogError("purego/x11: DPMSGetVersion", err)
			return
		}
		if err := x11DPMSSetEnabled(s, major, false); err != nil {
			fyne.LogError("purego/x11: DPMSDisable", err)
			return
		}
		x11BlankSurf = s
		x11BlankDPMSMajor = major
		x11BlankUseSS = false
		x11BlankDisabled = true
		x11WatchDriverQuit(d)
		return
	}
	if !x11BlankLogged {
		x11BlankLogged = true
		fyne.LogError("purego/x11: neither MIT-SCREEN-SAVER nor DPMS is available; SetDisableScreenBlanking is a no-op", nil)
	}
}

func x11BlankResumeLocked() {
	s := x11BlankSurf
	ssMajor := x11BlankSSMajor
	dpmsMajor := x11BlankDPMSMajor
	useSS := x11BlankUseSS
	x11BlankSurf = nil
	x11BlankSSMajor = 0
	x11BlankDPMSMajor = 0
	x11BlankUseSS = false
	x11BlankDisabled = false
	if s == nil || s.done == nil {
		return
	}
	select {
	case <-s.done:
		return
	default:
	}
	if useSS {
		if err := x11ScreenSaverSuspend(s, ssMajor, false); err != nil {
			fyne.LogError("purego/x11: ScreenSaverSuspend(false)", err)
		}
	} else if dpmsMajor != 0 {
		if err := x11DPMSSetEnabled(s, dpmsMajor, true); err != nil {
			fyne.LogError("purego/x11: DPMSEnable", err)
		}
	}
}

func x11ScreenSaverQueryVersion(s *x11Surface, major byte) error {
	req := make([]byte, ssQueryVersionReqLen)
	req[0] = major
	req[1] = ssMinorQueryVersion
	binary.LittleEndian.PutUint16(req[2:4], ssQueryVersionReqWords)
	req[4] = 1
	req[5] = 1
	_, err := s.request(req)
	return err
}

func x11ScreenSaverSuspend(s *x11Surface, major byte, suspend bool) error {
	req := make([]byte, ssSuspendReqLen)
	req[0] = major
	req[1] = ssMinorSuspend
	binary.LittleEndian.PutUint16(req[2:4], ssSuspendReqWords)
	if suspend {
		binary.LittleEndian.PutUint32(req[4:8], 1)
	}
	return s.writeRequest(req)
}

func x11ScreenSaverQueryInfo(s *x11Surface, major byte) (byte, error) {
	req := make([]byte, ssQueryInfoReqLen)
	req[0] = major
	req[1] = ssMinorQueryInfo
	binary.LittleEndian.PutUint16(req[2:4], ssQueryInfoReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.rootWindow)
	reply, err := s.request(req)
	if err != nil {
		return 0, err
	}
	if reply[0] != replyStatusReply {
		return 0, fmt.Errorf("purego/x11: ScreenSaverQueryInfo status %d", reply[0])
	}
	return reply[1], nil
}

func x11BlankingQueryInfoState() (byte, error) {
	x11BlankMu.Lock()
	defer x11BlankMu.Unlock()
	if x11BlankSurf == nil || !x11BlankUseSS {
		return 0, errX11BlankingInactive
	}
	return x11ScreenSaverQueryInfo(x11BlankSurf, x11BlankSSMajor)
}

func x11BlankingHeld() bool {
	x11BlankMu.Lock()
	defer x11BlankMu.Unlock()
	return x11BlankDisabled && x11BlankSurf != nil
}

func x11DPMSGetVersion(s *x11Surface, major byte) error {
	req := make([]byte, dpmsGetVersionReqLen)
	req[0] = major
	req[1] = dpmsMinorGetVersion
	binary.LittleEndian.PutUint16(req[2:4], dpmsGetVersionReqWords)
	binary.LittleEndian.PutUint16(req[4:6], 1)
	_, err := s.request(req)
	return err
}

func x11DPMSSetEnabled(s *x11Surface, major byte, enable bool) error {
	req := make([]byte, dpmsEnableReqLen)
	req[0] = major
	if enable {
		req[1] = dpmsMinorEnable
	} else {
		req[1] = dpmsMinorDisable
	}
	binary.LittleEndian.PutUint16(req[2:4], dpmsEnableReqWords)
	return s.writeRequest(req)
}
