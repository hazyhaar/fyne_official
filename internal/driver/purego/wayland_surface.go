//go:build purego && linux

package purego

import (
	"errors"
	"fmt"
	"image"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"fyne.io/fyne/v2"
)

// Declare conformity with surface, so the driver can select this backend
// behind the same window/driver code that already selects x11Surface.
var (
	_ surface       = (*waylandSurface)(nil)
	_ regionSurface = (*waylandSurface)(nil)
)

// wl_surface.damage_buffer (since version 4) reports damage in buffer
// coordinates. wl_compositor is bound at version 4, so the created surface
// supports this request.
const wlSurfaceOpDamageBuffer uint16 = 9

// waylandBufferCount is the number of wl_shm buffers rotated between frames.
// Two buffers let the compositor still hold the previous frame (pending
// wl_buffer.release) while the next one is being drawn into.
const waylandBufferCount = 2

// waylandRequestTimeout bounds how long roundtrip and waitConfigured wait
// for the compositor. A var so tests can shrink it instead of waiting out
// the real value.
var waylandRequestTimeout = 5 * time.Second

// waylandBuffer is one wl_shm-backed frame buffer: a memfd-mapped region and
// the wl_buffer object the compositor was told to wrap around it.
type waylandBuffer struct {
	bufferID uint32
	poolID   uint32
	mem      []byte
	fd       int
	width    int
	height   int
	stride   int
	busy     bool
}

// waylandSurface implements surface directly against the Wayland wire
// protocol over a Unix domain socket, without any client library dependency.
// It covers registry binding, wl_shm double buffering, xdg_shell
// configure/close, and pointer/keyboard input. Wheel motion arrives as
// scrollEvent (see pointerEvent in surface.go).
type waylandSurface struct {
	mu   sync.Mutex
	conn *net.UnixConn

	// writeMu serializes every write to conn, separately from mu (which
	// protects surface state such as buffers/configured/width/height).
	// Present's render-path sends (attach/damage/commit) and readLoop's own
	// replies (handlePing's pong, handleSurfaceConfigure's ack+commit) run
	// on different goroutines and neither holds mu across its send calls;
	// without a lock dedicated to the connection itself, their writes to
	// the same SOCK_STREAM socket could interleave mid-message, corrupting
	// the compositor's view of the message boundary for both. A separate
	// mutex, rather than reusing mu,
	// avoids turning every unrelated mu-protected state access into a
	// point where a slow write could block it.
	writeMu sync.Mutex

	nextID uint32

	registryID   uint32
	compositorID uint32
	shmID        uint32
	wmBaseID     uint32
	seatID       uint32
	pointerID    uint32
	keyboardID   uint32

	dataDeviceManagerID uint32
	dataDeviceID        uint32
	cursorShapeMgrID    uint32
	idleInhibitMgrID    uint32
	iconMgrID           uint32

	shmFormat uint32
	haveArgb  bool

	surfaceID    uint32
	xdgSurfaceID uint32
	toplevelID   uint32

	width, height int
	configured    bool
	// configuredCh is closed exactly once, the first time handleSurface-
	// Configure observes an xdg_surface.configure event, under mu. Waiting
	// on it (see waitConfigured) avoids polling s.configured in a tight,
	// unbounded loop.
	configuredCh chan struct{}

	buffers [waylandBufferCount]waylandBuffer
	active  int

	// pendingBuffers holds a buffer releaseBuffers found still busy (the
	// compositor had not yet sent wl_buffer.release for it) at the moment a
	// resize needed to replace it. Destroying a wl_buffer object, or
	// unmapping the memory a compositor may still be reading from to
	// composite the current frame, while it is busy is a wl_buffer protocol
	// violation a strict compositor answers with a fatal wl_display.error.
	// Its actual destruction is deferred to the
	// wl_buffer.release event handler in dispatch, once the compositor
	// itself says it is done with it.
	pendingBuffers []waylandBuffer

	lastPointerX, lastPointerY int
	modifiers                  keyModifiers
	pendingFDs                 []int
	xkb                        *xkbKeymap
	wlModsDepressed            uint32
	wlModsLatched              uint32
	wlModsLocked               uint32
	wlGroup                    uint32

	syncing  bool
	syncID   uint32
	syncDone chan struct{}

	events chan surfaceEvent
	wake   chan struct{}
	// readLoop never blocks on events: it appends to evQueue and pumpEvents
	// forwards to events, matching x11Surface. A bounded channel written by
	// readLoop would stall the socket reader once the window loop lagged.
	evMu      sync.Mutex
	evQueue   []surfaceEvent
	evSignal  chan struct{}
	pumpOnce  sync.Once
	closeOnce sync.Once
	done      chan struct{}
	closed    bool
	hidden    bool

	state waylandSurfaceState
}

