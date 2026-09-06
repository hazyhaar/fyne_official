//go:build purego

package purego

import (
	"context"
	"math"
	"os"
	"slices"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	intapp "fyne.io/fyne/v2/internal/app"
	"fyne.io/fyne/v2/internal/driver"
)

const (
	defaultWidth  = 480
	defaultHeight = 320
)

// window implements fyne.Window over a surface (X11 or Wayland). Methods
// the active backend cannot act on still keep a value Fyne expects to
// round-trip (Icon, FixedSize, Padded) and forward it when the surface
// implements the matching optional interface in optional.go.
type window struct {
	mu sync.Mutex

	driver  *driverImpl
	surface surface
	canvas  *canvas

	title      string
	fixedSize  bool
	fullscreen bool
	master     bool
	icon       fyne.Resource
	mainMenu   *fyne.MainMenu

	shown    bool
	visible  bool
	centered bool
	closed   bool

	// systemScale is the scale detected for this window's output (RandR DPI
	// or wl_output factor). Device.SystemScaleForWindow returns it once
	// the window has been shown, and 1 before that.
	systemScale float32

	// localClipboard is the process-local fallback fyne.Clipboard (see
	// clipboard.go), created once and reused: it holds its content in a Go
	// field, not in the X server, so a fresh instance per Clipboard() call
	// would silently lose whatever was copied on every subsequent call.
	localClipboard fyne.Clipboard

	onClosed       func()
	closeIntercept func()
	onDropped      func(fyne.Position, []fyne.URI)

	mouseOver            desktop.Hoverable
	mousePos             fyne.Position
	mouseButton          desktop.MouseButton
	mousePressed         fyne.CanvasObject
	mouseDragged         fyne.Draggable
	mouseDraggedObjStart fyne.Position
	mouseDraggedOffset   fyne.Position
	mouseDragPos         fyne.Position
	mouseDragStarted     bool
	mouseLastClick       fyne.CanvasObject
	mouseClickCount      int
	mouseCancelFunc      context.CancelFunc
	mouseMod             keyModifiers
	pointerMu            sync.Mutex
	cursor               desktop.Cursor
	wakeOnce             sync.Once

	menuTogglePending       fyne.KeyName
	menuDeactivationPending fyne.KeyName
}

// cursorSurface is satisfied by a backend that can change the pointer
// shape. window.go calls it on hover; a surface that does not implement it
// is a silent no-op.
type cursorSurface interface {
	SetCursor(cursor desktop.Cursor) error
}

var _ fyne.Window = (*window)(nil)

func newWindow(title string) *window {
	w := &window{title: title, canvas: newCanvas()}
	return w
}

func (w *window) Title() string { return w.title }

func (w *window) SetTitle(title string) {
	w.title = title
	if w.surface != nil {
		_ = w.surface.SetTitle(title)
	}
}

// FullScreen and SetFullScreen track the requested state; when the active
// surface implements the optional fullscreenSurface (see optional.go —
// _NET_WM_STATE_FULLSCREEN on X11, xdg_toplevel.set_fullscreen on Wayland),
// the request is also forwarded to it. Without that optional interface the
// state is memorised only.
func (w *window) FullScreen() bool { return w.fullscreen }

func (w *window) SetFullScreen(full bool) {
	w.fullscreen = full
	if fs, ok := w.surface.(fullscreenSurface); ok {
		_ = fs.SetFullscreen(full)
	}
}

// Window dimensions accepted by this driver, in pixels. Anything outside is
// clamped before it reaches the canvas or the display server: a zero or
// negative request would otherwise be reinterpreted by the X11 wire protocol
// (a CARD16 field) as a window tens of thousands of pixels wide, and the
// resulting configure notification would make the canvas allocate a frame of
// several gigabytes.
const (
	minWindowDim = 1
	maxWindowDim = 16384
)

func clampWindowDim(v int) int {
	if v < minWindowDim {
		return minWindowDim
	}
	if v > maxWindowDim {
		return maxWindowDim
	}
	return v
}

func clampWindowSize(size fyne.Size) fyne.Size {
	return fyne.NewSize(float32(clampWindowDim(int(size.Width))), float32(clampWindowDim(int(size.Height))))
}

func (w *window) Resize(size fyne.Size) {
	size = clampWindowSize(size)
	w.canvas.Resize(size)
	if w.surface != nil {
		_ = w.surface.Resize(int(size.Width), int(size.Height))
	}
}

// RequestFocus asks the active surface to take keyboard focus through the
// optional focusSurface interface (x11Surface implements it via
// SetInputFocus, see x11.go).
func (w *window) RequestFocus() {
	if fs, ok := w.surface.(focusSurface); ok {
		_ = fs.RequestFocus()
	}
}

