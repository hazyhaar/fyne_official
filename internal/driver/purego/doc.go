//go:build purego

// Package purego provides a CGo-free Linux desktop driver for Fyne. It is
// selected exclusively through the `purego` build tag and has no effect on
// any other build.
//
// Two windowing backends share the surface interface in surface.go:
//
//   - X11, spoken as the core protocol over a Unix domain socket. Frames
//     are presented through MIT-SHM when the server supports it, otherwise
//     through core PutImage split into CARD16-sized horizontal bands.
//   - Wayland, spoken as the wire protocol over a Unix domain socket, with
//     wl_shm double buffering, xdg-shell, and wl_seat input.
//
// openSurface prefers $WAYLAND_DISPLAY and falls back to $DISPLAY (X11)
// when the Wayland socket cannot be used, so an XWayland session still
// works. Rendering is delegated to fyne.io/fyne/v2/internal/painter/software;
// this package turns scene graphs into pixels and moves the image onto the
// screen.
//
// Frame pipeline boundaries call probeStage, which is a nil-check against
// an optional SetFrameProbe hook. Production builds leave the hook unset.
//
// Known limits: xdg-shell has no client-side placement request, so
// CenterOnScreen is a no-op on Wayland. The window icon on Wayland is
// optional and requires the compositor to advertise xdg_toplevel_icon_v1.
package purego
