//go:build purego && linux

package purego

import (
	"encoding/binary"
	"image"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

func wlToplevelConfigurePayload(w, h int32, states ...uint32) []byte {
	p := make([]byte, 12+4*len(states))
	var uw, uh uint32
	if w > 0 {
		uw = uint32(w)
	}
	if h > 0 {
		uh = uint32(h)
	}
	binary.LittleEndian.PutUint32(p[0:4], uw)
	binary.LittleEndian.PutUint32(p[4:8], uh)
	binary.LittleEndian.PutUint32(p[8:12], uint32(4*len(states))) //nolint:gosec // test-only state count
	for i, st := range states {
		binary.LittleEndian.PutUint32(p[12+4*i:16+4*i], st)
	}
	return p
}

func TestWaylandFullscreenConfigureState(t *testing.T) {
	s := &waylandSurface{
		width:  320,
		height: 240,
		events: make(chan surfaceEvent, 1),
		done:   make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	s.handleToplevel(xdgToplevelEventConfigure, wlToplevelConfigurePayload(800, 600, xdgToplevelStateFullscreen))
	waylandToplevelConfigure(s, xdgToplevelEventConfigure, wlToplevelConfigurePayload(800, 600, xdgToplevelStateFullscreen))
	if !waylandToplevelIsFullscreen(s) {
		t.Fatal("configure with fullscreen state was not recorded")
	}

	waylandToplevelConfigure(s, xdgToplevelEventConfigure, wlToplevelConfigurePayload(320, 240))
	if waylandToplevelIsFullscreen(s) {
		t.Fatal("configure without fullscreen state still reported fullscreen")
	}
}

func TestWaylandSetFullscreenSendsRequest(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:       client,
		toplevelID: 42,
		done:       make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	if err := s.SetFullscreen(true); err != nil {
		t.Fatalf("SetFullscreen(true): %v", err)
	}
	obj, op, payload := readWlMessage(t, server)
	if obj != 42 || op != xdgToplevelOpSetFullscreen {
		t.Fatalf("SetFullscreen(true) sent object=%d opcode=%d, want 42/%d", obj, op, xdgToplevelOpSetFullscreen)
	}
	if len(payload) < 4 || binary.LittleEndian.Uint32(payload[:4]) != 0 {
		t.Fatalf("set_fullscreen payload = %v, want null output (0)", payload)
	}

	if err := s.SetFullscreen(false); err != nil {
		t.Fatalf("SetFullscreen(false): %v", err)
	}
	obj, op, payload = readWlMessage(t, server)
	if obj != 42 || op != xdgToplevelOpUnsetFullscreen {
		t.Fatalf("SetFullscreen(false) sent object=%d opcode=%d, want 42/%d", obj, op, xdgToplevelOpUnsetFullscreen)
	}
	if len(payload) != 0 {
		t.Fatalf("unset_fullscreen payload = %v, want empty", payload)
	}
}

func TestWaylandFullscreenConfigureUnderCage(t *testing.T) {
	s := openWaylandForTest(t)
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}

	if err := s.waitConfigured(); err != nil {
		t.Fatal(err)
	}

	if waylandToplevelIsFullscreen(s) {
		if err := s.SetFullscreen(false); err != nil {
			t.Fatalf("SetFullscreen(false): %v", err)
		}
		if err := s.roundtrip(); err != nil {
			t.Fatalf("roundtrip after unset_fullscreen: %v", err)
		}
		if waylandToplevelIsFullscreen(s) {
			// cage forces every mapped toplevel fullscreen and ignores
			// unset_fullscreen; this test only reads the configured state.
			return
		}
		if err := s.SetFullscreen(true); err != nil {
			t.Fatalf("SetFullscreen(true): %v", err)
		}
		if err := s.roundtrip(); err != nil {
			t.Fatalf("roundtrip after set_fullscreen: %v", err)
		}
		waitWaylandFullscreenState(t, s)
		return
	}

	if err := s.SetFullscreen(true); err != nil {
		t.Fatalf("SetFullscreen(true): %v", err)
	}
	if err := s.roundtrip(); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	waitWaylandFullscreenState(t, s)
}

func waitWaylandFullscreenState(t *testing.T, s *waylandSurface) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !waylandToplevelIsFullscreen(s) {
		select {
		case <-s.Events():
		case <-tick.C:
		case <-deadline:
			t.Fatal("timed out waiting for xdg_toplevel.configure with fullscreen state")
		}
	}
}

func TestWaylandSetFullscreenUnderCage(t *testing.T) {
	s := openWaylandForTest(t)
	if err := s.SetFullscreen(true); err != nil {
		t.Fatalf("SetFullscreen(true): %v", err)
	}
	if err := s.roundtrip(); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
}