func (w *window) FixedSize() bool { return w.fixedSize }

func (w *window) SetFixedSize(fixed bool) {
	w.fixedSize = fixed
	if fs, ok := w.surface.(fixedSizeSurface); ok {
		_ = fs.SetFixedSize(fixed)
	}
}

// CenterOnScreen places the window at the centre of the current output.
// Called before Show, the request is stored and applied by ConfigureWindow
// on X11 before the first MapWindow. After Show, a single ConfigureWindow
// moves the already-mapped window. On Wayland, xdg-shell does not let a
// client set its own position: the compositor places the toplevel, so this
// is a documented no-op.
func (w *window) CenterOnScreen() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.centered = true
	if w.surface == nil {
		return
	}
	w.centerOnScreenLocked()
}

func (w *window) centerOnScreenLocked() {
	ps, ok := w.surface.(positionSurface)
	if !ok {
		return
	}
	ss, ok := w.surface.(screenSurface)
	if !ok {
		return
	}
	screens, err := ss.Screens()
	if err != nil || len(screens) == 0 {
		return
	}
	scr := screens[0]
	for i := range screens {
		if screens[i].Primary {
			scr = screens[i]
			break
		}
	}
	size := w.canvas.Size()
	ww, wh := int(size.Width), int(size.Height)
	if ww <= 0 {
		ww = defaultWidth
	}
	if wh <= 0 {
		wh = defaultHeight
	}
	x := scr.X + (scr.Width-ww)/2
	y := scr.Y + (scr.Height-wh)/2
	_ = ps.SetPosition(x, y)
}

func (w *window) Padded() bool { return w.canvas.padded }

func (w *window) SetPadded(padded bool) { w.canvas.SetPadded(padded) }

func (w *window) Icon() fyne.Resource { return w.icon }

// SetIcon remembers the resource and, when the active surface implements
// the optional iconSurface (see optional.go — _NET_WM_ICON on X11,
// xdg_toplevel_icon_v1 on Wayland when advertised), publishes it to the
// window manager.
func (w *window) SetIcon(icon fyne.Resource) {
	w.icon = icon
	if is, ok := w.surface.(iconSurface); ok {
		_ = is.SetWindowIcon(icon)
	}
}

func (w *window) SetMaster() { w.master = true }

func (w *window) MainMenu() *fyne.MainMenu { return w.mainMenu }

func (w *window) SetMainMenu(menu *fyne.MainMenu) {
	w.mainMenu = menu
	w.canvas.buildMenu(w, menu)
}

func (w *window) SetOnClosed(fn func())                           { w.onClosed = fn }
func (w *window) SetCloseIntercept(fn func())                     { w.closeIntercept = fn }
func (w *window) SetOnDropped(fn func(fyne.Position, []fyne.URI)) { w.onDropped = fn }

func (w *window) Show() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.shown {
		w.showMappedLocked()
		return
	}
	if w.surface == nil {
		width, height := defaultWidth, defaultHeight
		if size := w.canvas.Size(); size.Width > 0 && size.Height > 0 {
			width = int(size.Width)
			height = int(size.Height)
		}
		surf, err := openSurface(width, height)
		if err != nil {
			fyne.LogError("purego: failed to open window", err)
			return
		}
		w.surface = surf
		w.systemScale = detectedScale(surf)
		w.canvas.scale = windowScale(w.systemScale)
		w.canvas.Resize(fyne.NewSize(float32(width), float32(height)))
		if xs, ok := surf.(*x11Surface); ok {
			subscribeRandRScreenChange(xs)
		}
	}
	if w.title != "" {
		_ = w.surface.SetTitle(w.title)
	}
	w.shown = true
	w.visible = true
	if w.driver != nil {
		w.driver.watchWindow(w)
	}
	if w.fixedSize {
		if fs, ok := w.surface.(fixedSizeSurface); ok {
			_ = fs.SetFixedSize(true)
		}
	}
	w.finishFirstShowLocked()
	// GLFW's glfwShowWindow focuses the window it shows by default (documented)
	// behaviour, absent GLFW_FOCUS_ON_SHOW=false), which is why an ordinary
	// glfw-backed Fyne window can receive keyboard input right after Show()
	// with no separate RequestFocus call from application code. Without a
	// window manager (bare Xvfb, as under this package's own tests), X11's
	// default input focus mode is PointerRoot: nothing but a pointer motion
	// grants a freshly mapped window the keyboard, so a focused
	// widget.Focusable would never receive a key event at all — confirmed
	// directly: TestHarnessXTestKeyReachesFocusedEntry only starts passing
	// once Show() requests focus here, with no change to the test itself.
	if fs, ok := w.surface.(focusSurface); ok {
		_ = fs.RequestFocus()
	}
}

