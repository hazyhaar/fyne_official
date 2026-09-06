//go:build purego && linux

package purego

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"golang.org/x/sys/unix"

	"fyne.io/fyne/v2"
)

func testPNGResource(t *testing.T) fyne.Resource {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.NRGBA{R: 200, G: 10, B: 10, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return fyne.NewStaticResource("org.fyne.test", buf.Bytes())
}

func TestWaylandSetWindowIconWithoutManagerSendsAppID(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:       client,
		done:       make(chan struct{}),
		toplevelID: 42,
	}
	t.Cleanup(func() { close(s.done) })

	if err := s.SetWindowIcon(testPNGResource(t)); err != nil {
		t.Fatalf("SetWindowIcon: %v", err)
	}
	obj, op, payload := readWlMessage(t, server)
	if obj != 42 || op != xdgToplevelOpSetAppID {
		t.Fatalf("without icon manager sent object=%d opcode=%d, want set_app_id on 42", obj, op)
	}
	r := wlParser{p: payload}
	id, err := r.str()
	if err != nil || id != "org.fyne.test" {
		t.Fatalf("set_app_id %q, want org.fyne.test", id)
	}
}

func TestWaylandSetWindowIconWithManagerSendsBuffers(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:         client,
		done:         make(chan struct{}),
		toplevelID:   42,
		iconMgrID:    9,
		compositorID: 2,
		shmID:        3,
		haveArgb:     true,
		nextID:       50,
	}
	t.Cleanup(func() { close(s.done) })

	if err := s.SetWindowIcon(testPNGResource(t)); err != nil {
		t.Fatalf("SetWindowIcon: %v", err)
	}

	var sawCreate, sawAdd, sawSet bool
	var addCount int
	for i := 0; i < 40; i++ {
		obj, op, fds, err := readWlMessageMaybeFD(server)
		if err != nil {
			break
		}
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		switch {
		case obj == 9 && op == 1: // xdg_toplevel_icon_manager_v1.create_icon
			sawCreate = true
		case op == 2 && obj != 9 && obj != 42: // xdg_toplevel_icon_v1.add_buffer
			sawAdd = true
			addCount++
		case obj == 9 && op == 2: // xdg_toplevel_icon_manager_v1.set_icon
			sawSet = true
		}
		if sawCreate && sawAdd && sawSet && addCount >= 4 {
			break
		}
	}
	if !sawCreate || !sawAdd || !sawSet {
		t.Fatalf("icon protocol messages create=%v add=%v set=%v addCount=%d", sawCreate, sawAdd, sawSet, addCount)
	}
	if addCount != 4 {
		t.Fatalf("add_buffer count = %d, want 4 (16/32/48/64)", addCount)
	}
}
