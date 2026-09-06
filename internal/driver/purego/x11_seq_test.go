//go:build purego && linux

package purego

import (
	"encoding/binary"
	"testing"
	"time"
)

// TestDeliverPendingRoutesBySequenceNotFIFO is the regression test for
// a pending-queue desync: an error produced by a
// fire-and-forget request must not be handed to whichever request() call
// happens to be at the head of a plain FIFO. It drives deliverPending
// directly, the same entry point readLoop calls, with a synthetic error for
// a sequence number nobody is waiting on interleaved between two real
// pending waiters, and requires each waiter to receive its own reply, not
// its neighbour's error. Before the sequence-keyed pendingBySeq map (see
// x11.go), a FIFO deliverPending handed the unrelated error to the older of
// the two waiters and, once their real replies eventually arrived, had
// nothing left to route the younger waiter's own reply to.
func TestDeliverPendingRoutesBySequenceNotFIFO(t *testing.T) {
	s, err := openX11("", 32, 32)
	if err != nil {
		t.Fatalf("openX11: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Register two waiters exactly as request() would, without going
	// through request() itself so the test controls exactly which sequence
	// numbers are in play.
	s.mu.Lock()
	seqOld := s.nextSeq()
	chOld := make(chan []byte, 1)
	s.pendingBySeq[seqOld] = chOld
	seqNew := s.nextSeq()
	chNew := make(chan []byte, 1)
	s.pendingBySeq[seqNew] = chNew
	s.mu.Unlock()

	// A synthetic X11 error (32 bytes, status 0) for a sequence number
	// belonging to neither waiter — the fire-and-forget request the
	// scenario describes. It must be dropped, not delivered to seqOld just
	// because seqOld was registered first.
	strayErr := make([]byte, coreEventSize)
	strayErr[0] = replyStatusError
	binary.LittleEndian.PutUint16(strayErr[2:4], seqOld-1)
	s.deliverPending(seqOld-1, strayErr)

	select {
	case <-chOld:
		t.Fatal("stray error for an unrelated sequence number was delivered to the older waiter (FIFO misrouting)")
	case <-time.After(20 * time.Millisecond):
	}

	// Now the real replies arrive, out of registration order (younger
	// first): each must reach its own waiter.
	replyNew := make([]byte, coreEventSize)
	replyNew[0] = replyStatusReply
	binary.LittleEndian.PutUint16(replyNew[2:4], seqNew)
	replyNew[8] = 0xAA
	s.deliverPending(seqNew, replyNew)

	replyOld := make([]byte, coreEventSize)
	replyOld[0] = replyStatusReply
	binary.LittleEndian.PutUint16(replyOld[2:4], seqOld)
	replyOld[8] = 0xBB
	s.deliverPending(seqOld, replyOld)

	select {
	case got := <-chNew:
		if got[8] != 0xAA {
			t.Fatalf("younger waiter got wrong payload byte %#x, want 0xAA (misrouted)", got[8])
		}
	case <-time.After(time.Second):
		t.Fatal("younger waiter never received its own reply")
	}
	select {
	case got := <-chOld:
		if got[8] != 0xBB {
			t.Fatalf("older waiter got wrong payload byte %#x, want 0xBB (misrouted)", got[8])
		}
	case <-time.After(time.Second):
		t.Fatal("older waiter never received its own reply")
	}
}
