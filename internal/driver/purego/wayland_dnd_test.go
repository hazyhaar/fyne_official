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

func TestWaylandDndForgedDropDeliversTwoURIs(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:         client,
		dataDeviceID: 10,
		events:       make(chan surfaceEvent, 4),
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	offerID := uint32(0xff000003)
	offer := make([]byte, 4)
	binary.LittleEndian.PutUint32(offer, offerID)
	if !waylandClipboardDispatch(s, 10, wlDataDeviceEventDataOffer, offer) {
		t.Fatal("data_offer was not dispatched")
	}
	if !waylandClipboardDispatch(s, offerID, wlDataOfferEventOffer, wlStringBytes(wlURIListMime)) {
		t.Fatal("offer mime was not dispatched")
	}

	payload := "file:///tmp/purego-dnd-a\r\nfile:///tmp/purego-dnd-b\r\n"
	go serveWaylandDndReceive(server, offerID, payload)

	const dropX, dropY = 40, 60
	enter := make([]byte, 20)
	binary.LittleEndian.PutUint32(enter[0:4], 7)
	binary.LittleEndian.PutUint32(enter[4:8], 2)
	binary.LittleEndian.PutUint32(enter[8:12], uint32(dropX*wlFixedShift))
	binary.LittleEndian.PutUint32(enter[12:16], uint32(dropY*wlFixedShift))
	binary.LittleEndian.PutUint32(enter[16:20], offerID)
	if !waylandDndDispatch(s, 10, wlDataDeviceEventEnter, enter) {
		t.Fatal("enter was not dispatched")
	}

	motion := make([]byte, 12)
	binary.LittleEndian.PutUint32(motion[0:4], 1)
	binary.LittleEndian.PutUint32(motion[4:8], uint32(dropX*wlFixedShift))
	binary.LittleEndian.PutUint32(motion[8:12], uint32(dropY*wlFixedShift))
	if !waylandDndDispatch(s, 10, wlDataDeviceEventMotion, motion) {
		t.Fatal("motion was not dispatched")
	}
	if !waylandDndDispatch(s, 10, wlDataDeviceEventDrop, nil) {
		t.Fatal("drop was not dispatched")
	}

	select {
	case ev := <-s.events:
		d, ok := ev.(dropEvent)
		if !ok {
			t.Fatalf("event type %T, want dropEvent", ev)
		}
		if int(d.Position.X) != dropX || int(d.Position.Y) != dropY {
			t.Fatalf("drop position = %v, want (%d,%d)", d.Position, dropX, dropY)
		}
		if len(d.URIs) != 2 {
			t.Fatalf("got %d URIs, want 2", len(d.URIs))
		}
		if d.URIs[0].Path() != "/tmp/purego-dnd-a" || d.URIs[1].Path() != "/tmp/purego-dnd-b" {
			t.Fatalf("URIs = %q, %q", d.URIs[0].Path(), d.URIs[1].Path())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dropEvent was not delivered after forged enter/motion/drop")
	}
}

func TestWaylandDndLeaveClearsOffer(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:         client,
		dataDeviceID: 10,
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	enter := make([]byte, 20)
	binary.LittleEndian.PutUint32(enter[16:20], 99)
	if !waylandDndDispatch(s, 10, wlDataDeviceEventEnter, enter) {
		t.Fatal("enter was not dispatched")
	}
	if !waylandDndDispatch(s, 10, wlDataDeviceEventLeave, nil) {
		t.Fatal("leave was not dispatched")
	}
	st := waylandDndOf(s)
	st.mu.Lock()
	active, offer := st.active, st.offer
	st.mu.Unlock()
	if active || offer != 0 {
		t.Fatalf("after leave active=%v offer=%d, want inactive", active, offer)
	}
}

func TestWaylandDndDeviceBoundUnderCage(t *testing.T) {
	s := openWaylandForTest(t)
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	if err := s.Present(img); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if err := s.roundtrip(); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	s.mu.Lock()
	dd := s.dataDeviceID
	s.mu.Unlock()
	if dd == 0 {
		t.Fatal("wl_data_device was not bound")
	}
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case ev := <-s.Events():
			if d, ok := ev.(dropEvent); ok {
				t.Logf("cage delivered a drop of %d URI(s) at %v", len(d.URIs), d.Position)
				return
			}
		case <-deadline:
			t.Log("cage did not produce a drag-and-drop offer; forged enter/motion/drop covers the wire path")
			return
		}
	}
}

