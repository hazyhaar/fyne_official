//go:build purego

package purego

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/internal/animation"
	intapp "fyne.io/fyne/v2/internal/app"
	"fyne.io/fyne/v2/internal/async"
	"fyne.io/fyne/v2/internal/cache"
	"fyne.io/fyne/v2/internal/driver"
	"fyne.io/fyne/v2/internal/painter"
	intRepo "fyne.io/fyne/v2/internal/repository"
	"fyne.io/fyne/v2/storage/repository"
)

// init pins this package's loading goroutine as the process's main thread
// and records it as Fyne's main goroutine, exactly as
// internal/driver/glfw/loop.go:30-33 does. Without this, async.IsMainGoroutine
// (used by Run below) never returns true for the actual main goroutine, so
// Run and ShowAndRun panic unconditionally — even in ordinary application
// use, not just under test — the moment they check it.
func init() {
	runtime.LockOSThread()
	async.SetMainGoroutine()
}

// frameInterval is the animation tick and the maximum redraw cadence (60 Hz).
// A single pending frame is presented immediately; the interval only delays
// a follow-up frame that arrives sooner than this.
const frameInterval = 16 * time.Millisecond

// doubleTapDelay mirrors the default double-click time used by the glfw
// driver when no platform-specific value is queried.
const doubleTapDelay = 300 * time.Millisecond

// Declare conformity with fyne.Driver and desktop.Driver.
var (
	_ fyne.Driver    = (*driverImpl)(nil)
	_ desktop.Driver = (*driverImpl)(nil)
)

type funcCall struct {
	fn   func()
	wait chan struct{}
}

type driverImpl struct {
	mu      sync.Mutex
	windows []*window
	done    chan struct{}
	running atomic.Bool

	animation animation.Runner
	funcQueue *async.UnboundedChan[funcCall]
	// queueMu guards the queue against a send racing its close at shutdown:
	// DoFromGoroutine holds it while sending, Run takes it exclusively to close.
	queueMu     sync.RWMutex
	queueClosed bool
	wake        chan struct{}
	focused     *window

	loopIters atomic.Uint64

	animTimer    *time.Timer
	cadenceTimer *time.Timer
	lastDraw     time.Time
	lastAnimTick time.Time
}

// NewDriver sets up a new Driver instance using the purego X11 backend.
// It has no effect unless the `purego` build tag is set.
func NewDriver() fyne.Driver {
	repository.Register(fyne.URISchemeFile, intRepo.NewFileRepository())
	return &driverImpl{
		done:      make(chan struct{}),
		funcQueue: async.NewUnboundedChan[funcCall](),
		wake:      make(chan struct{}, 1),
	}
}

func (d *driverImpl) bindWindow(w *window) {
	w.driver = d
	w.canvas.onDirty = d.nudge
}

func (d *driverImpl) CreateWindow(title string) fyne.Window {
	w := newWindow(title)
	d.bindWindow(w)
	d.mu.Lock()
	d.windows = append(d.windows, w)
	d.mu.Unlock()
	return w
}

func (d *driverImpl) CreateSplashWindow() fyne.Window {
	// Native X11 override-redirect and centering live on newX11Surface
	// (withOverrideRedirect, x11_splash.go): window.Show calls openSurface
	// with no splash-specific options, so a splash window still uses the
	// ordinary surface path. Wayland xdg_toplevel is created without a
	// zxdg_decoration request, so a splash stays undecorated on that backend.
	w := newWindow("")
	d.bindWindow(w)
	w.SetPadded(false)
	w.CenterOnScreen()
	d.mu.Lock()
	d.windows = append(d.windows, w)
	d.mu.Unlock()
	return w
}

func (d *driverImpl) CurrentKeyModifiers() fyne.KeyModifier {
	return currentKeyModifiers(d)
}

func (d *driverImpl) HasSecondaryDisplay() bool {
	return hasSecondaryDisplay(d)
}

func (d *driverImpl) AllWindows() []fyne.Window {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]fyne.Window, len(d.windows))
	for i, w := range d.windows {
		out[i] = w
	}
	return out
}

