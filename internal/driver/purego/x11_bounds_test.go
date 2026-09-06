//go:build purego && linux

package purego

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"net"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

// TestCheckWireAllocBytesRejectsOverLimit is a plain unit test of the shared
// guard every wire-driven allocation in this package now goes through (see
// wire_bounds.go).
func TestCheckWireAllocBytesRejectsOverLimit(t *testing.T) {
	if err := checkWireAllocBytes(maxWireAlloc, maxWireAlloc, "x"); err != nil {
		t.Fatalf("exactly the limit must be accepted, got %v", err)
	}
	if err := checkWireAllocBytes(maxWireAlloc+1, maxWireAlloc, "x"); err == nil {
		t.Fatal("one byte over the limit must be refused")
	}
}

// TestPresentPutImageBandsLargeWindow guards against a regression once seen in this code:
// core PutImage request under the CARD16 length limit used to have its
// request length silently wrap, corrupting the connection. It forces the
// PutImage fallback path (MIT-SHM disabled) on a window larger than the old
// single-request ceiling (~256x256 at 32bpp) and confirms the frame reaches
// the server intact by reading a pixel back with GetImage — which requires
// every band presentPutImage now splits the frame into to have used a valid,
// non-wrapped CARD16 length, or the connection would have been dropped by
// the server before this read could succeed.
func TestPresentPutImageBandsLargeWindow(t *testing.T) {
	const width, height = 600, 400 // well past the ~256x256 single-request ceiling
	s, err := openX11("", width, height)
	if err != nil {
		t.Fatalf("openX11: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.shm = &shmExtension{available: false} // force the PutImage fallback path

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	fill := color.NRGBA{R: 5, G: 250, B: 60, A: 255}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	for _, pt := range [][2]int{{0, 0}, {width / 2, height / 2}, {width - 1, height - 1}} {
		got, err := getImagePixel(s, pt[0], pt[1])
		if err != nil {
			t.Fatalf("getImagePixel%v: %v (connection likely desynchronized by an overflowed PutImage length)", pt, err)
		}
		if got != fill {
			t.Fatalf("pixel at %v after banded Present = %+v, want %+v", pt, got, fill)
		}
	}
}

// TestSetWindowIconRefusesOverCard16Limit guards against a regression once seen in this code:
// request, one over the field's range. Before the bound check,
// uint16(65544) silently wrapped to 8 and corrupted the connection instead
// of returning an error.
func TestSetWindowIconRefusesOverCard16Limit(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })

	const iconW, iconH = 256, 256
	img := image.NewRGBA(image.Rect(0, 0, iconW, iconH))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	res := fyne.NewStaticResource("big-icon.png", buf.Bytes())

	if err := s.SetWindowIcon(res); err == nil {
		t.Fatal("SetWindowIcon with a 256x256 icon (65544 words) returned no error; want a refusal instead of a wrapped CARD16 length")
	}
}

// TestWlEncodeRefusesOverWireSizeLimit guards against a regression once seen in this code:
// silently truncated by the << 16 encoding (e.g. 65540 encodes as 4).
// wlEncode must refuse such a message instead of producing a malformed one.
func TestWlEncodeRefusesOverWireSizeLimit(t *testing.T) {
	var w wlMsgWriter
	w.buf = make([]byte, maxWlMessageSize) // wlHeaderLen + this already overflows
	if _, err := wlEncode(1, 0, w); err == nil {
		t.Fatal("wlEncode with an oversized payload returned no error; want a refusal instead of a wrapped 16-bit size field")
	}

	var ok wlMsgWriter
	ok.buf = make([]byte, maxWlMessageSize-wlHeaderLen)
	if _, err := wlEncode(1, 0, ok); err != nil {
		t.Fatalf("wlEncode at exactly the size limit was refused: %v", err)
	}
}

// TestReadLoopRejectsHostileReplyLength guards against a regression once seen in this code:
// would make() an allocation far beyond any real X11 reply (here, 0x3fffffff
// words, ~4 GiB) must close the connection with a logged error instead of
// attempting the allocation. It drives readLoop directly over a net.Pipe
// standing in for the server connection, so the process never actually
// performs (or attempts and fails) a multi-gigabyte allocation.
func TestReadLoopRejectsHostileReplyLength(t *testing.T) {
	client, server := net.Pipe()
	s := &x11Surface{
		conn:         client,
		events:       make(chan surfaceEvent, eventChanCapacity),
		done:         make(chan struct{}),
		pendingBySeq: make(map[uint16]chan []byte),
	}
	go s.readLoop()

	forged := make([]byte, coreEventSize)
	forged[0] = replyStatusReply
	binary.LittleEndian.PutUint32(forged[4:8], 0x3fffffff) // ~4 GiB of extra words
	writeErr := make(chan error, 1)
	go func() { _, err := server.Write(forged); writeErr <- err }()

	select {
	case ev := <-s.events:
		if _, ok := ev.(closeEvent); !ok {
			t.Fatalf("got event %T, want closeEvent after a hostile reply length", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not close the connection after a hostile reply length ")
	}
	<-writeErr
	_ = server.Close()
}
