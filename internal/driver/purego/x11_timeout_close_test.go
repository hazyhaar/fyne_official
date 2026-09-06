//go:build purego && linux

package purego

import (
	"encoding/binary"
	"io"
	"testing"
	"time"
)

func TestRequestTimesOutOnSilentServer(t *testing.T) {
	client, server := unixSocketPair(t)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	s := &x11Surface{
		conn:         client,
		events:       make(chan surfaceEvent, eventChanCapacity),
		done:         make(chan struct{}),
		pendingBySeq: make(map[uint16]chan []byte),
	}
	go s.readLoop()

	old := x11RequestTimeout
	x11RequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { x11RequestTimeout = old })

	req := make([]byte, 8)
	req[0] = opInternAtom
	binary.LittleEndian.PutUint16(req[2:4], 2)
	start := time.Now()
	_, err := s.request(req)
	if err == nil {
		t.Fatal("request to a silent server returned no error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s, want around %s", elapsed, x11RequestTimeout)
	}
}

func TestX11CloseIsIdempotent(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestWaylandCloseIsIdempotent(t *testing.T) {
	client, server := unixSocketPair(t)
	defer server.Close()
	s := &waylandSurface{
		conn:   client,
		done:   make(chan struct{}),
		events: make(chan surfaceEvent, 1),
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func readX11Request(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	words := binary.LittleEndian.Uint16(hdr[2:4])
	if words == 0 {
		words = 1
	}
	rest := make([]byte, int(words)*setupWordSize-4)
	if len(rest) > 0 {
		if _, err := io.ReadFull(r, rest); err != nil {
			return nil, err
		}
	}
	return append(hdr, rest...), nil
}

func TestInternAtomAfterPartialShmQuery(t *testing.T) {
	client, server := unixSocketPair(t)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	errCh := make(chan error, 1)
	go func() {
		if _, err := readX11Request(server); err != nil {
			errCh <- err
			return
		}
		qe := make([]byte, coreEventSize)
		qe[0] = replyStatusReply
		qe[8] = 1
		qe[9] = 140
		if _, err := server.Write(qe); err != nil {
			errCh <- err
			return
		}
		if _, err := readX11Request(server); err != nil {
			errCh <- err
			return
		}
		verErr := make([]byte, coreEventSize)
		verErr[0] = replyStatusError
		if _, err := server.Write(verErr); err != nil {
			errCh <- err
			return
		}
		if _, err := readX11Request(server); err != nil {
			errCh <- err
			return
		}
		reply := make([]byte, coreEventSize)
		reply[0] = replyStatusReply
		binary.LittleEndian.PutUint16(reply[2:4], 3)
		binary.LittleEndian.PutUint32(reply[8:12], 42)
		_, err := server.Write(reply)
		errCh <- err
	}()

	shm, sent, qerr := queryShmExtension(client)
	if sent != 2 {
		t.Fatalf("sent = %d, want 2 after QueryExtension + ShmQueryVersion", sent)
	}
	if qerr == nil {
		t.Fatal("ShmQueryVersion error status must be returned as an error")
	}
	s := &x11Surface{
		conn:         client,
		shm:          shm,
		events:       make(chan surfaceEvent, eventChanCapacity),
		done:         make(chan struct{}),
		pendingBySeq: make(map[uint16]chan []byte),
	}
	if sent16, err := card16FromInt(sent); err == nil {
		s.seq += sent16
	}
	go s.readLoop()

	atom, err := s.internAtom("PUREGO_SHM_SEQ")
	if err != nil {
		t.Fatalf("InternAtom after partial SHM query: %v", err)
	}
	if atom != 42 {
		t.Fatalf("atom = %d, want 42", atom)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake server did not finish")
	}
}
