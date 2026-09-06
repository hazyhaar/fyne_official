//go:build purego && linux

package purego

import "encoding/binary"

// EWMH _NET_WM_STATE ClientMessage plumbing (opcode 33 sent through
// SendEvent, opcode 25) used to ask the window manager to toggle fullscreen,
// per the Extended Window Manager Hints specification.
const (
	opSendEvent = 25

	eventCodeClientMessageByte = 33

	netWMStateActionRemove = 0
	netWMStateActionAdd    = 1

	sendEventHeaderLen = 12
	sendEventReqLen    = sendEventHeaderLen + coreEventSize
	sendEventReqWords  = sendEventReqLen / setupWordSize

	// SubstructureRedirect|SubstructureNotify: the mask EWMH requires this
	// message to be sent with, so the window manager (not the root window
	// itself) receives it.
	eventMaskSubstructureNotify   = 0x00080000
	eventMaskSubstructureRedirect = 0x00100000
)

var _ fullscreenSurface = (*x11Surface)(nil)

// SetFullscreen implements fullscreenSurface (optional.go, core-owned). It
// does two things, both EWMH-compliant: it ChangeProperty's _NET_WM_STATE on
// the window directly (the form EWMH-aware clients use to declare their
// *initial* state, or any state on a window with no reparenting window
// manager watching it — the case a bare Xvfb test server run without a WM
// is in), and it also sends the _NET_WM_STATE ClientMessage a window manager
// is expected to honour for a window that is already mapped. Atoms are
// resolved through request() on this surface's connection.
func (s *x11Surface) SetFullscreen(full bool) error {
	stateAtom, err := s.internAtom("_NET_WM_STATE")
	if err != nil {
		return err
	}
	fullscreenAtom, err := s.internAtom("_NET_WM_STATE_FULLSCREEN")
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.changeNetWMState(stateAtom, fullscreenAtom, full); err != nil {
		return err
	}
	return s.sendNetWMStateClientMessage(stateAtom, fullscreenAtom, full)
}

// changeNetWMState replaces _NET_WM_STATE outright with either an empty
// list (full == false: this backend only ever manages the fullscreen state
// itself, so "not fullscreen" is "no states") or a single-atom list naming
// fullscreenAtom.
func (s *x11Surface) changeNetWMState(stateAtom, fullscreenAtom uint32, full bool) error {
	const propHeaderWords = 6
	var data []uint32
	if full {
		data = []uint32{fullscreenAtom}
	}
	reqWords := propHeaderWords + len(data)
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opChangeProperty
	req[1] = propModeReplace
	reqWords16, err := card16FromInt(reqWords)
	if err != nil {
		return err
	}
	nData, err := uint32FromInt(len(data))
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(req[2:4], reqWords16)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], stateAtom)
	binary.LittleEndian.PutUint32(req[12:16], atomATOM)
	req[changePropertyFormatOffset] = propFormat32
	binary.LittleEndian.PutUint32(req[20:24], nData)
	for i, v := range data {
		binary.LittleEndian.PutUint32(req[24+i*4:28+i*4], v)
	}
	s.nextSeq() // every request advances the sequence the server will stamp on its replies
	_, err = s.conn.Write(req)
	return err
}

// sendNetWMStateClientMessage sends the ClientMessage EWMH-aware window
// managers act on for a window that is already mapped; a bare Xvfb test
// server with no window manager simply has nothing pick it up, which is why
// changeNetWMState above also sets the property directly.
func (s *x11Surface) sendNetWMStateClientMessage(stateAtom, fullscreenAtom uint32, full bool) error {
	action := uint32(netWMStateActionRemove)
	if full {
		action = netWMStateActionAdd
	}

	event := make([]byte, coreEventSize)
	event[0] = eventCodeClientMessageByte
	event[1] = propFormat32
	binary.LittleEndian.PutUint32(event[4:8], s.winID)
	binary.LittleEndian.PutUint32(event[8:12], stateAtom)
	binary.LittleEndian.PutUint32(event[12:16], action)
	binary.LittleEndian.PutUint32(event[16:20], fullscreenAtom)
	// data.l[2..4] stay zero: no second property, source indication 0.

	req := make([]byte, sendEventReqLen)
	req[0] = opSendEvent
	req[1] = 0 // propagate = false
	binary.LittleEndian.PutUint16(req[2:4], uint16(sendEventReqWords))
	binary.LittleEndian.PutUint32(req[4:8], s.rootWindow)
	binary.LittleEndian.PutUint32(req[8:12], eventMaskSubstructureNotify|eventMaskSubstructureRedirect)
	copy(req[sendEventHeaderLen:], event)

	s.nextSeq() // every request advances the sequence the server will stamp on its replies
	_, err := s.conn.Write(req)
	return err
}