func (w *window) finishFirstShowLocked() {
	if w.centered {
		w.centerOnScreenLocked()
	}
	switch s := w.surface.(type) {
	case *x11Surface:
		_ = s.mapWindow()
		if w.centered {
			w.centerOnScreenLocked()
		}
	case *waylandSurface:
	default:
		if hs, ok := w.surface.(hideSurface); ok {
			_ = hs.Show()
		}
	}
}

// showMappedLocked re-maps an already-opened surface after Hide. The first
// Show path above creates the native window; this path must not open a
// second one. The caller holds w.mu.
func (w *window) showMappedLocked() {
	if w.visible || w.closed || w.surface == nil {
		return
	}
	if hs, ok := w.surface.(hideSurface); ok {
		_ = hs.Show()
	}
	w.visible = true
	w.canvas.SetDirty()
	w.redrawIfDirty()
}

// openSurface selects the windowing backend the same way glfw's own Wayland
// detection does: $WAYLAND_DISPLAY first, since a Wayland session also
// exposes an XWayland $DISPLAY that would otherwise be picked in preference
// to the native backend; $DISPLAY (X11) otherwise. When $WAYLAND_DISPLAY is
// set but newWaylandSurface fails, this falls back to X11 rather than
// failing outright, so the driver stays usable on an XWayland session.
func openSurface(width, height int) (surface, error) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		s, err := newWaylandSurface(width, height)
		if err == nil {
			return s, nil
		}
		fyne.LogError("purego: Wayland backend unavailable, falling back to X11", err)
	}
	return openX11("", width, height, withoutMap())
}

func (w *window) Hide() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.shown || !w.visible || w.closed || w.surface == nil {
		return
	}
	if hs, ok := w.surface.(hideSurface); ok {
		_ = hs.Hide()
	}
	w.visible = false
}

func (w *window) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	surface := w.surface
	onClosed := w.onClosed
	w.onClosed = nil
	w.mu.Unlock()

	if w.driver != nil && w.driver.focused == w {
		w.applyFocus(false)
	}

	if surface != nil {
		_ = surface.Close() // close is idempotent
	}
	if onClosed != nil {
		onClosed()
	}
	if w.driver != nil {
		w.driver.removeWindow(w)
	}
	w.canvas.releaseCaches()
}

func (w *window) requestClose() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	intercept := w.closeIntercept
	w.mu.Unlock()
	if intercept != nil {
		intercept()
		return
	}
	w.Close()
}

func (w *window) ShowAndRun() {
	w.Show()
	if w.driver != nil {
		w.driver.Run()
	}
}

func (w *window) Content() fyne.CanvasObject { return w.canvas.Content() }

func (w *window) SetContent(content fyne.CanvasObject) {
	w.canvas.SetContent(content)
}

func (w *window) Canvas() fyne.Canvas { return w.canvas }

// Clipboard returns a clipboard backed by the active surface's optional
// clipboardSurface (see optional.go) when it implements one, or the
// process-local fallback in clipboard.go otherwise — see that file for why
// the fallback does not talk to the X11 CLIPBOARD/PRIMARY selections.
func (w *window) Clipboard() fyne.Clipboard {
	if cs, ok := w.surface.(clipboardSurface); ok {
		return &surfaceClipboard{surface: cs}
	}
	if w.localClipboard == nil {
		w.localClipboard = NewClipboard()
	}
	return w.localClipboard
}

// surfaceClipboard adapts a clipboardSurface to fyne.Clipboard, swallowing
// errors the same way the rest of this optional-capability wiring does: a
// failed native clipboard call degrades to an empty read or a dropped
// write rather than propagating, since fyne.Clipboard has no error return.
type surfaceClipboard struct {
	surface clipboardSurface
}

func (c *surfaceClipboard) Content() string {
	text, err := c.surface.ClipboardText()
	if err != nil {
		return ""
	}
	return text
}

func (c *surfaceClipboard) SetContent(content string) {
	logErr("purego: SetClipboardText", c.surface.SetClipboardText(content))
}

// drainEvents converts pending surfaceEvents into Fyne callbacks/canvas
// state changes. It must only be called from the driver's Run loop.
func (w *window) drainEvents() {
	if w.surface == nil {
		return
	}
	// Without a running main loop (tests pumping a window by hand) the
	// work posted through DoFromGoroutine would never run: execute it here,
	// on the pumping goroutine, so no call ever runs concurrently with it.
	if w.driver != nil && !w.driver.running.Load() {
		w.driver.drainFuncQueue()
	}
	for {
		select {
		case ev := <-w.surface.Events():
			w.handleEvent(ev)
		default:
			return
		}
	}
}