// newWaylandSurface dials the Wayland compositor named by $WAYLAND_DISPLAY,
// performs the registry handshake, binds the globals this backend needs and
// creates a toplevel window of the requested size. Its signature is fixed by
// the core's construction point (window.go's openSurface): any additional
// information a caller wants (initial title, output scale) is obtained
// afterwards through surface methods or an optional interface, not through
// extra parameters here.
func newWaylandSurface(width, height int) (surface, error) {
	sockPath, err := waylandSocketPath("")
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("purego/wayland: cannot reach compositor at %s: %w", sockPath, err)
	}

	s := &waylandSurface{
		conn:         conn,
		nextID:       2,
		width:        width,
		height:       height,
		events:       make(chan surfaceEvent, eventChanCapacity),
		wake:         make(chan struct{}, 1),
		done:         make(chan struct{}),
		configuredCh: make(chan struct{}),
	}
	go s.readLoop()

	s.registryID = s.allocID()
	if err := s.send(wlDisplayObjectID, wlDisplayOpGetRegistry, wlMsgWriter{}.putU(s.registryID)); err != nil {
		s.fail()
		return nil, fmt.Errorf("purego/wayland: get_registry: %w", err)
	}
	if err := s.roundtrip(); err != nil {
		s.fail()
		return nil, fmt.Errorf("purego/wayland: initial roundtrip: %w", err)
	}
	if err := s.roundtrip(); err != nil {
		s.fail()
		return nil, fmt.Errorf("purego/wayland: bind roundtrip: %w", err)
	}
	if s.compositorID == 0 || s.shmID == 0 || s.wmBaseID == 0 {
		s.fail()
		return nil, errors.New("purego/wayland: compositor did not advertise wl_compositor/wl_shm/xdg_wm_base")
	}
	waylandEnsureDataDevice(s)

	if err := s.createToplevel(width, height); err != nil {
		s.fail()
		return nil, err
	}
	return s, nil
}

// waylandSocketPath resolves the Unix socket a Wayland compositor listens
// on, following the same $WAYLAND_DISPLAY/$XDG_RUNTIME_DIR resolution the
// reference client libraries use.
func waylandSocketPath(dpyName string) (string, error) {
	name := dpyName
	if name == "" {
		name = os.Getenv("WAYLAND_DISPLAY")
	}
	if name == "" {
		name = "wayland-0"
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return "", errors.New("purego/wayland: XDG_RUNTIME_DIR is not set")
	}
	path := filepath.Join(runtimeDir, name)
	if _, err := os.Stat(path); err != nil { //nolint:gosec // path is XDG_RUNTIME_DIR plus WAYLAND_DISPLAY, a local socket name
		return "", fmt.Errorf("purego/wayland: socket %s: %w", path, err)
	}
	return path, nil
}

func (s *waylandSurface) allocID() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	return id
}

// send writes one request with no file descriptor attached. writeMu (not mu)
// serializes it against every other write on the same connection: see
// writeMu's doc-comment on waylandSurface for why a plain, unsynchronized
// net.Conn.Write here is unsafe.
func (s *waylandSurface) send(objectID uint32, opcode uint16, b wlMsgWriter) error {
	data, err := wlEncode(objectID, opcode, b)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.conn.Write(data)
	return err
}

// sendFD writes one request that carries a file descriptor as ancillary
// data, the mechanism wl_shm.create_pool uses to hand the compositor the
// memfd backing a buffer without copying its contents through the socket.
// It shares writeMu with send so a buffer-pool creation cannot interleave
// with a concurrent attach/damage/commit or pong/ack on the same socket.
func (s *waylandSurface) sendFD(objectID uint32, opcode uint16, b wlMsgWriter, fd int) error {
	data, err := wlEncode(objectID, opcode, b)
	if err != nil {
		return err
	}
	oob := unix.UnixRights(fd)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _, err = s.conn.WriteMsgUnix(data, oob, nil)
	return err
}

func (b wlMsgWriter) putU(v uint32) wlMsgWriter { b.putUint32(v); return b }

// roundtrip issues wl_display.sync and blocks until the compositor's
// wl_callback.done event for it arrives, guaranteeing every request sent
// before the call has been processed.
func (s *waylandSurface) roundtrip() error {
	s.mu.Lock()
	cb := s.nextID
	s.nextID++
	s.syncID = cb
	s.syncing = true
	s.syncDone = make(chan struct{})
	done := s.syncDone
	s.mu.Unlock()

	if err := s.send(wlDisplayObjectID, wlDisplayOpSync, wlMsgWriter{}.putU(cb)); err != nil {
		return err
	}
	timer := time.NewTimer(waylandRequestTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-s.done:
		return errors.New("purego/wayland: connection closed during roundtrip")
	case <-timer.C:
		s.mu.Lock()
		if s.syncDone == done {
			s.syncing = false
			s.syncDone = nil
		}
		s.mu.Unlock()
		return fmt.Errorf("purego/wayland: timed out waiting for roundtrip after %s", waylandRequestTimeout)
	}
}

