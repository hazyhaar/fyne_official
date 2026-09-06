//go:build purego && linux

package purego

import (
	"errors"
	"sync"

	"golang.org/x/sys/unix"

	"fyne.io/fyne/v2/driver/desktop"
)

const (
	wlPointerOpSetCursor uint16 = 0

	wpCursorShapeMgrOpGetPointer uint16 = 1
	wpCursorShapeDevOpSetShape   uint16 = 1

	wpCursorShapeDefault    uint32 = 1
	wpCursorShapePointer    uint32 = 4
	wpCursorShapeCrosshair  uint32 = 8
	wpCursorShapeText       uint32 = 9
	wpCursorShapeEwResize   uint32 = 26
	wpCursorShapeNsResize   uint32 = 27
	wpCursorShapeNeswResize uint32 = 28
	wpCursorShapeNwseResize uint32 = 29

	wlCursorSize   = 16
	wlCursorStride = wlCursorSize * bytesPerPixelWl
)

var _ cursorSurface = (*waylandSurface)(nil)

type wlCursorState struct {
	mu       sync.Mutex
	shapeDev uint32
	surfID   uint32
	bufID    uint32
	mem      []byte
	last     desktop.Cursor
}

func waylandCursorOf(s *waylandSurface) *wlCursorState {
	return &s.state.cursor
}

func (s *waylandSurface) releaseAttached() {
	st := waylandCursorOf(s)
	st.mu.Lock()
	mem := st.mem
	st.mem = nil
	st.mu.Unlock()
	if mem != nil {
		_ = unix.Munmap(mem)
	}
	wlStopKeyRepeat(s)
	// An idle inhibitor left alive after the surface is gone keeps the
	// screen awake until the connection drops; release it explicitly.
	waylandSetIdleInhibit(s, false)
}

func waylandCursorShape(c desktop.StandardCursor) uint32 {
	switch c {
	case desktop.TextCursor:
		return wpCursorShapeText
	case desktop.CrosshairCursor:
		return wpCursorShapeCrosshair
	case desktop.PointerCursor:
		return wpCursorShapePointer
	case desktop.HResizeCursor:
		return wpCursorShapeEwResize
	case desktop.VResizeCursor:
		return wpCursorShapeNsResize
	case desktop.NESWResizeCursor:
		return wpCursorShapeNeswResize
	case desktop.NWSEResizeCursor:
		return wpCursorShapeNwseResize
	default:
		return wpCursorShapeDefault
	}
}

func waylandPointerEnterSerial(s *waylandSurface) uint32 {
	st := waylandClipboardOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pointerSerial != 0 {
		return st.pointerSerial
	}
	return st.serial
}

func (s *waylandSurface) SetCursor(cursor desktop.Cursor) error {
	s.mu.Lock()
	pointerID := s.pointerID
	shapeMgr := s.cursorShapeMgrID
	s.mu.Unlock()
	if pointerID == 0 {
		return nil
	}
	serial := waylandPointerEnterSerial(s)
	if std, ok := cursor.(desktop.StandardCursor); ok && std == desktop.HiddenCursor {
		return waylandPointerSetCursor(s, pointerID, serial, 0, 0, 0)
	}
	if shapeMgr != 0 {
		return waylandSetCursorShape(s, pointerID, shapeMgr, serial, cursor)
	}
	return waylandSetCursorSHM(s, pointerID, serial, cursor)
}

func waylandPointerSetCursor(s *waylandSurface, pointerID, serial, surface uint32, hx, hy int32) error {
	w := wlMsgWriter{}
	w.putUint32(serial)
	w.putUint32(surface)
	w.putInt32(hx)
	w.putInt32(hy)
	return s.send(pointerID, wlPointerOpSetCursor, w)
}

