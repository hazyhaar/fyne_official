package menu

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func TestNewMenuBarIsInactive(t *testing.T) {
	test.NewTempApp(t)
	w := test.NewWindow(nil)
	t.Cleanup(w.Close)
	bar := NewMenuBar(fyne.NewMainMenu(fyne.NewMenu("File", fyne.NewMenuItem("Open", func() {}))), w.Canvas())
	if bar.IsActive() {
		t.Fatal("NewMenuBar returned an active bar")
	}
	bar.Toggle()
	if !bar.IsActive() {
		t.Fatal("Toggle did not activate the bar")
	}
	bar.Toggle()
	if bar.IsActive() {
		t.Fatal("Toggle did not deactivate the bar")
	}
}
