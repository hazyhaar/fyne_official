//go:build purego && linux

package purego

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"fyne.io/fyne/v2"
)

// X11 core protocol opcodes, atoms and event codes used by this backend.
// Values come from the X Window System core protocol specification.
const (
	opCreateWindow    = 1
	opMapWindow       = 8
	opUnmapWindow     = 10
	opConfigureWindow = 12
	opInternAtom      = 16
	opChangeProperty  = 18
	opCreateGC        = 55
	opPutImage        = 72
	opQueryExtension  = 98
	opDestroyWindow   = 4
	opSetInputFocus   = 42

	revertToParent  = 2
	timeCurrentTime = 0

	byteOrderLSBFirst = 0x6c
	protocolMajor     = 11

	depthTrueColor24       = 24
	windowClassInputOutput = 1

	cwBackPixmap        = 0x00000001
	cwBitGravity        = 0x00000010
	cwOverrideRedirect  = 0x00000200
	cwEventMask         = 0x00000800
	bitGravityNorthWest = 1

	eventMaskKeyPress        = 1
	eventMaskKeyRelease      = 2
	eventMaskButtonPress     = 4
	eventMaskButtonRelease   = 8
	eventMaskEnterWindow     = 16
	eventMaskLeaveWindow     = 32
	eventMaskPointerMotion   = 64
	eventMaskExposure        = 32768
	eventMaskStructureNotify = 131072
	eventMaskFocusChange     = 0x00200000

	configureValueX      = 0x0001
	configureValueY      = 0x0002
	configureValueWidth  = 0x0004
	configureValueHeight = 0x0008

	sizeHintPMinSize     = 16
	sizeHintPMaxSize     = 32
	sizeHintFieldCount   = 18
	sizeHintMinWidthOff  = 5
	sizeHintMinHeightOff = 6
	sizeHintMaxWidthOff  = 7
	sizeHintMaxHeightOff = 8
	atomWMSizeHints      = 41

	atomWMName      = 39
	atomSTRING      = 31
	atomATOM        = 4
	propModeReplace = 0
	propFormat8     = 8
	propFormat32    = 32

	imageFormatZPixmap = 2

	eventCodeKeyPress       = 2
	eventCodeKeyRelease     = 3
	eventCodeButtonPress    = 4
	eventCodeButtonRelease  = 5
	eventCodeMotionNotify   = 6
	eventCodeEnterNotify    = 7
	eventCodeLeaveNotify    = 8
	eventCodeFocusIn        = 9
	eventCodeFocusOut       = 10
	eventCodeExpose         = 12
	notifyModeNormal        = 0
	x11NotifyModeOffset     = 30
	notifyDetailPointer     = 5
	notifyDetailPointerRoot = 6

	x11ButtonWheelUp         = 4
	x11ButtonWheelDown       = 5
	x11ButtonWheelLeft       = 6
	x11ButtonWheelRight      = 7
	eventCodeDestroyNotify   = 17
	eventCodeConfigureNotify = 22
	eventCodeClientMessage   = 33
	eventCodeMask            = 0x7f

	changePropertyFormatOffset = 16
	// putImageDepthOffset is the depth byte's offset in a core PutImage
	// request header (opcode, format, length(2), drawable(4), gc(4),
	// width(2), height(2), dst-x(2), dst-y(2), left-pad(1), depth(1),
	// unused(2) = 24 bytes): left-pad is byte 20, depth is byte 21. This was
	// previously 20 (the left-pad byte), leaving depth at its zero value and
	// left-pad set to depthTrueColor24 (24) instead of 0 — latent because
	// this backend's own tests always had MIT-SHM available and never
	// exercised the PutImage fallback against a real server until the
	// horizontal-banding fix did.
	putImageDepthOffset = 21

	replyStatusError = 0
	replyStatusReply = 1

	setupHeaderLen = 8
	setupWordSize  = 4
	coreEventSize  = 32

	x11ErrorCodeOffset        = 1
	x11ErrorMajorOpcodeOffset = 10

	opGetKeyboardMapping = 101
	opGetModifierMapping = 119

	eventCodeMappingNotify = 34

	setupMinKeycodeOffset = 26
	setupMaxKeycodeOffset = 27

	x11ModifierCount      = 8
	x11LockModifierIndex  = 1
	keyboardMappingReqLen = 8
	modifierMappingReqLen = 4
	maxKeycodeCount       = 255
)

// Declare conformity with surface, so future backends (Wayland) can be
// selected behind the same window/driver code.
var (
	_ surface          = (*x11Surface)(nil)
	_ regionSurface    = (*x11Surface)(nil)
	_ hideSurface      = (*x11Surface)(nil)
	_ fixedSizeSurface = (*x11Surface)(nil)
	_ positionSurface  = (*x11Surface)(nil)
)

// x11Surface implements surface directly against the X11 wire protocol over
// a Unix domain socket, without any Xlib/XCB dependency.
type x11Surface struct {
	shmFallbackOnce sync.Once // logs the first MIT-SHM -> PutImage fallback
	pixels          []byte    // BGRX staging buffer reused across Present calls
	putImageBuf     []byte    // PutImage request buffer reused across bands/calls (see putImageBand)
	mu              sync.Mutex

	// screensMu/screensRandR* cache the RandR opcode Screens() needs.
	// screensMu is separate from mu: Screens() never touches surface state
	// mu protects.
	screensMu              sync.Mutex
	screensRandRChecked    bool
	screensRandRMajor      byte
	screensRandRFirstEvent byte
	screensRandROK         bool
	conn                   net.Conn

	idBase, idMask, nextID    uint32
	rootWindow, rootVisual    uint32
	rootWidthPx, rootHeightPx int
	rootWidthMM, rootHeightMM int

	winID, gcID   uint32
	width, height int

	wmDeleteWin     uint32
	normalHintsAtom uint32
	fixedSize       bool

	shm     *shmExtension
	shmSeg  *sysVShmSegment
	shmXID  uint32
	shmSize int

	events chan surfaceEvent
	// droppedErrors counts server errors that answered a fire-and-forget
	// request; tests assert it stays at zero.
	droppedErrors atomic.Int64
	wake          chan struct{}
	// readLoop never blocks on events: it appends to evQueue and pumpEvents
	// forwards to events. A bounded channel written by readLoop deadlocked
	// the driver: a request issued from the main loop (SHM re-attach after
	// a resize) waited for its reply while readLoop was stuck pushing a
	// ConfigureNotify into the full channel that only the main loop drains.
	evMu       sync.Mutex
	evQueue    []surfaceEvent
	evSignal   chan struct{}
	pumpOnce   sync.Once
	closeOnce  sync.Once
	done       chan struct{}
	repeatNext bool

	// seq is the 16-bit sequence number of the last request written on this
	// connection, matching the counter the X server maintains for the same
	// connection (every request increments it by one, mod 65536, including
	// requests this backend never waits on a reply for). It must only be
	// advanced under s.mu, by nextSeq, at the same time the request is
	// written, so the number recorded for a pending waiter always matches
	// the one the server echoes back in the reply or error it produces for
	// that exact request.
	seq uint16

	// pendingBySeq routes a reply or error to the request() call that is
	// waiting for it, keyed by the sequence number the server echoes back in
	// every reply and error (bytes 2:4 of the 32-byte header). A plain FIFO
	// misroutes whenever an error arrives for a fire-and-forget request sent
	// between two synchronous ones: the FIFO has no way to know that error
	// belongs to neither of the two waiters, and hands it to whichever
	// happens to be at the head, shifting every later reply by one. Keying
	// by sequence number instead lets deliverPending drop an error or reply
	// with no matching waiter (fire-and-forget request, or a request nobody
	// is blocked on) without disturbing anyone else's pending entry.
	pendingBySeq map[uint16]chan []byte

	// subs delivers copies of matching events from readLoop to callers that
	// need SelectionNotify, PropertyNotify and similar without opening a
	// second connection. readLoop is the only reader of s.conn; a second
	// Read on the same socket would steal bytes from it.
	subMu   sync.Mutex
	subNext uint64
	subs    map[uint64]*x11EventSub

	minKeycode, maxKeycode byte
	keysymsPer             int
	keysyms                []uint32
	modeSwitchMask         uint16
	numLockMask            uint16
	lockMeaning            uint32
	keymapLoadMu           sync.Mutex

	state x11SurfaceState
}

// eventChanCapacity bounds how many undelivered events a surface buffers
// before the reader goroutine blocks; it has no protocol meaning.
const eventChanCapacity = 256