func (w *window) applyFocus(gained bool) {
	if gained {
		if w.driver != nil && w.driver.focused == nil {
			if f := lifecycleHook((*intapp.Lifecycle).OnEnteredForeground); f != nil {
				f()
			}
		}
		if w.driver != nil {
			w.driver.focused = w
		}
		w.canvas.FocusGained()
		return
	}
	w.canvas.FocusLost()
	w.menuTogglePending = desktop.KeyNone
	w.menuDeactivationPending = desktop.KeyNone
	if w.driver == nil || w.driver.focused != w {
		return
	}
	w.driver.focused = nil
	if f := lifecycleHook((*intapp.Lifecycle).OnExitedForeground); f != nil {
		f()
	}
}

func (w *window) handleEvent(ev surfaceEvent) {
	switch e := ev.(type) {
	case resizeEvent:
		w.canvas.Resize(clampWindowSize(fyne.NewSize(float32(e.Width), float32(e.Height))))
	case exposeEvent:
		w.canvas.SetDirty()
	case closeEvent:
		w.requestClose()
	case pointerEvent:
		w.handlePointer(e)
	case scrollEvent:
		w.handleScroll(e)
	case keyEvent:
		w.handleKey(e)
	case focusEvent:
		w.applyFocus(e.Gained)
	case dropEvent:
		if w.onDropped != nil {
			w.onDropped(e.Position, e.URIs)
		}
	case scaleEvent:
		w.applyScaleChange(e)
	}
}

// applyScaleChange recalculates the window scale, resizes the canvas in
// points from the current pixel size, and marks the whole canvas dirty,
// matching glfw's processMoved/reloadScale/RescaleContext path.
func (w *window) applyScaleChange(e scaleEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	detected := float32(0)
	switch {
	case e.Factor > 0:
		detected = float32(e.Factor)
	case e.Width > 0 && e.WidthMM > 0:
		detected = scaleFromGeometry(e.Width, e.WidthMM)
	default:
		if w.surface != nil {
			detected = detectedScale(w.surface)
		}
	}
	if detected <= 0 {
		detected = 1
	}
	newScale := windowScale(detected)
	oldScale := w.canvas.scale
	if oldScale <= 0 {
		oldScale = 1
	}
	if detected == w.systemScale && newScale == oldScale {
		return
	}
	pixelW := int(w.canvas.size.Width * oldScale)
	pixelH := int(w.canvas.size.Height * oldScale)
	if pixelW < minWindowDim {
		pixelW = defaultWidth
	}
	if pixelH < minWindowDim {
		pixelH = defaultHeight
	}
	w.systemScale = detected
	w.canvas.scale = newScale
	w.canvas.Resize(clampWindowSize(fyne.NewSize(float32(pixelW)/newScale, float32(pixelH)/newScale)))
}

// handlePointer routes pointer motion and button transitions the same way
// internal/driver/glfw/window.go does in processMouseMoved / processMouseClicked:
// hover on motion, MouseDown on press, Unfocus, drag while the button is held,
// MouseUp / DragEnd on release, TappedSecondary on the secondary button, and
// Tapped or DoubleTapped on the primary button. DoubleTappable objects wait
// for DoubleTapDelay before Tapped, matching mouseClickedHandleTapDoubleTap.
func (w *window) handlePointer(e pointerEvent) {
	w.pointerMu.Lock()
	defer w.pointerMu.Unlock()
	w.mouseMod = e.Modifiers

	pos := fyne.NewPos(float32(e.X), float32(e.Y))
	if e.Left {
		if !w.objIsDragged(w.mouseOver) {
			w.mouseOut()
		}
		w.applyCursor(desktop.DefaultCursor)
		return
	}
	if e.Moved {
		w.processPointerMoved(pos)
	}
	if e.Button != 0 {
		if btn := desktopMouseButton(e.Button); btn != 0 {
			w.processPointerClicked(pos, btn, e.Pressed)
		}
	}
}

func desktopMouseButton(button int) desktop.MouseButton {
	switch button {
	case 1:
		return desktop.MouseButtonPrimary
	case 2:
		return desktop.MouseButtonTertiary
	case 3:
		return desktop.MouseButtonSecondary
	default:
		return 0
	}
}

func (w *window) findObjectAtPositionMatching(pos fyne.Position, matches func(fyne.CanvasObject) bool) (fyne.CanvasObject, fyne.Position) {
	var overlay, menu, content fyne.CanvasObject
	if w.canvas != nil {
		overlay = w.canvas.Overlays().Top()
		menu = w.canvas.menu
		content = w.canvas.Content()
	}
	obj, objPos, _ := driver.FindObjectAtPositionMatching(pos, matches, overlay, menu, content)
	return obj, objPos
}