func (*driverImpl) RenderedTextSize(text string, fontSize float32, style fyne.TextStyle, source fyne.Resource) (fyne.Size, float32) {
	return painter.RenderedTextSize(text, fontSize, style, source)
}

func (*driverImpl) CanvasForObject(obj fyne.CanvasObject) fyne.Canvas {
	return cache.GetCanvasForObject(obj)
}

func (d *driverImpl) AbsolutePositionForObject(obj fyne.CanvasObject) fyne.Position {
	c := d.CanvasForObject(obj)
	if c == nil {
		return fyne.NewPos(0, 0)
	}
	pc, ok := c.(*canvas)
	if !ok {
		return fyne.NewPos(0, 0)
	}
	return driver.AbsolutePositionForObject(obj, pc.ObjectTrees())
}

func (*driverImpl) Device() fyne.Device { return &device{} }

func (d *driverImpl) Run() {
	if !async.IsMainGoroutine() {
		panic("Run() or ShowAndRun() must be called from main goroutine")
	}
	d.running.Store(true)
	defer d.running.Store(false)
	defer stopTimer(d.animTimer)
	defer stopTimer(d.cadenceTimer)

	if d.wake == nil {
		d.wake = make(chan struct{}, 1)
	}
	if f := lifecycleHook((*intapp.Lifecycle).OnStarted); f != nil {
		f()
	}

	var animC, cadenceC <-chan time.Time
	for {
		d.watchWindows()
		waitStart := time.Now()
		select {
		case <-d.done:
			// Refuse new work first, then run what is already queued (so a
			// waiter posted just before the stop is released), then close.
			d.queueMu.Lock()
			d.queueClosed = true
			d.queueMu.Unlock()
			d.drainFuncQueue()
			if d.funcQueue != nil {
				d.funcQueue.Close()
			}
			if f := lifecycleHook((*intapp.Lifecycle).OnStopped); f != nil {
				f()
			}
			return
		case call := <-d.funcQueue.Out():
			d.execCall(call)
			d.drainFuncQueue()
		case <-d.wake:
			d.drainWake()
		case <-animC:
		case <-cadenceC:
		}
		idle := time.Since(waitStart) >= time.Millisecond
		d.loopIters.Add(1)
		d.step(idle)
		animC = d.armAnimTimer()
		cadenceC = d.armCadenceTimer()
	}
}

func (*driverImpl) execCall(call funcCall) {
	if call.fn != nil {
		call.fn()
	}
	if call.wait != nil {
		call.wait <- struct{}{}
	}
}

func (d *driverImpl) drainFuncQueue() {
	if d.funcQueue == nil {
		return
	}
	for {
		select {
		case call, ok := <-d.funcQueue.Out():
			if !ok {
				return
			}
			d.execCall(call)
		default:
			return
		}
	}
}

func lifecycleHook(sel func(*intapp.Lifecycle) func()) func() {
	a := fyne.CurrentApp()
	if a == nil {
		return nil
	}
	l, ok := a.Lifecycle().(*intapp.Lifecycle)
	if !ok || l == nil {
		return nil
	}
	return sel(l)
}

func (d *driverImpl) windowSnapshot() []*window {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*window(nil), d.windows...)
}

func (d *driverImpl) step(idle bool) {
	d.drainFuncQueue()
	for _, w := range d.windowSnapshot() {
		w.drainEvents()
	}
	now := time.Now()
	if d.animation.Running() && (d.lastAnimTick.IsZero() || now.Sub(d.lastAnimTick) >= frameInterval) {
		d.animation.TickAnimations()
		d.lastAnimTick = now
	}
	if !d.anyDirty() {
		return
	}
	if idle || d.lastDraw.IsZero() || now.Sub(d.lastDraw) >= frameInterval {
		for _, w := range d.windowSnapshot() {
			w.redrawIfDirty()
		}
		d.lastDraw = time.Now()
	}
}