func waylandSetCursorShape(s *waylandSurface, pointerID, shapeMgr, serial uint32, cursor desktop.Cursor) error {
	st := waylandCursorOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.shapeDev == 0 {
		id := s.allocID()
		w := wlMsgWriter{}
		w.putUint32(id)
		w.putUint32(pointerID)
		if err := s.send(shapeMgr, wpCursorShapeMgrOpGetPointer, w); err != nil {
			return err
		}
		st.shapeDev = id
	}
	shape := wpCursorShapeDefault
	if std, ok := cursor.(desktop.StandardCursor); ok {
		shape = waylandCursorShape(std)
	}
	req := wlMsgWriter{}
	req.putUint32(serial)
	req.putUint32(shape)
	if err := s.send(st.shapeDev, wpCursorShapeDevOpSetShape, req); err != nil {
		return err
	}
	st.last = cursor
	return nil
}

func waylandSetCursorSHM(s *waylandSurface, pointerID, serial uint32, cursor desktop.Cursor) error {
	st := waylandCursorOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := waylandEnsureCursorBuffer(s, st); err != nil {
		return err
	}
	hx, hy := waylandDrawCursor(st.mem, cursor)
	if err := s.send(st.surfID, wlSurfaceOpAttach, func() wlMsgWriter {
		w := wlMsgWriter{}
		w.putUint32(st.bufID)
		w.putInt32(0)
		w.putInt32(0)
		return w
	}()); err != nil {
		return err
	}
	if err := s.send(st.surfID, wlSurfaceOpDamageBuffer, func() wlMsgWriter {
		w := wlMsgWriter{}
		w.putInt32(0)
		w.putInt32(0)
		w.putInt32(wlCursorSize)
		w.putInt32(wlCursorSize)
		return w
	}()); err != nil {
		return err
	}
	if err := s.send(st.surfID, wlSurfaceOpCommit, wlMsgWriter{}); err != nil {
		return err
	}
	if err := waylandPointerSetCursor(s, pointerID, serial, st.surfID, hx, hy); err != nil {
		return err
	}
	st.last = cursor
	return nil
}

func waylandEnsureCursorBuffer(s *waylandSurface, st *wlCursorState) error {
	if st.mem != nil {
		return nil
	}
	s.mu.Lock()
	compositor := s.compositorID
	shm := s.shmID
	format := wlShmFormatXrgb8888
	if s.haveArgb {
		format = wlShmFormatArgb8888
	}
	s.mu.Unlock()
	if compositor == 0 || shm == 0 {
		return errors.New("purego/wayland: cursor needs wl_compositor and wl_shm")
	}
	size := wlCursorStride * wlCursorSize
	fd, err := unix.MemfdCreate("purego-wayland-cursor", unix.MFD_CLOEXEC)
	if err != nil {
		return err
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		_ = unix.Close(fd) // close is idempotent
		return err
	}
	mem, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd) // close is idempotent
		return err
	}
	surfID := s.allocID()
	poolID := s.allocID()
	bufID := s.allocID()
	if err := s.send(compositor, wlCompositorOpCreateSurface, wlMsgWriter{}.putU(surfID)); err != nil {
		_ = unix.Munmap(mem)
		_ = unix.Close(fd) // close is idempotent
		return err
	}
	size32, err := int32FromInt(size)
	if err != nil {
		_ = unix.Munmap(mem)
		_ = unix.Close(fd) // close is idempotent
		return err
	}
	poolReq := wlMsgWriter{}
	poolReq.putUint32(poolID)
	poolReq.putInt32(size32)
	if err := s.sendFD(shm, wlShmOpCreatePool, poolReq, fd); err != nil {
		_ = unix.Munmap(mem)
		_ = unix.Close(fd) // close is idempotent
		return err
	}
	_ = unix.Close(fd) // close is idempotent
	bufReq := wlMsgWriter{}
	bufReq.putUint32(bufID)
	bufReq.putInt32(0)
	bufReq.putInt32(wlCursorSize)
	bufReq.putInt32(wlCursorSize)
	bufReq.putInt32(wlCursorStride)
	bufReq.putUint32(format)
	if err := s.send(poolID, wlShmPoolOpCreateBuffer, bufReq); err != nil {
		_ = unix.Munmap(mem)
		return err
	}
	logErr("purego/wayland: wl_shm_pool.destroy", s.send(poolID, wlShmPoolOpDestroy, wlMsgWriter{}))
	st.surfID = surfID
	st.bufID = bufID
	st.mem = mem
	return nil
}

