//go:build purego && linux

package purego

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
)

var errXdndRefused = errors.New("purego/x11: XdndSelection conversion refused")

const (
	xdndVersion    = 5
	xdndCoordShift = 16
	xdndCoordMask  = 0xffff

	xdndEnterMoreTypes    = 1
	xdndStatusAccept      = 1
	xdndFinishedAccepted  = 1
	xdndTypeSlotCount     = 3
	xdndClientDataCount   = 5
	xdndAwareValueCount   = 1
	xdndAwarePropReqLen   = 28
	xdndAwarePropReqWords = 7

	xdndEventTypeOffset              = 8
	xdndEventData0Offset             = 12
	xdndEventDataWord                = 4
	x11SelectionNotifyPropertyOffset = 20

	xdndURIListAtomName = "text/uri-list"
	xdndPropertyName    = "PUREGO_XDND"
	xdndFileScheme      = "file"
)

type x11DndState struct {
	aware, enter, position, status, leave, drop, finished uint32
	selection, actionCopy, actionMove, actionLink         uint32
	typeList, uriList, prop                               uint32

	mu       sync.Mutex
	source   uint32
	types    []uint32
	posX     int
	posY     int
	action   uint32
	dropGen  uint32
	active   bool
	dropping bool
}

func internXdndAtom(s *x11Surface, name string, dst *uint32) error {
	a, err := s.internAtom(name)
	if err != nil {
		return err
	}
	*dst = a
	return nil
}

func internXdndAtoms(s *x11Surface) (uint32, error) {
	st := &s.state.dnd
	if err := internXdndAtom(s, "XdndAware", &st.aware); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndEnter", &st.enter); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndPosition", &st.position); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndStatus", &st.status); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndLeave", &st.leave); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndDrop", &st.drop); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndFinished", &st.finished); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndSelection", &st.selection); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndActionCopy", &st.actionCopy); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndActionMove", &st.actionMove); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndActionLink", &st.actionLink); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, "XdndTypeList", &st.typeList); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, xdndURIListAtomName, &st.uriList); err != nil {
		return 0, err
	}
	if err := internXdndAtom(s, xdndPropertyName, &st.prop); err != nil {
		return 0, err
	}
	return st.aware, nil
}

func writeXdndAwareLocked(s *x11Surface, aware uint32) {
	prop := make([]byte, xdndAwarePropReqLen)
	prop[0] = opChangeProperty
	binary.LittleEndian.PutUint16(prop[2:4], xdndAwarePropReqWords)
	binary.LittleEndian.PutUint32(prop[4:8], s.winID)
	binary.LittleEndian.PutUint32(prop[8:12], aware)
	binary.LittleEndian.PutUint32(prop[12:16], atomATOM)
	prop[changePropertyFormatOffset] = propFormat32
	binary.LittleEndian.PutUint32(prop[20:24], xdndAwareValueCount)
	binary.LittleEndian.PutUint32(prop[24:28], xdndVersion)
	s.nextSeq()
	if _, err := s.conn.Write(prop); err != nil {
		logErr("purego/x11: XdndAware ChangeProperty", err)
	}
}

func x11DndOf(s *x11Surface) *x11DndState {
	st := &s.state.dnd
	if st.aware == 0 {
		return nil
	}
	return st
}

func handleXdndClientMessage(s *x11Surface, buf []byte) bool {
	st := x11DndOf(s)
	if st == nil || len(buf) < coreEventSize {
		return false
	}
	typ := binary.LittleEndian.Uint32(buf[xdndEventTypeOffset : xdndEventTypeOffset+xdndEventDataWord])
	switch typ {
	case st.enter:
		handleXdndEnter(s, st, buf)
	case st.position:
		handleXdndPosition(s, st, buf)
	case st.leave:
		handleXdndLeave(st)
	case st.drop:
		handleXdndDrop(s, st, buf)
	default:
		return false
	}
	return true
}

func xdndData(buf []byte, slot int) uint32 {
	off := xdndEventData0Offset + slot*xdndEventDataWord
	return binary.LittleEndian.Uint32(buf[off : off+xdndEventDataWord])
}

