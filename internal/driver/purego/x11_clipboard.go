//go:build purego && linux

package purego

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

// clipboardSelectionNotifyTimeout bounds how long ClipboardText waits for
// the CLIPBOARD owner to answer a ConvertSelection request. The ICCCM
// specifies no server-enforced timeout for this exchange: an owner that is
// stopped, wedged or simply never answers otherwise blocks this call, and
// the UI thread that invokes it (a paste keystroke), forever. Five seconds
// is generous for a cooperating
// application on a local display while still bounding a hung one.
// A var, not a const, so tests can shrink it instead of waiting out the real
// value to exercise the timeout path.
var clipboardSelectionNotifyTimeout = 5 * time.Second

var errINCRPropertyTooLarge = errors.New("purego/x11: INCR property exceeds size limit")

// ICCCM CLIPBOARD selection exchange, without xclip or any other helper
// binary: ConvertSelection/SelectionNotify/SelectionRequest/GetProperty/
// ChangeProperty on the surface connection. A hidden CLIPBOARD owner (no
// shown Fyne window) is an ordinary x11Surface whose window is never mapped.
const (
	opConvertSelection  = 24
	opGetProperty       = 20
	opSetSelectionOwner = 22

	convertSelectionReqLen, convertSelectionReqWords   = 24, 6
	getPropertyReqLen, getPropertyReqWords             = 24, 6
	setSelectionOwnerReqLen, setSelectionOwnerReqWords = 16, 4

	eventCodeSelectionNotify  = 31
	eventCodeSelectionRequest = 30
	eventCodeSelectionClear   = 29
	eventCodePropertyNotify   = 28

	eventMaskPropertyChange = 0x00400000

	propertyNotifyStateOffset = 16
	propertyStateNewValue     = 0
	propertyStateDeleted      = 1

	opGetSelectionOwner                                = 23
	getSelectionOwnerReqLen, getSelectionOwnerReqWords = 8, 2
	clipboardOwnerAckPoll                              = 2 * time.Millisecond

	atomNone            = 0
	atomAnyPropertyType = 0

	// clipboardPropertyName is the property this backend asks selection
	// owners (including itself) to stash the converted value under. Its
	// name has no protocol meaning; ICCCM only requires requestor and owner
	// to agree, and both roles are played by this same package.
	clipboardPropertyName = "PUREGO_X11_CLIPBOARD"
	clipboardAtomName     = "CLIPBOARD"
	clipboardUTF8AtomName = "UTF8_STRING"
	clipboardINCRAtomName = "INCR"
)

var _ clipboardSurface = (*x11Surface)(nil)

// ClipboardText implements clipboardSurface. It converts the CLIPBOARD
// selection to UTF8_STRING on this surface's own connection and window,
// waits for SelectionNotify through subscribeX11Event, and fetches the
// resulting property, per the ICCCM. A second connection would be a
// distinct X11 client; the primary connection already has request() and a
// non-blocking reader, so the round trip belongs here. It returns "", nil
// when no application currently owns CLIPBOARD, matching common toolkit
// behaviour rather than treating an empty clipboard as an error.
func (s *x11Surface) ClipboardText() (string, error) {
	clipboardAtom, err := s.internAtom(clipboardAtomName)
	if err != nil {
		return "", err
	}
	utf8Atom, err := s.internAtom(clipboardUTF8AtomName)
	if err != nil {
		return "", err
	}
	propAtom, err := s.internAtom(clipboardPropertyName)
	if err != nil {
		return "", err
	}

	selID, selCh := s.subscribeX11Event(eventCodeSelectionNotify, s.winID)
	defer s.unsubscribeX11Event(selID)

	req := make([]byte, convertSelectionReqLen)
	req[0] = opConvertSelection
	binary.LittleEndian.PutUint16(req[2:4], convertSelectionReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], clipboardAtom)
	binary.LittleEndian.PutUint32(req[12:16], utf8Atom)
	binary.LittleEndian.PutUint32(req[16:20], propAtom)
	binary.LittleEndian.PutUint32(req[20:24], 0) // CurrentTime
	if err := s.writeRequest(req); err != nil {
		return "", err
	}

	buf, err := waitX11Event(selCh, s.done, clipboardSelectionNotifyTimeout)
	if err != nil {
		return "", fmt.Errorf("purego/x11: no CLIPBOARD owner answered within %s: %w", clipboardSelectionNotifyTimeout, err)
	}
	property := binary.LittleEndian.Uint32(buf[20:24])
	if property == atomNone {
		return "", nil
	}
	return s.getPropertyString(s.winID, propAtom)
}

