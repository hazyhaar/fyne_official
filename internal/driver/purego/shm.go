//go:build purego && linux

package purego

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const (
	shmMinorAttach   = 1
	shmMinorDetach   = 2
	shmMinorPutImage = 3

	shmQueryExtensionReqWords = 1
	shmAttachReqLen           = 16
	shmAttachReqWords         = 4
	shmDetachReqLen           = 8
	shmDetachReqWords         = 2
	shmPutImageReqLen         = 40
	shmPutImageReqWords       = 10

	extensionNameHeaderLen = 8

	sendEventFalse = 0

	shmPutImageDepthOffset     = 28
	shmPutImageFormatOffset    = 29
	shmPutImageSendEventOffset = 30
)

// shmExtension records the MIT-SHM extension's negotiated opcode, once
// QueryExtension has located it, so ShmAttach/ShmPutImage/ShmDetach can be
// issued as extension requests.
type shmExtension struct {
	available   bool
	majorOpcode byte
}

// queryShmExtension asks the server whether MIT-SHM is present, following
// the QueryExtension(name) + ShmQueryVersion(1 word) sequence. It runs
// before this surface's readLoop starts (see openX11), reading its own
// replies directly off conn, so it is the one place outside x11Surface
// allowed to do that; it returns sent, the number of requests it wrote, so
// the caller can advance its own sequence counter by the same amount and
// keep it in step with the server's (see x11Surface.seq).
func queryShmExtension(conn net.Conn) (ext *shmExtension, sent int, err error) {
	name := "MIT-SHM"
	pad := (setupWordSize - len(name)%setupWordSize) % setupWordSize
	reqWords := (extensionNameHeaderLen + len(name) + pad) / setupWordSize
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opQueryExtension
	words, err := card16FromInt(reqWords)
	if err != nil {
		return nil, 0, err
	}
	nameLen, err := card16FromInt(len(name))
	if err != nil {
		return nil, 0, err
	}
	binary.LittleEndian.PutUint16(req[2:4], words)
	binary.LittleEndian.PutUint16(req[4:6], nameLen)
	copy(req[8:], name)

	if _, err := conn.Write(req); err != nil {
		return nil, 0, fmt.Errorf("purego/x11: QueryExtension(MIT-SHM): %w", err)
	}
	sent = 1
	var reply [coreEventSize]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return nil, sent, fmt.Errorf("purego/x11: QueryExtension(MIT-SHM) reply: %w", err)
	}
	const extensionPresentOffset, extensionMajorOpcodeOffset = 8, 9
	if reply[0] != replyStatusReply || reply[extensionPresentOffset] == 0 {
		return &shmExtension{available: false}, sent, nil
	}
	majorOpcode := reply[extensionMajorOpcodeOffset]

	verReq := make([]byte, setupWordSize)
	verReq[0] = majorOpcode
	binary.LittleEndian.PutUint16(verReq[2:4], shmQueryExtensionReqWords)
	if _, err := conn.Write(verReq); err != nil {
		return nil, sent, fmt.Errorf("purego/x11: ShmQueryVersion: %w", err)
	}
	sent = 2
	var verReply [coreEventSize]byte
	if _, err := io.ReadFull(conn, verReply[:]); err != nil {
		return nil, sent, fmt.Errorf("purego/x11: ShmQueryVersion reply: %w", err)
	}
	if verReply[0] != replyStatusReply {
		return &shmExtension{available: false}, sent, fmt.Errorf("purego/x11: ShmQueryVersion status %d", verReply[0])
	}
	return &shmExtension{available: true, majorOpcode: majorOpcode}, sent, nil
}

func (e *shmExtension) attach(conn net.Conn, shmseg uint32, shmid int) error {
	req := make([]byte, shmAttachReqLen)
	req[0] = e.majorOpcode
	req[1] = shmMinorAttach
	binary.LittleEndian.PutUint16(req[2:4], shmAttachReqWords)
	binary.LittleEndian.PutUint32(req[4:8], shmseg)
	id, err := uint32FromInt(shmid)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(req[8:12], id)
	_, err = conn.Write(req)
	return err
}

func (e *shmExtension) detach(conn net.Conn, shmseg uint32) error {
	req := make([]byte, shmDetachReqLen)
	req[0] = e.majorOpcode
	req[1] = shmMinorDetach
	binary.LittleEndian.PutUint16(req[2:4], shmDetachReqWords)
	binary.LittleEndian.PutUint32(req[4:8], shmseg)
	_, err := conn.Write(req)
	return err
}

// putImage issues ShmPutImage (minor opcode 3) to present the full attached
// segment onto drawable through gc, at depth 24 / ZPixmap.
func (e *shmExtension) putImage(conn net.Conn, drawable, gc uint32, w, h int, shmseg uint32, offset uint32) error {
	req := make([]byte, shmPutImageReqLen)
	req[0] = e.majorOpcode
	req[1] = shmMinorPutImage
	binary.LittleEndian.PutUint16(req[2:4], shmPutImageReqWords)
	binary.LittleEndian.PutUint32(req[4:8], drawable)
	binary.LittleEndian.PutUint32(req[8:12], gc)
	cw, err := card16FromInt(w)
	if err != nil {
		return err
	}
	ch, err := card16FromInt(h)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(req[12:14], cw)
	binary.LittleEndian.PutUint16(req[14:16], ch)
	binary.LittleEndian.PutUint16(req[16:18], 0) // srcX
	binary.LittleEndian.PutUint16(req[18:20], 0) // srcY
	binary.LittleEndian.PutUint16(req[20:22], cw)
	binary.LittleEndian.PutUint16(req[22:24], ch)
	req[shmPutImageDepthOffset] = depthTrueColor24
	req[shmPutImageFormatOffset] = imageFormatZPixmap
	req[shmPutImageSendEventOffset] = sendEventFalse
	binary.LittleEndian.PutUint32(req[32:36], shmseg)
	binary.LittleEndian.PutUint32(req[36:40], offset)
	_, err = conn.Write(req)
	return err
}