func serveWaylandDndReceive(c *net.UnixConn, offerID uint32, payload string) {
	for {
		obj, op, fds, err := readWlMessageMaybeFD(c)
		if err != nil {
			return
		}
		if obj == offerID && op == wlDataOfferOpReceive && len(fds) > 0 {
			f := os.NewFile(uintptr(fds[0]), "wl-dnd-offer")
			_, _ = f.WriteString(payload)
			_ = f.Close()
			return
		}
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}
}

func readWlMessageMaybeFD(c *net.UnixConn) (objectID uint32, opcode uint16, fds []int, err error) {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	header := make([]byte, wlHeaderLen)
	oob := make([]byte, unix.CmsgSpace(4))
	hn, oobn, _, _, err := c.ReadMsgUnix(header, oob)
	if err != nil || hn < wlHeaderLen {
		return 0, 0, nil, err
	}
	objectID, opcode, size := decodeWlHeader(header)
	if size < wlHeaderLen {
		return 0, 0, nil, io.ErrUnexpectedEOF
	}
	payload := make([]byte, size-wlHeaderLen)
	if len(payload) > 0 {
		if _, err := io.ReadFull(c, payload); err != nil {
			return 0, 0, nil, err
		}
	}
	if oobn > 0 {
		cmsgs, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err == nil {
			for i := range cmsgs {
				got, err := unix.ParseUnixRights(&cmsgs[i])
				if err == nil {
					fds = append(fds, got...)
				}
			}
		}
	}
	return objectID, opcode, fds, nil
}

func TestWaylandDndRejectsNonURISendsAcceptNull(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:         client,
		dataDeviceID: 10,
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	offerID := uint32(0xff000010)
	offer := make([]byte, 4)
	binary.LittleEndian.PutUint32(offer, offerID)
	if !waylandClipboardDispatch(s, 10, wlDataDeviceEventDataOffer, offer) {
		t.Fatal("data_offer was not dispatched")
	}
	if !waylandClipboardDispatch(s, offerID, wlDataOfferEventOffer, wlStringBytes("image/png")) {
		t.Fatal("offer mime was not dispatched")
	}

	enter := make([]byte, 20)
	binary.LittleEndian.PutUint32(enter[0:4], 7)
	binary.LittleEndian.PutUint32(enter[16:20], offerID)
	if !waylandDndDispatch(s, 10, wlDataDeviceEventEnter, enter) {
		t.Fatal("enter was not dispatched")
	}

	obj, op, payload := readWlMessage(t, server)
	if obj != offerID || op != wlDataOfferOpAccept {
		t.Fatalf("got object %d opcode %d, want accept on offer %d", obj, op, offerID)
	}
	if len(payload) < 8 {
		t.Fatalf("accept payload %d bytes, want serial plus null mime", len(payload))
	}
	serial := binary.LittleEndian.Uint32(payload[0:4])
	strLen := binary.LittleEndian.Uint32(payload[4:8])
	if serial != 7 || strLen != 0 {
		t.Fatalf("accept serial=%d mime_len=%d, want serial=7 and null mime (len 0)", serial, strLen)
	}
}

