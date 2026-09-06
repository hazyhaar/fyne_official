//go:build purego && linux

package purego

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	softwaredriver "fyne.io/fyne/v2/driver/software"
	_ "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// parityScene builds a fixed scene: one
// widget.Label, one widget.Button and one canvas.Rectangle, arranged so all
// three occupy distinct, non-overlapping regions.
func parityScene() fyne.CanvasObject {
	label := widget.NewLabel("parity")
	button := widget.NewButton("go", func() {})
	rect := fynecanvas.NewRectangle(color.NRGBA{R: 200, G: 40, B: 40, A: 255})
	rect.SetMinSize(fyne.NewSize(60, 40))
	return container.NewVBox(label, button, rect)
}

// getImageFull issues a core GetImage (opcode 73, ZPixmap) for the whole
// surface and decodes it to *image.NRGBA, generalizing
// x11_wire_test.go's single-pixel getImagePixel to a full-window capture so
// it can be compared against driver/software's output pixel for pixel. conn
// must be a connection with no other reader (see harness_x11_client_test.go
// for why w.surface.conn itself cannot be used once its readLoop is
// running); drawable is the target window's ID, which can belong to a
// different connection than conn (X11 IDs and extension opcodes are valid
// server-wide, not connection-scoped).
func getImageFull(conn net.Conn, drawable uint32, w, h int) (*image.NRGBA, error) {
	const (
		opGetImage          = 73
		getImageReqLen      = 20
		getImageReqWords    = 5
		getImageReplyHeader = 32
		visualMaskAllPlanes = 0xffffffff
	)
	req := make([]byte, getImageReqLen)
	req[0] = opGetImage
	req[1] = imageFormatZPixmap
	binary.LittleEndian.PutUint16(req[2:4], getImageReqWords)
	binary.LittleEndian.PutUint32(req[4:8], drawable)
	binary.LittleEndian.PutUint16(req[8:10], 0)
	binary.LittleEndian.PutUint16(req[10:12], 0)
	binary.LittleEndian.PutUint16(req[12:14], uint16(w)) //nolint:gosec // test scene stays well within uint16
	binary.LittleEndian.PutUint16(req[14:16], uint16(h)) //nolint:gosec // test scene stays well within uint16
	binary.LittleEndian.PutUint32(req[16:20], visualMaskAllPlanes)

	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	reply := make([]byte, getImageReplyHeader)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return nil, err
	}
	if reply[0] != replyStatusReply {
		return nil, fmt.Errorf("purego/x11: GetImage error status %d", reply[0])
	}
	replyLenWords := binary.LittleEndian.Uint32(reply[4:8])
	data := make([]byte, replyLenWords*4)
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}

	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	stride := w * bytesPerPixel
	for y := 0; y < h; y++ {
		row := data[y*stride : y*stride+w*bytesPerPixel]
		dst := out.Pix[y*out.Stride : y*out.Stride+w*bytesPerPixel]
		for x := 0; x < w; x++ {
			b, g, r := row[x*bytesPerPixel+0], row[x*bytesPerPixel+1], row[x*bytesPerPixel+2]
			dst[x*bytesPerPixel+0] = r
			dst[x*bytesPerPixel+1] = g
			dst[x*bytesPerPixel+2] = b
			dst[x*bytesPerPixel+3] = 0xff
		}
	}
	return out, nil
}

