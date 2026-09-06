//go:build purego && linux

package purego

import (
	"fmt"
	"syscall"
	"unsafe"

	"fyne.io/fyne/v2"
)

const (
	ipcCreat = 01000
	ipcRmid  = 0
)

// sysVShmSegment is a SysV shared memory segment used to hand pixel data to
// the X server without a copy through the connection socket, via MIT-SHM.
type sysVShmSegment struct {
	ShmID int
	addr  uintptr
	size  int
}

// allocSysVShm allocates a SysV SHM segment, attaches it to this process and
// immediately marks it for destruction (IPC_RMID). On Linux a segment marked
// for destruction keeps existing while at least one process is attached, and
// other processes may still attach to it; the X server therefore attaches
// normally when it handles the ShmAttach request that follows, and the
// segment disappears on its own once both sides have detached, however this
// process ends. Deferring the mark until the server acknowledged its
// attachment (this function's previous behaviour) left every segment whose
// surface was closed before that acknowledgment unmarked, and the kernel's
// table of 4096 segments filled up in a day of test runs, after which every
// Present fell back to PutImage without a word.
func allocSysVShm(size int) (*sysVShmSegment, error) {
	if size <= 0 {
		return nil, fmt.Errorf("purego/x11: invalid shm size %d", size)
	}
	id, _, errno := syscall.Syscall(syscall.SYS_SHMGET, 0, uintptr(size), uintptr(ipcCreat|0600))
	if int(id) < 0 {
		return nil, fmt.Errorf("purego/x11: shmget(%d): %w", size, errno)
	}
	addr, _, errno := syscall.Syscall(syscall.SYS_SHMAT, id, 0, 0)
	if int(addr) == -1 {
		if rmErr := markShmForDestruction(int(id)); rmErr != nil {
			fyne.LogError("purego/x11: shmctl(IPC_RMID) after failed shmat", rmErr)
		}
		return nil, fmt.Errorf("purego/x11: shmat: %w", errno)
	}
	if err := markShmForDestruction(int(id)); err != nil {
		seg := &sysVShmSegment{ShmID: int(id), addr: addr, size: size}
		logErr("purego/x11: shmdt after failed IPC_RMID", seg.Detach())
		return nil, err
	}
	return &sysVShmSegment{ShmID: int(id), addr: addr, size: size}, nil
}

// markShmForDestruction issues shmctl(IPC_RMID) on id and reports the system
// call's own return code instead of discarding it: a failure here (e.g. an
// LSM/SELinux/AppArmor policy denial, or a resource-limit rejection) used to
// go unnoticed, leaving the segment un-marked and therefore leaked once
// every attacher eventually detaches.
func markShmForDestruction(id int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_SHMCTL, uintptr(id), ipcRmid, 0)
	if errno != 0 {
		return fmt.Errorf("purego/x11: shmctl(%d, IPC_RMID): %w", id, errno)
	}
	return nil
}

// Slice returns a byte view over the segment for the caller to fill with
// pixel data before it is presented.
func (s *sysVShmSegment) Slice() []byte {
	if s == nil || s.size <= 0 || s.addr == 0 {
		return nil
	}
	// The address returned by shmat points outside the Go heap, so it is not
	// subject to garbage-collector relocation; converting the stored uintptr
	// back through a pointer-to-pointer indirection (rather than a direct
	// unsafe.Pointer(uintptr) conversion) keeps `go vet` from flagging what
	// would otherwise look like an unsafe stored-pointer pattern.
	addr := s.addr
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr)) //nolint:gosec // shmat address, outside the Go heap
	return unsafe.Slice((*byte)(ptr), s.size)        //nolint:gosec // bounded by the segment's recorded size
}

// Detach unmaps the segment from this process's address space.
func (s *sysVShmSegment) Detach() error {
	if s == nil || s.addr == 0 {
		return nil
	}
	addr := s.addr
	s.addr = 0
	_, _, errno := syscall.Syscall(syscall.SYS_SHMDT, addr, 0, 0)
	if errno != 0 {
		return fmt.Errorf("purego/x11: shmdt: %w", errno)
	}
	return nil
}
