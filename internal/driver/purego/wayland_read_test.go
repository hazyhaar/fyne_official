//go:build purego && linux

package purego

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWlSplitMessageByteAtATime(t *testing.T) {
	msg, err := wlEncode(4, xdgWmBaseEventPing, wlMsgWriter{}.putU(99))
	if err != nil {
		t.Fatalf("wlEncode: %v", err)
	}
	var buf []byte
	for i, b := range msg {
		buf = append(buf, b)
		got, rest, splitErr := wlSplitMessage(buf)
		if splitErr != nil {
			t.Fatalf("split at %d: %v", i, splitErr)
		}
		if i+1 < len(msg) {
			if got != nil {
				t.Fatalf("complete message at %d bytes, want need-more", i+1)
			}
			continue
		}
		if got == nil {
			t.Fatal("full buffer did not yield a message")
		}
		if len(rest) != 0 {
			t.Fatalf("rest = %d, want 0", len(rest))
		}
		id, op, _ := decodeWlHeader(got)
		if id != 4 || op != xdgWmBaseEventPing {
			t.Fatalf("id=%d opcode=%d", id, op)
		}
	}
}

func TestWaylandReadLoopReassemblesFragments(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:     client,
		done:     make(chan struct{}),
		events:   make(chan surfaceEvent, 4),
		wmBaseID: 4,
	}
	t.Cleanup(func() { _ = s.Close() })
	go s.readLoop()

	msg, err := wlEncode(4, xdgWmBaseEventPing, wlMsgWriter{}.putU(7))
	if err != nil {
		t.Fatalf("wlEncode: %v", err)
	}
	for _, b := range msg {
		if _, err := server.Write([]byte{b}); err != nil {
			t.Fatalf("write byte: %v", err)
		}
	}

	objectID, opcode, payload := readWlMessage(t, server)
	if objectID != 4 || opcode != xdgWmBaseOpPong {
		t.Fatalf("got object %d opcode %d, want pong", objectID, opcode)
	}
	if binary.LittleEndian.Uint32(payload) != 7 {
		t.Fatalf("pong serial = %d, want 7", binary.LittleEndian.Uint32(payload))
	}
}

func TestWaylandReadLoopNotBlockedByFullEventQueue(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:     client,
		done:     make(chan struct{}),
		events:   make(chan surfaceEvent, eventChanCapacity),
		wmBaseID: 4,
	}
	t.Cleanup(func() { _ = s.Close() })
	go s.readLoop()

	for i := 0; i < 300; i++ {
		s.emit(resizeEvent{Width: 1, Height: 1})
	}

	msg, err := wlEncode(4, xdgWmBaseEventPing, wlMsgWriter{}.putU(11))
	if err != nil {
		t.Fatalf("wlEncode: %v", err)
	}
	if _, err := server.Write(msg); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	if err := server.SetReadDeadline(deadline); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	objectID, opcode, _ := readWlMessage(t, server)
	if objectID != 4 || opcode != xdgWmBaseOpPong {
		t.Fatalf("got object %d opcode %d, want pong while 300 events were pending", objectID, opcode)
	}
}

func TestWaylandKeymapOverLimitFails(t *testing.T) {
	s := &waylandSurface{
		done:   make(chan struct{}),
		events: make(chan surfaceEvent, 1),
	}
	t.Cleanup(func() { _ = s.Close() })
	p := make([]byte, wlKeymapArgsLen)
	binary.LittleEndian.PutUint32(p[0:4], wlKeyboardKeymapFormatXKBV1)
	binary.LittleEndian.PutUint32(p[4:8], 2<<20)
	s.handleKeyboardKeymap(p)
	if !s.closed {
		t.Fatal("2 MiB keymap must close the surface")
	}
}

func TestWaylandRecvTruncatedReportsCTRUNC(t *testing.T) {
	if err := waylandRecvTruncated(0); err != nil {
		t.Fatalf("no flags: %v", err)
	}
	if err := waylandRecvTruncated(unix.MSG_CTRUNC); err == nil {
		t.Fatal("MSG_CTRUNC must be reported")
	}
}