func (s *waylandSurface) createToplevel(width, height int) error {
	s.mu.Lock()
	s.surfaceID = s.nextID
	s.nextID++
	s.xdgSurfaceID = s.nextID
	s.nextID++
	s.toplevelID = s.nextID
	s.nextID++
	surfaceID, xdgSurfaceID, toplevelID := s.surfaceID, s.xdgSurfaceID, s.toplevelID
	compositorID, wmBaseID := s.compositorID, s.wmBaseID
	s.mu.Unlock()

	if err := s.send(compositorID, wlCompositorOpCreateSurface, wlMsgWriter{}.putU(surfaceID)); err != nil {
		return fmt.Errorf("purego/wayland: create_surface: %w", err)
	}
	w := wlMsgWriter{}
	w.putUint32(xdgSurfaceID)
	w.putUint32(surfaceID)
	if err := s.send(wmBaseID, xdgWmBaseOpGetXdgSurface, w); err != nil {
		return fmt.Errorf("purego/wayland: get_xdg_surface: %w", err)
	}
	if err := s.send(xdgSurfaceID, xdgSurfaceOpGetToplevel, wlMsgWriter{}.putU(toplevelID)); err != nil {
		return fmt.Errorf("purego/wayland: get_toplevel: %w", err)
	}
	title := wlMsgWriter{}
	title.putString("")
	if err := s.send(toplevelID, xdgToplevelOpSetTitle, title); err != nil {
		return fmt.Errorf("purego/wayland: set_title: %w", err)
	}
	if err := s.send(surfaceID, wlSurfaceOpCommit, wlMsgWriter{}); err != nil {
		return fmt.Errorf("purego/wayland: initial commit: %w", err)
	}
	// Wait for the first xdg_surface.configure so the surface is ready to
	// attach a buffer to, per the xdg-shell protocol.
	if err := s.waitConfigured(); err != nil {
		return err
	}
	_, _ = width, height
	return nil
}

// waitConfigured blocks until this surface has received its first
// xdg_surface.configure event, the connection closes, or waylandRequestTimeout
// elapses. Polling s.configured in a tight, unbounded loop (locking and
// unlocking s.mu millions of times a second) until the compositor answered
// would peg a CPU core at 100% for however long that took. configuredCh
// (closed once, under s.mu, by handleSurfaceConfigure) turns the wait into
// a plain blocking channel receive instead.
func (s *waylandSurface) waitConfigured() error {
	timer := time.NewTimer(waylandRequestTimeout)
	defer timer.Stop()
	select {
	case <-s.configuredCh:
		return nil
	case <-s.done:
		return errors.New("purego/wayland: connection closed before initial configure")
	case <-timer.C:
		return fmt.Errorf("purego/wayland: timed out waiting for configure after %s", waylandRequestTimeout)
	}
}

func (s *waylandSurface) SetTitle(title string) error {
	w := wlMsgWriter{}
	w.putString(title)
	return s.send(s.toplevelID, xdgToplevelOpSetTitle, w)
}

// Resize has no effect: unlike X11's ConfigureWindow, the xdg-shell protocol
// gives clients no request to impose their outer size on the compositor —
// only xdg_toplevel.configure, sent by the compositor, ever changes it. This
// is a documented gap, not a silent partial behaviour.
func (*waylandSurface) Resize(int, int) error { return nil }

func (s *waylandSurface) Events() <-chan surfaceEvent { return s.events }

func (s *waylandSurface) Wake() <-chan struct{} { return s.wake }

func (s *waylandSurface) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.markClosed()
		s.releaseAttached()
		s.releaseBuffers()
		s.closeLeftoverFDs()
		if s.toplevelID != 0 {
			_ = s.send(s.toplevelID, xdgToplevelOpDestroy, wlMsgWriter{}) // best-effort: teardown
		}
		if s.xdgSurfaceID != 0 {
			_ = s.send(s.xdgSurfaceID, xdgSurfaceOpDestroy, wlMsgWriter{}) // best-effort: teardown
		}
		if s.surfaceID != 0 {
			_ = s.send(s.surfaceID, wlSurfaceOpDestroy, wlMsgWriter{}) // best-effort: teardown
		}
		if s.conn != nil {
			err = s.conn.Close()
		}

		s.mu.Lock()
		pending := s.pendingBuffers
		s.pendingBuffers = nil
		s.mu.Unlock()
		for _, buf := range pending {
			if buf.mem != nil {
				_ = unix.Munmap(buf.mem)
			}
		}
	})
	return err
}