// x11EventSubBuffer bounds how many unread copies one subscribeX11Event
// waiter may hold. A full buffer drops the event rather than blocking
// readLoop, which must keep draining the socket.
const x11EventSubBuffer = 256

// x11EventSub is one typed waiter registered with subscribeX11Event.
type x11EventSub struct {
	code   byte
	window uint32
	ch     chan []byte
}

// x11RequestTimeout bounds how long request() waits for a reply. A var so
// tests can shrink it instead of waiting out the real value.
var x11RequestTimeout = 5 * time.Second

// x11Option configures a single CreateWindow attribute at surface
// construction. Existing openX11 callers pass none and keep the previous
// decorated, window-manager-managed window.
type x11Option func(*x11OpenConfig)

type x11OpenConfig struct {
	overrideRedirect bool
	skipMap          bool
}

func withoutMap() x11Option {
	return func(c *x11OpenConfig) { c.skipMap = true }
}

// withOverrideRedirect creates a splash-style window the window manager
// does not reparent or decorate (X11 CWOverrideRedirect).
func withOverrideRedirect() x11Option {
	return func(c *x11OpenConfig) { c.overrideRedirect = true }
}

// newX11Surface opens an X11 surface of the requested size. Options such as
// withOverrideRedirect are applied at CreateWindow; omitting them matches
// openX11("", width, height).
func newX11Surface(width, height int, opts ...x11Option) (*x11Surface, error) {
	return openX11("", width, height, opts...)
}