func (s *x11Surface) getPropertyOnce(window, property uint32, remove bool) (uint32, []byte, error) {
	req := make([]byte, getPropertyReqLen)
	req[0] = opGetProperty
	if remove {
		req[1] = 1
	}
	binary.LittleEndian.PutUint16(req[2:4], getPropertyReqWords)
	binary.LittleEndian.PutUint32(req[4:8], window)
	binary.LittleEndian.PutUint32(req[8:12], property)
	binary.LittleEndian.PutUint32(req[12:16], atomAnyPropertyType)
	binary.LittleEndian.PutUint32(req[20:24], getPropertyMaxWords)
	reply, err := s.request(req)
	if err != nil {
		return 0, nil, err
	}
	return parseGetPropertyReply(reply)
}

func parseGetPropertyReply(reply []byte) (uint32, []byte, error) {
	if len(reply) < coreEventSize {
		return 0, nil, errors.New("purego/x11: short GetProperty reply")
	}
	if reply[0] != replyStatusReply {
		return 0, nil, fmt.Errorf("purego/x11: GetProperty error status %d", reply[0])
	}
	extraWords := binary.LittleEndian.Uint32(reply[4:8])
	valueLen := binary.LittleEndian.Uint32(reply[16:20])
	format := reply[1]
	typ := binary.LittleEndian.Uint32(reply[8:12])
	if err := checkWireAllocBytes(uint64(extraWords)*setupWordSize, maxPropertyBytes, "GetProperty reply value"); err != nil {
		return 0, nil, err
	}
	extra := reply[coreEventSize:]
	unitBytes := 1
	if format == propFormat32 {
		unitBytes = 4
	}
	nItems, err := intFromUint32(valueLen)
	if err != nil {
		nItems = len(extra) / unitBytes
	}
	n := nItems * unitBytes
	if n > len(extra) || n < 0 {
		n = len(extra)
	}
	return typ, extra[:n], nil
}

func (s *x11Surface) getPropertyString(window, property uint32) (string, error) {
	incrAtom, err := s.internAtom(clipboardINCRAtomName)
	if err != nil {
		incrAtom = 0
	}
	propID, propCh := s.subscribeX11Event(eventCodePropertyNotify, window)
	defer s.unsubscribeX11Event(propID)
	typ, value, err := s.getPropertyOnce(window, property, true)
	if err != nil {
		return "", err
	}
	if incrAtom != 0 && typ == incrAtom {
		return s.readINCRProperty(window, property, propCh)
	}
	return string(value), nil
}

func (s *x11Surface) readINCRProperty(window, property uint32, notifies <-chan []byte) (string, error) {
	var buf []byte
	limit := int(getPropertyMaxWords * setupWordSize)
	for {
		ev, err := waitX11Event(notifies, s.done, clipboardSelectionNotifyTimeout)
		if err != nil {
			if len(buf) > 0 {
				return string(buf), nil
			}
			return "", fmt.Errorf("purego/x11: timed out waiting for INCR property after %s", clipboardSelectionNotifyTimeout)
		}
		atom := binary.LittleEndian.Uint32(ev[8:12])
		if atom != property || ev[propertyNotifyStateOffset] != propertyStateNewValue {
			continue
		}
		_, chunk, err := s.getPropertyOnce(window, property, true)
		if err != nil {
			return "", err
		}
		if len(chunk) == 0 {
			if len(buf) == 0 {
				continue
			}
			return string(buf), nil
		}
		if len(buf)+len(chunk) > limit {
			return "", errINCRPropertyTooLarge
		}
		buf = append(buf, chunk...)
	}
}

