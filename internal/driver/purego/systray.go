//go:build purego

package purego

import (
	"bytes"
	"image"
	"image/draw"
	_ "image/jpeg" // JPEG resources used as tray icons
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"fyne.io/systray"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/internal/painter"
	"fyne.io/fyne/v2/internal/svg"
	"fyne.io/fyne/v2/lang"
	"fyne.io/fyne/v2/theme"
)

const systrayIconSize = 64

var (
	_ interface {
		SetSystemTrayMenu(*fyne.Menu)
		SetSystemTrayIcon(fyne.Resource)
		SetSystemTrayWindow(fyne.Window)
	} = (*driverImpl)(nil)

	// systrayMu guards the package state below: it is written from the
	// app goroutine, the tray callbacks and the driver-stop goroutine.
	systrayMu         sync.Mutex
	systrayIcon       fyne.Resource
	systrayRunning    bool
	trayStop          func()
	systraySkipOnce   sync.Once
	sessionBusPresent = findSessionBus
)

var systrayShortcutKeys = map[fyne.KeyName]string{
	fyne.KeyEnter:    "Enter",
	fyne.KeyPageDown: "PageDown",
	fyne.KeyPageUp:   "PageUp",
}

func findSessionBus() bool {
	if addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); addr != "" && addr != "autolaunch:" {
		return true
	}
	dir := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	if _, err := os.Stat(filepath.Join(dir, "bus")); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, "dbus-session"))
	return err == nil
}

func skipSystray() bool {
	if sessionBusPresent() {
		return false
	}
	systraySkipOnce.Do(func() {
		fyne.LogError("system tray unavailable without a session D-Bus", nil)
	})
	return true
}

func (d *driverImpl) SetSystemTrayMenu(m *fyne.Menu) {
	if skipSystray() {
		return
	}
	systrayMu.Lock()
	first := !systrayRunning
	systrayRunning = true
	systrayMu.Unlock()
	if first {
		d.runSystray(m)
	}
	d.refreshSystray(m)
}

func (d *driverImpl) runSystray(m *fyne.Menu) {
	var start, stop func()
	start, stop = systray.RunWithExternalLoop(func() {
		systrayMu.Lock()
		icon := systrayIcon
		systrayMu.Unlock()
		if icon != nil {
			d.SetSystemTrayIcon(icon)
		} else if app := fyne.CurrentApp(); app != nil && app.Icon() != nil {
			d.SetSystemTrayIcon(app.Icon())
		} else {
			d.SetSystemTrayIcon(theme.BrokenImageIcon())
		}

		if app := fyne.CurrentApp(); app != nil {
			title := app.Metadata().Name
			if title == "" {
				title = app.UniqueID()
			}
			systray.SetTitle(title)
		}

		if m != nil {
			d.DoFromGoroutine(func() {
				d.refreshSystray(m)
			}, false)
		}
	}, func() {})
	systrayMu.Lock()
	trayStop = stop
	systrayMu.Unlock()
	start()

	w := d.CreateWindow("SystrayMonitor")
	w.SetCloseIntercept(d.Quit)
	go func() {
		<-d.done
		systrayMu.Lock()
		stop := trayStop
		trayStop = nil
		systrayMu.Unlock()
		if stop != nil {
			stop()
		}
	}()
}

func systrayShortcutKey(key fyne.KeyName) string {
	if name, ok := systrayShortcutKeys[key]; ok {
		return name
	}
	return string(key)
}

func systrayModifiers(mod fyne.KeyModifier) (mods systray.KeyModifier) {
	if mod&fyne.KeyModifierShift != 0 {
		mods |= systray.KeyModifierShift
	}
	if mod&fyne.KeyModifierControl != 0 {
		mods |= systray.KeyModifierControl
	}
	if mod&fyne.KeyModifierAlt != 0 {
		mods |= systray.KeyModifierAlt
	}
	if mod&fyne.KeyModifierSuper != 0 {
		mods |= systray.KeyModifierSuper
	}
	return mods
}

func itemForMenuItem(i *fyne.MenuItem, parent *systray.MenuItem) *systray.MenuItem {
	if i.IsSeparator {
		if parent != nil {
			parent.AddSeparator()
		} else {
			systray.AddSeparator()
		}
		return nil
	}

	var item *systray.MenuItem
	if i.Checked {
		if parent != nil {
			item = parent.AddSubMenuItemCheckbox(i.Label, "", true)
		} else {
			item = systray.AddMenuItemCheckbox(i.Label, "", true)
		}
	} else if parent != nil {
		item = parent.AddSubMenuItem(i.Label, "")
	} else {
		item = systray.AddMenuItem(i.Label, "")
	}
	if i.Disabled {
		item.Disable()
	}
	if s, ok := i.Shortcut.(fyne.KeyboardShortcut); ok {
		item.SetShortcut(systrayModifiers(s.Mod()), systrayShortcutKey(s.Key()))
	}
	applyMenuItemIcon(item, i.Icon)
	return item
}

