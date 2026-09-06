//go:build purego && linux

package purego

import "encoding/binary"

// convertRowRGBAToBGRX rewrites one row of w pixels from image.NRGBA's byte
// order (R,G,B,A) to the BGRX/BGRA byte order (B,G,R,A) the X11 and Wayland
// paths in this package present, one 32-bit little-endian word at a time
// instead of one byte at a time: swapping the R and B bytes of the word
// leaves G and A untouched and needs one load and one store per pixel
// instead of four of each. That loop is the dominant per-frame CPU cost at
// 4K/60fps. dst and src must each hold at least w*4 bytes and may be the
// same slice (in-place conversion).
// Masks of a little-endian RGBA word: bytes 0 and 2 (R and B) are swapped,
// bytes 1 and 3 (G and A) are kept.
const (
	laneRB = uint32(0x00ff00ff)
	laneB  = uint32(0x00ff0000)
	laneR  = uint32(0x000000ff)
	laneW  = 16
)

func convertRowRGBAToBGRX(dst, src []byte, w int) {
	for x := 0; x < w; x++ {
		i := x * 4
		v := binary.LittleEndian.Uint32(src[i : i+4])
		// v is R | G<<8 | B<<16 | A<<24 as a little-endian word; swapping
		// the byte 0 (R) and byte 2 (B) lanes and leaving byte 1 (G) and
		// byte 3 (A) alone produces B | G<<8 | R<<16 | A<<24.
		v = (v &^ laneRB) | (v&laneB)>>laneW | (v&laneR)<<laneW
		binary.LittleEndian.PutUint32(dst[i:i+4], v)
	}
}
