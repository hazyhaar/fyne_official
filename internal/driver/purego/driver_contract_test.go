//go:build purego

package purego

import (
	"image/color"
	"runtime"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/internal/async"
	"fyne.io/fyne/v2/test"
)

func TestDoAndWaitFromRunCallback(t *testing.T) {
	async.SetMainGoroutine()
	d := NewDriver().(*driverImpl)
	returned := make(chan struct{})

	go func() {
		d.DoFromGoroutine(func() {
			d.DoFromGoroutine(func() {}, true)
			close(returned)
			d.Quit()
		}, false)
	}()

	watch := time.AfterFunc(3*time.Second, func() {
		panic("DoAndWait from a Run callback deadlocked")
	})
	d.Run()
	watch.Stop()

	select {
	case <-returned:
	default:
		t.Fatal("DoAndWait from a Run callback did not return")
	}
}

func TestDoFromGoroutineDoesNotBlockEmitter(t *testing.T) {
	d := NewDriver().(*driverImpl)
	d.running.Store(true)
	t.Cleanup(d.Quit)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			d.DoFromGoroutine(func() {}, false)
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DoFromGoroutine blocked the emitter once the queue filled")
	}
}

type mainGoroutineTapRec struct {
	*fynecanvas.Rectangle
	quit   func()
	onMain bool
	called bool
}

func (r *mainGoroutineTapRec) Tapped(*fyne.PointEvent) {
	r.onMain = async.IsMainGoroutine()
	r.called = true
	if r.quit != nil {
		r.quit()
	}
}

func (r *mainGoroutineTapRec) DoubleTapped(*fyne.PointEvent) {
	r.onMain = async.IsMainGoroutine()
	r.called = true
	if r.quit != nil {
		r.quit()
	}
}

func TestDeferredTapRunsOnMainGoroutine(t *testing.T) {
	async.SetMainGoroutine()
	test.NewApp()
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("deferred-tap").(*window)
	rec := &mainGoroutineTapRec{
		Rectangle: fynecanvas.NewRectangle(color.White),
		quit:      d.Quit,
	}
	rec.SetMinSize(fyne.NewSize(200, 200))
	w.SetContent(rec)
	w.canvas.Resize(fyne.NewSize(200, 200))
	w.surface = &stubSurface{events: make(chan surfaceEvent, 4)}

	go func() {
		d.DoFromGoroutine(func() {
			w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 1, Pressed: true})
			w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 1, Pressed: false})
		}, false)
	}()

	watch := time.AfterFunc(3*time.Second, func() {
		panic("deferred tap Run loop did not return")
	})
	d.Run()
	watch.Stop()

	if !rec.called {
		t.Fatal("deferred Tapped/DoubleTapped was not called")
	}
	if !rec.onMain {
		t.Fatal("deferred Tapped/DoubleTapped did not run on the main goroutine")
	}
}

func TestLifecycleStartedStopped(t *testing.T) {
	async.SetMainGoroutine()
	a := test.NewApp()
	var started, stopped int
	a.Lifecycle().SetOnStarted(func() { started++ })
	a.Lifecycle().SetOnStopped(func() { stopped++ })
	d := NewDriver().(*driverImpl)
	go func() {
		time.Sleep(20 * time.Millisecond)
		d.Quit()
	}()
	d.Run()
	if started != 1 {
		t.Fatalf("OnStarted called %d times, want 1", started)
	}
	if stopped != 1 {
		t.Fatalf("OnStopped called %d times, want 1", stopped)
	}
}

func TestLifecycleForegroundOnFocus(t *testing.T) {
	a := test.NewApp()
	var entered, exited int
	a.Lifecycle().SetOnEnteredForeground(func() { entered++ })
	a.Lifecycle().SetOnExitedForeground(func() { exited++ })
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("focus-life").(*window)
	w.handleEvent(focusEvent{Gained: true})
	if entered != 1 {
		t.Fatalf("OnEnteredForeground called %d times, want 1", entered)
	}
	w.handleEvent(focusEvent{Gained: false})
	if exited != 1 {
		t.Fatalf("OnExitedForeground called %d times, want 1", exited)
	}
}

func TestCloseFocusedWindowEmitsExitedForeground(t *testing.T) {
	a := test.NewApp()
	var seq []string
	a.Lifecycle().SetOnEnteredForeground(func() { seq = append(seq, "entered") })
	a.Lifecycle().SetOnExitedForeground(func() { seq = append(seq, "exited") })
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("close-focus").(*window)
	w.handleEvent(focusEvent{Gained: true})
	w.Close()
	if d.focused != nil {
		t.Fatal("d.focused was not cleared after Close of the focused window")
	}
	if len(seq) != 2 || seq[0] != "entered" || seq[1] != "exited" {
		t.Fatalf("lifecycle sequence = %v, want [entered exited]", seq)
	}
}

func TestQuitEmitsExitedForeground(t *testing.T) {
	a := test.NewApp()
	var seq []string
	a.Lifecycle().SetOnEnteredForeground(func() { seq = append(seq, "entered") })
	a.Lifecycle().SetOnExitedForeground(func() { seq = append(seq, "exited") })
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("quit-focus").(*window)
	w.handleEvent(focusEvent{Gained: true})
	d.Quit()
	if d.focused != nil {
		t.Fatal("d.focused was not cleared after Quit")
	}
	if len(seq) != 2 || seq[0] != "entered" || seq[1] != "exited" {
		t.Fatalf("lifecycle sequence = %v, want [entered exited]", seq)
	}
}

func TestDoFromGoroutineFIFONoSpawn(t *testing.T) {
	async.SetMainGoroutine()
	d := NewDriver().(*driverImpl)
	n0 := runtime.NumGoroutine()
	const n = 1000
	got := make([]int, 0, n)
	go func() {
		for i := 0; i < n; i++ {
			i := i
			d.DoFromGoroutine(func() { got = append(got, i) }, false)
		}
		d.DoFromGoroutine(func() { d.Quit() }, false)
	}()
	d.Run()
	if len(got) != n {
		t.Fatalf("got %d callbacks, want %d", len(got), n)
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("order[%d] = %d, want FIFO sequence", i, v)
		}
	}
	n1 := runtime.NumGoroutine()
	if n1 > n0+5 {
		t.Fatalf("NumGoroutine grew from %d to %d after 1000 DoFromGoroutine calls", n0, n1)
	}
}