func (w *window) processPointerClicked(pos fyne.Position, button desktop.MouseButton, pressed bool) {
	w.mousePos = pos
	w.mouseDragPos = pos
	mouseDragStarted := w.mouseDragStarted
	co, objPos := w.findObjectAtPositionMatching(pos, func(object fyne.CanvasObject) bool {
		switch object.(type) {
		case fyne.Tappable, fyne.SecondaryTappable, fyne.DoubleTappable, fyne.Focusable, desktop.Mouseable:
			return true
		case fyne.Draggable:
			return mouseDragStarted
		default:
			return false
		}
	})
	ev := &fyne.PointEvent{Position: objPos, AbsolutePosition: pos}
	if wid, ok := co.(desktop.Mouseable); ok {
		mev := &desktop.MouseEvent{Button: button, Modifier: fyneKeyModifier(w.mouseMod)}
		mev.Position = ev.Position
		mev.AbsolutePosition = pos
		w.mouseClickedHandleMouseable(mev, pressed, wid)
	}

	w.unfocusIfClickOutside(co, pos)

	if pressed {
		w.mouseButton |= button
	} else {
		w.mouseButton &= ^button
	}

	mouseDragged := w.mouseDragged
	shouldMouseOut := w.objIsDragged(w.mouseOver) && !w.objIsDragged(co)
	if !pressed && mouseDragged != nil {
		if mouseDragStarted {
			mouseDragged.DragEnd()
			w.mouseDragStarted = false
		}
		if shouldMouseOut {
			w.mouseOut()
		}
		w.mouseDragged = nil
	}

	_, tap := co.(fyne.Tappable)
	secondary, altTap := co.(fyne.SecondaryTappable)
	if tap || altTap {
		if pressed {
			w.mousePressed = co
		} else if co == w.mousePressed && button == desktop.MouseButtonSecondary && altTap {
			w.dispatchTappedSecondary(secondary, ev, pos)
		}
	}

	if !pressed && button == desktop.MouseButtonPrimary && !mouseDragStarted {
		w.mouseClickedHandleTapDoubleTap(co, ev)
	}
}

func (w *window) mouseClickedHandleMouseable(mev *desktop.MouseEvent, pressed bool, wid desktop.Mouseable) {
	if pressed {
		wid.MouseDown(mev)
		return
	}
	if dragged, ok := w.mouseDragged.(desktop.Mouseable); ok && w.mouseDragged != nil {
		mev.Position = mev.AbsolutePosition.Subtract(w.mouseDraggedOffset)
		dragged.MouseUp(mev)
		return
	}
	wid.MouseUp(mev)
}

func (w *window) mouseClickedHandleTapDoubleTap(co fyne.CanvasObject, ev *fyne.PointEvent) {
	_, doubleTap := co.(fyne.DoubleTappable)
	if doubleTap {
		w.mouseClickCount++
		w.mouseLastClick = co
		if w.mouseCancelFunc != nil {
			w.mouseCancelFunc()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), w.doubleTapDelay())
		w.mouseCancelFunc = cancel
		go w.waitForDoubleTap(ctx, co, ev)
		return
	}
	if wid, ok := co.(fyne.Tappable); ok && co == w.mousePressed {
		wid.Tapped(ev)
	}
	w.mousePressed = nil
}

func (w *window) doubleTapDelay() time.Duration {
	if w.driver != nil {
		return w.driver.DoubleTapDelay()
	}
	return doubleTapDelay
}

func (w *window) waitForDoubleTap(ctx context.Context, co fyne.CanvasObject, ev *fyne.PointEvent) {
	<-ctx.Done()
	finish := func() {
		w.pointerMu.Lock()
		w.waitForDoubleTapEnded(co, ev)
		w.pointerMu.Unlock()
	}
	if w.driver != nil {
		w.driver.DoFromGoroutine(finish, false)
		return
	}
	finish()
}

func (w *window) waitForDoubleTapEnded(co fyne.CanvasObject, ev *fyne.PointEvent) {
	if w.mouseClickCount == 2 && w.mouseLastClick == co {
		if wid, ok := co.(fyne.DoubleTappable); ok {
			wid.DoubleTapped(ev)
		}
	} else if co == w.mousePressed {
		if wid, ok := co.(fyne.Tappable); ok {
			wid.Tapped(ev)
		}
	}
	w.mouseClickCount = 0
	w.mousePressed = nil
	w.mouseCancelFunc = nil
	w.mouseLastClick = nil
}

