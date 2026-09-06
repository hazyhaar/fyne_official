//go:build purego && linux

package purego

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	_ "image/jpeg" // registers image.Decode support for JPEG window icons
	_ "image/png"  // registers image.Decode support for PNG window icons

	xdraw "golang.org/x/image/draw"
	"golang.org/x/sys/unix"

	"fyne.io/fyne/v2"
)

var errWaylandIconUnavailable = errors.New("purego/wayland: xdg_toplevel_icon_manager_v1 is not bound")

const (
	xdgToplevelIconMgrOpDestroy    uint16 = 0
	xdgToplevelIconMgrOpCreateIcon uint16 = 1
	xdgToplevelIconMgrOpSetIcon    uint16 = 2

	xdgToplevelIconOpDestroy   uint16 = 0
	xdgToplevelIconOpAddBuffer uint16 = 2
)

var waylandIconSizes = []int{16, 32, 48, 64}

func waylandSetToplevelIcon(s *waylandSurface, icon fyne.Resource) error {
	img, _, err := image.Decode(bytes.NewReader(icon.Content()))
	if err != nil {
		return err
	}
	s.mu.Lock()
	mgr := s.iconMgrID
	toplevel := s.toplevelID
	shm := s.shmID
	s.mu.Unlock()
	if mgr == 0 || shm == 0 || toplevel == 0 {
		return errWaylandIconUnavailable
	}

	iconID := s.allocID()
	if err := s.send(mgr, xdgToplevelIconMgrOpCreateIcon, wlMsgWriter{}.putU(iconID)); err != nil {
		return err
	}
	for _, size := range waylandIconSizes {
		bufID, mem, err := waylandCreateIconBuffer(s, img, size)
		if err != nil {
			return err
		}
		req := wlMsgWriter{}
		req.putUint32(bufID)
		req.putInt32(1)
		if err := s.send(iconID, xdgToplevelIconOpAddBuffer, req); err != nil {
			_ = unix.Munmap(mem)
			return err
		}
		logErr("purego/wayland: icon wl_buffer.destroy", s.send(bufID, wlBufferOpDestroy, wlMsgWriter{}))
		_ = unix.Munmap(mem)
	}
	set := wlMsgWriter{}
	set.putUint32(toplevel)
	set.putUint32(iconID)
	if err := s.send(mgr, xdgToplevelIconMgrOpSetIcon, set); err != nil {
		return err
	}
	logErr("purego/wayland: xdg_toplevel_icon.destroy", s.send(iconID, xdgToplevelIconOpDestroy, wlMsgWriter{}))
	return nil
}

func waylandCreateIconBuffer(s *waylandSurface, src image.Image, size int) (uint32, []byte, error) {
	scaled := image.NewNRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), src, src.Bounds(), draw.Over, nil)

	s.mu.Lock()
	shm := s.shmID
	format := wlShmFormatXrgb8888
	if s.haveArgb {
		format = wlShmFormatArgb8888
	}
	s.mu.Unlock()
	if shm == 0 {
		return 0, nil, errWaylandIconUnavailable
	}

	stride := size * bytesPerPixelWl
	nbytes := stride * size
	fd, err := unix.MemfdCreate("purego-wayland-icon", unix.MFD_CLOEXEC)
	if err != nil {
		return 0, nil, err
	}
	if err := unix.Ftruncate(fd, int64(nbytes)); err != nil {
		_ = unix.Close(fd) // close is idempotent
		return 0, nil, err
	}
	mem, err := unix.Mmap(fd, 0, nbytes, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd) // close is idempotent
		return 0, nil, err
	}
	for y := 0; y < size; y++ {
		srow := scaled.Pix[y*scaled.Stride : y*scaled.Stride+size*bytesPerPixelWl]
		drow := mem[y*stride : y*stride+size*bytesPerPixelWl]
		convertRowRGBAToBGRX(drow, srow, size)
	}

	poolID := s.allocID()
	bufID := s.allocID()
	nb, err := int32FromInt(nbytes)
	if err != nil {
		_ = unix.Munmap(mem)
		_ = unix.Close(fd) // close is idempotent
		return 0, nil, err
	}
	sz, err := int32FromInt(size)
	if err != nil {
		_ = unix.Munmap(mem)
		_ = unix.Close(fd) // close is idempotent
		return 0, nil, err
	}
	st, err := int32FromInt(stride)
	if err != nil {
		_ = unix.Munmap(mem)
		_ = unix.Close(fd) // close is idempotent
		return 0, nil, err
	}
	poolReq := wlMsgWriter{}
	poolReq.putUint32(poolID)
	poolReq.putInt32(nb)
	if err := s.sendFD(shm, wlShmOpCreatePool, poolReq, fd); err != nil {
		_ = unix.Munmap(mem)
		_ = unix.Close(fd) // close is idempotent
		return 0, nil, err
	}
	_ = unix.Close(fd) // close is idempotent

	bufReq := wlMsgWriter{}
	bufReq.putUint32(bufID)
	bufReq.putInt32(0)
	bufReq.putInt32(sz)
	bufReq.putInt32(sz)
	bufReq.putInt32(st)
	bufReq.putUint32(format)
	if err := s.send(poolID, wlShmPoolOpCreateBuffer, bufReq); err != nil {
		_ = unix.Munmap(mem)
		return 0, nil, err
	}
	logErr("purego/wayland: icon wl_shm_pool.destroy", s.send(poolID, wlShmPoolOpDestroy, wlMsgWriter{}))
	return bufID, mem, nil
}