// openX11 dials the X server named by the DISPLAY environment variable (or
// the display name given in dpyName, when non-empty), performs the
// connection handshake, creates a top-level window of the requested size and
// starts the background event-reading goroutine.
func openX11(dpyName string, width, height int, opts ...x11Option) (*x11Surface, error) {
	if dpyName == "" {
		dpyName = os.Getenv("DISPLAY")
	}
	if dpyName == "" {
		dpyName = ":0"
	}
	displayNum := "0"
	if strings.HasPrefix(dpyName, ":") {
		displayNum = strings.SplitN(strings.TrimPrefix(dpyName, ":"), ".", 2)[0]
	}
	sockPath := "/tmp/.X11-unix/X" + displayNum

	conn, err := net.Dial("unix", sockPath) //nolint:gosec // sockPath is /tmp/.X11-unix/X plus $DISPLAY, a local socket not a remote URL
	if err != nil {
		return nil, fmt.Errorf("purego/x11: cannot reach X server at %s: %w", sockPath, err)
	}

	s := &x11Surface{conn: conn, width: width, height: height, events: make(chan surfaceEvent, eventChanCapacity), wake: make(chan struct{}, 1), done: make(chan struct{}), pendingBySeq: make(map[uint16]chan []byte)}
	if err := s.handshake(displayNum); err != nil {
		conn.Close()
		return nil, err
	}
	shm, sent, _ := queryShmExtension(conn)
	s.shm = shm
	if sent16, err := card16FromInt(sent); err == nil {
		s.seq += sent16
	}
	// readLoop must be running before createWindow: createWindow calls
	// internAtom, which now waits for its reply through request()/
	// deliverPending (see their doc comments), and readLoop is the only
	// goroutine that ever reads s.conn once it starts.
	go s.readLoop()
	var cfg x11OpenConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if err := s.createWindow(width, height, cfg.overrideRedirect, cfg.skipMap); err != nil {
		conn.Close()
		return nil, err
	}
	if err := s.loadKeymap(); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// handshake performs the X11 connection setup exchange and records the
// identifiers this surface needs: the resource ID range assigned to this
// client, and the root window/visual of the first screen.
//
// It authenticates with the MIT-MAGIC-COOKIE-1 credential xvfb-run (and
// every other X server that does not disable access control) requires: the
// cookie lives in the file named by $XAUTHORITY (or ~/.Xauthority),
// selected by displayNum via lookupXauth (see xauth.go). Without a matching
// entry — no file, or a server that disables access control — the request
// carries an empty authorization name/data, exactly the unauthenticated
// setup this backend always sent before this file existed; servers that do
// not require authentication accept that request unchanged.
func (s *x11Surface) handshake(displayNum string) error {
	authName, authData, _ := lookupXauth(displayNum)
	nameLen, dataLen := len(authName), len(authData)
	namePad := (setupWordSize - nameLen%setupWordSize) % setupWordSize
	dataPad := (setupWordSize - dataLen%setupWordSize) % setupWordSize

	const handshakeHeaderLen = 12
	req := make([]byte, handshakeHeaderLen+nameLen+namePad+dataLen+dataPad)
	req[0] = byteOrderLSBFirst
	binary.LittleEndian.PutUint16(req[2:4], protocolMajor)
	nameLen16, err := card16FromInt(nameLen)
	if err != nil {
		return err
	}
	dataLen16, err := card16FromInt(dataLen)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(req[6:8], nameLen16)
	binary.LittleEndian.PutUint16(req[8:10], dataLen16)
	copy(req[handshakeHeaderLen:], authName)
	copy(req[handshakeHeaderLen+nameLen+namePad:], authData)
	if _, err := s.conn.Write(req); err != nil {
		return fmt.Errorf("purego/x11: handshake write: %w", err)
	}

	header := make([]byte, setupHeaderLen)
	if _, err := io.ReadFull(s.conn, header); err != nil {
		return fmt.Errorf("purego/x11: handshake header read: %w", err)
	}
	if header[0] != replyStatusReply {
		return fmt.Errorf("purego/x11: server refused connection setup (status %d)", header[0])
	}
	setupExtraWords := binary.LittleEndian.Uint16(header[6:8])
	if err := checkWireAllocBytes(uint64(setupExtraWords)*uint64(setupWordSize), maxX11ReplyBytes, "handshake body"); err != nil {
		return err
	}
	extra := make([]byte, intFromUint16(setupExtraWords)*setupWordSize)
	if _, err := io.ReadFull(s.conn, extra); err != nil {
		return fmt.Errorf("purego/x11: handshake body read: %w", err)
	}

	fields, err := x11ParseSetupExtra(extra)
	if err != nil {
		return err
	}
	s.idBase = fields.idBase
	s.idMask = fields.idMask
	s.rootWindow = fields.rootWindow
	s.rootVisual = fields.rootVisual
	s.minKeycode = fields.minKeycode
	s.maxKeycode = fields.maxKeycode
	s.rootWidthPx, s.rootHeightPx = fields.rootWidthPx, fields.rootHeightPx
	s.rootWidthMM, s.rootHeightMM = fields.rootWidthMM, fields.rootHeightMM
	return nil
}

type x11SetupFields struct {
	idBase, idMask, rootWindow, rootVisual               uint32
	minKeycode, maxKeycode                               byte
	rootWidthPx, rootHeightPx, rootWidthMM, rootHeightMM int
}

// x11ParseSetupExtra reads the resource-id range and first SCREEN from an
// X11 connection-setup extra block. Every slice is checked against len(extra)
// so a truncated or hostile extra returns an error instead of panicking.
func x11ParseSetupExtra(extra []byte) (x11SetupFields, error) {
	const (
		setupIDBaseOff      = 4
		setupIDMaskOff      = 8
		setupVendorLenOff   = 16
		setupNumFormatsOff  = 21
		setupPrefixMinLen   = 22
		screenRecordOffset  = 32
		formatRecordSize    = 8
		screenRootVisualOff = 32
		screenRootVisualEnd = 36
		screenRecordMinSize = 40
	)
	if len(extra) < setupPrefixMinLen {
		return x11SetupFields{}, errors.New("purego/x11: truncated connection setup")
	}
	out := x11SetupFields{
		idBase: binary.LittleEndian.Uint32(extra[setupIDBaseOff : setupIDBaseOff+setupWordSize]),
		idMask: binary.LittleEndian.Uint32(extra[setupIDMaskOff : setupIDMaskOff+setupWordSize]),
	}
	vendorLen := intFromUint16(binary.LittleEndian.Uint16(extra[setupVendorLenOff : setupVendorLenOff+2]))
	numFormats := int(extra[setupNumFormatsOff])
	vendorPad := (vendorLen + (setupWordSize - 1)) &^ (setupWordSize - 1)
	offset := screenRecordOffset + vendorPad + numFormats*formatRecordSize
	if offset < screenRecordOffset || offset > len(extra) || len(extra)-offset < screenRecordMinSize {
		return x11SetupFields{}, errors.New("purego/x11: truncated screen record in connection setup")
	}
	out.rootWindow = binary.LittleEndian.Uint32(extra[offset : offset+setupWordSize])
	out.rootVisual = binary.LittleEndian.Uint32(extra[offset+screenRootVisualOff : offset+screenRootVisualEnd])
	// SCREEN: width/height in pixels at 20/22, in millimetres at 24/26; all
	// inside screenRecordMinSize, already checked above.
	out.rootWidthPx = int(binary.LittleEndian.Uint16(extra[offset+20 : offset+22]))
	out.rootHeightPx = int(binary.LittleEndian.Uint16(extra[offset+22 : offset+24]))
	out.rootWidthMM = int(binary.LittleEndian.Uint16(extra[offset+24 : offset+26]))
	out.rootHeightMM = int(binary.LittleEndian.Uint16(extra[offset+26 : offset+28]))
	if len(extra) > setupMaxKeycodeOffset {
		out.minKeycode = extra[setupMinKeycodeOffset]
		out.maxKeycode = extra[setupMaxKeycodeOffset]
	}
	return out, nil
}

func (s *x11Surface) allocID() uint32 {
	s.nextID++
	return s.idBase | (s.nextID & s.idMask)
}

// nextSeq advances and returns this connection's sequence counter. The
// caller must hold s.mu and must call it in the same critical section that
// writes the request the number is being assigned to, so the counter this
// backend keeps never drifts from the one the X server maintains for the
// same connection: the server increments its counter for every request,
// including ones nobody here ever calls request() for.
func (s *x11Surface) nextSeq() uint16 {
	s.seq++
	return s.seq
}

// writeRequest advances the sequence counter and writes req as one atomic,
// locked operation, for every request this backend sends without waiting
// for a reply (SetTitle, Resize, RequestFocus, DestroyWindow, and the
// CreateWindow/CreateGC/ChangeProperty/MapWindow sequence). Routing pending
// replies and errors by sequence number (see pendingBySeq) only works if
// every request written on the connection — not just the ones request()
// waits on — advances the same counter at the moment it is written.
func (s *x11Surface) writeRequest(req []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq()
	_, err := s.conn.Write(req)
	return err
}

// request writes req and waits for the reply or error the server sends back
// for it, delivered by readLoop (see deliverPending). This is the only
// correct way to get a synchronous answer out of this surface: readLoop is
// the connection's single reader goroutine, so a second, independent read on
// s.conn races it. Whichever goroutine's Read() call happens to run first
// consumes whatever bytes are available on the stream next, without regard
// to X11's own message framing (a reply's length is only known after reading
// its first 32 bytes); the loser then blocks forever on a reply that already
// went to someone else, or misreads a fragment of one message as the start
// of the next.
func (s *x11Surface) request(req []byte) ([]byte, error) {
	ch := make(chan []byte, 1)
	s.mu.Lock()
	seq := s.nextSeq()
	s.pendingBySeq[seq] = ch
	_, err := s.conn.Write(req)
	s.mu.Unlock()
	if err != nil {
		s.mu.Lock()
		delete(s.pendingBySeq, seq)
		s.mu.Unlock()
		return nil, err
	}
	timer := time.NewTimer(x11RequestTimeout)
	defer timer.Stop()
	select {
	case data := <-ch:
		if data == nil {
			return nil, errors.New("purego/x11: connection closed while waiting for a reply")
		}
		return data, nil
	case <-s.done:
		return nil, errors.New("purego/x11: surface closed while waiting for a reply")
	case <-timer.C:
		s.mu.Lock()
		delete(s.pendingBySeq, seq)
		s.mu.Unlock()
		return nil, fmt.Errorf("purego/x11: timed out waiting for a reply after %s", x11RequestTimeout)
	}
}

// deliverPending routes one reply or error, already fully read by readLoop,
// to the request() call waiting for it, matched by the sequence number the
// server echoes back in bytes 2:4 of every reply and error header. An error
// or reply whose sequence number has no registered waiter — a fire-and-
// forget request such as SetTitle or Resize failing, or a reply arriving
// after request() already gave up — is logged and dropped rather than
// misrouted to a different, unrelated waiter: a plain FIFO (this method's
// earlier form) handed such an error to whichever call happened to be at the
// head of the queue, corrupting that call's result and, once the real reply
// for it arrived, permanently shifting every later reply by one.
func (s *x11Surface) deliverPending(seq uint16, data []byte) {
	s.mu.Lock()
	ch, ok := s.pendingBySeq[seq]
	if ok {
		delete(s.pendingBySeq, seq)
	}
	s.mu.Unlock()
	if ch != nil {
		ch <- data
		return
	}
	if !ok {
		if len(data) > x11ErrorMajorOpcodeOffset && data[0] == replyStatusError {
			s.droppedErrors.Add(1)
			fyne.LogError(fmt.Sprintf("purego/x11: error sequence %d code %d major opcode %d has no waiter, dropping", seq, data[x11ErrorCodeOffset], data[x11ErrorMajorOpcodeOffset]), nil)
			return
		}
		fyne.LogError(fmt.Sprintf("purego/x11: reply/error for sequence %d has no waiter, dropping", seq), nil)
	}
}

// failPending unblocks every request() call still waiting when the
// connection is lost, instead of leaving them blocked forever.
func (s *x11Surface) failPending() {
	s.mu.Lock()
	pending := s.pendingBySeq
	s.pendingBySeq = make(map[uint16]chan []byte)
	s.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

// subscribeX11Event delivers copies of events whose code and window match.
// window 0 matches any window for that code. The returned id is passed to
// unsubscribeX11Event. The channel is never written from readLoop if its
// buffer is full, so a slow waiter cannot stall the socket reader.
func (s *x11Surface) subscribeX11Event(code byte, window uint32) (uint64, <-chan []byte) {
	ch := make(chan []byte, x11EventSubBuffer)
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.subs == nil {
		s.subs = make(map[uint64]*x11EventSub)
	}
	s.subNext++
	id := s.subNext
	s.subs[id] = &x11EventSub{code: code, window: window, ch: ch}
	return id, ch
}

func (s *x11Surface) unsubscribeX11Event(id uint64) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.subs == nil {
		return
	}
	delete(s.subs, id)
}

func (s *x11Surface) dispatchX11Event(code byte, buf []byte) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, sub := range s.subs {
		if sub.code != code {
			continue
		}
		if sub.window != 0 && !x11EventMatchesWindow(code, buf, sub.window) {
			continue
		}
		ev := append([]byte(nil), buf[:coreEventSize]...)
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

const x11EventWindowPairBytes = 16

func x11EventMatchesWindow(code byte, buf []byte, window uint32) bool {
	if len(buf) < x11EventWindowPairBytes {
		return false
	}
	w1, w2 := x11EventWindows(code, buf)
	return window == w1 || (w2 != 0 && window == w2)
}

func x11EventWindows(code byte, buf []byte) (primary, secondary uint32) {
	switch code {
	case eventCodeSelectionRequest, eventCodeSelectionNotify, eventCodeSelectionClear:
		return binary.LittleEndian.Uint32(buf[8:12]), 0
	case eventCodePropertyNotify:
		return binary.LittleEndian.Uint32(buf[4:8]), 0
	default:
		return binary.LittleEndian.Uint32(buf[8:12]), binary.LittleEndian.Uint32(buf[12:16])
	}
}

func waitX11Event(ch <-chan []byte, done <-chan struct{}, d time.Duration) ([]byte, error) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case buf, ok := <-ch:
		if !ok {
			return nil, errors.New("purego/x11: event subscription closed")
		}
		return buf, nil
	case <-done:
		return nil, errors.New("purego/x11: surface closed while waiting for an event")
	case <-timer.C:
		return nil, fmt.Errorf("purego/x11: timed out waiting for an event after %s", d)
	}
}

func (s *x11Surface) internAtom(name string) (uint32, error) {
	const reqHeaderLen = 8
	nameLen := len(name)
	pad := (setupWordSize - nameLen%setupWordSize) % setupWordSize
	reqWords := (reqHeaderLen + nameLen + pad) / setupWordSize
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opInternAtom
	reqWords16, err := card16FromInt(reqWords)
	if err != nil {
		return 0, err
	}
	nameLen16, err := card16FromInt(nameLen)
	if err != nil {
		return 0, err
	}
	binary.LittleEndian.PutUint16(req[2:4], reqWords16)
	binary.LittleEndian.PutUint16(req[4:6], nameLen16)
	copy(req[8:], name)

	reply, err := s.request(req)
	if err != nil {
		return 0, fmt.Errorf("purego/x11: InternAtom(%s): %w", name, err)
	}
	if reply[0] != replyStatusReply {
		return 0, fmt.Errorf("purego/x11: InternAtom(%s) error status %d", name, reply[0])
	}
	return binary.LittleEndian.Uint32(reply[8:12]), nil
}