func (w *window) processPointerMoved(pos fyne.Position) {
	previousPos := w.mousePos
	w.mousePos = pos
	if w.mouseButton != 0 && w.mouseButton != desktop.MouseButtonSecondary && !w.mouseDragStarted {
		obj, objPos := w.findObjectAtPositionMatching(previousPos, func(object fyne.CanvasObject) bool {
			_, ok := object.(fyne.Draggable)
			return ok
		})
		deltaX := pos.X - w.mouseDragPos.X
		deltaY := pos.Y - w.mouseDragPos.Y
		overThreshold := math.Abs(float64(deltaX)) >= dragMoveThreshold || math.Abs(float64(deltaY)) >= dragMoveThreshold
		if wid, ok := obj.(fyne.Draggable); ok && overThreshold {
			w.mouseDragged = wid
			w.mouseDraggedOffset = previousPos.Subtract(objPos)
			w.mouseDraggedObjStart = obj.Position()
			w.mouseDragStarted = true
		}
	}

	w.handleHover(pos)

	if w.mouseDragged != nil && w.mouseButton != desktop.MouseButtonSecondary {
		if w.mouseButton > 0 {
			draggedObjDelta := w.mouseDraggedObjStart.Subtract(w.mouseDragged.(fyne.CanvasObject).Position())
			ev := &fyne.DragEvent{}
			ev.AbsolutePosition = pos
			ev.Position = pos.Subtract(w.mouseDraggedOffset).Add(draggedObjDelta)
			ev.Dragged = fyne.NewDelta(pos.X-w.mouseDragPos.X, pos.Y-w.mouseDragPos.Y)
			w.mouseDragged.Dragged(ev)
		}
		w.mouseDragStarted = true
		w.mouseDragPos = pos
	}
}

func (w *window) objIsDragged(obj any) bool {
	if w.mouseDragged != nil && obj != nil {
		draggedObj, _ := obj.(fyne.Draggable)
		return draggedObj == w.mouseDragged
	}
	return false
}

func (w *window) unfocusIfClickOutside(co fyne.CanvasObject, pos fyne.Position) {
	focused := w.canvas.Focused()
	if wid, ok := co.(fyne.Focusable); !ok || wid != focused {
		ignore := false
		if focusedObj, ok := focused.(fyne.CanvasObject); ok {
			found, _ := w.findObjectAtPositionMatching(pos, func(object fyne.CanvasObject) bool {
				return object == focusedObj
			})
			ignore = found != nil
		}
		if !ignore {
			w.canvas.Unfocus()
		}
	}
}

func (w *window) dispatchTappedSecondary(secondary fyne.SecondaryTappable, ev *fyne.PointEvent, pos fyne.Position) {
	prevOverlay := w.canvas.Overlays().Top()
	secondary.TappedSecondary(ev)
	if prevOverlay == nil || slices.Contains(w.canvas.Overlays().List(), prevOverlay) {
		return
	}
	co2, pos2 := w.findObjectAtPositionMatching(pos, func(object fyne.CanvasObject) bool {
		_, ok := object.(fyne.SecondaryTappable)
		return ok
	})
	if sec2, ok := co2.(fyne.SecondaryTappable); ok {
		sec2.TappedSecondary(&fyne.PointEvent{Position: pos2, AbsolutePosition: pos})
	}
}

func (w *window) handleHover(pos fyne.Position) {
	cursor := desktop.Cursor(desktop.DefaultCursor)
	var overlay, menu fyne.CanvasObject
	if w.canvas != nil {
		overlay = w.canvas.Overlays().Top()
		menu = w.canvas.menu
	}
	obj, objPos, _ := driver.FindObjectAtPositionMatching(pos, func(object fyne.CanvasObject) bool {
		if cursorable, ok := object.(desktop.Cursorable); ok {
			cursor = cursorable.Cursor()
		}
		_, hover := object.(desktop.Hoverable)
		return hover
	}, overlay, menu, w.canvas.Content())
	w.applyCursor(cursor)
	if obj != nil && !w.objIsDragged(obj) {
		if hovered, ok := obj.(desktop.Hoverable); ok {
			ev := &desktop.MouseEvent{Modifier: fyneKeyModifier(w.mouseMod)}
			ev.AbsolutePosition = pos
			ev.Position = objPos
			if hovered == w.mouseOver {
				hovered.MouseMoved(ev)
			} else {
				w.mouseOut()
				hovered.MouseIn(ev)
				w.mouseOver = hovered
			}
			return
		}
	}
	if w.mouseOver != nil && !w.objIsDragged(w.mouseOver) {
		w.mouseOut()
	}
}

func (w *window) mouseOut() {
	if w.mouseOver != nil {
		w.mouseOver.MouseOut()
		w.mouseOver = nil
	}
}

const (
	scrollSpeed       = float32(25)
	dragMoveThreshold = 2
)

