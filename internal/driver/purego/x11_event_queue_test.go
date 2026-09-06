//go:build purego

package purego

import (
	"testing"
	"time"
)

// TestRequestCompletesWhileEventChannelIsFull reproduces the deadlock seen
// on the D4 (resize every frame) stratum: the window loop, sole consumer of
// Events(), issues a reply-bearing request (here InternAtom, as the SHM
// re-attach after a resize does) while more events than eventChanCapacity
// are pending. With readLoop blocking on the channel the reply could never
// be read; the queue between readLoop and the channel keeps the reader
// free, so the request must return.
func TestRequestCompletesWhileEventChannelIsFull(t *testing.T) {
	s := openX11ForTest(t, 200, 100)
	t.Cleanup(func() { _ = s.Close() })

	// Nobody drains Events(): every ConfigureNotify below stays pending.
	for i := 0; i < eventChanCapacity*2; i++ {
		w := 200 + i%2
		if err := s.Resize(w, 100); err != nil {
			t.Fatalf("Resize: %v", err)
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.internAtom("PUREGO_EVENT_QUEUE_TEST")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InternAtom: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("InternAtom did not return while events were pending: readLoop blocked on the event channel")
	}
	// The events are still all there, in order, once someone drains.
	n := 0
	deadline := time.After(5 * time.Second)
	for n < eventChanCapacity+1 {
		select {
		case <-s.Events():
			n++
		case <-deadline:
			t.Fatalf("drained only %d events, want more than %d", n, eventChanCapacity)
		}
	}
}