func (s *waylandSurface) markClosed() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.done != nil {
			close(s.done)
		}
	}
	s.mu.Unlock()
}

func (s *waylandSurface) fail() {
	s.closeOnce.Do(func() {
		s.markClosed()
		s.releaseAttached()
		s.closeLeftoverFDs()
		if s.conn != nil {
			_ = s.conn.Close() // close is idempotent
		}
	})
}

func (s *waylandSurface) enqueuePendingFDs(fds []int) {
	if len(fds) == 0 {
		return
	}
	s.mu.Lock()
	s.pendingFDs = append(s.pendingFDs, fds...)
	s.mu.Unlock()
}

func (s *waylandSurface) takePendingFD() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pendingFDs) == 0 {
		return 0, false
	}
	fd := s.pendingFDs[0]
	s.pendingFDs = s.pendingFDs[1:]
	return fd, true
}

// closeLeftoverFDs closes descriptors that no dispatched message claimed.
// Callers must only invoke it when every complete message from the current
// recvmsg has been dispatched: a later message still in the byte buffer
// (wl_keyboard.keymap, wl_data_source.send) may own a descriptor already
// sitting in pendingFDs because SOCK_STREAM coalesces sendmsg batches.
func (s *waylandSurface) closeLeftoverFDs() {
	s.mu.Lock()
	fds := s.pendingFDs
	s.pendingFDs = nil
	s.mu.Unlock()
	for _, fd := range fds {
		_ = unix.Close(fd) // close is idempotent
	}
}

const (
	waylandRecvBufSize      = 4096
	waylandMaxAncillaryFDs  = 32
	waylandAncillaryFDBytes = 4
)

var waylandCmsgSpace = unix.CmsgSpace(waylandAncillaryFDBytes * waylandMaxAncillaryFDs)

func waylandParseRights(oob []byte) []int {
	if len(oob) == 0 {
		return nil
	}
	cmsgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	var fds []int
	for i := range cmsgs {
		got, ferr := unix.ParseUnixRights(&cmsgs[i])
		if ferr == nil {
			fds = append(fds, got...)
		}
	}
	return fds
}

func waylandRecvTruncated(flags int) error {
	if flags&unix.MSG_CTRUNC != 0 {
		return errors.New("purego/wayland: ancillary data truncated (MSG_CTRUNC)")
	}
	return nil
}

func (s *waylandSurface) enqueueEvent(ev surfaceEvent) {
	s.pumpOnce.Do(func() { go s.pumpEvents() })
	s.evMu.Lock()
	if len(s.evQueue) == 0 && s.events != nil {
		select {
		case s.events <- ev:
			s.evMu.Unlock()
			signalWake(s.wake)
			return
		default:
		}
	}
	s.evQueue = append(s.evQueue, ev)
	sig := s.signalLocked()
	s.evMu.Unlock()
	select {
	case sig <- struct{}{}:
	default:
	}
}

func (s *waylandSurface) signalLocked() chan struct{} {
	if s.evSignal == nil {
		s.evSignal = make(chan struct{}, 1)
	}
	return s.evSignal
}

func (s *waylandSurface) pumpEvents() {
	s.evMu.Lock()
	sig := s.signalLocked()
	s.evMu.Unlock()
	for {
		s.evMu.Lock()
		var ev surfaceEvent
		if len(s.evQueue) > 0 {
			ev = s.evQueue[0]
			s.evQueue[0] = nil
			s.evQueue = s.evQueue[1:]
		}
		s.evMu.Unlock()
		if ev == nil {
			if s.done == nil {
				return
			}
			select {
			case <-sig:
				continue
			case <-s.done:
				return
			}
		}
		if s.events == nil {
			continue
		}
		select {
		case s.events <- ev:
			signalWake(s.wake)
		case <-s.done:
			select {
			case s.events <- ev:
				signalWake(s.wake)
			default:
			}
			return
		}
	}
}

// readLoop decodes wl_* event headers and their payload, reassembling
// SOCK_STREAM fragments and collecting SCM_RIGHTS from every recvmsg.
// Descriptors are claimed by the message that carries them (keymap, data
// source send). Only descriptors still unclaimed once no incomplete
// message remains in the byte buffer are closed.
func (s *waylandSurface) readLoop() {
	s.pumpOnce.Do(func() { go s.pumpEvents() })
	pending := make([]byte, 0, waylandRecvBufSize)
	tmp := make([]byte, waylandRecvBufSize)
	oob := make([]byte, waylandCmsgSpace)
	for {
		n, oobn, flags, _, err := s.conn.ReadMsgUnix(tmp, oob)
		if err != nil {
			s.closeLeftoverFDs()
			s.enqueueEvent(closeEvent{})
			return
		}
		if truncErr := waylandRecvTruncated(flags); truncErr != nil {
			if oobn > 0 {
				s.enqueuePendingFDs(waylandParseRights(oob[:oobn]))
				s.closeLeftoverFDs()
			}
			fyne.LogError("purego/wayland: closing connection", truncErr)
			s.enqueueEvent(closeEvent{})
			s.fail()
			return
		}
		if oobn > 0 {
			s.enqueuePendingFDs(waylandParseRights(oob[:oobn]))
		}
		pending = append(pending, tmp[:n]...)
		var splitErr error
		pending, splitErr = s.consumeWaylandMessages(pending)
		if splitErr != nil {
			fyne.LogError("purego/wayland: closing connection", splitErr)
			s.fail()
			s.enqueueEvent(closeEvent{})
			return
		}
		if len(pending) == 0 {
			s.closeLeftoverFDs()
		}
	}
}