func (s *x11Surface) getSelectionOwner(selection uint32) (uint32, error) {
	req := make([]byte, getSelectionOwnerReqLen)
	req[0] = opGetSelectionOwner
	binary.LittleEndian.PutUint16(req[2:4], getSelectionOwnerReqWords)
	binary.LittleEndian.PutUint32(req[4:8], selection)
	reply, err := s.request(req)
	if err != nil {
		return 0, err
	}
	if reply[0] != replyStatusReply {
		return 0, fmt.Errorf("purego/x11: GetSelectionOwner error status %d", reply[0])
	}
	return binary.LittleEndian.Uint32(reply[8:12]), nil
}

// clipboardOwner keeps the window a client uses while it owns CLIPBOARD.
// Shown windows and the hidden owner (no Fyne window) both use an x11Surface
// so SelectionRequest and INCR PropertyNotify share readLoop, request() and
// subscribeX11Event.
type clipboardOwner struct {
	surface  *x11Surface
	winID    uint32
	stop     chan struct{}
	stopOnce sync.Once
	unsub    func()

	mu   sync.Mutex
	text string
}

func (owner *clipboardOwner) internAtom(name string) (uint32, error) {
	return owner.surface.internAtom(name)
}

func (owner *clipboardOwner) write(req []byte) error {
	return owner.surface.writeRequest(req)
}

// SetClipboardText implements clipboardSurface: it makes this surface's
// clipboard window the CLIPBOARD selection owner and records text, then
// (the first time this surface publishes anything) starts the goroutine
// that answers other clients' SelectionRequest with it.
func (s *x11Surface) SetClipboardText(text string) error {
	owner, err := clipboardOwnerFor(s)
	if err != nil {
		return err
	}
	return owner.publish(text)
}

type x11HiddenClipboard struct{}

var (
	hiddenClipMu sync.Mutex
	hiddenSurf   *x11Surface
)

func hiddenX11Surface() (*x11Surface, error) {
	hiddenClipMu.Lock()
	if hiddenSurf != nil {
		s := hiddenSurf
		hiddenClipMu.Unlock()
		return s, nil
	}
	hiddenClipMu.Unlock()

	s, err := newX11Surface(1, 1, withoutMap())
	if err != nil {
		return nil, err
	}

	hiddenClipMu.Lock()
	if hiddenSurf != nil {
		existing := hiddenSurf
		hiddenClipMu.Unlock()
		_ = s.Close() // close is idempotent
		return existing, nil
	}
	hiddenSurf = s
	hiddenClipMu.Unlock()
	return s, nil
}

// closeHiddenClipboard releases the unmapped surface that owns CLIPBOARD
// without a shown window, so a driver that quits leaves no connection or
// goroutine behind.
func closeHiddenClipboard() {
	hiddenClipMu.Lock()
	s := hiddenSurf
	hiddenSurf = nil
	hiddenClipMu.Unlock()
	if s != nil {
		_ = s.Close() // close is idempotent
	}
}

func (x11HiddenClipboard) ClipboardText() (string, error) {
	s, err := hiddenX11Surface()
	if err != nil {
		return "", err
	}
	return s.ClipboardText()
}

func (x11HiddenClipboard) SetClipboardText(text string) error {
	s, err := hiddenX11Surface()
	if err != nil {
		return err
	}
	if err := s.SetClipboardText(text); err != nil {
		forgetHiddenClipboard()
		s, err = hiddenX11Surface()
		if err != nil {
			return err
		}
		return s.SetClipboardText(text)
	}
	return nil
}

func forgetHiddenClipboard() {
	hiddenClipMu.Lock()
	s := hiddenSurf
	hiddenSurf = nil
	hiddenClipMu.Unlock()
	if s != nil {
		_ = s.Close() // close is idempotent
	}
}

