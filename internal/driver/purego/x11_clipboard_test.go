//go:build purego && linux

package purego

import (
	"testing"
	"time"
)

// TestClipboardRoundTripSameProcess exercises SetClipboardText/ClipboardText
// against a real xvfb server: it makes s the CLIPBOARD owner with a known
// string and reads it back through the same interface, the way two windows
// of the same application would share the clipboard. This test failed
// before x11_clipboard.go existed (clipboardSurface had no implementation
// to call).
func TestClipboardRoundTripSameProcess(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() {
		_ = s.Close()
		waitClipboardOwnerGone(t, s)
	})

	const want = "purego clipboard round trip"
	if err := s.SetClipboardText(want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}

	got, err := s.ClipboardText()
	if err != nil {
		t.Fatalf("ClipboardText: %v", err)
	}
	if got != want {
		t.Fatalf("ClipboardText() = %q, want %q", got, want)
	}
}

// TestClipboardRoundTripSecondClient plays the part of a second X11 client:
// it opens its own unmapped x11Surface on the same display and reads the
// CLIPBOARD selection through ClipboardText, the same way any real
// application (a terminal emulator pasting, for instance) would. It failed
// before x11_clipboard.go's SelectionRequest responder existed to answer it.
func TestClipboardRoundTripSecondClient(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() {
		_ = s.Close()
		waitClipboardOwnerGone(t, s)
	})

	const want = "purego clipboard second client"
	if err := s.SetClipboardText(want); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}

	client := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = client.Close() })

	got, err := client.ClipboardText()
	if err != nil {
		t.Fatalf("second client ClipboardText: %v", err)
	}
	if got != want {
		t.Fatalf("second client read %q, want %q", got, want)
	}
}

// TestClipboardTextNoOwnerReturnsEmpty checks the ICCCM "nobody owns this
// selection" case: a freshly created surface that never called
// SetClipboardText must not error, and must report an empty string, since
// no CLIPBOARD owner exists to answer ConvertSelection. Run in isolation
// (a fresh Xvfb has no other clipboard owner); if another test in this
// package races to own CLIPBOARD first, that also passing is a stronger
// tolerated outcome, but an error return is not.
func TestClipboardTextNoOwnerReturnsEmpty(t *testing.T) {
	s := openX11ForTest(t, 16, 16)
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.ClipboardText(); err != nil {
		t.Fatalf("ClipboardText with no owner returned an error instead of empty: %v", err)
	}
}

func waitClipboardOwnerGone(t *testing.T, s *x11Surface) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.state.mu.Lock()
		present := s.state.clip != nil
		s.state.mu.Unlock()
		if !present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("clipboard owner for surface did not tear down in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSetClipboardTextAcknowledgedByServer(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() {
		_ = s.Close()
		waitClipboardOwnerGone(t, s)
	})
	if err := s.SetClipboardText("ack"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	s.state.mu.Lock()
	owner := s.state.clip
	s.state.mu.Unlock()
	if owner == nil {
		t.Fatal("clipboard owner missing after SetClipboardText")
	}
	peer := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = peer.Close() })
	clipboardAtom, err := peer.internAtom("CLIPBOARD")
	if err != nil {
		t.Fatalf("internAtom(CLIPBOARD): %v", err)
	}
	got, err := peer.getSelectionOwner(clipboardAtom)
	if err != nil {
		t.Fatalf("GetSelectionOwner: %v", err)
	}
	if got != owner.winID {
		t.Fatalf("GetSelectionOwner = %d, want owner window %d", got, owner.winID)
	}
}

func TestClipboardSecondClientTimesOutAgainstMuteOwner(t *testing.T) {
	old := clipboardSelectionNotifyTimeout
	clipboardSelectionNotifyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { clipboardSelectionNotifyTimeout = old })

	_ = muteClipboardOwner(t)

	client := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = client.Close() })
	_, err := client.ClipboardText()
	if err == nil {
		t.Fatal("ClipboardText returned no error against a mute CLIPBOARD owner")
	}
}

func TestClipboardOwnerShutdownStopsRespondLoop(t *testing.T) {
	s := openX11ForTest(t, 16, 16)
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SetClipboardText("stop"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	s.state.mu.Lock()
	owner := s.state.clip
	s.state.mu.Unlock()
	if owner == nil {
		t.Fatal("clipboard owner missing")
	}
	owner.shutdown()
	waitClipboardOwnerGone(t, s)
}
