//go:build purego && linux

package purego

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"fyne.io/fyne/v2"
)

// TestSetWindowIconPublishesNetWMIcon checks that SetWindowIcon(res) results
// in a non-empty _NET_WM_ICON property whose declared width/height match the
// resource's decoded image, read back with a raw GetProperty from a second
// connection. This test failed before x11_icon.go existed (iconSurface had
// no implementation to call).
func TestSetWindowIconPublishesNetWMIcon(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })

	const iconW, iconH = 4, 3
	img := image.NewRGBA(image.Rect(0, 0, iconW, iconH))
	for y := 0; y < iconH; y++ {
		for x := 0; x < iconW; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 20, B: 20, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	res := fyne.NewStaticResource("test-icon.png", buf.Bytes())

	if err := s.SetWindowIcon(res); err != nil {
		t.Fatalf("SetWindowIcon: %v", err)
	}

	peer := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = peer.Close() })

	netWMIcon, err := peer.internAtom("_NET_WM_ICON")
	if err != nil {
		t.Fatalf("internAtom(_NET_WM_ICON): %v", err)
	}
	atoms, err := readAtomListPropertyOfType(peer, s.winID, netWMIcon, icccmCARDINAL)
	if err != nil {
		t.Fatalf("reading _NET_WM_ICON: %v", err)
	}
	if len(atoms) < 2+iconW*iconH {
		t.Fatalf("_NET_WM_ICON has %d CARDINAL entries, want at least %d (header + %dx%d pixels)",
			len(atoms), 2+iconW*iconH, iconW, iconH)
	}
	if atoms[0] != iconW || atoms[1] != iconH {
		t.Fatalf("_NET_WM_ICON declares %dx%d, want %dx%d", atoms[0], atoms[1], iconW, iconH)
	}
}

// TestSetWindowIconNilResource checks that a nil resource is rejected
// rather than silently publishing a garbage property.
func TestSetWindowIconNilResource(t *testing.T) {
	s := openX11ForTest(t, 16, 16)
	t.Cleanup(func() { _ = s.Close() })

	if err := s.SetWindowIcon(nil); err == nil {
		t.Fatal("SetWindowIcon(nil) returned no error")
	}
}
