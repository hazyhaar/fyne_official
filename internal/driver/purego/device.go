//go:build purego

package purego

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/lang"
)

type device struct{}

var _ fyne.Device = (*device)(nil)

func (*device) Locale() fyne.Locale                 { return lang.SystemLocale() }
func (*device) Orientation() fyne.DeviceOrientation { return fyne.OrientationHorizontalLeft }
func (*device) HasKeyboard() bool                   { return true }
func (*device) IsBrowser() bool                     { return false }
func (*device) IsMobile() bool                      { return false }
func (*device) SystemScaleForWindow(win fyne.Window) float32 {
	w, ok := win.(*window)
	if !ok || w == nil {
		return 1
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.shown || w.systemScale <= 0 {
		return 1
	}
	return w.systemScale
}