func TestWaylandReadLoopDetectsCTRUNC(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	oldSpace := waylandCmsgSpace
	waylandCmsgSpace = unix.CmsgSpace(waylandAncillaryFDBytes)
	t.Cleanup(func() { waylandCmsgSpace = oldSpace })

	s := &waylandSurface{
		conn:   client,
		done:   make(chan struct{}),
		events: make(chan surfaceEvent, 4),
	}
	t.Cleanup(func() { _ = s.Close() })
	go s.readLoop()

	nFDs := 16
	files := make([]*os.File, 0, nFDs)
	t.Cleanup(func() {
		for _, f := range files {
			_ = f.Close()
		}
	})
	for i := 0; i < nFDs; i++ {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		files = append(files, pr)
		_ = pw.Close()
	}
	rights := make([]int, nFDs)
	for i := range files {
		rights[i] = int(files[i].Fd())
	}
	msg, err := wlEncode(1, 0, wlMsgWriter{}.putU(1))
	if err != nil {
		t.Fatalf("wlEncode: %v", err)
	}
	oob := unix.UnixRights(rights...)
	if _, _, err := server.WriteMsgUnix(msg, oob, nil); err != nil {
		t.Fatalf("WriteMsgUnix: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-s.events:
			if _, ok := ev.(closeEvent); ok {
				return
			}
		case <-deadline:
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				t.Fatal("readLoop did not close after truncated ancillary data")
			}
			return
		}
	}
}

func TestWaylandClipboardSendDoesNotBlockReadLoop(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:     client,
		done:     make(chan struct{}),
		events:   make(chan surfaceEvent, 4),
		wmBaseID: 4,
		nextID:   3,
	}
	t.Cleanup(func() { _ = s.Close() })
	go s.readLoop()

	st := waylandClipboardOf(s)
	st.mu.Lock()
	st.sourceID = 11
	st.text = strings.Repeat("x", 100*1024)
	st.mu.Unlock()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()

	sendMsg, err := wlEncode(11, wlDataSourceEventSend, func() wlMsgWriter {
		w := wlMsgWriter{}
		w.putString(wlClipboardMime)
		return w
	}())
	if err != nil {
		t.Fatalf("wlEncode send: %v", err)
	}
	if _, _, err := server.WriteMsgUnix(sendMsg, unix.UnixRights(int(pw.Fd())), nil); err != nil {
		t.Fatalf("WriteMsgUnix send: %v", err)
	}

	ping, err := wlEncode(4, xdgWmBaseEventPing, wlMsgWriter{}.putU(3))
	if err != nil {
		t.Fatalf("wlEncode ping: %v", err)
	}
	if _, err := server.Write(ping); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if err := server.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	for {
		objectID, opcode, _ := readWlMessage(t, server)
		if objectID == 4 && opcode == xdgWmBaseOpPong {
			return
		}
	}
}

func TestRecvmsgKeymapFDReadableAfterDispatch(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:       client,
		done:       make(chan struct{}),
		events:     make(chan surfaceEvent, 4),
		wmBaseID:   4,
		keyboardID: 9,
	}
	t.Cleanup(func() { _ = s.Close() })
	go s.readLoop()

	raw := append([]byte(frenchSyntheticKeymap), 0)
	fd, err := unix.MemfdCreate("purego-keymap-fd", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatalf("memfd_create: %v", err)
	}
	if _, err := unix.Write(fd, raw); err != nil {
		_ = unix.Close(fd)
		t.Fatalf("write keymap: %v", err)
	}

	ping, err := wlEncode(4, xdgWmBaseEventPing, wlMsgWriter{}.putU(1))
	if err != nil {
		_ = unix.Close(fd)
		t.Fatalf("wlEncode ping: %v", err)
	}
	n, err := uint32FromInt(len(raw))
	if err != nil {
		_ = unix.Close(fd)
		t.Fatalf("keymap length: %v", err)
	}
	kmWriter := wlMsgWriter{}
	kmWriter.putUint32(wlKeyboardKeymapFormatXKBV1)
	kmWriter.putUint32(n)
	keymap, err := wlEncode(9, wlKeyboardEventKeymap, kmWriter)
	if err != nil {
		_ = unix.Close(fd)
		t.Fatalf("wlEncode keymap: %v", err)
	}
	combined := append(append([]byte{}, ping...), keymap...)
	if _, _, err := server.WriteMsgUnix(combined, unix.UnixRights(fd), nil); err != nil {
		_ = unix.Close(fd)
		t.Fatalf("WriteMsgUnix: %v", err)
	}
	_ = unix.Close(fd)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		km := s.xkb
		s.mu.Unlock()
		if km != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("keymap fd was not readable after dispatch; closeLeftoverFDs closed it with the preceding ping")
}