// consumeWaylandMessages dispatches every complete Wayland message in
// pending and returns the unparsed tail. File descriptors stay in
// pendingFDs so a later message in the same recvmsg can claim them.
func (s *waylandSurface) consumeWaylandMessages(pending []byte) ([]byte, error) {
	for {
		msg, rest, splitErr := wlSplitMessage(pending)
		if splitErr != nil {
			return pending, splitErr
		}
		if msg == nil {
			return pending, nil
		}
		objectID, opcode, _ := decodeWlHeader(msg)
		payload := append([]byte(nil), msg[wlHeaderLen:]...)
		s.dispatch(objectID, opcode, payload)
		pending = append([]byte(nil), rest...)
	}
}

func decodeWlHeader(h []byte) (objectID uint32, opcode uint16, size int) {
	objectID = leUint32(h[0:4])
	sizeAndOp := leUint32(h[4:8])
	return objectID, uint16(sizeAndOp & 0xffff), int(sizeAndOp >> 16)
}

const (
	byteShift1 = 8
	byteShift2 = 16
	byteShift3 = 24
)

func leUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<byteShift1 | uint32(b[2])<<byteShift2 | uint32(b[3])<<byteShift3
}

func (s *waylandSurface) dispatch(objectID uint32, opcode uint16, p []byte) {
	s.mu.Lock()
	switch {
	case s.syncing && objectID == s.syncID && opcode == wlCallbackEventDone:
		s.syncing = false
		done := s.syncDone
		s.mu.Unlock()
		close(done)
		return
	case objectID == s.registryID && opcode == wlRegistryEventGlobal:
		s.mu.Unlock()
		s.handleGlobal(p)
		return
	case objectID == s.shmID && opcode == wlShmEventFormat:
		s.mu.Unlock()
		s.handleShmFormat(p)
		return
	case objectID == s.wmBaseID && opcode == xdgWmBaseEventPing:
		s.mu.Unlock()
		s.handlePing(p)
		return
	case objectID == s.xdgSurfaceID && opcode == xdgSurfaceEventConfigure:
		s.mu.Unlock()
		s.handleSurfaceConfigure(p)
		return
	case objectID == s.toplevelID:
		s.mu.Unlock()
		s.handleToplevel(opcode, p)
		waylandToplevelConfigure(s, opcode, p)
		return
	case objectID == s.seatID && opcode == wlSeatEventCapabilities:
		s.mu.Unlock()
		s.handleSeatCapabilities(p)
		return
	case objectID == s.pointerID:
		s.mu.Unlock()
		waylandNoteInputSerial(s, true, opcode, p)
		s.handlePointer(opcode, p)
		return
	case objectID == s.keyboardID:
		s.mu.Unlock()
		waylandNoteInputSerial(s, false, opcode, p)
		s.handleKeyboard(opcode, p)
		return
	}
	for i := range s.buffers {
		if s.buffers[i].bufferID == objectID && opcode == wlBufferEventRelease {
			s.buffers[i].busy = false
			s.mu.Unlock()
			return
		}
	}
	// The release may instead be for a buffer releaseBuffers already
	// retired from s.buffers while it was still busy (see pendingBuffers):
	// only now, with the compositor itself confirming it is done with it,
	// is destroying the wl_buffer object and unmapping its memory safe.
	for i, buf := range s.pendingBuffers {
		if buf.bufferID == objectID && opcode == wlBufferEventRelease {
			s.pendingBuffers = append(s.pendingBuffers[:i], s.pendingBuffers[i+1:]...)
			s.mu.Unlock()
			destroyWaylandBuffer(s, buf)
			return
		}
	}
	s.mu.Unlock()
	if waylandDndDispatch(s, objectID, opcode, p) {
		return
	}
	if waylandClipboardDispatch(s, objectID, opcode, p) {
		return
	}
	if waylandOutputDispatch(s, objectID, opcode, p) {
		return
	}
	_ = waylandSurfaceScaleDispatch(s, objectID, opcode, p)
}