func handleXdndEnter(s *x11Surface, st *x11DndState, buf []byte) {
	source := xdndData(buf, 0)
	flags := xdndData(buf, 1)
	types := make([]uint32, 0, xdndTypeSlotCount)
	for slot := 0; slot < xdndTypeSlotCount; slot++ {
		if t := xdndData(buf, slot+2); t != 0 {
			types = append(types, t)
		}
	}
	st.mu.Lock()
	st.source = source
	st.types = types
	st.active = true
	st.action = st.actionCopy
	st.dropGen++
	gen := st.dropGen
	st.mu.Unlock()
	if flags&xdndEnterMoreTypes != 0 {
		go xdndApplyTypeList(s, st, source, gen)
	}
}

func xdndApplyTypeList(s *x11Surface, st *x11DndState, source, gen uint32) {
	listed := xdndFetchTypeList(s, st, source)
	if len(listed) == 0 {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.source == source && st.dropGen == gen {
		st.types = listed
	}
}

func xdndHonorAction(st *x11DndState, requested uint32) uint32 {
	switch requested {
	case st.actionCopy, st.actionMove, st.actionLink:
		return requested
	default:
		return st.actionCopy
	}
}

func handleXdndPosition(s *x11Surface, st *x11DndState, buf []byte) {
	requested := xdndData(buf, 4)
	root := xdndData(buf, 2)
	x := int(root >> xdndCoordShift)
	y := int(root & xdndCoordMask)
	st.mu.Lock()
	active := st.active
	accept := xdndTypesInclude(st.types, st.uriList)
	action := uint32(0)
	if accept {
		action = xdndHonorAction(st, requested)
		st.action = action
	} else {
		st.action = 0
	}
	if active {
		st.posX, st.posY = x, y
	}
	st.mu.Unlock()
	if !active {
		return
	}
	flags := uint32(0)
	if accept {
		flags = xdndStatusAccept
	}
	logErr("purego/x11: XdndStatus", s.sendXdndClientMessage(xdndData(buf, 0), st.status, [xdndClientDataCount]uint32{
		s.winID, flags, 0, 0, action,
	}))
}

func handleXdndLeave(st *x11DndState) {
	st.mu.Lock()
	st.active = false
	st.source = 0
	st.types = nil
	st.mu.Unlock()
}

func handleXdndDrop(s *x11Surface, st *x11DndState, buf []byte) {
	st.mu.Lock()
	if st.dropping || !st.active || st.source == 0 {
		st.mu.Unlock()
		return
	}
	source := st.source
	accept := xdndTypesInclude(st.types, st.uriList)
	stamp := xdndData(buf, 2)
	posX, posY := st.posX, st.posY
	action := st.action
	gen := st.dropGen
	if !accept {
		st.mu.Unlock()
		logErr("purego/x11: XdndFinished", s.sendXdndClientMessage(source, st.finished, [xdndClientDataCount]uint32{s.winID, 0, 0, 0, 0}))
		handleXdndLeave(st)
		return
	}
	st.dropping = true
	st.mu.Unlock()
	go xdndCompleteDrop(s, st, source, stamp, gen, posX, posY, action)
}

func xdndCompleteDrop(s *x11Surface, st *x11DndState, source, stamp, gen uint32, posX, posY int, action uint32) {
	defer func() {
		st.mu.Lock()
		if st.dropGen == gen {
			st.active = false
			st.source = 0
			st.types = nil
		}
		st.dropping = false
		st.mu.Unlock()
	}()
	text, err := xdndConvertURIList(s, st, stamp)
	accepted := uint32(0)
	finishedAction := uint32(0)
	if err == nil {
		uris := uriListToFileURIs(text)
		if len(uris) > 0 {
			accepted = xdndFinishedAccepted
			finishedAction = action
			pos := fyne.NewPos(float32(posX), float32(posY))
			select {
			case s.events <- dropEvent{Position: pos, URIs: uris}:
			case <-s.done:
			}
		}
	}
	logErr("purego/x11: XdndFinished", s.sendXdndClientMessage(source, st.finished, [xdndClientDataCount]uint32{
		s.winID, accepted, finishedAction, 0, 0,
	}))
}

func xdndConvertURIList(s *x11Surface, st *x11DndState, stamp uint32) (string, error) {
	selID, selCh := s.subscribeX11Event(eventCodeSelectionNotify, s.winID)
	defer s.unsubscribeX11Event(selID)

	req := make([]byte, convertSelectionReqLen)
	req[0] = opConvertSelection
	binary.LittleEndian.PutUint16(req[2:4], convertSelectionReqWords)
	binary.LittleEndian.PutUint32(req[4:8], s.winID)
	binary.LittleEndian.PutUint32(req[8:12], st.selection)
	binary.LittleEndian.PutUint32(req[12:16], st.uriList)
	binary.LittleEndian.PutUint32(req[16:20], st.prop)
	binary.LittleEndian.PutUint32(req[20:24], stamp)
	if err := s.writeRequest(req); err != nil {
		return "", err
	}
	buf, err := waitX11Event(selCh, s.done, clipboardSelectionNotifyTimeout)
	if err != nil {
		return "", err
	}
	property := binary.LittleEndian.Uint32(buf[x11SelectionNotifyPropertyOffset : x11SelectionNotifyPropertyOffset+xdndEventDataWord])
	if property == atomNone {
		return "", errXdndRefused
	}
	return s.getPropertyString(s.winID, st.prop)
}

func xdndFetchTypeList(s *x11Surface, st *x11DndState, source uint32) []uint32 {
	_, value, err := s.getPropertyOnce(source, st.typeList, false)
	if err != nil {
		return nil
	}
	nItems, err := uint32FromInt(len(value) / xdndEventDataWord)
	if err != nil {
		return nil
	}
	out, err := xdndParseAtomList(value, nItems)
	if err != nil {
		return nil
	}
	return out
}

func xdndParseAtomList(extra []byte, valueLen uint32) ([]uint32, error) {
	maxN := len(extra) / xdndEventDataWord
	if maxN > maxAtomListCount {
		maxN = maxAtomListCount
	}
	if uint64(valueLen) > uint64(maxN) {
		return nil, fmt.Errorf("purego/x11: XdndTypeList announced %d atoms, over the limit of %d", valueLen, maxN)
	}
	n, err := intFromUint32(valueLen)
	if err != nil {
		return nil, err
	}
	out := make([]uint32, 0, n)
	for i := 0; i < n; i++ {
		off := i * xdndEventDataWord
		a := binary.LittleEndian.Uint32(extra[off : off+xdndEventDataWord])
		if a != 0 {
			out = append(out, a)
		}
	}
	return out, nil
}

func xdndTypesInclude(types []uint32, want uint32) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

func (s *x11Surface) sendXdndClientMessage(dest, typeAtom uint32, data [xdndClientDataCount]uint32) error {
	event := make([]byte, coreEventSize)
	event[0] = eventCodeClientMessage
	event[1] = propFormat32
	binary.LittleEndian.PutUint32(event[4:8], dest)
	binary.LittleEndian.PutUint32(event[8:12], typeAtom)
	for i, v := range data {
		binary.LittleEndian.PutUint32(event[xdndEventData0Offset+i*xdndEventDataWord:], v)
	}
	req := make([]byte, sendEventReqLen)
	req[0] = opSendEvent
	binary.LittleEndian.PutUint16(req[2:4], uint16(sendEventReqWords))
	binary.LittleEndian.PutUint32(req[4:8], dest)
	copy(req[sendEventHeaderLen:], event)
	return s.writeRequest(req)
}

func uriListToFileURIs(text string) []fyne.URI {
	lines := strings.Split(text, "\n")
	out := make([]fyne.URI, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := storage.ParseURI(line)
		if err != nil || u == nil {
			continue
		}
		if u.Scheme() == xdndFileScheme {
			out = append(out, storage.NewFileURI(u.Path()))
			continue
		}
		out = append(out, u)
	}
	return out
}