// createWindow issues CreateWindow, CreateGC and registers WM_DELETE_WINDOW.
// When skipMap is true the window is left unmapped so CenterOnScreen can
// ConfigureWindow before the first MapWindow.
func (s *x11Surface) createWindow(w, h int, overrideRedirect, skipMap bool) error {
	s.winID = s.allocID()
	s.gcID = s.allocID()

	const (
		createWindowFixedLen     = 32
		createWindowValueLen     = 12
		createWindowSplashValues = 16
		createWindowValuesOff    = 40
		createWindowSplashOff    = 44
	)
	valueMask := uint32(cwBackPixmap | cwBitGravity | cwEventMask)
	valueLen := createWindowValueLen
	if overrideRedirect {
		valueMask |= cwOverrideRedirect
		valueLen = createWindowSplashValues
	}
	reqWords := (createWindowFixedLen + valueLen) / setupWordSize
	create := make([]byte, createWindowFixedLen+valueLen)
	create[0] = opCreateWindow
	create[1] = depthTrueColor24
	reqWords16, err := card16FromInt(reqWords)
	if err != nil {
		return err
	}
	w16, err := card16FromInt(w)
	if err != nil {
		return err
	}
	h16, err := card16FromInt(h)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(create[2:4], reqWords16)
	binary.LittleEndian.PutUint32(create[4:8], s.winID)
	binary.LittleEndian.PutUint32(create[8:12], s.rootWindow)
	binary.LittleEndian.PutUint16(create[16:18], w16)
	binary.LittleEndian.PutUint16(create[18:20], h16)
	binary.LittleEndian.PutUint16(create[22:24], windowClassInputOutput)
	binary.LittleEndian.PutUint32(create[24:28], s.rootVisual)
	binary.LittleEndian.PutUint32(create[28:32], valueMask)
	binary.LittleEndian.PutUint32(create[36:40], bitGravityNorthWest)
	off := createWindowValuesOff
	if overrideRedirect {
		binary.LittleEndian.PutUint32(create[40:44], 1)
		off = createWindowSplashOff
	}
	binary.LittleEndian.PutUint32(create[off:off+4],
		eventMaskKeyPress|eventMaskKeyRelease|eventMaskButtonPress|eventMaskButtonRelease|
			eventMaskEnterWindow|eventMaskLeaveWindow|eventMaskPointerMotion|
			eventMaskExposure|eventMaskStructureNotify|eventMaskFocusChange|
			eventMaskPropertyChange)

	const createGCReqLen, createGCReqWords = 16, 4
	gc := make([]byte, createGCReqLen)
	gc[0] = opCreateGC
	binary.LittleEndian.PutUint16(gc[2:4], createGCReqWords)
	binary.LittleEndian.PutUint32(gc[4:8], s.gcID)
	binary.LittleEndian.PutUint32(gc[8:12], s.winID)

	wmProtocols, errP := s.internAtom("WM_PROTOCOLS")
	wmDelete, errD := s.internAtom("WM_DELETE_WINDOW")
	xdndAware, errXdnd := internXdndAtoms(s)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq()
	if _, err := s.conn.Write(create); err != nil {
		return fmt.Errorf("purego/x11: CreateWindow: %w", err)
	}
	s.nextSeq()
	if _, err := s.conn.Write(gc); err != nil {
		return fmt.Errorf("purego/x11: CreateGC: %w", err)
	}
	if errP == nil && errD == nil {
		const protoPropReqLen, protoPropReqWords = 28, 7
		prop := make([]byte, protoPropReqLen)
		prop[0] = opChangeProperty
		binary.LittleEndian.PutUint16(prop[2:4], protoPropReqWords)
		binary.LittleEndian.PutUint32(prop[4:8], s.winID)
		binary.LittleEndian.PutUint32(prop[8:12], wmProtocols)
		binary.LittleEndian.PutUint32(prop[12:16], atomATOM)
		prop[changePropertyFormatOffset] = propFormat32
		binary.LittleEndian.PutUint32(prop[20:24], 1)
		binary.LittleEndian.PutUint32(prop[24:28], wmDelete)
		s.nextSeq()
		if _, err := s.conn.Write(prop); err == nil {
			s.wmDeleteWin = wmDelete
		}
	}
	if errXdnd == nil {
		writeXdndAwareLocked(s, xdndAware)
	}
	if skipMap {
		return nil
	}
	const mapReqLen, mapReqWords = 8, 2
	mapReq := make([]byte, mapReqLen)
	mapReq[0] = opMapWindow
	binary.LittleEndian.PutUint16(mapReq[2:4], mapReqWords)
	binary.LittleEndian.PutUint32(mapReq[4:8], s.winID)
	s.nextSeq()
	if _, err := s.conn.Write(mapReq); err != nil {
		return fmt.Errorf("purego/x11: MapWindow: %w", err)
	}
	return nil
}

// SetTitle issues ChangeProperty(WM_NAME) with the given title.
func (s *x11Surface) SetTitle(title string) error {
	b := []byte(title)
	reqLen, err := x11ChangePropertyWords(len(b))
	if err != nil {
		return err
	}
	words, err := card16FromInt(reqLen)
	if err != nil {
		return err
	}
	nBytes, err := uint32FromInt(len(b))
	if err != nil {
		return err
	}
	req := make([]byte, reqLen*setupWordSize)
	req[0] = opChangeProperty
	binary.LittleEndian.PutUint16(req[2:4], words)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], atomWMName)
	binary.LittleEndian.PutUint32(req[12:16], atomSTRING)
	req[changePropertyFormatOffset] = propFormat8
	binary.LittleEndian.PutUint32(req[20:24], nBytes)
	copy(req[24:], b)

	return s.writeRequest(req)
}

// Resize issues ConfigureWindow (opcode 12) with the Width|Height value-mask.
func (s *x11Surface) Resize(width, height int) error {
	if width < minWindowDim || height < minWindowDim || width > maxWindowDim || height > maxWindowDim {
		return fmt.Errorf("purego/x11: refusing to resize window to %dx%d (allowed %d..%d)", width, height, minWindowDim, maxWindowDim)
	}
	const configureReqLen, configureReqWords = 20, 5
	req := make([]byte, configureReqLen)
	req[0] = opConfigureWindow
	binary.LittleEndian.PutUint16(req[2:4], configureReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], configureValueWidth|configureValueHeight)
	width32, err := uint32FromInt(width)
	if err != nil {
		return err
	}
	height32, err := uint32FromInt(height)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(req[12:16], width32)
	binary.LittleEndian.PutUint32(req[16:20], height32)

	if err := s.writeRequest(req); err != nil {
		return err
	}
	s.mu.Lock()
	s.width, s.height = width, height
	fixed := s.fixedSize
	s.mu.Unlock()
	if fixed {
		return s.writeNormalHints(width, height, width, height, sizeHintPMinSize|sizeHintPMaxSize)
	}
	return nil
}

func (s *x11Surface) ensureNormalHintsAtom() (uint32, error) {
	s.mu.Lock()
	atom := s.normalHintsAtom
	s.mu.Unlock()
	if atom != 0 {
		return atom, nil
	}
	atom, err := s.internAtom("WM_NORMAL_HINTS")
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.normalHintsAtom = atom
	s.mu.Unlock()
	return atom, nil
}

func (s *x11Surface) writeNormalHints(minW, minH, maxW, maxH int, flags uint32) error {
	atom, err := s.ensureNormalHintsAtom()
	if err != nil {
		return err
	}
	const headerWords = 6
	reqWords := headerWords + sizeHintFieldCount
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opChangeProperty
	binary.LittleEndian.PutUint16(req[2:4], uint16(reqWords))
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], atom)
	binary.LittleEndian.PutUint32(req[12:16], atomWMSizeHints)
	req[changePropertyFormatOffset] = propFormat32
	binary.LittleEndian.PutUint32(req[20:24], sizeHintFieldCount)
	put := func(i int, v uint32) {
		binary.LittleEndian.PutUint32(req[24+i*4:28+i*4], v)
	}
	minWu, err := uint32FromInt(minW)
	if err != nil {
		return err
	}
	minHu, err := uint32FromInt(minH)
	if err != nil {
		return err
	}
	maxWu, err := uint32FromInt(maxW)
	if err != nil {
		return err
	}
	maxHu, err := uint32FromInt(maxH)
	if err != nil {
		return err
	}
	put(0, flags)
	put(sizeHintMinWidthOff, minWu)
	put(sizeHintMinHeightOff, minHu)
	put(sizeHintMaxWidthOff, maxWu)
	put(sizeHintMaxHeightOff, maxHu)
	return s.writeRequest(req)
}

