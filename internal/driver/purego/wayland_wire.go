//go:build purego && linux

package purego

import (
	"encoding/binary"
	"fmt"
)

// Wayland wire protocol opcodes and object IDs used by this backend. Values
// come from the public Wayland core and xdg-shell protocol XML descriptions,
// not from any implementation this package was built from.
const (
	wlDisplayObjectID uint32 = 1

	// wl_display
	wlDisplayOpSync        uint16 = 0
	wlDisplayOpGetRegistry uint16 = 1

	// wl_registry
	wlRegistryEventGlobal uint16 = 0
	wlRegistryOpBind      uint16 = 0

	// wl_callback
	wlCallbackEventDone uint16 = 0

	// wl_compositor
	wlCompositorOpCreateSurface uint16 = 0

	// wl_surface
	wlSurfaceOpAttach  uint16 = 1
	wlSurfaceOpDamage  uint16 = 2
	wlSurfaceOpDestroy uint16 = 0
	wlSurfaceOpCommit  uint16 = 6

	// wl_shm
	wlShmEventFormat    uint16 = 0
	wlShmOpCreatePool   uint16 = 0
	wlShmFormatArgb8888 uint32 = 0
	wlShmFormatXrgb8888 uint32 = 1

	// wl_shm_pool
	wlShmPoolOpCreateBuffer uint16 = 0
	wlShmPoolOpDestroy      uint16 = 1

	// wl_buffer
	wlBufferEventRelease uint16 = 0
	wlBufferOpDestroy    uint16 = 0

	// wl_seat
	wlSeatEventCapabilities uint16 = 0
	wlSeatOpGetPointer      uint16 = 0
	wlSeatOpGetKeyboard     uint16 = 1
	wlSeatCapPointer        uint32 = 1
	wlSeatCapKeyboard       uint32 = 2

	// wl_pointer
	wlPointerEventMotion uint16 = 2
	wlPointerEventButton uint16 = 3

	// wl_keyboard
	wlKeyboardEventKeymap      uint16 = 0
	wlKeyboardEventKey         uint16 = 3
	wlKeyboardEventModifiers   uint16 = 4
	wlKeyboardKeyStateReleased uint32 = 0

	// xdg_wm_base
	xdgWmBaseEventPing       uint16 = 0
	xdgWmBaseOpGetXdgSurface uint16 = 2
	xdgWmBaseOpPong          uint16 = 3

	// xdg_surface
	xdgSurfaceEventConfigure uint16 = 0
	xdgSurfaceOpGetToplevel  uint16 = 1
	xdgSurfaceOpAckConfigure uint16 = 4
	xdgSurfaceOpDestroy      uint16 = 0

	// xdg_toplevel
	xdgToplevelEventConfigure uint16 = 0
	xdgToplevelEventClose     uint16 = 1
	xdgToplevelOpSetTitle     uint16 = 2
	xdgToplevelOpSetAppID     uint16 = 3
	xdgToplevelOpDestroy      uint16 = 0

	wlHeaderLen = 8
	wlWordSize  = 4

	wlOpcodeBits = 16
	wlFixedShift = 256
)

// wlMsgWriter accumulates the argument bytes of one outgoing wire message
// behind the object ID and (size<<16 | opcode) header that wlEncode fills in
// once the argument count is known.
type wlMsgWriter struct {
	buf []byte
}

func (b *wlMsgWriter) putUint32(v uint32) {
	var t [4]byte
	binary.LittleEndian.PutUint32(t[:], v)
	b.buf = append(b.buf, t[:]...)
}

func (b *wlMsgWriter) putInt32(v int32) { b.putUint32(uint32FromInt32Bits(v)) }

func (b *wlMsgWriter) putString(s string) {
	raw := append([]byte(s), 0)
	n, err := uint32FromInt(len(raw))
	if err != nil {
		return
	}
	b.putUint32(n)
	b.buf = append(b.buf, raw...)
	if pad := (wlWordSize - len(raw)%wlWordSize) % wlWordSize; pad > 0 {
		b.buf = append(b.buf, make([]byte, pad)...)
	}
}

// maxWlMessageSize is the largest value the Wayland wire format's 16-bit
// message-size field (the top half of the object header's second word) can
// hold. A message at or beyond this size would have its size silently
// truncated by the << wlOpcodeBits shift in wlEncode below — e.g. a total of
// 65540 encodes as 4, an announced length shorter than the message's own
// mandatory 8-byte header — desynchronizing the compositor's framing of
// every later message on the connection.
const maxWlMessageSize = 1<<16 - 1

