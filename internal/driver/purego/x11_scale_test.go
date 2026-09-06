//go:build purego && linux

package purego

import (
	"encoding/binary"
	"os"
	"testing"
)

func randrScreenChangePacket(widthPx, heightPx, widthMM, heightMM int) []byte {
	buf := make([]byte, coreEventSize)
	binary.LittleEndian.PutUint16(buf[24:26], uint16(widthPx))  //nolint:gosec // test geometry fits uint16
	binary.LittleEndian.PutUint16(buf[26:28], uint16(heightPx)) //nolint:gosec // test geometry fits uint16
	binary.LittleEndian.PutUint16(buf[28:30], uint16(widthMM))  //nolint:gosec // test geometry fits uint16
	binary.LittleEndian.PutUint16(buf[30:32], uint16(heightMM)) //nolint:gosec // test geometry fits uint16
	return buf
}

func TestX11ScreenChangeNotifyRescalesCanvas(t *testing.T) {
	_ = os.Unsetenv(scaleEnvKey)
	d := NewDriver().(*driverImpl)
	installScaleApp(t, d)
	w := d.CreateWindow("randr-scale").(*window)
	w.Show()
	if w.surface == nil {
		t.Skip("purego/x11: no display server reachable")
	}
	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Skipf("surface is %T, want *x11Surface", w.surface)
	}
	t.Cleanup(func() { w.Close() })

	beforeScale := w.canvas.Scale()
	beforeSize := w.canvas.Size()

	pkt := randrScreenChangePacket(3840, 2160, 300, 169)
	xs.events <- decodeRandRScreenChange(pkt)
	w.drainEvents()

	want := windowScale(scaleFromGeometry(3840, 300))
	if w.canvas.Scale() != want {
		t.Fatalf("canvas.Scale() = %v after ScreenChangeNotify, want %v (was %v)", w.canvas.Scale(), want, beforeScale)
	}
	if w.canvas.Scale() == 1 {
		t.Fatal("canvas.Scale() stayed 1 after a 4K/300mm ScreenChangeNotify")
	}
	if w.canvas.Size() == beforeSize {
		t.Fatalf("canvas point size stayed %+v after scale change", beforeSize)
	}
}
