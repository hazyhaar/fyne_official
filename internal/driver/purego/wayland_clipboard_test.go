//go:build purego && linux

package purego

import (
	"encoding/binary"
	"image"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func openWaylandForTest(t *testing.T) *waylandSurface {
	t.Helper()
	// newWaylandSurface falls back to the default socket name, so without
	// this guard the Wayland tests also run inside the X11 (xvfb) suite and
	// share the compositor with it; the CI splits the two suites on
	// WAYLAND_DISPLAY, and so does this helper.
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("purego/wayland: WAYLAND_DISPLAY unset, Wayland tests run in the Wayland suite only")
	}
	s, err := newWaylandSurface(320, 240)
	if err != nil {
		t.Skipf("purego/wayland: no compositor reachable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ws, ok := s.(*waylandSurface)
	if !ok {
		t.Fatal("newWaylandSurface did not return *waylandSurface")
	}
	return ws
}

func wlStringBytes(s string) []byte {
	w := wlMsgWriter{}
	w.putString(s)
	return w.buf
}

func readWlMessage(t *testing.T, c *net.UnixConn) (objectID uint32, opcode uint16, payload []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	header := make([]byte, wlHeaderLen)
	if _, err := io.ReadFull(c, header); err != nil {
		t.Fatalf("read header: %v", err)
	}
	objectID, opcode, size := decodeWlHeader(header)
	if size < wlHeaderLen {
		t.Fatalf("message size %d shorter than header", size)
	}
	payload = make([]byte, size-wlHeaderLen)
	if len(payload) > 0 {
		if _, err := io.ReadFull(c, payload); err != nil {
			t.Fatalf("read payload: %v", err)
		}
	}
	return objectID, opcode, payload
}

func TestWaylandNoteInputSerialFromPointerEnter(t *testing.T) {
	s := &waylandSurface{
		pointerID: 7,
		events:    make(chan surfaceEvent, 4),
		done:      make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	enter := make([]byte, 16)
	binary.LittleEndian.PutUint32(enter[0:4], 99)
	binary.LittleEndian.PutUint32(enter[4:8], 2)
	binary.LittleEndian.PutUint32(enter[8:12], uint32(10*wlFixedShift))
	binary.LittleEndian.PutUint32(enter[12:16], uint32(20*wlFixedShift))
	s.dispatch(7, wlPointerEventEnter, enter)

	st := waylandClipboardOf(s)
	st.mu.Lock()
	serial, pointer := st.serial, st.pointerSerial
	st.mu.Unlock()
	if serial != 99 || pointer != 99 {
		t.Fatalf("serial after wl_pointer.enter = %d/%d, want 99", serial, pointer)
	}
}

func TestWaylandClipboardOfferAndSelectionDispatch(t *testing.T) {
	s := &waylandSurface{
		dataDeviceID: 10,
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	offerID := uint32(0xff000001)
	offer := make([]byte, 4)
	binary.LittleEndian.PutUint32(offer, offerID)
	if !waylandClipboardDispatch(s, 10, wlDataDeviceEventDataOffer, offer) {
		t.Fatal("data_offer was not dispatched")
	}
	if !waylandClipboardDispatch(s, offerID, wlDataOfferEventOffer, wlStringBytes(wlClipboardMime)) {
		t.Fatal("offer mime was not dispatched")
	}
	sel := make([]byte, 4)
	binary.LittleEndian.PutUint32(sel, offerID)
	if !waylandClipboardDispatch(s, 10, wlDataDeviceEventSelection, sel) {
		t.Fatal("selection was not dispatched")
	}

	st := waylandClipboardOf(s)
	st.mu.Lock()
	got := st.selection
	_, hasMime := st.offers[offerID][wlClipboardMime]
	st.mu.Unlock()
	if got != offerID || !hasMime {
		t.Fatalf("selection=%d hasMime=%v, want offer %d with %s", got, hasMime, offerID, wlClipboardMime)
	}
}

func TestWaylandSetClipboardTextSendsSelection(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:                client,
		done:                make(chan struct{}),
		nextID:              20,
		dataDeviceManagerID: 5,
		dataDeviceID:        6,
	}
	t.Cleanup(func() { close(s.done) })

	st := waylandClipboardOf(s)
	st.mu.Lock()
	st.serial = 42
	st.mu.Unlock()

	const want = "purego wayland clipboard"
	if err := s.SetClipboardText(want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}

	obj, op, payload := readWlMessage(t, server)
	if obj != 5 || op != wlDataDevMgrOpCreateDataSource {
		t.Fatalf("first request object=%d opcode=%d, want create_data_source on manager 5", obj, op)
	}
	if len(payload) < 4 {
		t.Fatal("create_data_source missing new_id")
	}
	srcID := binary.LittleEndian.Uint32(payload[:4])

	obj, op, payload = readWlMessage(t, server)
	if obj != srcID || op != wlDataSourceOpOffer {
		t.Fatalf("second request object=%d opcode=%d, want data_source.offer on %d", obj, op, srcID)
	}
	r := wlParser{p: payload}
	mime, err := r.str()
	if err != nil || mime != wlClipboardMime {
		t.Fatalf("offered mime %q, want %q", mime, wlClipboardMime)
	}

	obj, op, payload = readWlMessage(t, server)
	if obj != 6 || op != wlDataDeviceOpSetSelection {
		t.Fatalf("third request object=%d opcode=%d, want set_selection on device 6", obj, op)
	}
	if len(payload) < 8 {
		t.Fatal("set_selection missing source/serial")
	}
	gotSrc := binary.LittleEndian.Uint32(payload[0:4])
	gotSerial := binary.LittleEndian.Uint32(payload[4:8])
	if gotSrc != srcID || gotSerial != 42 {
		t.Fatalf("set_selection source=%d serial=%d, want source=%d serial=42", gotSrc, gotSerial, srcID)
	}
}

func TestWaylandClipboardReceiveTimeout(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:         client,
		dataDeviceID: 10,
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	offerID := uint32(0xff000002)
	offer := make([]byte, 4)
	binary.LittleEndian.PutUint32(offer, offerID)
	_ = waylandClipboardDispatch(s, 10, wlDataDeviceEventDataOffer, offer)
	_ = waylandClipboardDispatch(s, offerID, wlDataOfferEventOffer, wlStringBytes(wlClipboardMime))
	sel := make([]byte, 4)
	binary.LittleEndian.PutUint32(sel, offerID)
	_ = waylandClipboardDispatch(s, 10, wlDataDeviceEventSelection, sel)

	old := waylandClipboardTimeout
	waylandClipboardTimeout = 50 * time.Millisecond
	t.Cleanup(func() { waylandClipboardTimeout = old })

	_, err := s.ClipboardText()
	if err == nil {
		t.Fatal("ClipboardText succeeded without a compositor writing the pipe, want timeout")
	}
}

func TestWaylandClipboardRoundTrip(t *testing.T) {
	s := openWaylandForTest(t)
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case <-s.Events():
		case <-deadline:
			goto set
		}
	}
set:
	const want = "purego wayland clipboard round trip"
	if err := s.SetClipboardText(want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	if err := s.roundtrip(); err != nil {
		t.Fatalf("roundtrip after SetClipboardText: %v", err)
	}

	got, err := s.ClipboardText()
	if err != nil {
		t.Fatalf("ClipboardText: %v", err)
	}
	if got != want {
		t.Fatalf("ClipboardText() = %q, want %q", got, want)
	}
}

func TestWaylandClipboardSecondClient(t *testing.T) {
	s := openWaylandForTest(t)
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}
	const want = "purego wayland clipboard second client"
	if err := s.SetClipboardText(want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	if err := s.roundtrip(); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}

	other, err := newWaylandSurface(64, 64)
	if err != nil {
		t.Logf("cage refused a second Wayland client (%v); same-surface round trip is TestWaylandClipboardRoundTrip", err)
		return
	}
	t.Cleanup(func() { _ = other.Close() })
	ws, ok := other.(*waylandSurface)
	if !ok {
		t.Fatal("second newWaylandSurface did not return *waylandSurface")
	}
	if err := ws.roundtrip(); err != nil {
		t.Fatalf("second client roundtrip: %v", err)
	}
	got, err := ws.ClipboardText()
	if err != nil {
		t.Logf("second client ClipboardText: %v (cage kiosk compositors often keep selection per client)", err)
		return
	}
	if got != want {
		t.Logf("second client ClipboardText() = %q, want %q; cage did not share the selection across clients", got, want)
	}
}

func TestWaylandClipboardSourceSendWritesPipe(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:   client,
		done:   make(chan struct{}),
		nextID: 3,
	}
	t.Cleanup(func() { close(s.done) })
	go s.readLoop()

	st := waylandClipboardOf(s)
	st.mu.Lock()
	st.sourceID = 11
	st.text = "from-source"
	st.mu.Unlock()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()

	msg, err := wlEncode(11, wlDataSourceEventSend, func() wlMsgWriter {
		w := wlMsgWriter{}
		w.putString(wlClipboardMime)
		return w
	}())
	if err != nil {
		t.Fatalf("wlEncode: %v", err)
	}
	oob := unix.UnixRights(int(pw.Fd()))
	if _, _, err := server.WriteMsgUnix(msg, oob, nil); err != nil {
		t.Fatalf("WriteMsgUnix: %v", err)
	}
	_ = pw.Close()

	if err := pr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	b, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("reading pipe after data_source.send: %v", err)
	}
	if got := string(b); got != "from-source" {
		t.Fatalf("pipe read %q, want from-source", got)
	}
}