// destroyWaylandBuffer unmaps a buffer's memory and asks the compositor to
// destroy its wl_buffer object. The caller must not be holding s.mu: send
// only takes writeMu (see send's doc comment), but calling this with s.mu
// held is still avoided on principle since it does I/O.
func destroyWaylandBuffer(s *waylandSurface, buf waylandBuffer) {
	if buf.mem != nil {
		_ = unix.Munmap(buf.mem)
	}
	if buf.bufferID != 0 {
		_ = s.send(buf.bufferID, wlBufferOpDestroy, wlMsgWriter{}) // best-effort: buffer teardown
	}
}

func (s *waylandSurface) handleGlobal(p []byte) {
	r := wlParser{p: p}
	name, err1 := r.uint32()
	iface, err2 := r.str()
	version, err3 := r.uint32()
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}

	bind := func(id uint32, want uint32) {
		if version > want {
			version = want
		}
		w := wlMsgWriter{}
		w.putUint32(name)
		w.putString(iface)
		w.putUint32(version)
		w.putUint32(id)
		logErr("purego/wayland: registry.bind", s.send(s.registryID, wlRegistryOpBind, w))
	}

	s.mu.Lock()
	switch iface {
	case "wl_compositor":
		if s.compositorID == 0 {
			s.compositorID = s.nextID
			s.nextID++
			id := s.compositorID
			s.mu.Unlock()
			bind(id, wlCompositorBindVersion)
			return
		}
	case "wl_shm":
		if s.shmID == 0 {
			s.shmID = s.nextID
			s.nextID++
			id := s.shmID
			s.mu.Unlock()
			bind(id, 1)
			return
		}
	case "xdg_wm_base":
		if s.wmBaseID == 0 {
			s.wmBaseID = s.nextID
			s.nextID++
			id := s.wmBaseID
			s.mu.Unlock()
			bind(id, 2)
			return
		}
	case "wl_seat":
		if s.seatID == 0 {
			s.seatID = s.nextID
			s.nextID++
			id := s.seatID
			s.mu.Unlock()
			bind(id, 5)
			waylandEnsureDataDevice(s)
			return
		}
	case "wl_data_device_manager":
		if s.dataDeviceManagerID == 0 {
			s.dataDeviceManagerID = s.nextID
			s.nextID++
			id := s.dataDeviceManagerID
			s.mu.Unlock()
			bind(id, 3)
			waylandEnsureDataDevice(s)
			return
		}
	case "wl_output":
		id := s.nextID
		s.nextID++
		s.mu.Unlock()
		waylandRegisterOutput(s, id)
		bind(id, 4)
		return
	case "wp_cursor_shape_manager_v1":
		if s.cursorShapeMgrID == 0 {
			s.cursorShapeMgrID = s.nextID
			s.nextID++
			id := s.cursorShapeMgrID
			s.mu.Unlock()
			bind(id, 1)
			return
		}
	case "zwp_idle_inhibit_manager_v1":
		if s.idleInhibitMgrID == 0 {
			s.idleInhibitMgrID = s.nextID
			s.nextID++
			id := s.idleInhibitMgrID
			s.mu.Unlock()
			bind(id, 1)
			return
		}
	case "xdg_toplevel_icon_manager_v1":
		if s.iconMgrID == 0 {
			s.iconMgrID = s.nextID
			s.nextID++
			id := s.iconMgrID
			s.mu.Unlock()
			bind(id, 1)
			return
		}
	}
	s.mu.Unlock()
}

func (s *waylandSurface) handleShmFormat(p []byte) {
	r := wlParser{p: p}
	format, err := r.uint32()
	if err != nil {
		return
	}
	if format == wlShmFormatArgb8888 {
		s.mu.Lock()
		s.haveArgb = true
		s.mu.Unlock()
	}
}

func (s *waylandSurface) handlePing(p []byte) {
	r := wlParser{p: p}
	serial, err := r.uint32()
	if err != nil {
		return
	}
	logErr("purego/wayland: xdg_wm_base.pong", s.send(s.wmBaseID, xdgWmBaseOpPong, wlMsgWriter{}.putU(serial)))
}

func (s *waylandSurface) handleSurfaceConfigure(p []byte) {
	r := wlParser{p: p}
	serial, err := r.uint32()
	if err != nil {
		return
	}
	logErr("purego/wayland: xdg_surface.ack_configure", s.send(s.xdgSurfaceID, xdgSurfaceOpAckConfigure, wlMsgWriter{}.putU(serial)))
	logErr("purego/wayland: wl_surface.commit", s.send(s.surfaceID, wlSurfaceOpCommit, wlMsgWriter{}))
	s.mu.Lock()
	first := !s.configured
	s.configured = true
	s.mu.Unlock()
	if first {
		close(s.configuredCh)
	}
}

