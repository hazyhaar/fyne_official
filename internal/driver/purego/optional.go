//go:build purego

package purego

import "fyne.io/fyne/v2"

// The interfaces below are optional capabilities a concrete surface may
// implement in addition to the mandatory surface contract in surface.go.
// The core discovers them with a type assertion on the active surface and
// falls back to a no-op when a surface does not implement one, so a backend
// can add a capability without changing surface.go or any other core file.
// New optional interfaces do not change how existing code already using
// `surface` calls it.

// clipboardSurface lets a backend back fyne.Clipboard with the platform's
// real selection mechanism (X11 CLIPBOARD, Wayland wl_data_device) instead
// of the process-local fallback in clipboard.go.
type clipboardSurface interface {
	ClipboardText() (string, error)
	SetClipboardText(text string) error
}

// screenSurface lets a backend report real monitor geometry and DPI so the
// core can compute scale and answer CenterOnScreen, instead of the single
// default-DPI screen assumption the core falls back to.
type screenSurface interface {
	Screens() ([]ScreenGeometry, error)
}

// ScreenGeometry is the backend-neutral shape a screenSurface reports a
// monitor in: position, size in pixels, and physical size in millimetres.
type ScreenGeometry struct {
	X, Y, Width, Height int
	WidthMM, HeightMM   int
	Primary             bool
}

// fullscreenSurface lets a backend actually request fullscreen from the
// window manager/compositor; without it, SetFullScreen only remembers the
// requested state (see window.go).
type fullscreenSurface interface {
	SetFullscreen(fullscreen bool) error
}

// iconSurface lets a backend publish a window icon to the window
// manager/taskbar; without it, SetIcon only remembers the resource
// (see window.go).
type iconSurface interface {
	SetWindowIcon(icon fyne.Resource) error
}

// focusSurface lets a backend actually request keyboard focus; without it,
// RequestFocus is a no-op. x11Surface implements it (see x11.go,
// RequestFocus).
type focusSurface interface {
	RequestFocus() error
}

// hideSurface lets a backend unmap an already-created native window without
// destroying the surface or the canvas, and map it again on Show. Without
// it, Hide is a documented no-op.
type hideSurface interface {
	Hide() error
	Show() error
}

// fixedSizeSurface lets a backend lock the native window's size (ICCCM
// WM_NORMAL_HINTS on X11, xdg_toplevel set_min_size/set_max_size on
// Wayland). Without it, SetFixedSize only remembers the requested state.
type fixedSizeSurface interface {
	SetFixedSize(fixed bool) error
}

// positionSurface lets a backend move the native window. Wayland's
// xdg-shell protocol does not allow a client to place a toplevel, so
// waylandSurface does not implement this and CenterOnScreen is a no-op
// there.
type positionSurface interface {
	SetPosition(x, y int) error
}
