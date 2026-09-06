//go:build purego

package purego

import (
	"image"

	"fyne.io/fyne/v2"
)

// logErr reports a failed call that the public Fyne API cannot return
// (Window, Clipboard, and the run loop have no error path).
func logErr(what string, err error) {
	if err != nil {
		fyne.LogError(what, err)
	}
}

// surfaceEvent is the common type for every event a surface can deliver
// through its event channel. The concrete types are defined below.
type surfaceEvent any

// surface abstracts one platform windowing backend (X11 or Wayland, chosen
// at open time) behind the small set of operations the purego window and
// driver need: open a native window, present a rendered frame into it,
// receive its input and lifecycle events, and close it. Keeping this
// interface narrow is what lets the X11 backend be replaced or complemented
// without touching driver.go or window.go.
type surface interface {
	// Present uploads img and schedules it to be shown on screen, replacing
	// whatever was previously displayed. img must not be modified by the
	// caller until the surface has taken a copy or completed the present.
	Present(img *image.NRGBA) error

	// Events returns the channel on which the surface delivers input and
	// lifecycle events for as long as the surface is open.
	Events() <-chan surfaceEvent

	// Resize asks the backend to change the native window's outer size, in
	// pixels. It does not itself resize the Fyne canvas; callers update the
	// canvas from the ResizeEvent the backend reports back once the request
	// completes (window managers may grant a different size).
	Resize(width, height int) error

	// SetTitle sets the window title shown by the window manager.
	SetTitle(title string) error

	// Close releases every native resource held by the surface. Present and
	// Resize must not be called again afterwards.
	Close() error
}

// wakeSurface is an optional surface capability: a one-slot channel signalled
// after an event has been delivered to Events(), so the driver can wait
// instead of polling.
type wakeSurface interface {
	Wake() <-chan struct{}
}

func signalWake(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// regionSurface is an optional surface capability: present only rectangle r
// of the full retained frame img. Backends that do not implement it receive
// a whole-frame Present instead.
type regionSurface interface {
	PresentRegion(img *image.NRGBA, r image.Rectangle) error
}

// keyEvent reports a keyboard press or release.
type keyEvent struct {
	Pressed   bool
	Repeat    bool
	Scancode  uint32
	Rune      rune
	Modifiers keyModifiers
	KeySym    uint32
	Name      fyne.KeyName
}

// pointerEvent reports pointer motion or a button transition. Wheel motion
// arrives separately as scrollEvent (X11 buttons 4/5 and wl_pointer.axis).
type pointerEvent struct {
	X, Y      int
	Button    int // 0 for pure motion
	Pressed   bool
	Moved     bool
	Modifiers keyModifiers
	// Left is set when the pointer has left the surface (X11 LeaveNotify,
	// wl_pointer.leave). Coordinates then reflect the last known position.
	Left bool
}

// focusEvent reports that the surface gained or lost keyboard focus
// (X11 FocusIn/FocusOut, wl_keyboard.enter/leave).
type focusEvent struct {
	Gained bool
}

// resizeEvent reports the window's new pixel size, following a Resize
// request or an external move by the window manager.
type resizeEvent struct {
	Width, Height int
}

// exposeEvent asks the window to redraw the given (possibly partial) area.
type exposeEvent struct {
	X, Y, Width, Height int
}

// closeEvent reports that the window should close (window manager delete
// request, or the connection to the display was lost).
type closeEvent struct{}

// keyModifiers mirrors the modifier bits reported by the backend wire
// protocol, decoded into a backend-neutral form before it reaches window.go.
type keyModifiers uint16

const (
	modShift keyModifiers = 1 << iota
	modLock
	modControl
	modAlt
	modSuper
)

// scrollEvent reports a wheel or axis motion at the pointer position.
// DX/DY are the raw backend steps (X11 buttons 4–7 and wl_pointer axis
// discrete ticks use ±1 per notch), converted to fyne.ScrollEvent units by
// window.go using the same scrollSpeed as internal/driver/glfw, unless
// Continuous is set: then DX/DY are already canvas points (Wayland
// wl_pointer.axis values in pixels) and must not be multiplied by 25.
type scrollEvent struct {
	X, Y       int
	DX, DY     float32
	Continuous bool
}

// dropEvent reports a file drop at a canvas position, mirroring the
// information glfw's dropCallback delivers to fyne.Window.SetOnDropped.
type dropEvent struct {
	Position fyne.Position
	URIs     []fyne.URI
}

// scaleEvent reports that the output scale or DPI for this surface changed.
// Factor is the integer buffer scale (Wayland wl_output / preferred_buffer_scale)
// when non-zero. Width/Height and WidthMM/HeightMM carry RandR geometry when
// known, so the window can recompute scale without a live xrandr query.
type scaleEvent struct {
	Factor            int
	Width, Height     int
	WidthMM, HeightMM int
}