func (s *waylandSurface) handleToplevel(opcode uint16, p []byte) {
	switch opcode {
	case xdgToplevelEventConfigure:
		r := wlParser{p: p}
		w, err1 := r.int32()
		h, err2 := r.int32()
		_ = r.skipArray()
		if err1 != nil || err2 != nil {
			return
		}
		// Per the xdg-shell protocol: "If [width and height] are zero, it
		// means the client should decide its own window dimension". Real
		// compositors (GNOME Mutter, Sway) send exactly this on a floating
		// window's initial configure. Treating it as an error and dropping
		// the event (this function's previous behaviour) silently discarded
		// every such configure — including the very first one — instead of
		// confirming the client's own already-chosen size.
		s.mu.Lock()
		if w > 0 && h > 0 {
			s.width, s.height = int(w), int(h)
		}
		width, height := s.width, s.height
		s.mu.Unlock()
		s.emit(resizeEvent{Width: width, Height: height})
	case xdgToplevelEventClose:
		s.emit(closeEvent{})
	}
}

func (s *waylandSurface) emit(ev surfaceEvent) {
	s.enqueueEvent(ev)
}

const bytesPerPixelWl = 4

// Present converts img to the pixel layout negotiated with the compositor
// (ARGB8888 when advertised, since it is universally supported; XRGB8888
// otherwise) and attaches it through a rotating pair of wl_shm buffers.
func (s *waylandSurface) Present(img *image.NRGBA) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	return s.present(img, image.Rect(0, 0, w, h))
}

// PresentRegion attaches the full retained frame and reports damage only for r.
func (s *waylandSurface) PresentRegion(img *image.NRGBA, r image.Rectangle) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	r = r.Intersect(image.Rect(0, 0, w, h))
	if r.Empty() {
		return nil
	}
	return s.present(img, r)
}