func TestWaylandDndEnterSendsSetActionsCopy(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:         client,
		dataDeviceID: 10,
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	offerID := uint32(0xff000011)
	offer := make([]byte, 4)
	binary.LittleEndian.PutUint32(offer, offerID)
	if !waylandClipboardDispatch(s, 10, wlDataDeviceEventDataOffer, offer) {
		t.Fatal("data_offer was not dispatched")
	}
	if !waylandClipboardDispatch(s, offerID, wlDataOfferEventOffer, wlStringBytes(wlURIListMime)) {
		t.Fatal("offer mime was not dispatched")
	}

	enter := make([]byte, 20)
	binary.LittleEndian.PutUint32(enter[0:4], 9)
	binary.LittleEndian.PutUint32(enter[16:20], offerID)
	if !waylandDndDispatch(s, 10, wlDataDeviceEventEnter, enter) {
		t.Fatal("enter was not dispatched")
	}

	var sawSetActions bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sawSetActions {
		obj, op, payload := readWlMessage(t, server)
		if obj != offerID {
			continue
		}
		if op == wlDataOfferOpSetActions {
			if len(payload) < 8 {
				t.Fatalf("set_actions payload %d bytes, want 8", len(payload))
			}
			actions := binary.LittleEndian.Uint32(payload[0:4])
			preferred := binary.LittleEndian.Uint32(payload[4:8])
			if actions != wlDndActionCopy || preferred != wlDndActionCopy {
				t.Fatalf("set_actions actions=%d preferred=%d, want Copy", actions, preferred)
			}
			sawSetActions = true
		}
	}
	if !sawSetActions {
		t.Fatal("wl_data_offer.set_actions was not sent on enter")
	}
}

func TestWaylandDndLeaveDestroysOffers(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()

	s := &waylandSurface{
		conn:         client,
		dataDeviceID: 10,
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	const rounds = 100
	for i := 0; i < rounds; i++ {
		offerID := uint32(0xff010000 + i)
		offer := make([]byte, 4)
		binary.LittleEndian.PutUint32(offer, offerID)
		if !waylandClipboardDispatch(s, 10, wlDataDeviceEventDataOffer, offer) {
			t.Fatalf("data_offer %d was not dispatched", i)
		}
		if !waylandClipboardDispatch(s, offerID, wlDataOfferEventOffer, wlStringBytes(wlURIListMime)) {
			t.Fatalf("offer mime %d was not dispatched", i)
		}
		enter := make([]byte, 20)
		binary.LittleEndian.PutUint32(enter[0:4], uint32(i+1))
		binary.LittleEndian.PutUint32(enter[16:20], offerID)
		if !waylandDndDispatch(s, 10, wlDataDeviceEventEnter, enter) {
			t.Fatalf("enter %d was not dispatched", i)
		}
		if !waylandDndDispatch(s, 10, wlDataDeviceEventLeave, nil) {
			t.Fatalf("leave %d was not dispatched", i)
		}
	}
	clip := waylandClipboardOf(s)
	clip.mu.Lock()
	n := len(clip.offers)
	clip.mu.Unlock()
	if n != 0 {
		t.Fatalf("len(offers) = %d after %d enter/leave, want 0", n, rounds)
	}
}

func TestWaylandReceiveOfferRejectsOverLimit(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:         client,
		dataDeviceID: 10,
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	offerID := uint32(0xff000012)
	old := waylandOfferMaxBytes
	waylandOfferMaxBytes = 32
	t.Cleanup(func() { waylandOfferMaxBytes = old })

	go serveWaylandDndReceive(server, offerID, string(make([]byte, 64)))

	_, err := waylandReceiveOffer(s, offerID, wlURIListMime)
	if err == nil {
		t.Fatal("waylandReceiveOffer accepted a payload over the size limit")
	}
}

func TestWaylandDndReadsActionEvent(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()

	s := &waylandSurface{
		conn:         client,
		dataDeviceID: 10,
		done:         make(chan struct{}),
	}
	t.Cleanup(func() { close(s.done) })

	offerID := uint32(0xff000013)
	enter := make([]byte, 20)
	binary.LittleEndian.PutUint32(enter[16:20], offerID)
	if !waylandDndDispatch(s, 10, wlDataDeviceEventEnter, enter) {
		t.Fatal("enter was not dispatched")
	}
	action := make([]byte, 4)
	binary.LittleEndian.PutUint32(action, wlDndActionCopy)
	if !waylandDndDispatch(s, offerID, wlDataOfferEventAction, action) {
		t.Fatal("action event was not dispatched")
	}
	st := waylandDndOf(s)
	st.mu.Lock()
	got := st.action
	st.mu.Unlock()
	if got != wlDndActionCopy {
		t.Fatalf("stored action = %d, want Copy %d", got, wlDndActionCopy)
	}
}
