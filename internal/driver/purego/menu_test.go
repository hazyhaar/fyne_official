//go:build purego

package purego

import (
	"image"
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/internal/driver/menu"
	"fyne.io/fyne/v2/test"
)

func TestSetMainMenuOffsetsContentBelowBar(t *testing.T) {
	test.NewApp()
	w := newWindow("menu-offset")
	rect := fynecanvas.NewRectangle(color.NRGBA{R: 200, A: 255})
	w.SetContent(rect)
	w.canvas.Resize(fyne.NewSize(240, 180))
	yBefore := rect.Position().Y

	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", fyne.NewMenuItem("Open", func() {}))))

	if rect.Position().Y <= yBefore {
		t.Fatalf("content Y = %v after SetMainMenu, want greater than %v (content should sit below the menu bar)", rect.Position().Y, yBefore)
	}
}

func TestSetMainMenuAddsMissingQuit(t *testing.T) {
	test.NewApp()
	w := newWindow("menu-quit")
	w.canvas.Resize(fyne.NewSize(240, 180))
	main := fyne.NewMainMenu(fyne.NewMenu("File"))
	w.SetMainMenu(main)
	if len(main.Items) == 0 || len(main.Items[0].Items) == 0 {
		t.Fatal("SetMainMenu left the File menu empty; expected a Quit item")
	}
	last := main.Items[0].Items[len(main.Items[0].Items)-1]
	if !last.IsQuit {
		t.Fatalf("last File item IsQuit = false, label %q; expected addMissingQuitForMainMenu to insert Quit", last.Label)
	}
	if last.Action == nil {
		t.Fatal("inserted Quit item has a nil Action")
	}
}

func TestMenuBarEscapeDismisses(t *testing.T) {
	test.NewApp()
	w := newWindow("menu-escape")
	w.SetContent(fynecanvas.NewRectangle(color.NRGBA{R: 200, A: 255}))
	w.canvas.Resize(fyne.NewSize(240, 180))
	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", fyne.NewMenuItem("Open", func() {}))))
	bar, ok := w.canvas.menu.(*menu.MenuBar)
	if !ok || bar == nil {
		t.Fatal("expected MenuBar overlay")
	}
	bar.Toggle()
	if !bar.IsActive() {
		t.Fatal("Toggle did not activate the menu bar")
	}
	w.handleKey(keyEvent{Pressed: true, Name: fyne.KeyEscape})
	w.handleKey(keyEvent{Pressed: false, Name: fyne.KeyEscape})
	if bar.IsActive() {
		t.Fatal("Escape did not dismiss the menu bar")
	}
}

func TestAltReleaseTogglesMenuBar(t *testing.T) {
	test.NewApp()
	w := newWindow("menu-alt")
	w.SetContent(fynecanvas.NewRectangle(color.NRGBA{R: 200, A: 255}))
	w.canvas.Resize(fyne.NewSize(240, 180))
	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", fyne.NewMenuItem("Open", func() {}))))
	bar, ok := w.canvas.menu.(*menu.MenuBar)
	if !ok || bar == nil {
		t.Fatal("expected MenuBar overlay")
	}
	if bar.IsActive() {
		t.Fatal("menu bar started active")
	}
	w.handleKey(keyEvent{Pressed: true, Name: desktop.KeyAltLeft})
	if bar.IsActive() {
		t.Fatal("Alt press alone activated the menu bar")
	}
	w.handleKey(keyEvent{Pressed: false, Name: desktop.KeyAltLeft})
	if !bar.IsActive() {
		t.Fatal("Alt press then release did not activate the menu bar")
	}
}

func TestMainMenuShortcutRunsAction(t *testing.T) {
	test.NewApp()
	w := newWindow("menu-shortcut")
	w.canvas.Resize(fyne.NewSize(240, 180))
	ran := false
	item := fyne.NewMenuItem("DoIt", func() { ran = true })
	item.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierControl}
	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", item)))
	w.handleKey(keyEvent{Pressed: true, Name: fyne.KeyQ, Modifiers: modControl})
	if !ran {
		t.Fatal("Ctrl+Q did not run the menu item action")
	}
}

func TestCaptureDiffersInTopBandWithMainMenu(t *testing.T) {
	test.NewApp()
	w := newWindow("menu-capture")
	rect := fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 40, B: 40, A: 255})
	w.SetContent(rect)
	w.canvas.Resize(fyne.NewSize(240, 180))

	without := mustNRGBA(t, w.canvas.Capture())
	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", fyne.NewMenuItem("Open", func() {}))))
	withMenu := mustNRGBA(t, w.canvas.Capture())

	const band = 8
	if !topBandDiffers(without, withMenu, band) {
		t.Fatalf("top %d rows of Capture() are identical with and without SetMainMenu", band)
	}
}

func mustNRGBA(t *testing.T, img image.Image) *image.NRGBA {
	t.Helper()
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		t.Fatalf("Capture() returned %T, want *image.NRGBA", img)
	}
	return nrgba
}

func topBandDiffers(a, b *image.NRGBA, band int) bool {
	w := a.Rect.Dx()
	if b.Rect.Dx() < w {
		w = b.Rect.Dx()
	}
	h := band
	if a.Rect.Dy() < h {
		h = a.Rect.Dy()
	}
	if b.Rect.Dy() < h {
		h = b.Rect.Dy()
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if a.At(x, y) != b.At(x, y) {
				return true
			}
		}
	}
	return false
}
