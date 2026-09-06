//go:build purego && linux

package purego

import (
	"encoding/binary"
	"testing"
)

// TestSetFullscreenSetsNetWMStateProperty checks that SetFullscreen(true)
// results in _NET_WM_STATE carrying _NET_WM_STATE_FULLSCREEN, read back
// with a raw GetProperty from a second connection — the test asks for the
// property directly rather than relying on a window manager to react to the
// ClientMessage, since the xvfb server this voie's tests run under has none
// (see the comment on changeNetWMState in x11_fullscreen.go for why
// SetFullscreen sets the property itself for exactly this reason). This
// test failed before x11_fullscreen.go existed (fullscreenSurface had no
// implementation to call).
func TestSetFullscreenSetsNetWMStateProperty(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })

	if err := s.SetFullscreen(true); err != nil {
		t.Fatalf("SetFullscreen(true): %v", err)
	}

	peer := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = peer.Close() })

	stateAtom, err := peer.internAtom("_NET_WM_STATE")
	if err != nil {
		t.Fatalf("internAtom(_NET_WM_STATE): %v", err)
	}
	fullscreenAtom, err := peer.internAtom("_NET_WM_STATE_FULLSCREEN")
	if err != nil {
		t.Fatalf("internAtom(_NET_WM_STATE_FULLSCREEN): %v", err)
	}

	atoms, err := readAtomListPropertyOfType(peer, s.winID, stateAtom, atomATOM)
	if err != nil {
		t.Fatalf("reading _NET_WM_STATE: %v", err)
	}
	found := false
	for _, a := range atoms {
		if a == fullscreenAtom {
			found = true
		}
	}
	if !found {
		t.Fatalf("_NET_WM_STATE = %v, want it to contain _NET_WM_STATE_FULLSCREEN (%d)", atoms, fullscreenAtom)
	}

	if err := s.SetFullscreen(false); err != nil {
		t.Fatalf("SetFullscreen(false): %v", err)
	}
	atoms, err = readAtomListPropertyOfType(peer, s.winID, stateAtom, atomATOM)
	if err != nil {
		t.Fatalf("reading _NET_WM_STATE after clearing: %v", err)
	}
	for _, a := range atoms {
		if a == fullscreenAtom {
			t.Fatalf("_NET_WM_STATE still contains _NET_WM_STATE_FULLSCREEN after SetFullscreen(false): %v", atoms)
		}
	}
}

// readAtomListPropertyOfType issues GetProperty(delete=false) for a
// format-32 property of the given type and decodes it to the list of
// uint32 values (atoms, or CARDINALs) it holds. Shared by
// x11_fullscreen_test.go (ATOM-typed _NET_WM_STATE) and x11_icon_test.go
// (CARDINAL-typed _NET_WM_ICON).
func readAtomListPropertyOfType(s *x11Surface, window, property, propertyType uint32) ([]uint32, error) {
	req := make([]byte, getPropertyReqLen)
	req[0] = opGetProperty
	req[1] = 0 // delete = false: a fullscreen toggle test still needs to see the property change, not vanish
	binary.LittleEndian.PutUint16(req[2:4], getPropertyReqWords)
	binary.LittleEndian.PutUint32(req[4:8], window)
	binary.LittleEndian.PutUint32(req[8:12], property)
	binary.LittleEndian.PutUint32(req[12:16], propertyType)
	binary.LittleEndian.PutUint32(req[16:20], 0)
	binary.LittleEndian.PutUint32(req[20:24], getPropertyMaxWords)

	reply, err := s.request(req)
	if err != nil {
		return nil, err
	}
	_, extra, err := parseGetPropertyReply(reply)
	if err != nil {
		return nil, err
	}
	atoms := make([]uint32, 0, len(extra)/4)
	for i := 0; (i+1)*4 <= len(extra); i++ {
		atoms = append(atoms, binary.LittleEndian.Uint32(extra[i*4:i*4+4]))
	}
	return atoms, nil
}