// TestHarnessRenderParityAgainstSoftwareDriver renders the same scene once
// through the purego driver (X server round trip via GetImage) and once
// through fyne.io/fyne/v2/driver/software (in-process rasterization of the
// same object tree), and requires the pixels to match exactly: both paths
// end up calling the same internal/painter/software rasterizer (canvas.go
// documents this for the purego side), so the test expects exact
// parity rather than a PSNR threshold. Any mismatch is reported with the
// count of differing pixels and the first offending coordinate rather than a
// silently adjusted tolerance.
func TestHarnessRenderParityAgainstSoftwareDriver(t *testing.T) {
	const width, height = 240, 180

	d := newTestDriver()
	w := d.CreateWindow("harness-parity").(*window)
	w.SetContent(parityScene())
	w.Resize(fyne.NewSize(width, height))
	w.Show()
	t.Cleanup(w.Close)

	// Drive one redraw synchronously, the same call driverImpl.Run's ticker
	// would make from pumpWindows.
	w.canvas.EnsureMinSize()
	w.canvas.SetDirty()
	w.redrawIfDirty()
	// Let the X server process the Present before reading it back.
	time.Sleep(1 * time.Second)

	// GetImage goes over a dedicated connection (dialSyncX11), not
	// w.surface.conn: see harness_x11_client_test.go for why a synchronous
	// reply races x11Surface.readLoop once it is running, which it is here
	// since w.Show() started it.
	sync := dialSyncX11(t)
	xs, ok := w.surface.(*x11Surface)
	if !ok {
		t.Fatalf("surface is %T, want *x11Surface", w.surface)
	}
	got, err := getImageFull(sync.conn, xs.winID, width, height)
	if err != nil {
		t.Fatalf("getImageFull: %v", err)
	}

	headless := softwaredriver.NewCanvas()
	headless.SetContent(parityScene())
	headless.Resize(fyne.NewSize(width, height))
	want := toNRGBA(headless.Capture())
	if want == nil {
		t.Fatal("driver/software capture returned nil")
	}
	if want.Rect.Dx() != width || want.Rect.Dy() != height {
		t.Fatalf("driver/software capture size = %dx%d, want %dx%d", want.Rect.Dx(), want.Rect.Dy(), width, height)
	}

	// The purego canvas paints objects straight onto the background-filled
	// frame (one composite), whereas driver/software.Capture paints them
	// onto a transparent image first and composites that over the
	// background (two). Both are rounded 8-bit compositions, so an
	// antialiased edge may land one level apart; anything beyond that, or
	// more than 1% of such pixels, is a rendering difference and fails.
	const maxLevelDelta = 1
	offByOne, mismatches := 0, 0
	var firstX, firstY int
	var firstGot, firstWant color.NRGBA
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			g := got.NRGBAAt(x, y)
			wpix := want.NRGBAAt(x, y)
			// The wire capture round-trips through a 24-bit depth drawable
			// with no alpha channel (Present always writes A=0xff, see
			// x11.go), so alpha is intentionally excluded from the
			// comparison.
			d := channelDelta(g.R, wpix.R)
			if gd := channelDelta(g.G, wpix.G); gd > d {
				d = gd
			}
			if bd := channelDelta(g.B, wpix.B); bd > d {
				d = bd
			}
			switch {
			case d == 0:
			case d <= maxLevelDelta:
				offByOne++
			default:
				if mismatches == 0 {
					firstX, firstY, firstGot, firstWant = x, y, g, wpix
				}
				mismatches++
			}
		}
	}
	if mismatches > 0 || offByOne*100 > width*height {
		if os.Getenv("HARNESS_DUMP_PNG") != "" {
			dumpPNG(t, filepath.Join(t.TempDir(), "parity_got.png"), got)
			dumpPNG(t, filepath.Join(t.TempDir(), "parity_want.png"), want)
		}
		t.Fatalf("%d/%d pixels differ by more than %d level and %d by at most %d between purego GetImage capture and driver/software.Capture; first mismatch at (%d,%d): got %+v want %+v",
			mismatches, width*height, maxLevelDelta, offByOne, maxLevelDelta, firstX, firstY, firstGot, firstWant)
	}
	t.Logf("render parity: %d/%d pixels within %d level of driver/software, none beyond", offByOne, width*height, maxLevelDelta)
}

func channelDelta(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func dumpPNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Logf("dumpPNG %s: %v", path, err)
		return
	}
	defer f.Close()
	_ = png.Encode(f, img)
}
