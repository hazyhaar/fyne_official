//go:build purego && linux

package purego

import (
	"encoding/binary"
	"testing"
	"time"
)

// TestClipboardTextTimesOutWithoutOwnerResponse is the regression test for
// when a CLIPBOARD owner that never answers
// ConvertSelection with SelectionNotify used to block ClipboardText forever,
// since the ICCCM sets no server-enforced timeout for that exchange. It
// makes an unmapped window the CLIPBOARD owner and never starts a
// SelectionRequest responder — exactly a stopped or wedged client — then
// calls ClipboardText and requires it to return an error within a bounded
// time instead of hanging.
func TestClipboardTextTimesOutWithoutOwnerResponse(t *testing.T) {
	old := clipboardSelectionNotifyTimeout
	clipboardSelectionNotifyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { clipboardSelectionNotifyTimeout = old })

	owner := muteClipboardOwner(t)

	s := openX11ForTest(t, 16, 16)
	t.Cleanup(func() { _ = s.Close() })
	done := make(chan struct{})
	var text string
	var callErr error
	go func() {
		text, callErr = s.ClipboardText()
		close(done)
	}()

	select {
	case <-done:
		if callErr == nil {
			t.Fatalf("ClipboardText returned no error against a non-responding owner (got %q); want a timeout error", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ClipboardText did not return within 3s against a non-responding CLIPBOARD owner (an earlier defect regression)")
	}

	_ = owner
}

func muteClipboardOwner(t *testing.T) *x11Surface {
	t.Helper()
	s := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = s.Close() })
	clipboardAtom, err := s.internAtom(clipboardAtomName)
	if err != nil {
		t.Fatalf("internAtom(CLIPBOARD): %v", err)
	}
	req := make([]byte, setSelectionOwnerReqLen)
	req[0] = opSetSelectionOwner
	binary.LittleEndian.PutUint16(req[2:4], setSelectionOwnerReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], clipboardAtom)
	binary.LittleEndian.PutUint32(req[12:16], 0)
	if err := s.writeRequest(req); err != nil {
		t.Fatalf("SetSelectionOwner: %v", err)
	}
	if err := waitSelectionOwnerOn(s, s.winID, clipboardAtom); err != nil {
		t.Fatalf("waitSelectionOwnerOn: %v", err)
	}
	return s
}
