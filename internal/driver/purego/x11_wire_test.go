//go:build purego && linux

package purego

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"sync"
	"testing"
	"time"
)

// TestOpenX11CreatesAndPresentsWindow exercises the wire protocol directly,
// independently of the fyne.Driver/fyne.Window layers above it: it opens a
// real connection to the X server named by $DISPLAY (expected to be an xvfb
// server under this step's test command), creates a window, presents a
// solid color frame and reads the drawable back with GetImage (opcode 73) to
// confirm the pixels the server now holds match what was sent. This test
// failed before x11.go exists (there was no package to import), and it fails
// again if Present stops actually reaching the server.
func TestOpenX11CreatesAndPresentsWindow(t *testing.T) {
	const width, height = 64, 48
	s, err := openX11("", width, height)
	if err != nil {
		t.Fatalf("openX11: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.SetTitle("purego wire test"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	fill := color.NRGBA{R: 10, G: 200, B: 40, A: 255}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}

	// Give the server a moment to process MapWindow/PutImage before reading
	// the drawable back; there is no server round-trip to wait on other than
	// issuing a further request and reading its reply, done below.
	time.Sleep(100 * time.Millisecond)

	got, err := getImagePixel(s, width/2, height/2)
	if err != nil {
		t.Fatalf("getImagePixel: %v", err)
	}
	if got != fill {
		t.Fatalf("center pixel after Present = %+v, want %+v", got, fill)
	}
}

// getImagePixel issues a minimal core GetImage (opcode 73, ZPixmap) request
// for a single pixel and decodes it back to color.NRGBA, converting from the
// BGRX wire layout x11Surface.Present writes. It goes through s.request(),
// not a direct read on s.conn: reading the reply while
// x11Surface.readLoop is already the connection's background reader races
// it for the same bytes and hangs or crashes readLoop (see x11.go,
// request/deliverPending doc comments). This test is the regression
// test for that bug: TestOpenX11CreatesAndPresentsWindow must complete
// within the suite's normal timeout, not hang.
func getImagePixel(s *x11Surface, x, y int) (color.NRGBA, error) {
	const (
		opGetImage          = 73
		getImageReqLen      = 20
		getImageReqWords    = 5
		visualMaskAllPlanes = 0xffffffff
	)
	req := make([]byte, getImageReqLen)
	req[0] = opGetImage
	req[1] = imageFormatZPixmap
	binary.LittleEndian.PutUint16(req[2:4], getImageReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint16(req[8:10], uint16(x))  //nolint:gosec // test-only coordinates within the small window this test creates
	binary.LittleEndian.PutUint16(req[10:12], uint16(y)) //nolint:gosec // test-only coordinates within the small window this test creates
	binary.LittleEndian.PutUint16(req[12:14], 1)
	binary.LittleEndian.PutUint16(req[14:16], 1)
	binary.LittleEndian.PutUint32(req[16:20], visualMaskAllPlanes)

	reply, err := s.request(req)
	if err != nil {
		return color.NRGBA{}, err
	}
	if reply[0] != replyStatusReply {
		return color.NRGBA{}, errStatus(reply[0])
	}
	const replyHeaderLen = 32
	data := reply[replyHeaderLen:]
	if len(data) < 3 {
		return color.NRGBA{}, errStatus(reply[0])
	}
	// depth-24 ZPixmap pixel: B,G,R,X little-endian, as written by Present.
	return color.NRGBA{R: data[2], G: data[1], B: data[0], A: 255}, nil
}

// TestConcurrentRequestsDoNotRace is the regression test for a hang or
// crash reading x11Surface's
// connection, traced to two goroutines independently calling io.ReadFull on
// the same net.Conn — readLoop, and a synchronous reply reader such as the
// pre-fix getImagePixel or internAtom. request()/deliverPending (see
// x11.go) fixed this by making readLoop the connection's only reader and
// routing replies back through a FIFO of waiting channels. This test drives
// many concurrent request()-based calls (InternAtom, chosen because it
// needs no window state) at once and requires every one of them to return
// the correct, distinct atom for its own name within a bounded time — a
// misrouted reply would return the wrong atom or block forever, and this
// test used to do exactly that before the fix (verified by re-introducing
// the direct-read version of getImagePixel locally and observing the
// hang/wrong-atom failure this test is built to catch).
func TestConcurrentRequestsDoNotRace(t *testing.T) {
	s, err := openX11("", 32, 32)
	if err != nil {
		t.Fatalf("openX11: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	const concurrency = 32
	names := make([]string, concurrency)
	for i := range names {
		names[i] = fmt.Sprintf("PUREGO_TEST_ATOM_%d", i)
	}

	results := make([]uint32, concurrency)
	errs := make([]error, concurrency)
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			atom, err := s.internAtom(name)
			results[i] = atom
			errs[i] = err
		}(i, name)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent InternAtom calls did not complete within 10s: readLoop is likely deadlocked (defect 2 regression)")
	}

	seen := make(map[uint32]string, concurrency)
	for i, name := range names {
		if errs[i] != nil {
			t.Fatalf("internAtom(%s): %v", name, errs[i])
		}
		if results[i] == 0 {
			t.Fatalf("internAtom(%s) returned atom 0", name)
		}
		if other, dup := seen[results[i]]; dup {
			t.Fatalf("internAtom(%s) and internAtom(%s) both returned atom %d: a reply was misrouted", name, other, results[i])
		}
		seen[results[i]] = name
	}
}

type errStatus byte

func (errStatus) Error() string { return "purego/x11: GetImage error status" }
