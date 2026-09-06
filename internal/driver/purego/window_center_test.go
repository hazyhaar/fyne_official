//go:build purego && linux

package purego

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

const (
	opGetGeometryTest    = 14
	getGeometryWidthOff  = 16
	getGeometryHeightOff = 18
	xvfbScreenWidth      = 1280
	xvfbScreenHeight     = 800
	centerTolerancePx    = 2
)

func readWindowGeometry(s *x11Surface, window uint32) (x, y, width, height int, err error) {
	req := make([]byte, getGeometryReqLen)
	req[0] = opGetGeometryTest
	binary.LittleEndian.PutUint16(req[2:4], getGeometryReqWords)
	binary.LittleEndian.PutUint32(req[4:8], window)

	reply, err := s.request(req)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if reply[0] != replyStatusReply {
		return 0, 0, 0, 0, fmt.Errorf("GetGeometry status %d", reply[0])
	}
	x = int(int16(binary.LittleEndian.Uint16(reply[getGeometryXOffset : getGeometryXOffset+2]))) //nolint:gosec // X11 coordinates are INT16
	y = int(int16(binary.LittleEndian.Uint16(reply[getGeometryYOffset : getGeometryYOffset+2]))) //nolint:gosec // X11 coordinates are INT16
	width = int(binary.LittleEndian.Uint16(reply[getGeometryWidthOff : getGeometryWidthOff+2]))
	height = int(binary.LittleEndian.Uint16(reply[getGeometryHeightOff : getGeometryHeightOff+2]))
	return x, y, width, height, nil
}

// TestCenterOnScreenBeforeShowX11 checks that CenterOnScreen called before
// Show places the window so its centre is within ±2 px of the xvfb 1280x800
// screen centre. This failed while CenterOnScreen was a documented no-op.
func TestCenterOnScreenBeforeShowX11(t *testing.T) {
	test.NewApp()
	d := newTestDriver()
	w := d.CreateWindow("center").(*window)
	w.Resize(fyne.NewSize(200, 100))
	w.CenterOnScreen()
	w.Show()
	t.Cleanup(w.Close)
	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatalf("surface is %T, want *x11Surface", w.surface)
	}

	peer := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = peer.Close() })

	wantX, wantY := xvfbScreenWidth/2, xvfbScreenHeight/2
	var x, y, width, height, cx, cy int
	deadline := time.Now().Add(2 * time.Second)
	for {
		var err error
		x, y, width, height, err = readWindowGeometry(peer, xs.winID)
		if err != nil {
			t.Fatalf("GetGeometry: %v", err)
		}
		cx = x + width/2
		cy = y + height/2
		if absInt(cx-wantX) <= centerTolerancePx && absInt(cy-wantY) <= centerTolerancePx {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("window centre = (%d,%d) from geom x=%d y=%d %dx%d, want within ±%d of (%d,%d)",
				cx, cy, x, y, width, height, centerTolerancePx, wantX, wantY)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

type recordPosSurface struct {
	stubSurface
	ops []byte
}

func (r *recordPosSurface) SetPosition(int, int) error {
	r.ops = append(r.ops, opConfigureWindow)
	return nil
}

func (r *recordPosSurface) Show() error {
	r.ops = append(r.ops, opMapWindow)
	return nil
}

func (r *recordPosSurface) Hide() error {
	r.ops = append(r.ops, opUnmapWindow)
	return nil
}

func (*recordPosSurface) Screens() ([]ScreenGeometry, error) {
	return []ScreenGeometry{{Width: xvfbScreenWidth, Height: xvfbScreenHeight, Primary: true}}, nil
}

func TestCenterOnScreenBeforeShowDoesNotMapThenMove(t *testing.T) {
	rec := &recordPosSurface{stubSurface: stubSurface{events: make(chan surfaceEvent, 1)}}
	w := newWindow("center-order")
	w.surface = rec
	w.canvas.Resize(fyne.NewSize(200, 100))
	w.CenterOnScreen()
	w.Show()
	for i := 1; i < len(rec.ops); i++ {
		if rec.ops[i-1] == opMapWindow && rec.ops[i] == opConfigureWindow {
			t.Fatalf("request sequence %v contains Map followed by a move", rec.ops)
		}
	}
}

func TestCenterOnScreenX11FakeServerOrder(t *testing.T) {
	client, server := unixSocketPair(t)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	ops := make(chan byte, 8)
	go func() {
		for {
			req, err := readX11Request(server)
			if err != nil {
				return
			}
			if len(req) > 0 {
				ops <- req[0]
			}
		}
	}()
	s := &x11Surface{conn: client, winID: 1}
	if err := s.SetPosition(40, 60); err != nil {
		t.Fatalf("SetPosition: %v", err)
	}
	if err := s.mapWindow(); err != nil {
		t.Fatalf("mapWindow: %v", err)
	}
	var got []byte
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case op := <-ops:
			got = append(got, op)
		case <-deadline:
			t.Fatalf("opcodes %v, want ConfigureWindow then MapWindow", got)
		}
	}
	if got[0] != opConfigureWindow || got[1] != opMapWindow {
		t.Fatalf("opcodes %v, want ConfigureWindow (%d) then MapWindow (%d)", got, opConfigureWindow, opMapWindow)
	}
}
