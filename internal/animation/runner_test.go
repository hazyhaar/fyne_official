package animation

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

func TestRunning(t *testing.T) {
	r := &Runner{}
	if r.Running() {
		t.Fatal("empty runner reported running")
	}
	r.Start(&fyne.Animation{Duration: time.Millisecond, Tick: func(float32) {}})
	if !r.Running() {
		t.Fatal("started runner reported idle")
	}
	for i := 0; i < 30; i++ {
		r.TickAnimations()
		if !r.Running() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runner still running after ticks")
}

func BenchmarkRunnerAllocs(b *testing.B) {
	r := Runner{}
	var fl float32
	// setup some animations
	for i := 0; i < 10; i++ {
		r.pendingAnimations = append(r.pendingAnimations, newAnim(
			fyne.NewAnimation(1000*time.Second, func(f float32) {
				fl = f
			}),
		))
	}
	for n := 0; n < b.N; n++ {
		r.runOneFrame()
	}

	b.ReportAllocs()
	fl = fl + 1 // dummy use of variable
}