func (owner *clipboardOwner) publish(text string) error {
	owner.mu.Lock()
	owner.text = text
	owner.mu.Unlock()

	clipboardAtom, err := owner.internAtom(clipboardAtomName)
	if err != nil {
		return err
	}
	req := make([]byte, setSelectionOwnerReqLen)
	req[0] = opSetSelectionOwner
	binary.LittleEndian.PutUint16(req[2:4], setSelectionOwnerReqWords)
	binary.LittleEndian.PutUint32(req[4:8], owner.winID)
	binary.LittleEndian.PutUint32(req[8:12], clipboardAtom)
	binary.LittleEndian.PutUint32(req[12:16], 0) // CurrentTime

	if err := owner.write(req); err != nil {
		return err
	}
	return waitSelectionOwnerOn(owner.surface, owner.winID, clipboardAtom)
}

func waitSelectionOwnerOn(s *x11Surface, want, selection uint32) error {
	deadline := time.Now().Add(clipboardSelectionNotifyTimeout)
	for {
		owner, err := s.getSelectionOwner(selection)
		if err != nil {
			return err
		}
		if owner == want {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("purego/x11: CLIPBOARD owner was not acknowledged as window %d", want)
		}
		time.Sleep(clipboardOwnerAckPoll)
	}
}

// clipboardOwnerFor returns the persistent clipboard-owner state for s,
// using this surface's window as the CLIPBOARD owner the first time
// SetClipboardText runs.
func clipboardOwnerFor(s *x11Surface) (*clipboardOwner, error) {
	s.state.mu.Lock()
	if owner := s.state.clip; owner != nil {
		s.state.mu.Unlock()
		return owner, nil
	}
	s.state.mu.Unlock()

	if _, err := s.internAtom(clipboardAtomName); err != nil {
		return nil, err
	}
	reqID, reqCh := s.subscribeX11Event(eventCodeSelectionRequest, s.winID)
	clearID, clearCh := s.subscribeX11Event(eventCodeSelectionClear, s.winID)
	owner := &clipboardOwner{
		surface: s,
		winID:   s.winID,
		stop:    make(chan struct{}),
		unsub: func() {
			s.unsubscribeX11Event(reqID)
			s.unsubscribeX11Event(clearID)
		},
	}

	s.state.mu.Lock()
	if existing := s.state.clip; existing != nil {
		s.state.mu.Unlock()
		owner.unsub()
		return existing, nil
	}
	s.state.clip = owner
	s.state.mu.Unlock()
	go owner.respondLoopSurface(s, reqCh, clearCh)
	return owner, nil
}

func (s *x11Surface) releaseAttached() {
	s.state.mu.Lock()
	owner := s.state.clip
	s.state.mu.Unlock()
	if owner != nil {
		owner.shutdown()
	}
}

func (owner *clipboardOwner) shutdown() {
	owner.stopOnce.Do(func() { close(owner.stop) })
	if owner.unsub != nil {
		owner.unsub()
	}
}

func (owner *clipboardOwner) respondLoopSurface(s *x11Surface, reqs, clears <-chan []byte) {
	defer owner.clearClipRef(s)
	for {
		select {
		case <-owner.stop:
			return
		case <-s.done:
			return
		case buf, ok := <-reqs:
			if !ok {
				return
			}
			owner.handleSelectionRequest(buf)
		case _, ok := <-clears:
			if !ok {
				return
			}
			return
		}
	}
}

func (owner *clipboardOwner) clearClipRef(s *x11Surface) {
	if s == nil {
		return
	}
	s.state.mu.Lock()
	if s.state.clip == owner {
		s.state.clip = nil
	}
	s.state.mu.Unlock()
}