// SetFixedSize implements fixedSizeSurface: it writes ICCCM WM_NORMAL_HINTS
// with PMinSize|PMaxSize equal to the current size, and keeps those extents
// in sync from Resize for as long as the window stays fixed.
func (s *x11Surface) SetFixedSize(fixed bool) error {
	s.mu.Lock()
	s.fixedSize = fixed
	w, h := s.width, s.height
	s.mu.Unlock()
	if !fixed {
		return s.writeNormalHints(0, 0, 0, 0, 0)
	}
	return s.writeNormalHints(w, h, w, h, sizeHintPMinSize|sizeHintPMaxSize)
}

func (s *x11Surface) mapWindow() error {
	const mapReqLen, mapReqWords = 8, 2
	req := make([]byte, mapReqLen)
	req[0] = opMapWindow
	binary.LittleEndian.PutUint16(req[2:4], mapReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	return s.writeRequest(req)
}

func (s *x11Surface) unmapWindow() error {
	const unmapReqLen, unmapReqWords = 8, 2
	req := make([]byte, unmapReqLen)
	req[0] = opUnmapWindow
	binary.LittleEndian.PutUint16(req[2:4], unmapReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	return s.writeRequest(req)
}

// Hide implements hideSurface: UnmapWindow leaves the X window and the
// Fyne canvas intact so a later Show can MapWindow the same surface.
func (s *x11Surface) Hide() error { return s.unmapWindow() }

// Show implements hideSurface: MapWindow a surface previously unmapped by
// Hide, without creating a second native window.
func (s *x11Surface) Show() error { return s.mapWindow() }

// SetPosition implements positionSurface: ConfigureWindow with the X|Y
// value-mask. Called before the first Map when CenterOnScreen ran before
// Show (the window is unmapped, positioned, then mapped).
func (s *x11Surface) SetPosition(x, y int) error {
	const configureReqLen, configureReqWords = 20, 5
	req := make([]byte, configureReqLen)
	req[0] = opConfigureWindow
	binary.LittleEndian.PutUint16(req[2:4], configureReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], configureValueX|configureValueY)
	x32, err := int32FromInt(x)
	if err != nil {
		return err
	}
	y32, err := int32FromInt(y)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(req[12:16], uint32FromInt32Bits(x32))
	binary.LittleEndian.PutUint32(req[16:20], uint32FromInt32Bits(y32))
	return s.writeRequest(req)
}

func (s *x11Surface) Events() <-chan surfaceEvent { return s.events }

func (s *x11Surface) Wake() <-chan struct{} { return s.wake }

// RequestFocus issues SetInputFocus (opcode 42) for this window. It
// satisfies the optional focusSurface interface (see optional.go).
func (s *x11Surface) RequestFocus() error {
	const reqLen, reqWords = 12, 3
	req := make([]byte, reqLen)
	req[0] = opSetInputFocus
	req[1] = revertToParent
	binary.LittleEndian.PutUint16(req[2:4], reqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], timeCurrentTime)

	return s.writeRequest(req)
}

// Close issues DestroyWindow (opcode 4) before releasing local resources and
// closing the connection, so the server tears the window down immediately
// rather than relying on the implicit cleanup a client disconnect triggers.
func (s *x11Surface) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}

		const destroyReqLen, destroyReqWords = 8, 2
		destroy := make([]byte, destroyReqLen)
		destroy[0] = opDestroyWindow
		binary.LittleEndian.PutUint16(destroy[2:4], destroyReqWords)
		binary.LittleEndian.PutUint32(destroy[4:8], s.winID)

		s.mu.Lock()
		if s.conn != nil {
			s.nextSeq()
			_, _ = s.conn.Write(destroy) // best-effort: teardown, the socket may already be dead
			if s.shmXID != 0 && s.shm != nil {
				s.nextSeq()
				_ = s.shm.detach(s.conn, s.shmXID) // best-effort: teardown
			}
		}
		if s.shmSeg != nil {
			_ = s.shmSeg.Detach() // best-effort: teardown
		}
		s.mu.Unlock()

		stopRandRWatch(s)
		s.releaseAttached()

		if s.conn != nil {
			err = s.conn.Close()
		}
	})
	return err
}

// readLoop decodes fixed 32-byte core protocol events and translates them
// into surfaceEvent values: KeyPress(2)/KeyRelease(3),
// ButtonPress(4)/ButtonRelease(5), MotionNotify(6), Expose(12),
// ConfigureNotify(22), DestroyNotify(17), ClientMessage(33).
// enqueueEvent hands an event from readLoop to pumpEvents without ever
// blocking the socket reader.
func (s *x11Surface) enqueueEvent(ev surfaceEvent) {
	s.evMu.Lock()
	s.evQueue = append(s.evQueue, ev)
	sig := s.signalLocked()
	s.evMu.Unlock()
	select {
	case sig <- struct{}{}:
	default:
	}
}

// signalLocked returns the wake-up channel of pumpEvents, creating it on
// first use so surfaces built directly in tests need no extra setup.
func (s *x11Surface) signalLocked() chan struct{} {
	if s.evSignal == nil {
		s.evSignal = make(chan struct{}, 1)
	}
	return s.evSignal
}

// pumpEvents forwards queued events to the events channel the window
// drains; it is the only goroutine allowed to block on that channel.
func (s *x11Surface) pumpEvents() {
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
			select {
			case <-sig:
				continue
			case <-s.done:
				return
			}
		}
		select {
		case s.events <- ev:
			signalWake(s.wake)
		case <-s.done:
			return
		}
	}
}

func (s *x11Surface) readLoop() {
	s.pumpOnce.Do(func() { go s.pumpEvents() })
	buf := make([]byte, coreEventSize)
	for {
		if _, err := io.ReadFull(s.conn, buf); err != nil {
			s.failPending()
			s.enqueueEvent(closeEvent{})
			return
		}
		if buf[0] == replyStatusError {
			// Errors are always exactly 32 bytes: no length field to drain.
			// The sequence number lives in bytes 2:4 of every error header,
			// the same offset as in a reply.
			s.deliverPending(binary.LittleEndian.Uint16(buf[2:4]), append([]byte(nil), buf...))
			continue
		}
		if buf[0] == replyStatusReply {
			// A reply's true length is only known from its own header: words
			// 4:8 give the length of data AFTER the fixed 32-byte header, in
			// 4-byte words (zero for a fixed-size reply like InternAtom's,
			// non-zero for a variable one like GetImage's pixel data). That
			// trailing data must be read here, by the connection's one and
			// only reader, before the next iteration — leaving it on the
			// wire would desynchronize every later read, corrupting whatever
			// this loop decodes next as an event or another reply.
			extraBytes, extraErr := x11ReplyExtraBytes(buf)
			if extraErr != nil {
				fyne.LogError("purego/x11: closing connection", extraErr)
				s.failPending()
				_ = s.conn.Close() // close is idempotent
				s.enqueueEvent(closeEvent{})
				return
			}
			full := append([]byte(nil), buf...)
			if extraBytes > 0 {
				extra := make([]byte, extraBytes)
				if _, readErr := io.ReadFull(s.conn, extra); readErr != nil {
					s.failPending()
					s.enqueueEvent(closeEvent{})
					return
				}
				full = append(full, extra...)
			}
			s.deliverPending(binary.LittleEndian.Uint16(buf[2:4]), full)
			continue
		}
		eventCode := buf[0] & eventCodeMask
		s.dispatchX11Event(eventCode, buf)
		if eventCode == eventCodeKeyRelease {
			peek := make([]byte, coreEventSize)
			if peekX11Event(s.conn, peek) && x11KeyIsAutoRepeat(buf, peek) {
				s.repeatNext = true
				continue
			}
		}
		var ev surfaceEvent
		switch eventCode {
		case eventCodeKeyPress, eventCodeKeyRelease:
			scancode := uint32(buf[1])
			state := binary.LittleEndian.Uint16(buf[28:30])
			s.mu.Lock()
			ks := s.lookupKeysymLocked(buf[1], state)
			s.mu.Unlock()
			name, r := keysymToKey(ks)
			repeat := false
			if eventCode == eventCodeKeyPress && s.repeatNext {
				repeat = true
				s.repeatNext = false
			}
			ev = keyEvent{
				Pressed:   eventCode == eventCodeKeyPress,
				Repeat:    repeat,
				Scancode:  scancode,
				Rune:      r,
				Modifiers: decodeModifiers(state),
				KeySym:    ks,
				Name:      name,
			}
		case eventCodeMappingNotify:
			go func() { logErr("purego/x11: reload keymap", s.loadKeymap()) }()
			continue
		case eventCodeButtonPress, eventCodeButtonRelease:
			ev = decodeX11Button(buf, eventCode == eventCodeButtonPress)
		case eventCodeMotionNotify:
			ev = decodeX11Motion(buf, false)
		case eventCodeEnterNotify, eventCodeLeaveNotify:
			if buf[x11NotifyModeOffset] != notifyModeNormal {
				continue
			}
			ev = decodeX11Motion(buf, eventCode == eventCodeLeaveNotify)
		case eventCodeFocusIn, eventCodeFocusOut:
			if buf[1] == notifyDetailPointer || buf[1] == notifyDetailPointerRoot {
				continue
			}
			ev = focusEvent{Gained: eventCode == eventCodeFocusIn}
		case eventCodeExpose:
			ev = exposeEvent{
				X:      int(binary.LittleEndian.Uint16(buf[8:10])),
				Y:      int(binary.LittleEndian.Uint16(buf[10:12])),
				Width:  int(binary.LittleEndian.Uint16(buf[12:14])),
				Height: int(binary.LittleEndian.Uint16(buf[14:16])),
			}
		case eventCodeConfigureNotify:
			w := int(binary.LittleEndian.Uint16(buf[20:22]))
			h := int(binary.LittleEndian.Uint16(buf[22:24]))
			s.mu.Lock()
			s.width, s.height = w, h
			s.mu.Unlock()
			ev = resizeEvent{Width: w, Height: h}
		case eventCodeDestroyNotify:
			ev = closeEvent{}
		case eventCodeClientMessage:
			ev = s.clientMessageEvent(buf)
		default:
			continue
		}
		if ev == nil {
			continue
		}
		s.enqueueEvent(ev)
	}
}