func (w *window) handleScroll(e scrollEvent) {
	pos := fyne.NewPos(float32(e.X), float32(e.Y))
	var overlay, menu fyne.CanvasObject
	if w.canvas != nil {
		overlay = w.canvas.Overlays().Top()
		menu = w.canvas.menu
	}
	obj, objPos, _ := driver.FindObjectAtPositionMatching(pos, func(object fyne.CanvasObject) bool {
		_, ok := object.(fyne.Scrollable)
		return ok
	}, overlay, menu, w.canvas.Content())
	if wid, ok := obj.(fyne.Scrollable); ok {
		dx, dy := e.DX, e.DY
		if !e.Continuous {
			dx *= scrollSpeed
			dy *= scrollSpeed
		}
		ev := &fyne.ScrollEvent{}
		ev.Scrolled = fyne.NewDelta(dx, dy)
		ev.Position = objPos
		ev.AbsolutePosition = pos
		wid.Scrolled(ev)
	}
}

func (w *window) applyCursor(c desktop.Cursor) {
	if w.cursor == c {
		return
	}
	w.cursor = c
	if cs, ok := w.surface.(cursorSurface); ok {
		_ = cs.SetCursor(c)
	}
}

// handleKey routes a key press to the canvas's focused widget first, and
// only falls back to the canvas-level OnTypedKey/OnTypedRune callbacks when
// nothing is focused — the same order internal/driver/glfw/window.go uses
// (see its processKeyPressed and processCharInput, both checking
// w.canvas.Focused() before the canvas.onTypedKey/onTypedRune fallback).
// Canvas-level callbacks are nil for an ordinary application (they exist
// for e.g. a custom shortcut layer on the window itself), so skipping the
// focused widget would drop every key press on a widget.Entry. A focused
// desktop.Keyable receives KeyDown on press and KeyUp on release, matching
// glfw's processKeyPressed (KeyDown → TypedKey on press, KeyUp alone on
// release). Releases of X11 auto-repeat are dropped in the backend before
// they reach this method. Modifier chords are checked against shortcutForKey
// (glfw's triggersShortcut) before TypedKey/TypedRune.
func (w *window) handleKey(e keyEvent) {
	name, r := e.Name, e.Rune
	if name == "" && e.KeySym != 0 {
		n, rr := keysymToKey(e.KeySym)
		if name == "" {
			name = n
		}
		if r == 0 {
			r = rr
		}
	}
	focused := w.canvas.Focused()
	keyEvt := &fyne.KeyEvent{
		Name:     name,
		Physical: fyne.HardwareKey{ScanCode: int(e.Scancode)},
		Repeat:   e.Repeat,
	}

	pendingMenuToggle := w.menuTogglePending
	w.menuTogglePending = desktop.KeyNone
	pendingMenuDeactivation := w.menuDeactivationPending
	w.menuDeactivationPending = desktop.KeyNone

	if !e.Pressed {
		if name != "" {
			switch name {
			case pendingMenuToggle:
				w.canvas.ToggleMenu()
			case pendingMenuDeactivation:
				if w.canvas.DismissMenu() {
					return
				}
			}
		}
		if keyable, ok := focused.(desktop.Keyable); ok {
			keyable.KeyUp(keyEvt)
		} else if handler := w.canvas.OnKeyUp(); handler != nil {
			handler(keyEvt)
		}
		return
	}

	switch name {
	case desktop.KeyAltLeft, desktop.KeyAltRight:
		if fyneKeyModifier(e.Modifiers)&^fyne.KeyModifierAlt == 0 {
			w.menuTogglePending = name
		}
	case fyne.KeyEscape:
		w.menuDeactivationPending = name
	}

	if keyable, ok := focused.(desktop.Keyable); ok {
		keyable.KeyDown(keyEvt)
	} else if handler := w.canvas.OnKeyDown(); handler != nil {
		handler(keyEvt)
	}

	mod := fyneKeyModifier(e.Modifiers)
	modifierOtherThanShift := (mod & fyne.KeyModifierControl) |
		(mod & fyne.KeyModifierAlt) |
		(mod & fyne.KeyModifierSuper)
	if name == fyne.KeyTab && modifierOtherThanShift == 0 && !w.capturesTab(mod) {
		return
	}

	if shortcut := shortcutForKey(name, e.Modifiers, w.Clipboard()); shortcut != nil {
		// internal/driver/glfw/window.go:885-899 gives a shortcut to the
		// focused widget first, when it implements fyne.Shortcutable
		// (e.g. widget.Entry's Ctrl+C/V/X), and only falls back to the
		// canvas-level shortcut registry when nothing focused handles it
		// that way. Reproduced exactly: giving the canvas priority instead
		// would mean a focused Entry never sees Ctrl+C/V at all whenever
		// the window itself also has a matching shortcut registered.
		if shortcutable, ok := focused.(fyne.Shortcutable); ok {
			shortcutable.TypedShortcut(shortcut)
		} else {
			w.canvas.TypedShortcut(shortcut)
		}
		return
	}

	if name != "" {
		if focused != nil {
			focused.TypedKey(keyEvt)
		} else if handler := w.canvas.OnTypedKey(); handler != nil {
			handler(keyEvt)
		}
	}
	if r != 0 {
		if focused != nil {
			focused.TypedRune(r)
		} else if handler := w.canvas.OnTypedRune(); handler != nil {
			handler(r)
		}
	}
}

