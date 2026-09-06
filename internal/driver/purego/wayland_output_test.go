//go:build purego && linux

package purego

import (
	"encoding/binary"
	"os"
	"testing"

	"fyne.io/fyne/v2"
)

func TestWaylandOutputDispatchScreens(t *testing.T) {
	s := &waylandSurface{done: make(chan struct{})}
	t.Cleanup(func() { close(s.done) })

	const outID uint32 = 50
	waylandRegisterOutput(s, outID)

	geom := wlMsgWriter{}
	geom.putInt32(10)
	geom.putInt32(20)
	geom.putInt32(300)
	geom.putInt32(200)
	geom.putInt32(0)
	geom.putString("cage")
	geom.putString("headless")
	geom.putInt32(0)
	if !waylandOutputDispatch(s, outID, wlOutputEventGeometry, geom.buf) {
		t.Fatal("geometry was not dispatched")
	}

	mode := wlMsgWriter{}
	mode.putUint32(wlOutputModeCurrent)
	mode.putInt32(1280)
	mode.putInt32(800)
	mode.putInt32(60000)
	if !waylandOutputDispatch(s, outID, wlOutputEventMode, mode.buf) {
		t.Fatal("mode was not dispatched")
	}

	scale := make([]byte, 4)
	binary.LittleEndian.PutUint32(scale, 2)
	if !waylandOutputDispatch(s, outID, wlOutputEventScale, scale) {
		t.Fatal("scale was not dispatched")
	}

	screens, err := s.Screens()
	if err != nil {
		t.Fatalf("Screens: %v", err)
	}
	if len(screens) != 1 {
		t.Fatalf("Screens() = %d entries, want 1", len(screens))
	}
	got := screens[0]
	if got.X != 10 || got.Y != 20 || got.Width != 1280 || got.Height != 800 {
		t.Fatalf("screen pixels %+v, want 10,20 1280x800", got)
	}
	if got.WidthMM != 300 || got.HeightMM != 200 {
		t.Fatalf("screen mm %+v, want 300x200", got)
	}
	if !got.Primary {
		t.Fatal("single screen was not marked primary")
	}
}

func TestWaylandPreferredBufferScale(t *testing.T) {
	s := &waylandSurface{surfaceID: 3, done: make(chan struct{})}
	t.Cleanup(func() { close(s.done) })

	p := make([]byte, 4)
	binary.LittleEndian.PutUint32(p, 2)
	if !waylandSurfaceScaleDispatch(s, 3, wlSurfaceEventPreferredBufferScale, p) {
		t.Fatal("preferred_buffer_scale was not dispatched")
	}
	if waylandPreferredBufferScale(s) != 2 {
		t.Fatalf("preferred scale = %d, want 2", waylandPreferredBufferScale(s))
	}
}

func TestWaylandEnterOutputScale2(t *testing.T) {
	_ = os.Unsetenv(scaleEnvKey)
	s := &waylandSurface{
		surfaceID: 3, width: 320, height: 240,
		events: make(chan surfaceEvent, 4),
		done:   make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	const outID uint32 = 50
	waylandRegisterOutput(s, outID)
	scale := make([]byte, 4)
	binary.LittleEndian.PutUint32(scale, 2)
	if !waylandOutputDispatch(s, outID, wlOutputEventScale, scale) {
		t.Fatal("scale was not dispatched")
	}

	enter := make([]byte, 4)
	binary.LittleEndian.PutUint32(enter, outID)
	if !waylandSurfaceScaleDispatch(s, 3, wlSurfaceEventEnter, enter) {
		t.Fatal("enter was not dispatched")
	}

	d := NewDriver()
	installScaleApp(t, d)
	w := newWindow("wl-scale")
	w.driver = d.(*driverImpl)
	w.surface = s
	w.shown = true
	w.canvas.Resize(fyne.NewSize(320, 240))
	w.drainEvents()

	if w.canvas.Scale() != 2 {
		t.Fatalf("canvas.Scale() = %v after enter on scale=2 output, want 2", w.canvas.Scale())
	}
	bw, bh := waylandBufferPixelSize(s)
	if bw != 640 || bh != 480 {
		t.Fatalf("buffer pixels = %dx%d, want 640x480 (2x 320x240)", bw, bh)
	}
}

func TestWaylandScreensUnderCage(t *testing.T) {
	s := openWaylandForTest(t)
	screens, err := s.Screens()
	if err != nil {
		t.Fatalf("Screens: %v", err)
	}
	if len(screens) == 0 {
		t.Fatal("Screens() returned no wl_output")
	}
	found := false
	for _, sc := range screens {
		if sc.Width > 0 && sc.Height > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("Screens() = %+v, want at least one output with a positive pixel size", screens)
	}
	primary := 0
	for _, sc := range screens {
		if sc.Primary {
			primary++
		}
	}
	if primary != 1 {
		t.Fatalf("Screens() reported %d primary outputs, want 1", primary)
	}
}
