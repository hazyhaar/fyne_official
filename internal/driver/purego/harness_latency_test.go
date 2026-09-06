//go:build purego && linux

package purego

import (
	"sort"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/internal/async"
	_ "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

const clickLatencySamples = 100

func TestHarnessXTestClickLatency(t *testing.T) {
	async.SetMainGoroutine()
	d := NewDriver().(*driverImpl)
	w := d.CreateWindow("harness-xtest-latency").(*window)

	var mu sync.Mutex
	samples := make([]time.Duration, 0, clickLatencySamples)
	var start time.Time

	syncConn := dialSyncX11(t)
	xt, err := queryXTestExtension(syncConn.conn)
	if err != nil {
		t.Fatalf("queryXTestExtension: %v", err)
	}
	if !xt.available {
		t.Skip("XTEST extension not available on this X server")
	}

	var clickX, clickY int16
	var fireNext func()
	btn := widget.NewButton("tap me", func() {
		dt := time.Since(start)
		mu.Lock()
		samples = append(samples, dt)
		n := len(samples)
		mu.Unlock()
		if n >= clickLatencySamples {
			d.Quit()
			return
		}
		go fireNext()
	})
	w.SetContent(btn)
	w.Resize(fyne.NewSize(200, 100))
	w.Show()
	t.Cleanup(w.Close)

	pos := btn.Position()
	size := btn.Size()
	clickX = int16(pos.X + size.Width/2)
	clickY = int16(pos.Y + size.Height/2)
	root := syncConn.rootWindow
	const leftButton = 1
	const motionAbsolute = 0

	fireNext = func() {
		time.Sleep(2 * time.Millisecond)
		if err := xt.fakeInput(syncConn.conn, xtestMotionNotify, motionAbsolute, root, clickX, clickY); err != nil {
			return
		}
		start = time.Now()
		if err := xt.fakeInput(syncConn.conn, xtestButtonPress, leftButton, root, clickX, clickY); err != nil {
			return
		}
		if err := xt.fakeInput(syncConn.conn, xtestButtonRelease, leftButton, root, clickX, clickY); err != nil {
			return
		}
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		fireNext()
	}()
	watch := time.AfterFunc(30*time.Second, func() {
		panic("click latency Run loop did not return")
	})
	d.Run()
	watch.Stop()

	mu.Lock()
	got := append([]time.Duration(nil), samples...)
	mu.Unlock()
	if len(got) < clickLatencySamples {
		t.Fatalf("recorded %d click latencies, want %d", len(got), clickLatencySamples)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	median := got[len(got)/2]
	t.Logf("XTEST click→Tapped n=%d min=%s median=%s max=%s", len(got), got[0], median, got[len(got)-1])
	if median <= 0 {
		t.Fatal("median click latency was 0")
	}
}
