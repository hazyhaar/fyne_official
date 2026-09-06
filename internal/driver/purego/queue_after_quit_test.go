//go:build purego

package purego

import (
	"fyne.io/fyne/v2/internal/async"
	"sync"
	"testing"
	"time"
)

// TestDoFromGoroutineAfterQuitRunsInline pins that work posted after the
// main loop stopped runs instead of panicking on the closed queue: a
// settings file watcher keeps calling fyne.Do after Quit.
func TestDoFromGoroutineAfterQuitRunsInline(t *testing.T) {
	d := NewDriver().(*driverImpl)
	async.SetMainGoroutine()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
		d.Quit()
	}()
	d.Run()
	wg.Wait()
	ran := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.DoFromGoroutine(func() { ran <- struct{}{} }, true)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DoFromGoroutine after Quit did not return")
	}
	select {
	case <-ran:
	default:
		t.Fatal("DoFromGoroutine after Quit did not run the call")
	}
}

// TestDoFromGoroutineWaitersDuringQuitAllReturn pins the shutdown order:
// work posted while the loop is stopping is either run by the drain or run
// inline by the poster, never left in the queue with its waiter blocked.
func TestDoFromGoroutineWaitersDuringQuitAllReturn(t *testing.T) {
	d := NewDriver().(*driverImpl)
	async.SetMainGoroutine()
	const posters = 20
	var wg sync.WaitGroup
	wg.Add(posters)
	start := make(chan struct{})
	for i := 0; i < posters; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 50; j++ {
				d.DoFromGoroutine(func() {}, true)
			}
		}()
	}
	go func() {
		close(start)
		time.Sleep(5 * time.Millisecond)
		d.Quit()
	}()
	d.Run()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a DoFromGoroutine waiter stayed blocked after Quit")
	}
}