// wlEncode assembles a complete wire message for objectID/opcode from the
// bytes previously written into b. It refuses to encode a message that would
// overflow the 16-bit size field rather than silently truncating it (see
// maxWlMessageSize): a caller such as SetTitle can be handed arbitrarily
// long user-supplied text, so this is a normal, reportable error condition,
// not a programming bug to panic on.
func wlEncode(objectID uint32, opcode uint16, b wlMsgWriter) ([]byte, error) {
	total := wlHeaderLen + len(b.buf)
	if total > maxWlMessageSize {
		return nil, fmt.Errorf("purego/wayland: message for object %d opcode %d is %d bytes, over the %d-byte wire size limit", objectID, opcode, total, maxWlMessageSize)
	}
	out := make([]byte, total)
	binary.LittleEndian.PutUint32(out[0:4], objectID)
	sizeAndOp := (uint32(total) << wlOpcodeBits) | uint32(opcode)
	binary.LittleEndian.PutUint32(out[4:8], sizeAndOp)
	copy(out[8:], b.buf)
	return out, nil
}

// wlParser reads fixed-width arguments out of one event's payload in order,
// the layout every wl_* event uses on the wire.
type wlParser struct {
	p   []byte
	off int
}

func (r *wlParser) uint32() (uint32, error) {
	if r.off+4 > len(r.p) {
		return 0, fmt.Errorf("purego/wayland: truncated event argument at offset %d", r.off)
	}
	v := binary.LittleEndian.Uint32(r.p[r.off : r.off+4])
	r.off += 4
	return v, nil
}

func (r *wlParser) int32() (int32, error) {
	v, err := r.uint32()
	return int32FromUint32Bits(v), err
}

func (r *wlParser) fixed() (int, error) {
	v, err := r.int32()
	return int(v) / wlFixedShift, err
}

func (r *wlParser) skipArray() error {
	n, err := r.uint32()
	if err != nil {
		return err
	}
	_, err = r.takeAligned(n)
	if err != nil {
		return fmt.Errorf("purego/wayland: truncated array argument at offset %d", r.off)
	}
	return nil
}

func (r *wlParser) str() (string, error) {
	n, err := r.uint32()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	raw, err := r.takeAligned(n)
	if err != nil {
		return "", fmt.Errorf("purego/wayland: truncated string argument at offset %d", r.off)
	}
	if len(raw) == 0 {
		return "", nil
	}
	return string(raw[:len(raw)-1]), nil
}

// takeAligned consumes n bytes plus the 32-bit padding the Wayland wire
// format appends. n is refused when it does not fit in int, exceeds the
// remaining payload, or would invert the slice indices.
func (r *wlParser) takeAligned(n uint32) ([]byte, error) {
	nInt, err := intFromUint32(n)
	if err != nil {
		return nil, err
	}
	if r.off < 0 || r.off > len(r.p) {
		return nil, fmt.Errorf("purego/wayland: parser offset %d outside payload of %d", r.off, len(r.p))
	}
	remain := len(r.p) - r.off
	if nInt > remain {
		return nil, fmt.Errorf("purego/wayland: length %d exceeds remaining %d bytes at offset %d", nInt, remain, r.off)
	}
	aligned := (nInt + (wlWordSize - 1)) &^ (wlWordSize - 1)
	if aligned < nInt || aligned > remain {
		return nil, fmt.Errorf("purego/wayland: padded length %d exceeds remaining %d bytes at offset %d", aligned, remain, r.off)
	}
	data := r.p[r.off : r.off+nInt]
	r.off += aligned
	return data, nil
}

// wlSplitMessage peels one complete Wayland message off buf. When buf is too
// short for a header or for the announced size, msg is nil, rest is buf and
// err is nil. A size shorter than the 8-byte header is an error.
func wlSplitMessage(buf []byte) (msg, rest []byte, err error) {
	if len(buf) < wlHeaderLen {
		return nil, buf, nil
	}
	_, _, size := decodeWlHeader(buf)
	if size < wlHeaderLen {
		return nil, buf, fmt.Errorf("purego/wayland: message size %d shorter than the %d-byte header", size, wlHeaderLen)
	}
	if err := checkWireAllocBytes(uint64(size), maxWlMessageSize, "wayland message"); err != nil {
		return nil, buf, err
	}
	if len(buf) < size {
		return nil, buf, nil
	}
	return buf[:size], buf[size:], nil
}
