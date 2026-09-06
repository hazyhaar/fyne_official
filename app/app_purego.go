//go:build purego && linux && !ci && !android && !ios && !mobile && !tamago && !noos && !tinygo

package app

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/internal/driver/purego"
)

// NewWithID returns a new app instance using the CGo-free Linux driver
// (X11 or Wayland) instead of GLFW and OpenGL. Selected with the `purego`
// build tag.
func NewWithID(id string) fyne.App {
	return newAppWithDriver(purego.NewDriver(), purego.NewClipboard(), id)
}