func (s *waylandSurface) present(img *image.NRGBA, damage image.Rectangle) error {
	s.mu.Lock()
	hidden := s.hidden
	s.mu.Unlock()
	if hidden {
		return nil
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	stride := w * bytesPerPixelWl

	s.mu.Lock()
	needRealloc := s.buffers[0].mem == nil || s.buffers[0].width != w || s.buffers[0].height != h
	s.mu.Unlock()
	if needRealloc {
		if err := s.allocateBuffers(w, h, stride); err != nil {
			return err
		}
	}

	s.mu.Lock()
	idx := s.active
	if s.buffers[idx].busy && !s.buffers[1-idx].busy {
		idx = 1 - idx
	}
	if s.buffers[idx].busy {
		s.mu.Unlock()
		return errors.New("purego/wayland: both buffers busy, compositor has not released a frame")
	}
	buf := &s.buffers[idx]
	buf.busy = true
	s.active = 1 - idx
	s.mu.Unlock()

	// Converts directly into buf.mem, the memfd-backed buffer the
	// compositor reads from — no intermediate staging copy, same as the
	// X11 MIT-SHM path (x11.go's presentShm). convertRowRGBAToBGRX swaps
	// R and B one 32-bit word at a time instead of one byte at a time.
	src := img.Pix
	srcStride := img.Stride
	for y := 0; y < h; y++ {
		srow := src[y*srcStride : y*srcStride+w*4]
		drow := buf.mem[y*stride : y*stride+w*4]
		convertRowRGBAToBGRX(drow, srow, w)
	}

	if err := s.send(s.surfaceID, wlSurfaceOpAttach, func() wlMsgWriter {
		wm := wlMsgWriter{}
		wm.putUint32(buf.bufferID)
		wm.putInt32(0)
		wm.putInt32(0)
		return wm
	}()); err != nil {
		return err
	}
	dx, err := int32FromInt(damage.Min.X)
	if err != nil {
		return err
	}
	dy, err := int32FromInt(damage.Min.Y)
	if err != nil {
		return err
	}
	dw, err := int32FromInt(damage.Dx())
	if err != nil {
		return err
	}
	dh, err := int32FromInt(damage.Dy())
	if err != nil {
		return err
	}
	if err := s.send(s.surfaceID, wlSurfaceOpDamageBuffer, func() wlMsgWriter {
		wm := wlMsgWriter{}
		wm.putInt32(dx)
		wm.putInt32(dy)
		wm.putInt32(dw)
		wm.putInt32(dh)
		return wm
	}()); err != nil {
		return err
	}
	return s.send(s.surfaceID, wlSurfaceOpCommit, wlMsgWriter{})
}

func (s *waylandSurface) allocateBuffers(w, h, stride int) error {
	s.releaseBuffers()

	s.mu.Lock()
	format := wlShmFormatXrgb8888
	if s.haveArgb {
		format = wlShmFormatArgb8888
	}
	s.shmFormat = format
	s.mu.Unlock()

	size := stride * h
	for i := 0; i < waylandBufferCount; i++ {
		// MFD_CLOEXEC: without it, this fd survives into any child process
		// this application later forks/execs (os/exec or otherwise),
		// handing an unrelated process read/write access to the graphics
		// buffer's shared memory.
		fd, err := unix.MemfdCreate(fmt.Sprintf("purego-wayland-shm-%d", i), unix.MFD_CLOEXEC)
		if err != nil {
			s.releaseBuffers()
			return fmt.Errorf("purego/wayland: memfd_create: %w", err)
		}
		if err := unix.Ftruncate(fd, int64(size)); err != nil {
			_ = unix.Close(fd) // close is idempotent
			s.releaseBuffers()
			return fmt.Errorf("purego/wayland: ftruncate: %w", err)
		}
		mem, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			_ = unix.Close(fd) // close is idempotent
			s.releaseBuffers()
			return fmt.Errorf("purego/wayland: mmap: %w", err)
		}

		poolID := s.allocID()
		bufferID := s.allocID()
		size32, err := int32FromInt(size)
		if err != nil {
			_ = unix.Munmap(mem)
			_ = unix.Close(fd) // close is idempotent
			s.releaseBuffers()
			return err
		}
		w32, err := int32FromInt(w)
		if err != nil {
			_ = unix.Munmap(mem)
			_ = unix.Close(fd) // close is idempotent
			s.releaseBuffers()
			return err
		}
		h32, err := int32FromInt(h)
		if err != nil {
			_ = unix.Munmap(mem)
			_ = unix.Close(fd) // close is idempotent
			s.releaseBuffers()
			return err
		}
		stride32, err := int32FromInt(stride)
		if err != nil {
			_ = unix.Munmap(mem)
			_ = unix.Close(fd) // close is idempotent
			s.releaseBuffers()
			return err
		}
		poolReq := wlMsgWriter{}
		poolReq.putUint32(poolID)
		poolReq.putInt32(size32)
		if err := s.sendFD(s.shmID, wlShmOpCreatePool, poolReq, fd); err != nil {
			_ = unix.Munmap(mem)
			_ = unix.Close(fd) // close is idempotent
			s.releaseBuffers()
			return fmt.Errorf("purego/wayland: create_pool: %w", err)
		}
		// sendFD's SCM_RIGHTS transfer gives the compositor its own
		// duplicate of fd, independent of this process's copy, and mmap
		// above already mapped this process's view without needing fd to
		// stay open. Close it now instead of holding it until
		// releaseBuffers: this backend has no further use for it, and an
		// open descriptor it never touches again would leak a kernel
		// file descriptor per buffer.
		_ = unix.Close(fd) // close is idempotent
		bufReq := wlMsgWriter{}
		bufReq.putUint32(bufferID)
		bufReq.putInt32(0)
		bufReq.putInt32(w32)
		bufReq.putInt32(h32)
		bufReq.putInt32(stride32)
		bufReq.putUint32(format)
		if err := s.send(poolID, wlShmPoolOpCreateBuffer, bufReq); err != nil {
			// fd was already closed above; releaseBuffers must not be asked
			// to close it again (a double-close risks closing an unrelated
			// descriptor the kernel has since reused the same number for).
			_ = unix.Munmap(mem)
			s.releaseBuffers()
			return fmt.Errorf("purego/wayland: create_buffer: %w", err)
		}
		logErr("purego/wayland: wl_shm_pool.destroy", s.send(poolID, wlShmPoolOpDestroy, wlMsgWriter{}))

		s.mu.Lock()
		s.buffers[i] = waylandBuffer{bufferID: bufferID, poolID: poolID, mem: mem, width: w, height: h, stride: stride}
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.active = 0
	s.mu.Unlock()
	return nil
}

// releaseBuffers retires every current buffer, either destroying it right
// away (its mem/bufferID are cleared afterward, if it was not busy) or
// deferring that destruction to the wl_buffer.release event (see
// pendingBuffers) when the compositor may still be reading it to composite
// the frame currently on screen.
func (s *waylandSurface) releaseBuffers() {
	s.mu.Lock()
	bufs := s.buffers
	s.buffers = [waylandBufferCount]waylandBuffer{}
	var toDestroyNow []waylandBuffer
	for i := range bufs {
		if bufs[i].mem == nil && bufs[i].bufferID == 0 {
			continue // never allocated
		}
		if bufs[i].busy {
			s.pendingBuffers = append(s.pendingBuffers, bufs[i])
			continue
		}
		toDestroyNow = append(toDestroyNow, bufs[i])
	}
	s.mu.Unlock()
	for _, buf := range toDestroyNow {
		destroyWaylandBuffer(s, buf)
		if buf.fd != 0 {
			_ = unix.Close(buf.fd) // close is idempotent
		}
	}
}