func (s *x11Surface) clientMessageEvent(buf []byte) surfaceEvent {
	if handleXdndClientMessage(s, buf) {
		return nil
	}
	const clientMessageMinLen = 16
	if len(buf) >= clientMessageMinLen {
		atom0 := binary.LittleEndian.Uint32(buf[12:16])
		if s.wmDeleteWin != 0 && atom0 == s.wmDeleteWin {
			return closeEvent{}
		}
	}
	return nil
}

func x11KeyIsAutoRepeat(release, next []byte) bool {
	if len(release) < coreEventSize || len(next) < coreEventSize {
		return false
	}
	if release[0]&eventCodeMask != eventCodeKeyRelease {
		return false
	}
	if next[0]&eventCodeMask != eventCodeKeyPress {
		return false
	}
	if release[1] != next[1] {
		return false
	}
	return binary.LittleEndian.Uint32(release[4:8]) == binary.LittleEndian.Uint32(next[4:8])
}

func peekX11Event(conn net.Conn, dst []byte) bool {
	if len(dst) < coreEventSize {
		return false
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var n int
	var sysErr error
	if err := raw.Read(func(fd uintptr) bool {
		n, _, sysErr = unix.Recvfrom(int(fd), dst[:coreEventSize], unix.MSG_PEEK|unix.MSG_DONTWAIT)
		return true
	}); err != nil {
		return false
	}
	return sysErr == nil && n >= coreEventSize
}

func decodeX11EventXY(buf []byte) (x, y int) {
	return intFromWireINT16(binary.LittleEndian.Uint16(buf[24:26])), intFromWireINT16(binary.LittleEndian.Uint16(buf[26:28]))
}

func decodeX11Button(buf []byte, pressed bool) surfaceEvent {
	x, y := decodeX11EventXY(buf)
	button := int(buf[1])
	if button >= x11ButtonWheelUp && button <= x11ButtonWheelRight {
		if !pressed {
			return nil
		}
		return decodeX11Scroll(button, x, y)
	}
	return pointerEvent{
		Button:    button,
		X:         x,
		Y:         y,
		Pressed:   pressed,
		Modifiers: decodeModifiers(binary.LittleEndian.Uint16(buf[28:30])),
	}
}

func decodeX11Scroll(button, x, y int) surfaceEvent {
	var dx, dy float32
	switch button {
	case x11ButtonWheelUp:
		dy = 1
	case x11ButtonWheelDown:
		dy = -1
	case x11ButtonWheelLeft:
		dx = 1
	case x11ButtonWheelRight:
		dx = -1
	}
	return scrollEvent{X: x, Y: y, DX: dx, DY: dy}
}

func decodeX11Motion(buf []byte, left bool) surfaceEvent {
	x, y := decodeX11EventXY(buf)
	return pointerEvent{
		X:         x,
		Y:         y,
		Moved:     !left,
		Left:      left,
		Modifiers: decodeModifiers(binary.LittleEndian.Uint16(buf[28:30])),
	}
}

func decodeModifiers(state uint16) keyModifiers {
	var m keyModifiers
	if state&modifierShift != 0 {
		m |= modShift
	}
	if state&modifierLock != 0 {
		m |= modLock
	}
	if state&modifierControl != 0 {
		m |= modControl
	}
	if state&modifierAlt != 0 {
		m |= modAlt
	}
	if state&modifierSuper != 0 {
		m |= modSuper
	}
	return m
}

func (s *x11Surface) loadKeymap() error {
	s.keymapLoadMu.Lock()
	defer s.keymapLoadMu.Unlock()
	per, syms, err := s.fetchKeyboardMapping()
	if err != nil {
		return err
	}
	modeMask, numMask, lockMeaning, err := s.fetchModifierMapping(per, syms)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.keysymsPer = per
	s.keysyms = syms
	s.modeSwitchMask = modeMask
	s.numLockMask = numMask
	s.lockMeaning = lockMeaning
	s.mu.Unlock()
	return nil
}

func (s *x11Surface) fetchKeyboardMapping() (int, []uint32, error) {
	minKc, maxKc := s.minKeycode, s.maxKeycode
	if maxKc < minKc {
		return 0, nil, errors.New("purego/x11: invalid keycode range")
	}
	count := int(maxKc) - int(minKc) + 1
	if count > maxKeycodeCount {
		count = maxKeycodeCount
	}
	const reqWords = 2
	req := make([]byte, keyboardMappingReqLen)
	req[0] = opGetKeyboardMapping
	binary.LittleEndian.PutUint16(req[2:4], reqWords)
	req[4] = minKc
	req[5] = byte(count)
	reply, err := s.request(req)
	if err != nil {
		return 0, nil, fmt.Errorf("purego/x11: GetKeyboardMapping: %w", err)
	}
	if len(reply) < coreEventSize || reply[0] != replyStatusReply {
		return 0, nil, errors.New("purego/x11: GetKeyboardMapping: bad reply")
	}
	per := int(reply[1])
	if per == 0 {
		return 0, nil, errors.New("purego/x11: GetKeyboardMapping: zero keysyms-per-keycode")
	}
	n, err := intFromUint32(binary.LittleEndian.Uint32(reply[4:8]))
	if err != nil {
		return 0, nil, fmt.Errorf("purego/x11: GetKeyboardMapping: %w", err)
	}
	if n == 0 {
		return 0, nil, errors.New("purego/x11: GetKeyboardMapping: empty keysym list")
	}
	data := reply[coreEventSize:]
	if n > len(data)/setupWordSize {
		return 0, nil, errors.New("purego/x11: GetKeyboardMapping: truncated keysyms")
	}
	syms := make([]uint32, n)
	for i := 0; i < n; i++ {
		off := i * setupWordSize
		if off+setupWordSize > len(data) {
			return 0, nil, errors.New("purego/x11: GetKeyboardMapping: truncated keysyms")
		}
		syms[i] = binary.LittleEndian.Uint32(data[off : off+setupWordSize])
	}
	return per, syms, nil
}

func (s *x11Surface) fetchModifierMapping(per int, syms []uint32) (modeMask, numMask uint16, lockMeaning uint32, err error) {
	const reqWords = 1
	req := make([]byte, modifierMappingReqLen)
	req[0] = opGetModifierMapping
	binary.LittleEndian.PutUint16(req[2:4], reqWords)
	reply, err := s.request(req)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("purego/x11: GetModifierMapping: %w", err)
	}
	if len(reply) < coreEventSize || reply[0] != replyStatusReply {
		return 0, 0, 0, errors.New("purego/x11: GetModifierMapping: bad reply")
	}
	perMod := int(reply[1])
	codes := reply[coreEventSize:]
	need := x11ModifierCount * perMod
	if perMod == 0 || len(codes) < need {
		return 0, 0, 0, errors.New("purego/x11: GetModifierMapping: truncated keycodes")
	}
	for i := 0; i < x11ModifierCount; i++ {
		mask := uint16(1 << i)
		for j := 0; j < perMod; j++ {
			kc := codes[i*perMod+j]
			if kc == 0 {
				continue
			}
			for _, ks := range keysymsOf(s.minKeycode, per, syms, kc) {
				switch ks {
				case xkModeSwitch, xkISOLevel3Shift:
					modeMask |= mask
				case xkNumLock:
					numMask |= mask
				case xkCapsLock:
					if i == x11LockModifierIndex {
						lockMeaning = xkCapsLock
					}
				case xkShiftLock:
					if i == x11LockModifierIndex {
						lockMeaning = xkShiftLock
					}
				}
			}
		}
	}
	return modeMask, numMask, lockMeaning, nil
}

