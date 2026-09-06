//go:build purego && linux

package purego

import (
	"encoding/binary"
	"sync"

	"fyne.io/fyne/v2/driver/desktop"
)

// cursorSurface is declared on window.go so hover tracking can call
// SetCursor through a type assertion, with a no-op when the surface does
// not implement it.

// X11 core protocol opcodes and the "cursor" standard font glyph indices this
// file uses. The cursor font ships with every X server (it is part of the
// core protocol's font mechanism, not an extension), so mapping a
// desktop.StandardCursor to one of its glyphs needs no Xcursor library, per
// the voie contract.
const (
	opOpenFont             = 45
	opCreateGlyphCursor    = 94
	opChangeWindowAttrs    = 2
	cwCursor               = 0x00004000
	openFontReqHeaderLen   = 12
	createGlyphCursorLen   = 32
	createGlyphCursorWords = 8
	changeAttrsReqLen      = 16
	changeAttrsReqWords    = 4

	// Glyph indices from the X11 core "cursor" font (X11/cursorfont.h). Each
	// visible glyph is followed by its mask glyph at index+1, which
	// CreateGlyphCursor also expects.
	xcXCursor           = 0
	xcCrosshair         = 34
	xcHand2             = 60
	xcLeftPtr           = 68
	xcSbHDoubleArrow    = 108
	xcSbVDoubleArrow    = 116
	xcTopRightCorner    = 136
	xcBottomRightCorner = 14
	xcXterm             = 152
)

// cursorGlyph maps a desktop.StandardCursor to its "cursor" font glyph.
// HiddenCursor has no matching glyph: it is realised by creating a cursor
// from a fully-transparent 1x1 pixmap instead, in hiddenCursorPixmapGlyph.
func cursorGlyph(c desktop.StandardCursor) (uint16, bool) {
	switch c {
	case desktop.DefaultCursor:
		return xcLeftPtr, true
	case desktop.TextCursor:
		return xcXterm, true
	case desktop.CrosshairCursor:
		return xcCrosshair, true
	case desktop.PointerCursor:
		return xcHand2, true
	case desktop.HResizeCursor:
		return xcSbHDoubleArrow, true
	case desktop.VResizeCursor:
		return xcSbVDoubleArrow, true
	case desktop.NESWResizeCursor:
		return xcTopRightCorner, true
	case desktop.NWSEResizeCursor:
		return xcBottomRightCorner, true
	default:
		return xcXCursor, false
	}
}

// cursorFontState caches the "cursor" font ID and the glyph cursors already
// created for a surface, so repeated SetCursor calls with the same
// desktop.Cursor do not leak a new X11 resource on every call.
type cursorFontState struct {
	mu      sync.Mutex
	fontID  uint32
	glyphs  map[uint16]uint32
	hidden  uint32
	pixmap  uint32
	created bool
	last    desktop.Cursor
}

func cursorStateFor(s *x11Surface) *cursorFontState {
	st := &s.state.cursor
	st.mu.Lock()
	if st.glyphs == nil {
		st.glyphs = make(map[uint16]uint32)
	}
	st.mu.Unlock()
	return st
}

// forgetCursorState drops the cached cursor resources for a surface once it
// is closed; it does not itself free the X11-side resources, since the
// connection they were created on is already gone by the time Close()
// returns.
func forgetCursorState(s *x11Surface) {
	st := &s.state.cursor
	st.mu.Lock()
	st.glyphs = nil
	st.created = false
	st.fontID = 0
	st.hidden = 0
	st.pixmap = 0
	st.last = nil
	st.mu.Unlock()
}

var _ cursorSurface = (*x11Surface)(nil)

// SetCursor implements cursorSurface: it maps cursor to a core "cursor" font
// glyph (or a transparent pixmap for desktop.HiddenCursor), creates the X11
// cursor resource the first time it is needed, and sets it on the surface's
// window with ChangeWindowAttributes. A custom desktop.Cursor image is not
// realised (no CreateCursor-from-pixmap path is built here): its Image is
// ignored and the default arrow is kept, since the voie's contract asks for
// core-font glyphs only.
func (s *x11Surface) SetCursor(cursor desktop.Cursor) error {
	st := cursorStateFor(s)
	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.created {
		if err := s.openCursorFont(st); err != nil {
			return err
		}
		st.created = true
	}

	var cursorID uint32
	if std, ok := cursor.(desktop.StandardCursor); ok && std == desktop.HiddenCursor {
		id, err := s.hiddenCursor(st)
		if err != nil {
			return err
		}
		cursorID = id
	} else {
		glyph := uint16(xcLeftPtr)
		if std, ok := cursor.(desktop.StandardCursor); ok {
			if g, known := cursorGlyph(std); known {
				glyph = g
			}
		}
		id, ok := st.glyphs[glyph]
		if !ok {
			var err error
			id, err = s.createGlyphCursor(st.fontID, glyph)
			if err != nil {
				return err
			}
			st.glyphs[glyph] = id
		}
		cursorID = id
	}

	req := make([]byte, changeAttrsReqLen)
	req[0] = opChangeWindowAttrs
	binary.LittleEndian.PutUint16(req[2:4], changeAttrsReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], cwCursor)
	binary.LittleEndian.PutUint32(req[12:16], cursorID)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq() // every request advances the sequence the server will stamp on its replies
	_, err := s.conn.Write(req)
	if err == nil {
		st.last = cursor
	}
	return err
}

