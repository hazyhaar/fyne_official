//go:build purego && linux

package purego

import "os"

// hiddenClipboard returns a system clipboard that does not need a shown
// Fyne window. On X11 this is an ordinary unmapped x11Surface that reuses
// the connection's readLoop, request() and event subscriptions.
// On Wayland, wl_data_device.set_selection requires a serial from a pointer
// or keyboard event on a focused surface; without a window the compositor
// has no such serial, so this path is not available and the in-memory
// fallback in clipboard.go is used.
func hiddenClipboard() clipboardSurface {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return nil
	}
	if os.Getenv("DISPLAY") == "" {
		return nil
	}
	return x11HiddenClipboard{}
}