func keysymsOf(minKc byte, per int, all []uint32, kc byte) [coreKeysymCols]uint32 {
	var out [coreKeysymCols]uint32
	if per <= 0 || kc < minKc {
		return out
	}
	idx := int(kc-minKc) * per
	n := per
	if n > coreKeysymCols {
		n = coreKeysymCols
	}
	for i := 0; i < n && idx+i < len(all); i++ {
		out[i] = all[idx+i]
	}
	return out
}

func (s *x11Surface) lookupKeysymLocked(keycode byte, state uint16) uint32 {
	if s.keysymsPer == 0 || len(s.keysyms) == 0 {
		return 0
	}
	if keycode < s.minKeycode || keycode > s.maxKeycode {
		return 0
	}
	return selectCoreKeysym(keysymsOf(s.minKeycode, s.keysymsPer, s.keysyms, keycode), state, s.modeSwitchMask, s.numLockMask, s.lockMeaning)
}

const bytesPerPixel = 4

// convertNRGBAToBGRX rewrites img's pixels, row by row, from image.NRGBA's
// byte order into the BGRX/BGRA byte order the X server's depth-24
// TrueColor drawable expects, into dst (which must hold at least
// w*h*bytesPerPixel bytes and may be the shared-memory segment PutImage or
// MIT-SHM ultimately hands the server — see presentShm — rather than an
// intermediate staging buffer).
func convertNRGBAToBGRX(dst []byte, img *image.NRGBA, w, h int) {
	src := img.Pix
	stride := img.Stride
	for y := 0; y < h; y++ {
		row := src[y*stride : y*stride+w*bytesPerPixel]
		out := dst[y*w*bytesPerPixel : (y+1)*w*bytesPerPixel]
		convertRowRGBAToBGRX(out, row, w)
	}
}

func convertNRGBARegionToBGRX(dst []byte, img *image.NRGBA, dstStride int, r image.Rectangle) {
	src := img.Pix
	srcStride := img.Stride
	x0, y0 := r.Min.X, r.Min.Y
	rw, rh := r.Dx(), r.Dy()
	for y := 0; y < rh; y++ {
		row := src[(y0+y)*srcStride+x0*bytesPerPixel : (y0+y)*srcStride+(x0+rw)*bytesPerPixel]
		out := dst[(y0+y)*dstStride+x0*bytesPerPixel : (y0+y)*dstStride+(x0+rw)*bytesPerPixel]
		convertRowRGBAToBGRX(out, row, rw)
	}
}

func convertNRGBARegionPacked(dst []byte, img *image.NRGBA, r image.Rectangle) {
	src := img.Pix
	srcStride := img.Stride
	rw := r.Dx()
	dstStride := rw * bytesPerPixel
	for y := 0; y < r.Dy(); y++ {
		row := src[(r.Min.Y+y)*srcStride+r.Min.X*bytesPerPixel : (r.Min.Y+y)*srcStride+(r.Min.X+rw)*bytesPerPixel]
		out := dst[y*dstStride : (y+1)*dstStride]
		convertRowRGBAToBGRX(out, row, rw)
	}
}

// Present converts img to the 32-bit BGRX pixel layout the X server expects
// for a depth-24 TrueColor drawable and pushes it to the window, through
// MIT-SHM when available and by core PutImage otherwise. The MIT-SHM path
// (presentShm) converts directly into the shared-memory segment; only the
// PutImage fallback below needs its own staging buffer, since building that
// wire request needs a contiguous byte slice to copy into the request
// regardless.
func (s *x11Surface) Present(img *image.NRGBA) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()

	if s.shm != nil && s.shm.available {
		err := s.presentShm(img, w, h, image.Rect(0, 0, w, h))
		if err == nil {
			return nil
		}
		// Falling back to PutImage keeps the window alive but silently
		// changes what is being measured and costs a full-frame copy per
		// present: say so once, so a saturated kernel SHM table or a server
		// refusing attachments does not go unnoticed.
		s.shmFallbackOnce.Do(func() {
			fyne.LogError("purego/x11: MIT-SHM present failed, falling back to PutImage for this surface", err)
		})
	}

	needed := w * h * bytesPerPixel
	// The BGRX staging buffer is kept between frames: a full-frame allocation
	// per Present showed up as the dominant per-frame garbage in the frame
	// pipeline measurements, at no benefit since the buffer is fully
	// rewritten on every call.
	if cap(s.pixels) < needed {
		s.pixels = make([]byte, needed)
	}
	pixels := s.pixels[:needed]
	convertNRGBAToBGRX(pixels, img, w, h)
	return s.presentPutImage(pixels, w, h, 0, 0)
}

// PresentRegion uploads only rectangle r of the full retained frame img.
func (s *x11Surface) PresentRegion(img *image.NRGBA, r image.Rectangle) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	r = r.Intersect(image.Rect(0, 0, w, h))
	if r.Empty() {
		return nil
	}
	if r.Min.X == 0 && r.Min.Y == 0 && r.Dx() == w && r.Dy() == h {
		return s.Present(img)
	}

	if s.shm != nil && s.shm.available {
		err := s.presentShm(img, w, h, r)
		if err == nil {
			return nil
		}
		s.shmFallbackOnce.Do(func() {
			fyne.LogError("purego/x11: MIT-SHM present failed, falling back to PutImage for this surface", err)
		})
	}

	needed := r.Dx() * r.Dy() * bytesPerPixel
	if cap(s.pixels) < needed {
		s.pixels = make([]byte, needed)
	}
	pixels := s.pixels[:needed]
	convertNRGBARegionPacked(pixels, img, r)
	return s.presentPutImage(pixels, r.Dx(), r.Dy(), r.Min.X, r.Min.Y)
}

