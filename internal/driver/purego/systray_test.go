//go:build purego

package purego

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

func TestSetSystemTrayWithoutSessionBus(t *testing.T) {
	orig := sessionBusPresent
	sessionBusPresent = func() bool { return false }
	t.Cleanup(func() { sessionBusPresent = orig })

	d := newTestDriver()
	menu := fyne.NewMenu("Tray", fyne.NewMenuItem("Hello", func() {}))
	icon := theme.BrokenImageIcon()
	w := d.CreateWindow("main")

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.SetSystemTrayMenu(menu)
		d.SetSystemTrayIcon(icon)
		d.SetSystemTrayWindow(w)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetSystemTrayMenu/Icon/Window blocked without a session bus")
	}
	if systrayRunning {
		t.Fatal("systray started without a session bus")
	}
}