func TestWaylandSetWindowIconSendsAppID(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:       client,
		done:       make(chan struct{}),
		toplevelID: 42,
	}
	t.Cleanup(func() { close(s.done) })

	res := fyne.NewStaticResource("org.fyne.test", []byte{1, 2, 3})
	if err := s.SetWindowIcon(res); err != nil {
		t.Fatalf("SetWindowIcon: %v", err)
	}
	obj, op, payload := readWlMessage(t, server)
	if obj != 42 || op != xdgToplevelOpSetAppID {
		t.Fatalf("SetWindowIcon sent object=%d opcode=%d, want set_app_id on 42", obj, op)
	}
	r := wlParser{p: payload}
	id, err := r.str()
	if err != nil || id != "org.fyne.test" {
		t.Fatalf("set_app_id %q, want org.fyne.test", id)
	}
	if waylandToplevelAppID(s) != "org.fyne.test" {
		t.Fatalf("recorded app_id = %q", waylandToplevelAppID(s))
	}
}

func TestWaylandSurfaceHasNoPositionSurface(t *testing.T) {
	var s surface = &waylandSurface{}
	if _, ok := s.(positionSurface); ok {
		t.Fatal("waylandSurface must not implement positionSurface: xdg-shell cannot place a toplevel")
	}
}

func TestWaylandSetFixedSizeSendsMinMax(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:       client,
		toplevelID: 42,
		width:      200,
		height:     100,
		done:       make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	if err := s.SetFixedSize(true); err != nil {
		t.Fatalf("SetFixedSize(true): %v", err)
	}
	obj, op, payload := readWlMessage(t, server)
	if obj != 42 || op != xdgToplevelOpSetMinSize {
		t.Fatalf("SetFixedSize first message object=%d opcode=%d, want 42/%d", obj, op, xdgToplevelOpSetMinSize)
	}
	if len(payload) < 8 {
		t.Fatalf("set_min_size payload %v too short", payload)
	}
	if int32(binary.LittleEndian.Uint32(payload[0:4])) != 200 || int32(binary.LittleEndian.Uint32(payload[4:8])) != 100 { //nolint:gosec // test payload
		t.Fatalf("set_min_size = %v, want 200x100", payload)
	}
	obj, op, payload = readWlMessage(t, server)
	if obj != 42 || op != xdgToplevelOpSetMaxSize {
		t.Fatalf("SetFixedSize second message object=%d opcode=%d, want 42/%d", obj, op, xdgToplevelOpSetMaxSize)
	}
	if int32(binary.LittleEndian.Uint32(payload[0:4])) != 200 || int32(binary.LittleEndian.Uint32(payload[4:8])) != 100 { //nolint:gosec // test payload
		t.Fatalf("set_max_size = %v, want 200x100", payload)
	}
}

func TestWaylandHideShowSendsAttachNil(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:      client,
		surfaceID: 7,
		done:      make(chan struct{}),
		buffers: [waylandBufferCount]waylandBuffer{
			{bufferID: 99, mem: []byte{1}, width: 32, height: 24},
		},
	}

	if err := s.Hide(); err != nil {
		t.Fatalf("Hide: %v", err)
	}
	obj, op, payload := readWlMessage(t, server)
	if obj != 7 || op != wlSurfaceOpAttach {
		t.Fatalf("Hide first message object=%d opcode=%d, want attach on 7", obj, op)
	}
	if len(payload) < 4 || binary.LittleEndian.Uint32(payload[:4]) != 0 {
		t.Fatalf("Hide attach buffer id = %v, want nil (0)", payload)
	}
	obj, op, _ = readWlMessage(t, server)
	if obj != 7 || op != wlSurfaceOpCommit {
		t.Fatalf("Hide second message object=%d opcode=%d, want commit on 7", obj, op)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.Show() }()
	obj, op, payload = readWlMessage(t, server)
	if obj != 7 || op != wlSurfaceOpAttach {
		t.Fatalf("Show first message object=%d opcode=%d, want attach on 7", obj, op)
	}
	if len(payload) < 4 || binary.LittleEndian.Uint32(payload[:4]) != 99 {
		t.Fatalf("Show attach buffer id = %v, want 99", payload)
	}
	close(s.done)
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Show blocked on dummy compositor roundtrip")
	}
}

func TestWaylandSetWindowIconUnderCage(t *testing.T) {
	s := openWaylandForTest(t)
	res := fyne.NewStaticResource("org.fyne.test", []byte{1})
	if err := s.SetWindowIcon(res); err != nil {
		t.Fatalf("SetWindowIcon: %v", err)
	}
	if err := s.roundtrip(); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if waylandToplevelAppID(s) != "org.fyne.test" {
		t.Fatalf("app_id = %q, want org.fyne.test", waylandToplevelAppID(s))
	}
}