// presentShm reads and mutates s.shmSeg/s.shmXID/s.shmSize and writes
// shm.attach/detach/putImage requests on s.conn under s.mu (like Close's own
// access to the same fields), except while it waits for the server to
// acknowledge a new segment's attachment (see shmAttachAndConfirm): holding
// the lock across a network round trip would stall every unrelated call
// touching s.mu (SetTitle, Resize, Close) for as long as that round trip
// takes.
//
// It converts img's pixels directly into the shared-memory segment instead
// of into an intermediate staging buffer later copied into the segment: the
// previous two-pass version (convert into s.pixels, then copy(s.shmSeg.
// Slice(), pixels)) moved every frame's bytes through memory twice, doubling
// the memory bandwidth this hot path consumes for no functional reason.
func (s *x11Surface) presentShm(img *image.NRGBA, w, h int, region image.Rectangle) error {
	needed := w * h * bytesPerPixel
	s.mu.Lock()
	needsRealloc := s.shmSeg == nil || s.shmSize < needed
	var oldSeg *sysVShmSegment
	var oldXID uint32
	if needsRealloc {
		oldSeg, oldXID = s.shmSeg, s.shmXID
		s.shmSeg, s.shmXID, s.shmSize = nil, 0, 0
	}
	s.mu.Unlock()

	if needsRealloc {
		if oldSeg != nil {
			s.mu.Lock()
			s.nextSeq()
			logErr("purego/x11: detach old MIT-SHM", s.shm.detach(s.conn, oldXID))
			s.mu.Unlock()
			logErr("purego/x11: shmdt old segment", oldSeg.Detach())
		}
		seg, err := allocSysVShm(needed)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.nextID++
		xid := s.idBase | (s.nextID & s.idMask)
		s.mu.Unlock()

		if err := s.shmAttachAndConfirm(xid, seg.ShmID); err != nil {
			logErr("purego/x11: shmdt after failed ShmAttach", seg.Detach())
			return err
		}
		s.mu.Lock()
		s.shmSeg, s.shmXID, s.shmSize = seg, xid, needed
		s.mu.Unlock()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	full := region.Min.X == 0 && region.Min.Y == 0 && region.Dx() == w && region.Dy() == h
	if full || needsRealloc {
		convertNRGBAToBGRX(s.shmSeg.Slice(), img, w, h)
	} else {
		convertNRGBARegionToBGRX(s.shmSeg.Slice(), img, w*bytesPerPixel, region)
	}
	s.nextSeq()
	if full {
		return s.shm.putImage(s.conn, s.winID, s.gcID, w, h, s.shmXID, 0)
	}
	return s.shmPutImageRegion(w, h, region)
}

func (s *x11Surface) shmPutImageRegion(totalW, totalH int, r image.Rectangle) error {
	req := make([]byte, shmPutImageReqLen)
	req[0] = s.shm.majorOpcode
	req[1] = shmMinorPutImage
	binary.LittleEndian.PutUint16(req[2:4], shmPutImageReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], s.gcID)
	totalW16, err := card16FromInt(totalW)
	if err != nil {
		return err
	}
	totalH16, err := card16FromInt(totalH)
	if err != nil {
		return err
	}
	x16, err := card16FromInt(r.Min.X)
	if err != nil {
		return err
	}
	y16, err := card16FromInt(r.Min.Y)
	if err != nil {
		return err
	}
	dw16, err := card16FromInt(r.Dx())
	if err != nil {
		return err
	}
	dh16, err := card16FromInt(r.Dy())
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(req[12:14], totalW16)
	binary.LittleEndian.PutUint16(req[14:16], totalH16)
	binary.LittleEndian.PutUint16(req[16:18], x16)
	binary.LittleEndian.PutUint16(req[18:20], y16)
	binary.LittleEndian.PutUint16(req[20:22], dw16)
	binary.LittleEndian.PutUint16(req[22:24], dh16)
	binary.LittleEndian.PutUint16(req[24:26], x16)
	binary.LittleEndian.PutUint16(req[26:28], y16)
	req[shmPutImageDepthOffset] = depthTrueColor24
	req[shmPutImageFormatOffset] = imageFormatZPixmap
	req[shmPutImageSendEventOffset] = sendEventFalse
	binary.LittleEndian.PutUint32(req[32:36], s.shmXID)
	binary.LittleEndian.PutUint32(req[36:40], 0)
	_, err = s.conn.Write(req)
	return err
}

// shmAttachAndConfirm issues ShmAttach for shmid/xid and then forces a
// synchronous round trip (an ordinary InternAtom via request()) before
// returning: X11 guarantees requests on one connection are processed by the
// server strictly in the order they were sent, so receiving that round
// trip's reply guarantees the server has already processed ShmAttach,
// whether it succeeded or produced an error. Without this confirmation, the
// segment could be marked IPC_RMID (see presentShm) before the server has
// attached it at all.
func (s *x11Surface) shmAttachAndConfirm(xid uint32, shmid int) error {
	s.mu.Lock()
	s.nextSeq()
	err := s.shm.attach(s.conn, xid, shmid)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("purego/x11: ShmAttach: %w", err)
	}
	if _, err := s.internAtom("PUREGO_X11_SHM_ATTACH_SYNC"); err != nil {
		return fmt.Errorf("purego/x11: confirming ShmAttach was processed: %w", err)
	}
	return nil
}

// maxCoreRequestWords is the hard ceiling the X11 core protocol places on
// any request's length field (a CARD16): a request whose total size in
// 4-byte words exceeds this cannot be expressed without the BIG-REQUESTS
// extension, which this backend does not negotiate. presentPutImage stays
// under this ceiling by splitting the image into horizontal bands instead.
const maxCoreRequestWords = 65535

// presentPutImage falls back to the core PutImage request (opcode 72) when
// MIT-SHM is unavailable. A single PutImage covering the whole image
// overflowed its own CARD16 length field for anything larger than roughly
// 256x256 pixels at 32bpp: the truncated length
// this backend wrote made the server read a short request as PutImage and
// then misinterpret the remaining pixel bytes still on the wire as further
// requests, corrupting the connection. Splitting into horizontal bands, each
// its own PutImage request comfortably under maxCoreRequestWords, keeps
// every request's true size and its CARD16 length field in agreement
// regardless of window size.
// putImageLeftPadOffset is the offset of the two unused PutImage header bytes
// (left-pad and padding) that follow the depth byte.
const putImageLeftPadOffset = 22

func (s *x11Surface) presentPutImage(pixels []byte, w, h, dstX, dstY int) error {
	const putImageHeaderLen = 24
	rowBytes := w * bytesPerPixel
	if rowBytes == 0 || h == 0 {
		return nil
	}
	maxDataBytes := maxCoreRequestWords*setupWordSize - putImageHeaderLen
	rowsPerReq := maxDataBytes / rowBytes
	if rowsPerReq < 1 {
		return fmt.Errorf("purego/x11: window row (%d bytes) too wide for a single core PutImage request and BIG-REQUESTS is not negotiated", rowBytes)
	}
	for y0 := 0; y0 < h; y0 += rowsPerReq {
		rows := rowsPerReq
		if y0+rows > h {
			rows = h - y0
		}
		if err := s.putImageBand(pixels[y0*rowBytes:(y0+rows)*rowBytes], w, rows, dstX, dstY+y0); err != nil {
			return err
		}
	}
	return nil
}

// putImageBand issues one core PutImage request for a single horizontal
// band of the image, starting at destination row y0. Its request buffer
// (s.putImageBuf) is kept and reused across bands and across Present calls,
// the same reuse discipline as the BGRX staging buffer (s.pixels): a fresh
// make() of the whole band's bytes on every frame was measured as this
// path's dominant per-frame garbage.
func (s *x11Surface) putImageBand(bandPixels []byte, w, rows, dstX, y0 int) error {
	const putImageHeaderLen = 24
	reqLen := putImageHeaderLen + len(bandPixels)
	pad := (setupWordSize - reqLen%setupWordSize) % setupWordSize
	total := reqLen + pad
	totalWords := total / setupWordSize
	if totalWords > maxCoreRequestWords {
		// Defense in depth: presentPutImage's rowsPerReq computation already
		// guarantees this never triggers, but a request this large must
		// never be allowed to reach uint16() below and wrap silently.
		return fmt.Errorf("purego/x11: PutImage band of %d words exceeds the %d-word CARD16 limit", totalWords, maxCoreRequestWords)
	}
	if cap(s.putImageBuf) < total {
		s.putImageBuf = make([]byte, total)
	}
	req := s.putImageBuf[:total]
	req[0] = opPutImage
	req[1] = imageFormatZPixmap
	totalWords16, err := card16FromInt(totalWords)
	if err != nil {
		return err
	}
	w16, err := card16FromInt(w)
	if err != nil {
		return err
	}
	rows16, err := card16FromInt(rows)
	if err != nil {
		return err
	}
	dstX16, err := card16FromInt(dstX)
	if err != nil {
		return err
	}
	y016, err := card16FromInt(y0)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(req[2:4], totalWords16)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], s.gcID)
	binary.LittleEndian.PutUint16(req[12:14], w16)
	binary.LittleEndian.PutUint16(req[14:16], rows16)
	binary.LittleEndian.PutUint16(req[16:18], dstX16)
	binary.LittleEndian.PutUint16(req[18:20], y016)
	req[putImageDepthOffset] = depthTrueColor24
	req[putImageLeftPadOffset], req[putImageLeftPadOffset+1] = 0, 0 // unused header bytes; see the dst-x comment above for why this is explicit
	copy(req[24:], bandPixels)
	for i := reqLen; i < total; i++ {
		req[i] = 0 // word-alignment padding; likewise explicit for the same reuse reason
	}

	return s.writeRequest(req)
}
