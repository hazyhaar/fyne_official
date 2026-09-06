//go:build purego && linux

package purego

import "sync"

// x11SurfaceState holds the X11 extras that used to live in package maps
// keyed by *x11Surface. The zero value is created with the surface and
// released from Close.
type x11SurfaceState struct {
	mu     sync.Mutex
	cursor cursorFontState
	dnd    x11DndState
	clip   *clipboardOwner
	randr  *randrWatch
}

// waylandSurfaceState holds the Wayland extras that used to live in package
// maps keyed by *waylandSurface. The zero value is created with the surface
// and released from Close.
type waylandSurfaceState struct {
	cursor wlCursorState
	clip   wlClipboard
	outs   wlOutputs
	idle   wlIdleState
	dnd    wlDnd
	top    wlToplevelState
	repeat wlKeyRepeat
}
