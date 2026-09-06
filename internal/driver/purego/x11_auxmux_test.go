//go:build purego && linux

package purego

import (
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func countX11ClientSockets(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("ReadDir /proc/self/fd: %v", err)
	}
	n := 0
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		sa, err := unix.Getpeername(fd)
		if err != nil {
			continue
		}
		un, ok := sa.(*unix.SockaddrUnix)
		if !ok {
			continue
		}
		if strings.Contains(un.Name, ".X11-unix") {
			n++
		}
	}
	return n
}

func TestSubscribeX11EventDeliversPropertyNotify(t *testing.T) {
	s := openX11ForTest(t, 16, 16)
	t.Cleanup(func() { _ = s.Close() })

	id, ch := s.subscribeX11Event(eventCodePropertyNotify, s.winID)
	t.Cleanup(func() { s.unsubscribeX11Event(id) })

	prop, err := s.internAtom("PUREGO_X11_SUB_TEST")
	if err != nil {
		t.Fatalf("internAtom: %v", err)
	}
	value := []byte("n")
	reqWords, err := x11ChangePropertyWords(len(value))
	if err != nil {
		t.Fatalf("x11ChangePropertyWords: %v", err)
	}
	words, err := card16FromInt(reqWords)
	if err != nil {
		t.Fatalf("card16FromInt: %v", err)
	}
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opChangeProperty
	req[1] = propModeReplace
	binary.LittleEndian.PutUint16(req[2:4], words)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], prop)
	binary.LittleEndian.PutUint32(req[12:16], atomSTRING)
	req[changePropertyFormatOffset] = propFormat8
	nUnits, err := uint32FromInt(len(value))
	if err != nil {
		t.Fatalf("uint32FromInt: %v", err)
	}
	binary.LittleEndian.PutUint32(req[20:24], nUnits)
	copy(req[24:], value)
	if err := s.writeRequest(req); err != nil {
		t.Fatalf("ChangeProperty: %v", err)
	}

	buf, err := waitX11Event(ch, s.done, 2*time.Second)
	if err != nil {
		t.Fatalf("waiting for PropertyNotify: %v", err)
	}
	if buf[0]&eventCodeMask != eventCodePropertyNotify {
		t.Fatalf("event code = %d, want PropertyNotify %d", buf[0]&eventCodeMask, eventCodePropertyNotify)
	}
	gotWin := binary.LittleEndian.Uint32(buf[4:8])
	if gotWin != s.winID {
		t.Fatalf("PropertyNotify window = %d, want %d", gotWin, s.winID)
	}
}

func TestClipboardRoundTripTwoSurfacesNoAuxSocket(t *testing.T) {
	a := openX11ForTest(t, 32, 32)
	b := openX11ForTest(t, 32, 32)
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
		waitClipboardOwnerGone(t, a)
		waitClipboardOwnerGone(t, b)
	})

	before := countX11ClientSockets(t)
	const want = "purego two-surface clipboard"
	if err := a.SetClipboardText(want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	got, err := b.ClipboardText()
	if err != nil {
		t.Fatalf("ClipboardText: %v", err)
	}
	if got != want {
		t.Fatalf("ClipboardText() = %q, want %q", got, want)
	}
	after := countX11ClientSockets(t)
	if after > before {
		t.Fatalf("X11 client sockets before clipboard = %d, after = %d (auxiliary connection opened)", before, after)
	}
}

func TestClipboardSecondClientNoAuxSocketOnOwner(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() {
		_ = s.Close()
		waitClipboardOwnerGone(t, s)
	})

	before := countX11ClientSockets(t)
	const want = "purego second-client no aux"
	if err := s.SetClipboardText(want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	afterSet := countX11ClientSockets(t)
	if afterSet > before {
		t.Fatalf("SetClipboardText opened an X11 socket: before=%d after=%d", before, afterSet)
	}

	got, err := s.ClipboardText()
	if err != nil {
		t.Fatalf("ClipboardText: %v", err)
	}
	if got != want {
		t.Fatalf("ClipboardText() = %q, want %q", got, want)
	}
	afterRead := countX11ClientSockets(t)
	if afterRead > before {
		t.Fatalf("ClipboardText opened an X11 socket: before=%d after=%d", before, afterRead)
	}
}

func TestRandRSubscribeAndForgedScreenChangeNoAuxSocket(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })

	before := countX11ClientSockets(t)
	subscribeRandRScreenChange(s)
	afterSub := countX11ClientSockets(t)
	if afterSub != before {
		t.Fatalf("subscribeRandRScreenChange opened an X11 socket: before=%d after=%d", before, afterSub)
	}

	s.screensMu.Lock()
	code := s.screensRandRFirstEvent
	s.screensMu.Unlock()
	if code == 0 {
		t.Fatal("RandR first-event code is 0 after subscribeRandRScreenChange")
	}

	pkt := randrScreenChangePacket(3840, 2160, 300, 169)
	pkt[0] = code
	binary.LittleEndian.PutUint32(pkt[8:12], s.rootWindow)
	binary.LittleEndian.PutUint32(pkt[12:16], s.rootWindow)
	s.dispatchX11Event(code, pkt)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-s.Events():
			sc, ok := ev.(scaleEvent)
			if !ok {
				continue
			}
			if sc.Width != 3840 || sc.Height != 2160 {
				t.Fatalf("scaleEvent = %+v, want 3840x2160", sc)
			}
			after := countX11ClientSockets(t)
			if after != before {
				t.Fatalf("forged ScreenChangeNotify opened an X11 socket: before=%d after=%d", before, after)
			}
			return
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatal("no scaleEvent after forged RandR ScreenChangeNotify")
}
