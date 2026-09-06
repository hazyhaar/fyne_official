//go:build purego && linux

package purego

import "testing"

// TestScreensDoesNotOpenAuxiliaryConnection fails while Screens() dials a
// second X11 client to run RandR queries that request() can issue on the
// primary connection.
func TestScreensDoesNotOpenAuxiliaryConnection(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })

	before := countX11ClientSockets(t)
	if _, err := s.Screens(); err != nil {
		t.Fatalf("first Screens(): %v", err)
	}
	if _, err := s.Screens(); err != nil {
		t.Fatalf("second Screens(): %v", err)
	}
	after := countX11ClientSockets(t)
	if after != before {
		t.Fatalf("Screens() opened an X11 socket: before=%d after=%d", before, after)
	}
}

// TestPutImageBandReusesRequestBuffer guards against a regression once seen in this code:
// request buffer on every Present call. It forces the PutImage fallback
// path and requires the request buffer's backing array to stay the same
// across two same-size frames.
func TestPutImageBandReusesRequestBuffer(t *testing.T) {
	s := openX11ForTest(t, 40, 30)
	t.Cleanup(func() { _ = s.Close() })
	s.shm = &shmExtension{available: false}

	if err := s.putImageBand(make([]byte, 40*30*bytesPerPixel), 40, 30, 0, 0); err != nil {
		t.Fatalf("first putImageBand: %v", err)
	}
	if len(s.putImageBuf) == 0 {
		t.Fatal("putImageBand did not populate the reusable request buffer")
	}
	first := &s.putImageBuf[0]

	if err := s.putImageBand(make([]byte, 40*30*bytesPerPixel), 40, 30, 0, 0); err != nil {
		t.Fatalf("second putImageBand: %v", err)
	}
	if &s.putImageBuf[0] != first {
		t.Fatal("putImageBand reallocated its request buffer on a same-size band instead of reusing it")
	}
}
