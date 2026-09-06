//go:build purego

package purego

import (
	"fmt"
	"image"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/internal/async"
)

func TestRunIdleDoesNotSpin(t *testing.T) {
	async.SetMainGoroutine()
	d := NewDriver().(*driverImpl)
	errCh := make(chan string, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		n0 := d.loopIterations()
		time.Sleep(500 * time.Millisecond)
		n1 := d.loopIterations()
		if n1 != n0 {
			errCh <- fmt.Sprintf("idle loop iterations grew from %d to %d", n0, n1)
		} else {
			errCh <- ""
		}
		d.Quit()
	}()
	watch := time.AfterFunc(3*time.Second, func() {
		panic("idle Run loop did not return")
	})
	d.Run()
	watch.Stop()
	if msg := <-errCh; msg != "" {
		t.Fatal(msg)
	}
}

type presentCounter struct {
	stubSurface
	n atomic.Int32
}

func (s *presentCounter) Present(*image.NRGBA) error {
	s.n.Add(1)
	return nil
}

func TestRunWakesOnCanvasDirty(t *testing.T) {
	async.SetMainGoroutine()
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("dirty-wake").(*window)
	surf := &presentCounter{stubSurface: stubSurface{events: make(chan surfaceEvent, 1)}}
	w.surface = surf
	errCh := make(chan string, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		if n := d.loopIterations(); n != 0 {
			errCh <- fmt.Sprintf("loop spun %d times before SetDirty", n)
			d.Quit()
			return
		}
		d.DoFromGoroutine(func() { w.canvas.SetDirty() }, false)
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) {
			if surf.n.Load() >= 1 {
				errCh <- ""
				d.Quit()
				return
			}
			time.Sleep(time.Millisecond)
		}
		errCh <- "SetDirty did not cause a redraw"
		d.Quit()
	}()
	watch := time.AfterFunc(3*time.Second, func() {
		panic("dirty-wake Run loop did not return")
	})
	d.Run()
	watch.Stop()
	if msg := <-errCh; msg != "" {
		t.Fatal(msg)
	}
}

func TestRunRedrawCoalescesAndCapsCadence(t *testing.T) {
	async.SetMainGoroutine()
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("cadence").(*window)
	surf := &presentCounter{stubSurface: stubSurface{events: make(chan surfaceEvent, 1)}}
	w.surface = surf
	errCh := make(chan string, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		d.DoFromGoroutine(func() {
			for i := 0; i < 8; i++ {
				w.canvas.SetDirty()
			}
		}, false)
		deadline := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(deadline) && surf.n.Load() < 1 {
			time.Sleep(time.Millisecond)
		}
		if got := surf.n.Load(); got != 1 {
			errCh <- fmt.Sprintf("coalesced redraws = %d, want 1", got)
			d.Quit()
			return
		}
		t0 := time.Now()
		d.DoFromGoroutine(func() { w.canvas.SetDirty() }, false)
		deadline = time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(deadline) && surf.n.Load() < 2 {
			time.Sleep(time.Millisecond)
		}
		wait := time.Since(t0)
		if surf.n.Load() < 2 {
			errCh <- "second dirty canvas was never redrawn"
			d.Quit()
			return
		}
		if wait > frameInterval {
			errCh <- fmt.Sprintf("single pending frame waited %v, want less than %v", wait, frameInterval)
			d.Quit()
			return
		}
		errCh <- ""
		d.Quit()
	}()
	watch := time.AfterFunc(3*time.Second, func() {
		panic("cadence Run loop did not return")
	})
	d.Run()
	watch.Stop()
	if msg := <-errCh; msg != "" {
		t.Fatal(msg)
	}
}

type redirtySurface struct {
	presentCounter
	canvas *canvas
}

func (s *redirtySurface) Present(img *image.NRGBA) error {
	err := s.presentCounter.Present(img)
	if s.canvas != nil {
		s.canvas.SetDirty()
	}
	return err
}

func TestRunCapsContinuousRedraw(t *testing.T) {
	async.SetMainGoroutine()
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("cadence-cap").(*window)
	surf := &redirtySurface{presentCounter: presentCounter{stubSurface: stubSurface{events: make(chan surfaceEvent, 1)}}}
	w.surface = surf
	surf.canvas = w.canvas
	errCh := make(chan string, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		d.DoFromGoroutine(func() { w.canvas.SetDirty() }, false)
		time.Sleep(50 * time.Millisecond)
		n := surf.n.Load()
		limit := int32(50/16 + 3)
		if n < 2 {
			errCh <- fmt.Sprintf("continuous dirty produced %d redraws, want at least 2", n)
		} else if n > limit {
			errCh <- fmt.Sprintf("continuous dirty produced %d redraws in 50ms, want at most %d", n, limit)
		} else {
			errCh <- ""
		}
		d.Quit()
	}()
	watch := time.AfterFunc(3*time.Second, func() {
		panic("cadence-cap Run loop did not return")
	})
	d.Run()
	watch.Stop()
	if msg := <-errCh; msg != "" {
		t.Fatal(msg)
	}
}

type wakeStubSurface struct {
	stubSurface
	wake chan struct{}
}

func (s *wakeStubSurface) Wake() <-chan struct{} { return s.wake }

func (s *wakeStubSurface) push(ev surfaceEvent) {
	s.events <- ev
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func TestRunWakesOnSurfaceEvent(t *testing.T) {
	async.SetMainGoroutine()
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("event-wake").(*window)
	surf := &wakeStubSurface{
		stubSurface: stubSurface{events: make(chan surfaceEvent, 4)},
		wake:        make(chan struct{}, 1),
	}
	w.surface = surf
	d.watchWindow(w)
	errCh := make(chan string, 1)
	got := make(chan struct{}, 1)
	w.canvas.SetOnTypedKey(func(*fyne.KeyEvent) {
		select {
		case got <- struct{}{}:
		default:
		}
	})

	go func() {
		time.Sleep(20 * time.Millisecond)
		surf.push(keyEvent{Pressed: true, Name: fyne.KeyA})
		select {
		case <-got:
			errCh <- ""
		case <-time.After(200 * time.Millisecond):
			errCh <- "surface event did not wake the loop"
		}
		d.Quit()
	}()
	watch := time.AfterFunc(3*time.Second, func() {
		panic("event-wake Run loop did not return")
	})
	d.Run()
	watch.Stop()
	if msg := <-errCh; msg != "" {
		t.Fatal(msg)
	}
}
