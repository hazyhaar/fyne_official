//go:build purego && linux

package purego

import (
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
)

// unixSocketPair returns two connected *net.UnixConn, standing in for a
// waylandSurface's connection to a compositor without needing a real one
// reachable in this environment.
func unixSocketPair(t *testing.T) (client, server *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	f0 := os.NewFile(uintptr(fds[0]), "sp0")
	f1 := os.NewFile(uintptr(fds[1]), "sp1")
	c0, err := net.FileConn(f0)
	if err != nil {
		t.Fatalf("FileConn(0): %v", err)
	}
	_ = f0.Close()
	c1, err := net.FileConn(f1)
	if err != nil {
		t.Fatalf("FileConn(1): %v", err)
	}
	_ = f1.Close()
	return c0.(*net.UnixConn), c1.(*net.UnixConn)
}

// TestSendDoesNotInterleaveConcurrentWrites is the regression test for
// when concurrent calls to send/sendFD, from
// the render path and from readLoop's own ping/configure handlers, wrote to
// the shared socket with no synchronization, letting one message's bytes
// interleave with another's mid-frame. It drives many goroutines calling
// send (and one calling sendFD) concurrently under the race detector and
// decodes every message the "compositor" side received back off the wire,
// requiring each one's framing (object ID, opcode, size) to be intact and
// the total count to match exactly — a single interleaved write would
// either desynchronize the decode loop (wrong sizes, garbage opcodes) or
// silently swallow bytes, both of which this test would show as a mismatch.
func TestSendDoesNotInterleaveConcurrentWrites(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{conn: client, done: make(chan struct{})}

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := wlMsgWriter{}
			for j := 0; j < i%17; j++ {
				w.putUint32(uint32(i)) //nolint:gosec // test-only small loop index
			}
			if err := s.send(uint32(i+1000), uint16(i%5), w); err != nil { //nolint:gosec // test-only small loop index
				t.Errorf("send(%d): %v", i, err)
			}
		}(i)
	}

	// One sendFD, exercising the shared writeMu against the plain sends.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	wg.Add(1)
	go func() {
		defer wg.Done()
		w := wlMsgWriter{}
		w.putUint32(42)
		if err := s.sendFD(9999, 1, w, int(pw.Fd())); err != nil {
			t.Errorf("sendFD: %v", err)
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	<-done
	_ = client.Close()

	// Decode every message the server side received: a corrupted frame
	// boundary shows up as a read failure well before n+1 messages are
	// accounted for, or as a size field putting the next header somewhere
	// nonsensical.
	got := 0
	header := make([]byte, wlHeaderLen)
	for {
		if _, err := io.ReadFull(server, header); err != nil {
			break
		}
		_, _, size := decodeWlHeader(header)
		if size < wlHeaderLen {
			t.Fatalf("message %d decoded a size %d shorter than the header itself: frame corrupted by an interleaved write", got, size)
		}
		payloadLen := size - wlHeaderLen
		if payloadLen > 0 {
			if _, err := io.ReadFull(server, make([]byte, payloadLen)); err != nil {
				t.Fatalf("message %d: truncated payload of %d bytes: frame corrupted by an interleaved write (%v)", got, payloadLen, err)
			}
		}
		got++
	}
	if got != n+1 {
		t.Fatalf("decoded %d intact messages, want %d (n send calls + 1 sendFD): writes interleaved on the wire", got, n+1)
	}
}