func applyMenuItemIcon(item *systray.MenuItem, icon fyne.Resource) {
	if item == nil || icon == nil {
		return
	}
	data := icon.Content()
	if svg.IsResourceSVG(icon) {
		b := &bytes.Buffer{}
		img := painter.PaintImage(fynecanvas.NewImageFromResource(icon), nil, systrayIconSize, systrayIconSize)
		if err := png.Encode(b, img); err != nil {
			fyne.LogError("Failed to encode SVG icon for menu", err)
		} else {
			data = b.Bytes()
		}
	}
	img, err := toOSIcon(data)
	if err != nil {
		fyne.LogError("Failed to convert systray icon", err)
		return
	}
	if _, ok := icon.(*theme.ThemedResource); ok {
		item.SetTemplateIcon(img, img)
	} else {
		item.SetIcon(img)
	}
}

func (d *driverImpl) refreshSystray(m *fyne.Menu) {
	systray.ResetMenu()
	d.refreshSystrayMenu(m, nil)
	addMissingQuitForMenu(m, d)
}

func (d *driverImpl) refreshSystrayMenu(m *fyne.Menu, parent *systray.MenuItem) {
	if m == nil {
		return
	}
	for _, i := range m.Items {
		item := itemForMenuItem(i, parent)
		if item == nil {
			continue
		}
		if i.ChildMenu != nil {
			d.refreshSystrayMenu(i.ChildMenu, item)
		}
		fn := i.Action
		go func() {
			// Ends when the tray item is gone or the driver stops, so a
			// quit leaves no goroutine behind per menu item.
			for {
				select {
				case _, ok := <-item.ClickedCh:
					if !ok {
						return
					}
					if fn != nil {
						d.DoFromGoroutine(fn, false)
					}
				case <-d.done:
					return
				}
			}
		}()
	}
}

func (*driverImpl) SetSystemTrayIcon(resource fyne.Resource) {
	if skipSystray() {
		return
	}
	systrayMu.Lock()
	systrayIcon = resource
	systrayMu.Unlock()
	if resource == nil {
		return
	}

	if svg.IsResourceSVG(resource) {
		img := fynecanvas.NewImageFromResource(resource)
		c := software.NewTransparentCanvas()
		c.SetContent(img)
		c.SetPadded(false)
		c.Resize(fyne.NewSquareSize(systrayIconSize))
		buf := &bytes.Buffer{}
		if err := png.Encode(buf, c.Capture()); err != nil {
			fyne.LogError("Failed to encode SVG icon for system tray icon", err)
			return
		}
		resource = fyne.NewStaticResource(resource.Name()+".png", buf.Bytes())
	}

	img, err := toOSIcon(resource.Content())
	if err != nil {
		fyne.LogError("Failed to convert systray icon", err)
		return
	}
	if _, ok := resource.(*theme.ThemedResource); ok {
		systray.SetTemplateIcon(img, img)
	} else {
		systray.SetIcon(img)
	}
}

func (d *driverImpl) SetSystemTrayWindow(w fyne.Window) {
	if skipSystray() {
		return
	}
	systrayMu.Lock()
	first := !systrayRunning
	systrayRunning = true
	systrayMu.Unlock()
	if first {
		d.runSystray(nil)
	}

	w.SetCloseIntercept(w.Hide)
	pw, ok := w.(*window)
	if !ok {
		systray.SetOnTapped(func() { fyne.Do(w.Show) })
		return
	}
	if pw.Padded() {
		systray.SetOnTapped(func() { fyne.Do(pw.Show) })
	} else {
		systray.SetOnTapped(func() { fyne.Do(pw.toggleVisible) })
	}
}

func (w *window) toggleVisible() {
	if w.visible {
		w.Hide()
	} else {
		w.Show()
	}
}

func toOSIcon(icon []byte) ([]byte, error) {
	img, format, err := image.Decode(bytes.NewReader(icon))
	if err != nil {
		return nil, err
	}
	if format == "png" {
		return icon, nil
	}
	return convertToPNG(img)
}

func convertToPNG(img image.Image) ([]byte, error) {
	bounds := img.Bounds()
	nrgba := image.NewNRGBA(bounds)
	draw.Draw(nrgba, bounds, img, bounds.Min, draw.Src)
	buf := &bytes.Buffer{}
	if err := png.Encode(buf, nrgba); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func addMissingQuitForMenu(menu *fyne.Menu, d *driverImpl) {
	if menu == nil {
		return
	}
	localQuit := lang.L("Quit")
	var lastItem *fyne.MenuItem
	if len(menu.Items) > 0 {
		lastItem = menu.Items[len(menu.Items)-1]
		if lastItem.Label == localQuit {
			lastItem.IsQuit = true
		}
	}
	if lastItem == nil || !lastItem.IsQuit {
		quitItem := fyne.NewMenuItem(localQuit, nil)
		quitItem.IsQuit = true
		menu.Items = append(menu.Items, fyne.NewMenuItemSeparator(), quitItem)
	}
	for _, item := range menu.Items {
		if item.IsQuit && item.Action == nil {
			item.Action = d.Quit
		}
	}
}
