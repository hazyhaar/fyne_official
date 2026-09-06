//go:build purego && linux

package purego

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers image.Decode support for JPEG icon resources
	_ "image/png"  // registers image.Decode support for PNG icon resources, the common case for fyne.Resource icons

	"fyne.io/fyne/v2"
)

// _NET_WM_ICON (Extended Window Manager Hints) is a CARDINAL[] property:
// width, height, then width*height packed 0xAARRGGBB pixels, for one or more
// icon sizes concatenated back to back. This file publishes exactly one
// size: whatever the decoded resource's native dimensions are.
const (
	icccmCARDINAL = 6 // atom number for the predefined CARDINAL type
)

var _ iconSurface = (*x11Surface)(nil)

// SetWindowIcon implements iconSurface: it decodes res as a raster image and
// publishes it as _NET_WM_ICON in the format EWMH-aware window managers
// read for the taskbar/alt-tab icon. res must decode with the standard
// library's image.Decode (PNG and JPEG are registered by this file); an SVG
// or otherwise undecodable resource returns an error rather than silently
// doing nothing.
func (s *x11Surface) SetWindowIcon(res fyne.Resource) error {
	if res == nil {
		return errors.New("purego/x11: SetWindowIcon called with a nil resource")
	}
	img, _, err := image.Decode(bytes.NewReader(res.Content()))
	if err != nil {
		return fmt.Errorf("purego/x11: icon resource %q could not be decoded as a raster image: %w", res.Name(), err)
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return fmt.Errorf("purego/x11: icon resource %q decoded to an empty image", res.Name())
	}

	netWMIcon, err := s.internAtom("_NET_WM_ICON")
	if err != nil {
		return err
	}

	const iconHeaderFields = 2
	data := make([]uint32, iconHeaderFields+w*h)
	dw, err := uint32FromInt(w)
	if err != nil {
		return err
	}
	dh, err := uint32FromInt(h)
	if err != nil {
		return err
	}
	data[0] = dw
	data[1] = dh
	const channelShift = 8
	i := iconHeaderFields
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			// image.Color.RGBA returns 16-bit-per-channel, alpha-premultiplied
			// values; _NET_WM_ICON wants 8-bit-per-channel, alpha-premultiplied
			// ARGB, so a plain >>8 truncation keeps the same premultiplication.
			const (
				alphaShift = 24
				redShift   = 16
				greenShift = 8
			)
			pixel := (a>>channelShift)<<alphaShift | (r>>channelShift)<<redShift | (g>>channelShift)<<greenShift | (b >> channelShift)
			data[i] = pixel
			i++
		}
	}

	const propHeaderWords = 6
	reqWords := propHeaderWords + len(data)
	// A ChangeProperty request's length is a CARD16 word count: a 256x256
	// icon alone needs 6+2+65536 = 65544 words, one more than the field can
	// hold. uint16(65544) silently wraps to 8, so the request header would
	// claim a 32-byte request while the client still writes the full
	// 262176-byte payload on the wire — the same CARD16 overflow as
	// PutImage. Refuse outright instead of
	// truncating to the largest icon this single ChangeProperty request can
	// carry: BIG-REQUESTS is not negotiated, and splitting one property
	// value across requests is not meaningful for _NET_WM_ICON.
	if reqWords > maxCoreRequestWords {
		return fmt.Errorf("purego/x11: icon resource %q (%dx%d) needs %d words, over the %d-word CARD16 request limit and BIG-REQUESTS is not negotiated", res.Name(), w, h, reqWords, maxCoreRequestWords)
	}
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
	binary.LittleEndian.PutUint32(req[8:12], netWMIcon)
	binary.LittleEndian.PutUint32(req[12:16], icccmCARDINAL)
	req[changePropertyFormatOffset] = propFormat32
	binary.LittleEndian.PutUint32(req[20:24], nData)
	for j, v := range data {
		binary.LittleEndian.PutUint32(req[24+j*4:28+j*4], v)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq() // every request advances the sequence the server will stamp on its replies
	_, err = s.conn.Write(req)
	return err
}
