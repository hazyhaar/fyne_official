//go:build purego && linux && !ci

package app

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
)

func TestPuregoSetSystemTrayWindow(t *testing.T) {
	a := NewWithID("io.fyne.test.systray")
	var _ systrayDriver
	if _, ok := a.Driver().(systrayDriver); !ok {
		t.Fatal("purego driver is not a systrayDriver")
	}
	desk, ok := a.(desktop.App)
	if !ok {
		t.Fatal("purego app is not a desktop.App")
	}

	w := a.NewWindow("tray")
	menu := fyne.NewMenu("Tray", fyne.NewMenuItem("Hello", func() {}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		desk.SetSystemTrayMenu(menu)
		desk.SetSystemTrayIcon(theme.BrokenImageIcon())
		desk.SetSystemTrayWindow(w)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SetSystemTrayWindow blocked")
	}
}
