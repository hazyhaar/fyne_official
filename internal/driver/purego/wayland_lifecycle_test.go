//go:build purego && linux

package purego

import (
	"encoding/binary"
	"io"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// wlConfigurePayload builds an xdg_toplevel.configure event payload: int32
// width, int32 height, then an empty states array.
func wlConfigurePayload(w, h int32) []byte {
	p := make([]byte, 12)
	var uw, uh uint32
	if w > 0 {
		uw = uint32(w)
	}
	if h > 0 {
		uh = uint32(h)
	}
	binary.LittleEndian.PutUint32(p[0:4], uw)
	binary.LittleEndian.PutUint32(p[4:8], uh)
	binary.LittleEndian.PutUint32(p[8:12], 0) // empty wl_array length
	return p
}

// TestToplevelConfigureZeroSizeKeepsCurrentSize is the regression test for
// xdg-shell's "w=0,h=0 means choose your
// own size" was previously treated as an error and silently dropped,
// discarding the event instead of confirming the surface's already-chosen
// size to the rest of the driver.
func TestToplevelConfigureZeroSizeKeepsCurrentSize(t *testing.T) {
	s := &waylandSurface{
		width: 320, height: 240,
		events: make(chan surfaceEvent, 1),
		done:   make(chan struct{}),
	}

	s.handleToplevel(xdgToplevelEventConfigure, wlConfigurePayload(0, 0))

	select {
	case ev := <-s.events:
		r, ok := ev.(resizeEvent)
		if !ok {
			t.Fatalf("got event %T, want resizeEvent", ev)
		}
		if r.Width != 320 || r.Height != 240 {
			t.Fatalf("resizeEvent = %dx%d, want the surface's current 320x240 (w=0,h=0 means \"keep your own size\", not \"resize to zero\")", r.Width, r.Height)
		}
	case <-time.After(time.Second):
		t.Fatal("a w=0,h=0 configure emitted no resizeEvent at all (dropped)")
	}
	if s.width != 320 || s.height != 240 {
		t.Fatalf("surface size after a w=0,h=0 configure = %dx%d, want unchanged 320x240", s.width, s.height)
	}
}

// TestReleaseBuffersDoesNotDestroyABusyBuffer is the regression test for
// when releaseBuffers used to unconditionally
// munmap and wl_buffer.destroy every buffer, including one the compositor
// had not yet released (busy == true) — a wl_buffer protocol violation a
// strict compositor answers by killing the client. It fills buffers[0] with
// busy=true unmapped memory (memfd-backed, exactly like a real buffer) and
// requires releaseBuffers to move it to pendingBuffers instead of unmapping
// it, only actually destroying it once the corresponding wl_buffer.release
// event arrives through dispatch.
func TestReleaseBuffersDoesNotDestroyABusyBuffer(t *testing.T) {
	const size = 4096
	fd, err := unix.MemfdCreate("wayland-lifecycle-test", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatalf("memfd_create: %v", err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, size); err != nil {
		t.Fatalf("ftruncate: %v", err)
	}
	mem, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatalf("mmap: %v", err)
	}

	client, server := unixSocketPair(t)
	defer client.Close()
	go io.Copy(io.Discard, server)

	const busyBufferID = 4242
	s := &waylandSurface{conn: client, done: make(chan struct{})}
	s.buffers[0] = waylandBuffer{bufferID: busyBufferID, mem: mem, busy: true}
	s.buffers[1] = waylandBuffer{} // never allocated

	s.releaseBuffers()

	s.mu.Lock()
	pendingCount := len(s.pendingBuffers)
	var pendingID uint32
	if pendingCount > 0 {
		pendingID = s.pendingBuffers[0].bufferID
	}
	s.mu.Unlock()
	if pendingCount != 1 || pendingID != busyBufferID {
		t.Fatalf("pendingBuffers after releaseBuffers = %d entries (want 1 for the busy buffer, id %v)", pendingCount, pendingID)
	}

	// The memory must still be valid (not munmapped) while the buffer is
	// pending: touching it must not fault.
	mem[0] = 0xAB
	if mem[0] != 0xAB {
		t.Fatal("pending buffer's memory is no longer usable: it was unmapped despite still being busy")
	}

	// Now the compositor releases it: dispatch must destroy it and drop it
	// from pendingBuffers.
	s.dispatch(busyBufferID, wlBufferEventRelease, nil) // wl_buffer.release carries no arguments

	s.mu.Lock()
	pendingCount = len(s.pendingBuffers)
	s.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pendingBuffers still has %d entries after its wl_buffer.release: destroyWaylandBuffer was not reached", pendingCount)
	}
}