func (d *driverImpl) anyDirty() bool {
	for _, w := range d.windowSnapshot() {
		if w.surface == nil || w.canvas == nil {
			continue
		}
		if w.shown && !w.visible {
			continue
		}
		if w.canvas.isDirty() {
			return true
		}
	}
	return false
}

func (d *driverImpl) armAnimTimer() <-chan time.Time {
	if !d.animation.Running() {
		stopTimer(d.animTimer)
		return nil
	}
	wait := frameInterval
	if !d.lastAnimTick.IsZero() {
		wait = frameInterval - time.Since(d.lastAnimTick)
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
	}
	return resetTimer(&d.animTimer, wait)
}

func (d *driverImpl) armCadenceTimer() <-chan time.Time {
	if !d.anyDirty() {
		stopTimer(d.cadenceTimer)
		return nil
	}
	wait := frameInterval
	if !d.lastDraw.IsZero() {
		wait = frameInterval - time.Since(d.lastDraw)
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
	}
	return resetTimer(&d.cadenceTimer, wait)
}

func (d *driverImpl) nudge() {
	if d.wake == nil {
		return
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *driverImpl) drainWake() {
	if d.wake == nil {
		return
	}
	for {
		select {
		case <-d.wake:
		default:
			return
		}
	}
}

func (d *driverImpl) watchWindows() {
	for _, w := range d.windowSnapshot() {
		d.watchWindow(w)
	}
}

func (d *driverImpl) watchWindow(w *window) {
	if w == nil || w.surface == nil {
		return
	}
	ws, ok := w.surface.(wakeSurface)
	if !ok {
		return
	}
	w.wakeOnce.Do(func() {
		go d.relayWake(ws.Wake())
	})
}

func (d *driverImpl) relayWake(ch <-chan struct{}) {
	if ch == nil {
		return
	}
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
			d.nudge()
		case <-d.done:
			return
		}
	}
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func resetTimer(t **time.Timer, d time.Duration) <-chan time.Time {
	if *t == nil {
		*t = time.NewTimer(d)
		return (*t).C
	}
	stopTimer(*t)
	(*t).Reset(d)
	return (*t).C
}

func (d *driverImpl) removeWindow(w *window) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, existing := range d.windows {
		if existing == w {
			d.windows = append(d.windows[:i], d.windows[i+1:]...)
			break
		}
	}
	if w.master || len(d.windows) == 0 {
		d.quitLocked()
	}
}

func (d *driverImpl) Quit() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.quitLocked()
}

func (d *driverImpl) quitLocked() {
	select {
	case <-d.done:
	default:
		if d.focused != nil {
			d.focused.applyFocus(false)
		}
		for _, w := range d.windows {
			if w.surface != nil {
				_ = w.surface.Close() // close is idempotent
			}
		}
		closeHiddenClipboard()
		close(d.done)
	}
}

func (d *driverImpl) StartAnimation(a *fyne.Animation) {
	d.animation.Start(a)
	d.nudge()
}
func (d *driverImpl) StopAnimation(a *fyne.Animation) {
	d.animation.Stop(a)
	d.nudge()
}

func (*driverImpl) DoubleTapDelay() time.Duration { return doubleTapDelay }

func (d *driverImpl) SetDisableScreenBlanking(disable bool) {
	setDisableScreenBlanking(d, disable)
}

func (d *driverImpl) DoFromGoroutine(fn func(), wait bool) {
	if fn == nil {
		return
	}
	if async.IsMainGoroutine() {
		fn()
		return
	}
	if d.funcQueue == nil {
		fn()
		return
	}
	call := funcCall{fn: fn}
	if wait {
		call.wait = make(chan struct{})
	}
	d.queueMu.RLock()
	if d.queueClosed {
		// The loop has stopped: nothing will ever run the call, so run it
		// here rather than block or panic (a settings watcher may still
		// post work after Quit).
		d.queueMu.RUnlock()
		fn()
		return
	}
	d.funcQueue.In() <- call
	d.queueMu.RUnlock()
	if wait {
		<-call.wait
	}
}

func (d *driverImpl) loopIterations() uint64 {
	return d.loopIters.Load()
}
