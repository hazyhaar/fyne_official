//go:build purego && linux

package purego

import (
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

func TestDriverImplementsDesktopDriver(t *testing.T) {
	d := NewDriver()
	dd, ok := d.(desktop.Driver)
	if !ok {
		t.Fatal("purego driver does not implement desktop.Driver")
	}
	w := dd.CreateSplashWindow()
	if w.Padded() {
		t.Fatal("CreateSplashWindow returned a padded window")
	}
	if dd.HasSecondaryDisplay() && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Fatal("HasSecondaryDisplay is true with no display")
	}
}

func TestX11SplashOverrideRedirect(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("X11 splash test runs in the X11 suite only")
	}
	s, err := newX11Surface(64, 48, withOverrideRedirect())
	if err != nil {
		t.Fatalf("newX11Surface: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	got, err := s.windowOverrideRedirect()
	if err != nil {
		t.Fatalf("GetWindowAttributes: %v", err)
	}
	if !got {
		t.Fatal("override_redirect is 0, want 1")
	}
}

func TestX11SplashCentered(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("X11 splash test runs in the X11 suite only")
	}
	s, err := newX11Surface(64, 48, withOverrideRedirect())
	if err != nil {
		t.Fatalf("newX11Surface: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.centerOnScreen(); err != nil {
		t.Fatalf("centerOnScreen: %v", err)
	}
	x, y, err := s.windowGeometry()
	if err != nil {
		t.Fatalf("GetGeometry: %v", err)
	}
	screens, err := s.Screens()
	if err != nil || len(screens) == 0 {
		t.Fatalf("Screens: %v", err)
	}
	scr := screens[0]
	wantX := scr.X + (scr.Width-s.width)/2
	wantY := scr.Y + (scr.Height-s.height)/2
	if x != wantX || y != wantY {
		t.Fatalf("splash geometry = (%d,%d), want centered (%d,%d) on %dx%d", x, y, wantX, wantY, scr.Width, scr.Height)
	}
}

func TestCurrentKeyModifiersShiftXTest(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("XTEST modifier test runs in the X11 suite only")
	}
	d := NewDriver().(*driverImpl)
	dd, ok := fyne.Driver(d).(desktop.Driver)
	if !ok {
		t.Fatal("purego driver does not implement desktop.Driver")
	}
	w := d.CreateWindow("modifiers").(*window)
	w.Resize(fyne.NewSize(64, 48))
	w.Show()
	t.Cleanup(w.Close)

	if dd.CurrentKeyModifiers()&fyne.KeyModifierShift != 0 {
		t.Fatal("Shift reported before any key press")
	}

	sync := dialSyncX11(t)
	xt, err := queryXTestExtension(sync.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	code := shiftLeftKeycode(w.surface.(*x11Surface))
	if err := xt.fakeInput(sync.conn, xtestKeyPress, code, sync.rootWindow, 0, 0); err != nil {
		t.Fatalf("fakeInput Shift press: %v", err)
	}
	t.Cleanup(func() {
		_ = xt.fakeInput(sync.conn, xtestKeyRelease, code, sync.rootWindow, 0, 0)
	})

	if dd.CurrentKeyModifiers()&fyne.KeyModifierShift == 0 {
		t.Fatal("CurrentKeyModifiers does not contain Shift after XTEST press")
	}
}

func TestHasSecondaryScreenHelper(t *testing.T) {
	if hasSecondaryScreen([]ScreenGeometry{{Width: 1280, Height: 800, Primary: true}}) {
		t.Fatal("single screen reported as secondary")
	}
	if hasSecondaryScreen([]ScreenGeometry{
		{X: 0, Y: 0, Width: 1280, Height: 800, Primary: true},
		{X: 0, Y: 0, Width: 1280, Height: 800},
	}) {
		t.Fatal("mirrored screens reported as secondary")
	}
	if !hasSecondaryScreen([]ScreenGeometry{
		{X: 0, Y: 0, Width: 1280, Height: 800, Primary: true},
		{X: 1280, Y: 0, Width: 1920, Height: 1080},
	}) {
		t.Fatal("two distinct screens not reported as secondary")
	}
}
