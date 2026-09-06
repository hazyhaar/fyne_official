//go:build purego && linux

package purego

import (
	"encoding/binary"
	"testing"
	"time"
)

// TestInvalidConfigureWindowDoesNotStealInternAtomReply is the xvfb
// regression test for routing replies by sequence number: a fire-and-forget
// ConfigureWindow against a window id the server does not know produces an
// error with no waiter, and the InternAtom sent immediately afterwards must
// still receive its own reply. A FIFO deliverPending handed that error to
// InternAtom instead.
func TestInvalidConfigureWindowDoesNotStealInternAtomReply(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })

	const configureReqLen, configureReqWords = 20, 5
	configure := make([]byte, configureReqLen)
	configure[0] = opConfigureWindow
	binary.LittleEndian.PutUint16(configure[2:4], configureReqWords)
	binary.LittleEndian.PutUint32(configure[4:8], 0xffffffff)
	binary.LittleEndian.PutUint32(configure[8:12], configureValueWidth|configureValueHeight)
	binary.LittleEndian.PutUint32(configure[12:16], 32)
	binary.LittleEndian.PutUint32(configure[16:20], 32)

	const atomName = "PUREGO_SEQ_ROUTE_ATOM"
	nameLen := len(atomName)
	pad := (setupWordSize - nameLen%setupWordSize) % setupWordSize
	const internHeaderLen = 8
	reqWords := (internHeaderLen + nameLen + pad) / setupWordSize
	intern := make([]byte, reqWords*setupWordSize)
	intern[0] = opInternAtom
	binary.LittleEndian.PutUint16(intern[2:4], uint16(reqWords))
	binary.LittleEndian.PutUint16(intern[4:6], uint16(nameLen))
	copy(intern[8:], atomName)

	// Both requests are written in one critical section so readLoop cannot
	// deliver the ConfigureWindow error before the InternAtom waiter is
	// registered: that is the misrouting window, not the case where the
	// error arrives with nobody waiting and even a FIFO would drop it.
	ch := make(chan []byte, 1)
	s.mu.Lock()
	s.nextSeq()
	if _, err := s.conn.Write(configure); err != nil {
		s.mu.Unlock()
		t.Fatalf("ConfigureWindow write: %v", err)
	}
	seq := s.nextSeq()
	s.pendingBySeq[seq] = ch
	if _, err := s.conn.Write(intern); err != nil {
		delete(s.pendingBySeq, seq)
		s.mu.Unlock()
		t.Fatalf("InternAtom write: %v", err)
	}
	s.mu.Unlock()

	var reply []byte
	select {
	case reply = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("InternAtom waiter never received a reply")
	}
	if reply == nil {
		t.Fatal("InternAtom waiter unblocked with a nil payload (connection closed)")
	}
	if reply[0] != replyStatusReply {
		code, major := byte(0), byte(0)
		if len(reply) > 10 {
			code, major = reply[1], reply[10]
		}
		t.Fatalf("InternAtom received status %d (error code %d major opcode %d) instead of a reply: fire-and-forget ConfigureWindow error was misrouted", reply[0], code, major)
	}
	if binary.LittleEndian.Uint32(reply[8:12]) == 0 {
		t.Fatal("InternAtom reply carried atom 0")
	}
}