// handleSelectionRequest answers one SelectionRequest event: it publishes
// the requested target (TARGETS, UTF8_STRING or STRING; anything else is
// refused) on the requestor's named property and sends back SelectionNotify,
// exactly as ICCCM section 2.2 requires of a selection owner.
func (owner *clipboardOwner) handleSelectionRequest(buf []byte) {
	requestor := binary.LittleEndian.Uint32(buf[12:16])
	selection := binary.LittleEndian.Uint32(buf[16:20])
	target := binary.LittleEndian.Uint32(buf[20:24])
	property := binary.LittleEndian.Uint32(buf[24:28])
	if property == atomNone {
		property = target // ICCCM: an obsolete requestor may ask for target itself
	}

	targetsAtom, err1 := owner.internAtom("TARGETS")
	utf8Atom, err2 := owner.internAtom(clipboardUTF8AtomName)
	stringAtom, err3 := owner.internAtom("STRING")
	if err1 != nil || err2 != nil || err3 != nil {
		owner.sendSelectionNotify(requestor, selection, target, atomNone)
		return
	}

	owner.mu.Lock()
	text := owner.text
	owner.mu.Unlock()

	var ok bool
	switch target {
	case targetsAtom:
		ok = owner.changeProperty(requestor, property, atomATOM, propFormat32,
			atomsToBytes(targetsAtom, utf8Atom, stringAtom))
	case utf8Atom, stringAtom:
		ok = owner.changeProperty(requestor, property, target, propFormat8, []byte(text))
	default:
		ok = false
	}
	if !ok {
		property = atomNone
	}
	owner.sendSelectionNotify(requestor, selection, target, property)
}

func atomsToBytes(atoms ...uint32) []byte {
	out := make([]byte, len(atoms)*4)
	for i, a := range atoms {
		binary.LittleEndian.PutUint32(out[i*4:i*4+4], a)
	}
	return out
}

// changeProperty writes value onto window/property with ChangeProperty.
// Any client may name any window it knows the ID of in a core request, so
// writing the requestor's property needs no connection of the requestor's
// own.
func (owner *clipboardOwner) changeProperty(window, property, typ uint32, format byte, value []byte) bool {
	reqWords, err := x11ChangePropertyWords(len(value))
	if err != nil {
		return false
	}
	words, err := card16FromInt(reqWords)
	if err != nil {
		return false
	}
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opChangeProperty
	req[1] = propModeReplace
	binary.LittleEndian.PutUint16(req[2:4], words)
	binary.LittleEndian.PutUint32(req[4:8], window)
	binary.LittleEndian.PutUint32(req[8:12], property)
	binary.LittleEndian.PutUint32(req[12:16], typ)
	req[changePropertyFormatOffset] = format
	units := len(value)
	if format == propFormat32 {
		units /= 4
	}
	nUnits, err := uint32FromInt(units)
	if err != nil {
		return false
	}
	binary.LittleEndian.PutUint32(req[20:24], nUnits)
	copy(req[24:], value)

	return owner.write(req) == nil
}

// sendSelectionNotify replies to a SelectionRequest with the SelectionNotify
// event ICCCM requires, via SendEvent (opSendEvent, defined in
// x11_fullscreen.go).
func (owner *clipboardOwner) sendSelectionNotify(requestor, selection, target, property uint32) {
	event := make([]byte, coreEventSize)
	event[0] = eventCodeSelectionNotify
	binary.LittleEndian.PutUint32(event[4:8], 0) // time: CurrentTime
	binary.LittleEndian.PutUint32(event[8:12], requestor)
	binary.LittleEndian.PutUint32(event[12:16], selection)
	binary.LittleEndian.PutUint32(event[16:20], target)
	binary.LittleEndian.PutUint32(event[20:24], property)

	req := make([]byte, sendEventReqLen)
	req[0] = opSendEvent
	req[1] = 0
	binary.LittleEndian.PutUint16(req[2:4], uint16(sendEventReqWords))
	binary.LittleEndian.PutUint32(req[4:8], requestor)
	binary.LittleEndian.PutUint32(req[8:12], 0) // event-mask 0: delivered regardless, per ICCCM
	copy(req[sendEventHeaderLen:], event)

	if err := owner.write(req); err != nil {
		logErr("purego/x11: SelectionNotify", err)
	}
}