// shortcutForKey mirrors internal/driver/glfw/window.go's triggersShortcut:
// standard fyne.Shortcut* for Ctrl+C/V/X/A/Z/Y (and Ctrl+Shift+Z redo),
// then a desktop.CustomShortcut for any other non-shift-only modifier chord
// that is not itself a modifier key. clipboard is the window's own
// fyne.Clipboard (see window.Clipboard): building a fresh process-local
// NewClipboard() per keystroke would silently discard content any real
// clipboard backend holds.
func (w *window) capturesTab(modifier fyne.KeyModifier) bool {
	if ent, ok := w.canvas.Focused().(fyne.Tabbable); ok && ent.AcceptsTab() {
		return true
	}
	switch modifier {
	case 0:
		w.canvas.FocusNext()
	case fyne.KeyModifierShift:
		w.canvas.FocusPrevious()
	}
	return false
}

func shortcutForKey(name fyne.KeyName, modifiers keyModifiers, clipboard fyne.Clipboard) fyne.Shortcut {
	mod := fyneKeyModifier(modifiers)
	var shortcut fyne.Shortcut
	if mod == fyne.KeyModifierControl {
		switch name {
		case fyne.KeyZ:
			shortcut = &fyne.ShortcutUndo{}
		case fyne.KeyY:
			shortcut = &fyne.ShortcutRedo{}
		case fyne.KeyV:
			shortcut = &fyne.ShortcutPaste{Clipboard: clipboard}
		case fyne.KeyC:
			shortcut = &fyne.ShortcutCopy{Clipboard: clipboard}
		case fyne.KeyInsert:
			shortcut = &fyne.ShortcutCopy{Clipboard: clipboard, Secondary: true}
		case fyne.KeyX:
			shortcut = &fyne.ShortcutCut{Clipboard: clipboard}
		case fyne.KeyA:
			shortcut = &fyne.ShortcutSelectAll{}
		}
	}
	if mod == fyne.KeyModifierShift {
		switch name {
		case fyne.KeyInsert:
			shortcut = &fyne.ShortcutPaste{Clipboard: clipboard, Secondary: true}
		case fyne.KeyDelete:
			shortcut = &fyne.ShortcutCut{Clipboard: clipboard, Secondary: true}
		}
	}
	if shortcut == nil && mod == fyne.KeyModifierControl|fyne.KeyModifierShift && name == fyne.KeyZ {
		shortcut = &fyne.ShortcutRedo{}
	}
	if shortcut == nil && mod != 0 && !isKeyModifier(name) && mod != fyne.KeyModifierShift {
		shortcut = &desktop.CustomShortcut{KeyName: name, Modifier: mod}
	}
	return shortcut
}

func fyneKeyModifier(m keyModifiers) fyne.KeyModifier {
	var out fyne.KeyModifier
	if m&modShift != 0 {
		out |= fyne.KeyModifierShift
	}
	if m&modControl != 0 {
		out |= fyne.KeyModifierControl
	}
	if m&modAlt != 0 {
		out |= fyne.KeyModifierAlt
	}
	if m&modSuper != 0 {
		out |= fyne.KeyModifierSuper
	}
	return out
}

func isKeyModifier(name fyne.KeyName) bool {
	return name == desktop.KeyShiftLeft || name == desktop.KeyShiftRight ||
		name == desktop.KeyControlLeft || name == desktop.KeyControlRight ||
		name == desktop.KeyAltLeft || name == desktop.KeyAltRight ||
		name == desktop.KeySuperLeft || name == desktop.KeySuperRight
}

// redrawIfDirty renders the canvas through the software rasterizer and
// presents it via the surface when the canvas has pending changes.
func (w *window) redrawIfDirty() {
	if w.surface == nil {
		return
	}
	if w.shown && !w.visible {
		return
	}
	w.canvas.EnsureMinSize()
	if !w.canvas.CheckDirtyAndClear() {
		return
	}
	probeStage(StageF2Start)
	img, region, full := w.canvas.renderDirty()
	probeStage(StageF2End)
	if frame := toNRGBA(img); frame != nil {
		probeStage(StageF4Start)
		if !full {
			if rs, ok := w.surface.(regionSurface); ok {
				logErr("purego: PresentRegion", rs.PresentRegion(frame, region))
				probeStage(StageF4End)
				return
			}
		}
		logErr("purego: Present", w.surface.Present(frame))
		probeStage(StageF4End)
	}
}
