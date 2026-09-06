//go:build purego && linux

package purego

// Raw XTEST extension client, written for the harness tests only (this file
// is not imported by any non-test file). No non-test file in this package sends synthetic
// input, so event-injection tests need their own minimal XTEST client: the
// QueryExtension + XTestGetVersion + XTestFakeInput sequence below, encoded
// directly against the XTEST extension wire protocol (X.Org xtestproto.txt,
// requests 0 GetVersion and 2 FakeInput), following the same
// QueryExtension-then-extension-opcode pattern x11.go/shm.go already use for
// MIT-SHM.

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const (
	xtestMinorGetVersion = 0
	xtestMinorFakeInput  = 2

	xtestGetVersionReqLen   = 8
	xtestGetVersionReqWords = 2

	xtestFakeInputReqLen   = 36
	xtestFakeInputReqWords = 9

	// Event types accepted by XTestFakeInput, matching the core protocol
	// event codes x11.go already defines for the events it receives.
	xtestKeyPress      = eventCodeKeyPress
	xtestKeyRelease    = eventCodeKeyRelease
	xtestButtonPress   = eventCodeButtonPress
	xtestButtonRelease = eventCodeButtonRelease
	xtestMotionNotify  = eventCodeMotionNotify
)

type xtestExtension struct {
	available   bool
	majorOpcode byte
}

// queryXTestExtension performs QueryExtension("XTEST") followed by
// XTestGetVersion, which most X servers (including Xvfb) require a client to
// call once before FakeInput requests are honoured.
func queryXTestExtension(conn net.Conn) (*xtestExtension, error) {
	name := "XTEST"
	pad := (setupWordSize - len(name)%setupWordSize) % setupWordSize
	reqWords := (extensionNameHeaderLen + len(name) + pad) / setupWordSize
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opQueryExtension
	binary.LittleEndian.PutUint16(req[2:4], uint16(reqWords))  //nolint:gosec // bounded by the fixed "XTEST" name
	binary.LittleEndian.PutUint16(req[4:6], uint16(len(name))) //nolint:gosec // bounded by the fixed "XTEST" name
	copy(req[8:], name)

	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("purego/x11: QueryExtension(XTEST): %w", err)
	}
	var reply [coreEventSize]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return nil, fmt.Errorf("purego/x11: QueryExtension(XTEST) reply: %w", err)
	}
	const extensionPresentOffset, extensionMajorOpcodeOffset = 8, 9
	if reply[0] != replyStatusReply || reply[extensionPresentOffset] == 0 {
		return &xtestExtension{available: false}, nil
	}
	majorOpcode := reply[extensionMajorOpcodeOffset]

	verReq := make([]byte, xtestGetVersionReqLen)
	verReq[0] = majorOpcode
	verReq[1] = xtestMinorGetVersion
	binary.LittleEndian.PutUint16(verReq[2:4], xtestGetVersionReqWords)
	verReq[4] = 2                                 // client-claimed major version
	binary.LittleEndian.PutUint16(verReq[6:8], 2) // client-claimed minor version
	if _, err := conn.Write(verReq); err != nil {
		return nil, fmt.Errorf("purego/x11: XTestGetVersion: %w", err)
	}
	var verReply [coreEventSize]byte
	if _, err := io.ReadFull(conn, verReply[:]); err != nil {
		return nil, fmt.Errorf("purego/x11: XTestGetVersion reply: %w", err)
	}
	if verReply[0] != replyStatusReply {
		return &xtestExtension{available: false}, nil
	}
	return &xtestExtension{available: true, majorOpcode: majorOpcode}, nil
}

// fakeInput issues XTestFakeInput (minor opcode 2) to synthesize one input
// event as though it came from a real device. rootX/rootY are absolute
// coordinates on the given root window (0 for KeyPress/KeyRelease, which
// ignore them).
func (e *xtestExtension) fakeInput(conn net.Conn, eventType, detail byte, root uint32, rootX, rootY int16) error {
	req := make([]byte, xtestFakeInputReqLen)
	req[0] = e.majorOpcode
	req[1] = xtestMinorFakeInput
	binary.LittleEndian.PutUint16(req[2:4], xtestFakeInputReqWords)
	req[4] = eventType
	req[5] = detail
	// req[6:8] pad0
	// req[8:12] time (delay) = 0: deliver immediately
	binary.LittleEndian.PutUint32(req[12:16], root)
	// req[16:20] pad1, req[20:24] pad2 (per xXTestFakeInputReq in
	// xtestproto.h: reqType,xtReqType,length,type,detail,pad0,time,root,
	// pad1,pad2,rootX,rootY,pad3,pad4,pad5,deviceid), rootX/rootY sit at
	// 24:26/26:28, not immediately after root.
	binary.LittleEndian.PutUint16(req[24:26], uint16(rootX)) //nolint:gosec // screen coordinates fit INT16 by protocol definition
	binary.LittleEndian.PutUint16(req[26:28], uint16(rootY)) //nolint:gosec // screen coordinates fit INT16 by protocol definition
	// req[28:36] pad3/pad4/pad5/deviceid
	_, err := conn.Write(req)
	return err
}
