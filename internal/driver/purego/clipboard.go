//go:build purego

package purego

import "fyne.io/fyne/v2"

// clipboard implements fyne.Clipboard for the application. When a shown
// window's surface implements clipboardSurface, reads and writes go through
// that surface (X11 CLIPBOARD or Wayland wl_data_device). With no such
// window it still talks to the system clipboard through a hidden auxiliary
// connection when a display server is reachable, and stores content in
// process memory only when that is not possible.
type clipboard struct {
	content string
}

var _ fyne.Clipboard = (*clipboard)(nil)

func (c *clipboard) Content() string {
	if cs := activeClipboardSurface(); cs != nil {
		text, err := cs.ClipboardText()
		if err == nil {
			return text
		}
	}
	return c.content
}

func (c *clipboard) SetContent(s string) {
	c.content = s
	if cs := activeClipboardSurface(); cs != nil {
		logErr("purego: SetClipboardText", cs.SetClipboardText(s))
	}
}

func activeClipboardSurface() clipboardSurface {
	if cs := clipboardSurfaceFromApp(); cs != nil {
		return cs
	}
	return hiddenClipboard()
}

func clipboardSurfaceFromApp() clipboardSurface {
	app := fyne.CurrentApp()
	if app == nil {
		return nil
	}
	d, ok := app.Driver().(*driverImpl)
	if !ok {
		return nil
	}
	d.mu.Lock()
	windows := append([]*window(nil), d.windows...)
	d.mu.Unlock()
	for _, w := range windows {
		w.mu.Lock()
		shown := w.shown && !w.closed
		surf := w.surface
		w.mu.Unlock()
		if !shown || surf == nil {
			continue
		}
		cs, ok := surf.(clipboardSurface)
		if ok {
			return cs
		}
	}
	return nil
}

// NewClipboard returns the application clipboard used by the purego driver.
func NewClipboard() fyne.Clipboard { return &clipboard{} }