func appliedCursor(s *x11Surface) desktop.Cursor {
	st := cursorStateFor(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.last
}

// openCursorFont issues OpenFont("cursor"): the core standard cursor font
// bundled with every X server.
func (s *x11Surface) openCursorFont(st *cursorFontState) error {
	const fontName = "cursor"
	pad := (setupWordSize - len(fontName)%setupWordSize) % setupWordSize
	reqWords := (openFontReqHeaderLen + len(fontName) + pad) / setupWordSize
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opOpenFont
	reqWords16, err := card16FromInt(reqWords)
	if err != nil {
		return err
	}
	nameLen16, err := card16FromInt(len(fontName))
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(req[2:4], reqWords16)
	fontID := s.allocID()
	binary.LittleEndian.PutUint32(req[4:8], fontID)
	binary.LittleEndian.PutUint16(req[8:10], nameLen16)
	copy(req[12:], fontName)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq() // every request advances the sequence the server will stamp on its replies
	if _, err := s.conn.Write(req); err != nil {
		return err
	}
	st.fontID = fontID
	return nil
}

// createGlyphCursor issues CreateGlyphCursor for glyph (and glyph+1 as its
// mask, as the cursor font convention requires), in solid black-on-white.
func (s *x11Surface) createGlyphCursor(fontID uint32, glyph uint16) (uint32, error) {
	cursorID := s.allocID()
	req := make([]byte, createGlyphCursorLen)
	req[0] = opCreateGlyphCursor
	binary.LittleEndian.PutUint16(req[2:4], createGlyphCursorWords)
	binary.LittleEndian.PutUint32(req[4:8], cursorID)
	binary.LittleEndian.PutUint32(req[8:12], fontID)
	binary.LittleEndian.PutUint32(req[12:16], fontID)
	binary.LittleEndian.PutUint16(req[16:18], glyph)
	binary.LittleEndian.PutUint16(req[18:20], glyph+1)
	// Foreground black, background white: the conventional cursor colours.
	const colorChannelBlack = 0x0000
	binary.LittleEndian.PutUint16(req[20:22], colorChannelBlack)
	binary.LittleEndian.PutUint16(req[22:24], colorChannelBlack)
	binary.LittleEndian.PutUint16(req[24:26], colorChannelBlack)
	binary.LittleEndian.PutUint16(req[26:28], 0xffff)
	binary.LittleEndian.PutUint16(req[28:30], 0xffff)
	binary.LittleEndian.PutUint16(req[30:32], 0xffff)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq() // every request advances the sequence the server will stamp on its replies
	if _, err := s.conn.Write(req); err != nil {
		return 0, err
	}
	return cursorID, nil
}

// hiddenCursor realises desktop.HiddenCursor as a 1x1 fully-transparent
// pixmap cursor: CreatePixmap(1x1, depth 1) then CreateCursor from it as
// both source and mask, so nothing is ever painted.
func (s *x11Surface) hiddenCursor(st *cursorFontState) (uint32, error) {
	if st.hidden != 0 {
		return st.hidden, nil
	}
	const (
		opCreatePixmap    = 53
		opCreateCursor    = 93
		createPixmapLen   = 16
		createPixmapWords = 4
		createCursorLen   = 32
		createCursorWords = 8
		pixmapDepth1      = 1
	)
	pixmapID := s.allocID()
	pmReq := make([]byte, createPixmapLen)
	pmReq[0] = opCreatePixmap
	pmReq[1] = pixmapDepth1
	binary.LittleEndian.PutUint16(pmReq[2:4], createPixmapWords)
	binary.LittleEndian.PutUint32(pmReq[4:8], pixmapID)
	binary.LittleEndian.PutUint32(pmReq[8:12], s.rootWindow)
	binary.LittleEndian.PutUint16(pmReq[12:14], 1)
	binary.LittleEndian.PutUint16(pmReq[14:16], 1)

	cursorID := s.allocID()
	curReq := make([]byte, createCursorLen)
	curReq[0] = opCreateCursor
	binary.LittleEndian.PutUint16(curReq[2:4], createCursorWords)
	binary.LittleEndian.PutUint32(curReq[4:8], cursorID)
	binary.LittleEndian.PutUint32(curReq[8:12], pixmapID)
	binary.LittleEndian.PutUint32(curReq[12:16], pixmapID)
	// Foreground/background are irrelevant: both source and mask planes are
	// left at their allocated (undefined, effectively zero) pixmap content,
	// so the cursor paints nothing regardless of colour.
	binary.LittleEndian.PutUint16(curReq[28:30], 0)
	binary.LittleEndian.PutUint16(curReq[30:32], 0)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq() // every request advances the sequence the server will stamp on its replies
	if _, err := s.conn.Write(pmReq); err != nil {
		return 0, err
	}
	s.nextSeq() // every request advances the sequence the server will stamp on its replies
	if _, err := s.conn.Write(curReq); err != nil {
		return 0, err
	}
	st.pixmap = pixmapID
	st.hidden = cursorID
	return cursorID, nil
}
