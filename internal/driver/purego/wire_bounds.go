//go:build purego && linux

package purego

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	// maxX11ReplyBytes bounds a single X11 reply body (the extra words after
	// the 32-byte header). Without BIG-REQUESTS a core reply cannot exceed
	// 65535 4-byte words (~256 KiB); 1 MiB also covers GetImage used by tests
	// on modest windows while refusing a 64 MiB global ceiling.
	maxX11ReplyBytes = 1 << 20

	// maxPropertyBytes bounds GetProperty values (clipboard text, INCR chunks).
	maxPropertyBytes = 1 << 20

	// maxAtomListBytes bounds a list of ATOM values (XdndTypeList and similar).
	maxAtomListBytes = 64 << 10

	// maxKeymapBytes bounds a Wayland xkb keymap memfd. Real compositor
	// keymaps are tens of kilobytes; 1 MiB is a generous ceiling.
	maxKeymapBytes = 1 << 20

	// maxWireAlloc is the default ceiling for remaining wire-driven
	// allocations (handshake extra, RandR lists). It matches maxX11ReplyBytes
	// so call sites that have not been given a tighter cap stay consistent.
	maxWireAlloc = maxX11ReplyBytes

	// getPropertyMaxWords is the LONGS field of GetProperty: 1 MiB of 4-byte
	// units, far beyond a clipboard round trip, and a single bounded allocation.
	getPropertyMaxWords = maxPropertyBytes / 4

	// maxAtomListCount is the largest number of ATOM values this backend will
	// materialize from a property (maxAtomListBytes / 4).
	maxAtomListCount = maxAtomListBytes / 4
)

// checkWireAllocBytes refuses to let a wire-driven allocation of n bytes
// through when it exceeds limit. what names the call site for the error message.
func checkWireAllocBytes(n uint64, limit uint64, what string) error {
	if n > limit {
		return fmt.Errorf("purego: refusing to allocate %d bytes for %s (limit %d)", n, what, limit)
	}
	return nil
}

func intFromUint16(n uint16) int {
	return int(n)
}

func intFromUint32(n uint32) (int, error) {
	if uint64(n) > uint64(math.MaxInt) {
		return 0, fmt.Errorf("purego: length %d does not fit in int", n)
	}
	return int(n), nil
}

func card16FromInt(n int) (uint16, error) {
	if n < 0 || n > math.MaxUint16 {
		return 0, fmt.Errorf("purego: length %d does not fit in CARD16", n)
	}
	return uint16(n), nil
}

func uint32FromInt(n int) (uint32, error) {
	if n < 0 || uint64(n) > math.MaxUint32 {
		return 0, fmt.Errorf("purego: length %d does not fit in CARD32", n)
	}
	return uint32(n), nil
}

func int32FromInt(n int) (int32, error) {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, fmt.Errorf("purego: length %d does not fit in INT32", n)
	}
	return int32(n), nil
}

// intFromWireINT16 decodes an X11 INT16 coordinate from its little-endian
// CARD16 slot on the wire.
func intFromWireINT16(n uint16) int {
	return int(int16(n)) //nolint:gosec // X11 INT16 two's complement on the wire
}

// uint32FromInt32Bits reinterprets a signed 32-bit protocol value as the
// raw bits the Wayland/X11 wire carries.
func uint32FromInt32Bits(v int32) uint32 {
	return uint32(v) //nolint:gosec // wl_int / INT32 is a 32-bit two's complement bit pattern
}

// int32FromUint32Bits reinterprets a 32-bit wire word as a signed protocol value.
func int32FromUint32Bits(v uint32) int32 {
	return int32(v) //nolint:gosec // wl_int / INT32 is a 32-bit two's complement bit pattern
}

func uint32FromRune(r rune) uint32 {
	return uint32(r) //nolint:gosec // Unicode code points fit in 21 bits; Latin-1 in 8
}

// x11ReplyExtraBytes returns the extra-data size announced by an X11 reply
// header, or an error if the announcement exceeds maxX11ReplyBytes. A short
// buffer is refused without allocating.
func x11ReplyExtraBytes(header []byte) (int, error) {
	const extraWordsOffset = 4
	if len(header) < extraWordsOffset+setupWordSize {
		return 0, fmt.Errorf("purego/x11: short reply header (%d bytes)", len(header))
	}
	extraWords := binary.LittleEndian.Uint32(header[extraWordsOffset : extraWordsOffset+setupWordSize])
	if err := checkWireAllocBytes(uint64(extraWords)*uint64(setupWordSize), maxX11ReplyBytes, "x11 reply body"); err != nil {
		return 0, err
	}
	words, err := intFromUint32(extraWords)
	if err != nil {
		return 0, err
	}
	return words * setupWordSize, nil
}

// x11ChangePropertyWords returns the CARD16 word count of a ChangeProperty
// request carrying valueBytes of payload, or an error if that count would
// wrap the core protocol's 16-bit length field.
func x11ChangePropertyWords(valueBytes int) (int, error) {
	const changePropertyHeaderWords = 6
	if valueBytes < 0 {
		return 0, errors.New("purego/x11: negative ChangeProperty payload")
	}
	maxPayload := (maxCoreRequestWords - changePropertyHeaderWords) * 4
	if valueBytes > maxPayload {
		return 0, fmt.Errorf("purego/x11: ChangeProperty payload of %d bytes exceeds the %d-word CARD16 request limit", valueBytes, maxCoreRequestWords)
	}
	pad := (4 - valueBytes%4) % 4
	reqWords := changePropertyHeaderWords + (valueBytes+pad)/4
	if reqWords > maxCoreRequestWords {
		return 0, fmt.Errorf("purego/x11: ChangeProperty payload of %d bytes needs %d words, over the %d-word CARD16 request limit", valueBytes, reqWords, maxCoreRequestWords)
	}
	return reqWords, nil
}