func waylandCursorPlot(mem []byte, x, y int, r, g, b, a byte) {
	if x < 0 || y < 0 || x >= wlCursorSize || y >= wlCursorSize {
		return
	}
	i := y*wlCursorStride + x*bytesPerPixelWl
	mem[i] = b
	mem[i+1] = g
	mem[i+2] = r
	mem[i+3] = a
}

func waylandCursorClear(mem []byte) {
	for i := range mem {
		mem[i] = 0
	}
}

func waylandDrawCursor(mem []byte, cursor desktop.Cursor) (hx, hy int32) {
	waylandCursorClear(mem)
	std, ok := cursor.(desktop.StandardCursor)
	if !ok {
		std = desktop.DefaultCursor
	}
	rows, hotX, hotY := waylandCursorGlyph(std)
	const opaque byte = 0xff
	for y, row := range rows {
		for x := 0; x < wlCursorSize; x++ {
			if row&(1<<uint(wlCursorSize-1-x)) == 0 {
				continue
			}
			waylandCursorPlot(mem, x, y, opaque, opaque, opaque, opaque)
			waylandCursorPlot(mem, x+1, y, 0, 0, 0, opaque)
			waylandCursorPlot(mem, x, y+1, 0, 0, 0, opaque)
		}
	}
	return hotX, hotY
}

func waylandCursorGlyph(c desktop.StandardCursor) (rows [wlCursorSize]uint16, hx, hy int32) {
	mid := int32(wlCursorSize / 2)
	art := waylandCursorArt(c)
	const on uint16 = 1
	for y, line := range art {
		var row uint16
		for x := 0; x < len(line) && x < wlCursorSize; x++ {
			if line[x] == '#' {
				row |= on << uint(wlCursorSize-1-x)
			}
		}
		rows[y] = row
	}
	switch c {
	case desktop.PointerCursor:
		return rows, mid, mid / 2
	case desktop.DefaultCursor:
		const arrowHotspot int32 = 1
		return rows, arrowHotspot, arrowHotspot
	default:
		return rows, mid, mid
	}
}

func waylandCursorArt(c desktop.StandardCursor) [wlCursorSize]string {
	const (
		empty = "................"
		stem  = ".......#........"
		bar   = ".....#####......"
		wide  = "....#######....."
		tri   = "......###......."
		tips  = "....#...#......."
		beam  = "...#########...."
	)
	switch c {
	case desktop.TextCursor:
		return [wlCursorSize]string{
			empty, empty, bar, stem, stem, stem, stem, stem,
			stem, stem, stem, stem, stem, bar, empty, empty,
		}
	case desktop.PointerCursor:
		return [wlCursorSize]string{
			empty, empty, empty, tri, bar, wide, wide, wide,
			bar, tri, stem, stem, empty, empty, empty, empty,
		}
	case desktop.HResizeCursor:
		return [wlCursorSize]string{
			empty, empty, empty, empty, empty, empty, empty, tips,
			beam, tips, empty, empty, empty, empty, empty, empty,
		}
	case desktop.VResizeCursor:
		return [wlCursorSize]string{
			empty, empty, stem, tri, stem, stem, stem, stem,
			stem, stem, stem, tri, stem, empty, empty, empty,
		}
	default:
		return [wlCursorSize]string{
			empty,
			".#..............",
			".##.............",
			".###............",
			".####...........",
			".#####..........",
			".######.........",
			".#######........",
			".#####..........",
			".##.##..........",
			".#...##.........",
			"......##........",
			".......##.......",
			".......##.......",
			empty, empty,
		}
	}
}