func TestRecvmsgUnclaimedFDClosed(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:     client,
		done:     make(chan struct{}),
		events:   make(chan surfaceEvent, 4),
		wmBaseID: 4,
	}

	var pipe [2]int
	if err := unix.Pipe(pipe[:]); err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer unix.Close(pipe[1])

	ping, err := wlEncode(4, xdgWmBaseEventPing, wlMsgWriter{}.putU(1))
	if err != nil {
		_ = unix.Close(pipe[0])
		t.Fatalf("wlEncode: %v", err)
	}
	if _, _, err := server.WriteMsgUnix(ping, unix.UnixRights(pipe[0]), nil); err != nil {
		_ = unix.Close(pipe[0])
		t.Fatalf("WriteMsgUnix: %v", err)
	}
	_ = unix.Close(pipe[0])

	tmp := make([]byte, waylandRecvBufSize)
	oob := make([]byte, waylandCmsgSpace)
	n, oobn, _, _, err := client.ReadMsgUnix(tmp, oob)
	if err != nil {
		t.Fatalf("ReadMsgUnix: %v", err)
	}
	fds := waylandParseRights(oob[:oobn])
	if len(fds) != 1 {
		t.Fatalf("got %d fds, want 1", len(fds))
	}
	received := fds[0]
	s.enqueuePendingFDs(fds)
	pending, splitErr := s.consumeWaylandMessages(tmp[:n])
	if splitErr != nil {
		t.Fatalf("consumeWaylandMessages: %v", splitErr)
	}
	if len(pending) != 0 {
		t.Fatalf("pending leftover %d bytes", len(pending))
	}
	s.closeLeftoverFDs()

	var st unix.Stat_t
	if err := unix.Fstat(received, &st); err == nil {
		_ = unix.Close(received)
		t.Fatal("unclaimed fd still open after closeLeftoverFDs")
	}
}

func TestCloseLeftoverFDsLeavesFDForLaterMessage(t *testing.T) {
	var pipe [2]int
	if err := unix.Pipe(pipe[:]); err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer unix.Close(pipe[1])
	fd := pipe[0]

	s := &waylandSurface{
		done:   make(chan struct{}),
		events: make(chan surfaceEvent, 4),
	}
	s.enqueuePendingFDs([]int{fd})

	noop, err := wlEncode(99, 0, wlMsgWriter{}.putU(1))
	if err != nil {
		_ = unix.Close(fd)
		t.Fatalf("wlEncode: %v", err)
	}
	pending, splitErr := s.consumeWaylandMessages(noop)
	if splitErr != nil {
		_ = unix.Close(fd)
		t.Fatalf("consumeWaylandMessages: %v", splitErr)
	}
	if len(pending) != 0 {
		_ = unix.Close(fd)
		t.Fatalf("pending leftover %d bytes", len(pending))
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatalf("fd closed after a message that does not claim it: %v", err)
	}
	_ = unix.Close(fd)
}

func TestWlParserStrHostileLength(t *testing.T) {
	over := make([]byte, 4)
	binary.LittleEndian.PutUint32(over, 0xffffffff)
	if _, err := (&wlParser{p: over}).str(); err == nil {
		t.Fatal("length 0xFFFFFFFF must be refused")
	}
	short := make([]byte, 8)
	binary.LittleEndian.PutUint32(short, 100)
	if _, err := (&wlParser{p: short}).str(); err == nil {
		t.Fatal("length 100 with 4 remaining bytes must be refused")
	}
	w := wlMsgWriter{}
	w.putString("ok")
	got, err := (&wlParser{p: w.buf}).str()
	if err != nil || got != "ok" {
		t.Fatalf("got %q, %v; want ok, nil", got, err)
	}
}

func TestWlParserSkipArrayHostileLength(t *testing.T) {
	over := make([]byte, 4)
	binary.LittleEndian.PutUint32(over, 0xffffffff)
	if err := (&wlParser{p: over}).skipArray(); err == nil {
		t.Fatal("array length 0xFFFFFFFF must be refused")
	}
}

func FuzzWaylandMessage(f *testing.F) {
	msg, err := wlEncode(1, 0, wlMsgWriter{}.putU(1))
	if err == nil {
		f.Add(msg)
	}
	f.Add([]byte{1, 0, 0, 0, 4, 0, 0, 0})
	f.Add([]byte{0, 1})
	over := make([]byte, 4)
	binary.LittleEndian.PutUint32(over, 0xffffffff)
	f.Add(over)
	short := make([]byte, 8)
	binary.LittleEndian.PutUint32(short, 100)
	f.Add(short)
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _, _ = wlSplitMessage(data)
		r := wlParser{p: data}
		_, _ = r.str()
		a := wlParser{p: data}
		_ = a.skipArray()
	})
}

func TestWaylandClipboardSourceSendStillWrites(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:   client,
		done:   make(chan struct{}),
		events: make(chan surfaceEvent, 1),
		nextID: 3,
	}
	t.Cleanup(func() { _ = s.Close() })
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
	if _, _, err := server.WriteMsgUnix(msg, unix.UnixRights(int(pw.Fd())), nil); err != nil {
		t.Fatalf("WriteMsgUnix: %v", err)
	}
	_ = pw.Close()

	if err := pr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	b, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("reading pipe: %v", err)
	}
	if got := string(b); got != "from-source" {
		t.Fatalf("pipe read %q, want from-source", got)
	}
}
