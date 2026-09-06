//go:build purego && linux

package purego

import (
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/internal/driver/menu"
	_ "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestHarnessGetImageMenuBarDiffersInTopBand(t *testing.T) {
	const width, height = 240, 180

	d := newTestDriver()
	w := d.CreateWindow("harness-menu-bar").(*window)
	rect := fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 40, B: 40, A: 255})
	w.SetContent(rect)
	w.Resize(fyne.NewSize(width, height))
	w.Show()
	t.Cleanup(w.Close)

	w.canvas.EnsureMinSize()
	w.canvas.SetDirty()
	w.redrawIfDirty()
	time.Sleep(200 * time.Millisecond)

	sync := dialSyncX11(t)
	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatalf("surface is %T, want *x11Surface", w.surface)
	}
	without, err := getImageFull(sync.conn, xs.winID, width, height)
	if err != nil {
		t.Fatalf("getImageFull without menu: %v", err)
	}
	yWithout := rect.Position().Y

	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", fyne.NewMenuItem("Open", func() {}))))
	w.canvas.EnsureMinSize()
	w.canvas.SetDirty()
	w.redrawIfDirty()
	time.Sleep(200 * time.Millisecond)

	withMenu, err := getImageFull(sync.conn, xs.winID, width, height)
	if err != nil {
		t.Fatalf("getImageFull with menu: %v", err)
	}

	const band = 8
	if !topBandDiffers(without, withMenu, band) {
		t.Fatalf("top %d rows of GetImage are identical with and without SetMainMenu", band)
	}
	if rect.Position().Y <= yWithout {
		t.Fatalf("content Y = %v after SetMainMenu, want greater than %v (content should start below the menu bar)", rect.Position().Y, yWithout)
	}
}

func TestHarnessXTestMenuBarClickRunsAction(t *testing.T) {
	const width, height = 320, 240

	d := newTestDriver()
	w := d.CreateWindow("harness-menu-click").(*window)
	ran := false
	w.SetContent(fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 40, B: 40, A: 255}))
	w.Resize(fyne.NewSize(width, height))
	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", fyne.NewMenuItem("DoIt", func() { ran = true }))))
	w.Show()
	t.Cleanup(w.Close)

	w.canvas.EnsureMinSize()
	w.canvas.SetDirty()
	w.redrawIfDirty()

	bar, ok := w.canvas.menu.(*menu.MenuBar)
	if !ok || bar == nil || len(bar.Items) == 0 {
		t.Fatal("expected MenuBar overlay with at least one item")
	}
	fileItem := bar.Items[0]
	fileAbs := w.driver.AbsolutePositionForObject(fileItem)
	clickX := int16(fileAbs.X + fileItem.Size().Width/2)
	clickY := int16(fileAbs.Y + fileItem.Size().Height/2)

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}
	root := sync.rootWindow
	const leftButton = 1
	const motionAbsolute = 0
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput MotionNotify onto File: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonPress, leftButton, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput ButtonPress onto File: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, leftButton, root, clickX, clickY); err != nil {
		t.Fatalf("fakeInput ButtonRelease onto File: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	w.canvas.EnsureMinSize()
	w.redrawIfDirty()
	if !bar.IsActive() {
		t.Fatalf("menu bar is not active after XTEST click on File at (%d,%d)", clickX, clickY)
	}

	entry := fileItem.(interface{ Child() *widget.Menu }).Child()
	if len(entry.Items) == 0 {
		t.Fatal("dropdown has no items")
	}
	doIt := entry.Items[0]
	abs := w.driver.AbsolutePositionForObject(doIt)
	itemX := int16(abs.X + doIt.Size().Width/2)
	itemY := int16(abs.Y + doIt.Size().Height/2)
	if err := xt.fakeInput(sync.conn, xtestMotionNotify, motionAbsolute, root, itemX, itemY); err != nil {
		t.Fatalf("fakeInput MotionNotify onto DoIt: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonPress, leftButton, root, itemX, itemY); err != nil {
		t.Fatalf("fakeInput ButtonPress onto DoIt: %v", err)
	}
	if err := xt.fakeInput(sync.conn, xtestButtonRelease, leftButton, root, itemX, itemY); err != nil {
		t.Fatalf("fakeInput ButtonRelease onto DoIt: %v", err)
	}
	pumpFor(w, 500*time.Millisecond)
	if !ran {
		t.Fatalf("DoIt action did not run after XTEST click at (%d,%d)", itemX, itemY)
	}
}

func TestHarnessXTestMenuBarEscapeCloses(t *testing.T) {
	const width, height = 320, 240
	const xKeycodeEscape = 9

	d := newTestDriver()
	w := d.CreateWindow("harness-menu-escape").(*window)
	w.SetContent(fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 40, B: 40, A: 255}))
	w.Resize(fyne.NewSize(width, height))
	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", fyne.NewMenuItem("Open", func() {}))))
	w.Show()
	t.Cleanup(w.Close)

	w.canvas.EnsureMinSize()
	w.canvas.SetDirty()
	w.redrawIfDirty()

	bar := w.canvas.menu.(*menu.MenuBar)
	bar.Toggle()
	w.canvas.SetDirty()
	w.redrawIfDirty()
	if !bar.IsActive() {
		t.Fatal("Toggle did not activate the menu bar")
	}

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}
	xtestTap(t, xt, sync.conn, sync.rootWindow, xKeycodeEscape)
	pumpFor(w, 500*time.Millisecond)
	if bar.IsActive() {
		t.Fatal("Escape did not close the menu bar")
	}
}

func TestHarnessXTestMenuShortcutCtrlQ(t *testing.T) {
	const width, height = 320, 240
	const xKeycodeQ = 24
	const xKeycodeControlL = 37

	d := newTestDriver()
	w := d.CreateWindow("harness-menu-shortcut").(*window)
	ran := false
	item := fyne.NewMenuItem("DoIt", func() { ran = true })
	item.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierControl}
	w.SetContent(fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 40, B: 40, A: 255}))
	w.Resize(fyne.NewSize(width, height))
	w.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("File", item)))
	w.Show()
	t.Cleanup(w.Close)

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}
	xtestChord(t, xt, sync.conn, sync.rootWindow, xKeycodeControlL, xKeycodeQ)
	pumpFor(w, 500*time.Millisecond)
	if !ran {
		t.Fatal("Ctrl+Q did not run the menu item action")
	}
}
