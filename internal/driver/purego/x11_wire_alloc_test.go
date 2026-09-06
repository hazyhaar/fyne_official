//go:build purego && linux

package purego

import (
	"encoding/binary"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// TestReadLoopRejectsHostileReplyLengthOnUnixSocket is the regression test
// for a 32-bit extra-length field that would make() several gigabytes: a
// fake X server on a Unix socket announces ~4 GiB of extra reply data
// (0x3fffffff words). readLoop must close the connection with closeEvent in
// under a second, without attempting the allocation.
func TestReadLoopRejectsHostileReplyLengthOnUnixSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "X-hostile")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, accErr := ln.Accept()
		if accErr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})

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
	go func() { _, werr := server.Write(forged); writeErr <- werr }()

	start := time.Now()
	select {
	case ev := <-s.events:
		if _, ok := ev.(closeEvent); !ok {
			t.Fatalf("got event %T, want closeEvent after a hostile reply length", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("readLoop did not close the connection within 1s after a 4GiB extra-length reply")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("closeEvent took %s, want under 1s", elapsed)
	}
	<-writeErr
}
