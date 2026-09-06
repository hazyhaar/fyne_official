//go:build purego && linux

package purego

import (
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

const (
	wlClipboardMime = "text/plain;charset=utf-8"

	wlDataDevMgrOpCreateDataSource uint16 = 0
	wlDataDevMgrOpGetDataDevice    uint16 = 1

	wlDataDeviceOpSetSelection uint16 = 1
	wlDataDeviceEventDataOffer uint16 = 0
	wlDataDeviceEventSelection uint16 = 5

	wlDataSourceOpOffer     uint16 = 0
	wlDataSourceOpDestroy   uint16 = 1
	wlDataSourceEventSend   uint16 = 1
	wlDataSourceEventCancel uint16 = 2

	wlDataOfferOpReceive  uint16 = 1
	wlDataOfferOpDestroy  uint16 = 2
	wlDataOfferEventOffer uint16 = 0

	wlKeyboardEventEnter uint16 = 1
)

var waylandClipboardTimeout = 5 * time.Second

var waylandOfferMaxBytes int64 = 16 << 20

var _ clipboardSurface = (*waylandSurface)(nil)

type wlClipboard struct {
	mu sync.Mutex

	sourceID uint32
	text     string

	offers    map[uint32]map[string]struct{}
	selection uint32

	serial        uint32
	pointerSerial uint32

	gotSelection chan struct{}
}

func waylandClipboardOf(s *waylandSurface) *wlClipboard {
	st := &s.state.clip
	st.mu.Lock()
	if st.offers == nil {
		st.offers = make(map[uint32]map[string]struct{})
	}
	st.mu.Unlock()
	return st
}

func waylandNoteInputSerial(s *waylandSurface, pointer bool, opcode uint16, p []byte) {
	if pointer {
		if opcode != wlPointerEventEnter && opcode != wlPointerEventButton {
			return
		}
	} else if opcode != wlKeyboardEventEnter && opcode != wlKeyboardEventKey {
		return
	}
	r := wlParser{p: p}
	serial, err := r.uint32()
	if err != nil {
		return
	}
	st := waylandClipboardOf(s)
	st.mu.Lock()
	st.serial = serial
	if pointer && opcode == wlPointerEventEnter {
		st.pointerSerial = serial
	}
	st.mu.Unlock()
}

func waylandEnsureDataDevice(s *waylandSurface) {
	s.mu.Lock()
	if s.dataDeviceManagerID == 0 || s.seatID == 0 || s.dataDeviceID != 0 || s.closed {
		s.mu.Unlock()
		return
	}
	id := s.nextID
	s.nextID++
	s.dataDeviceID = id
	mgr, seat := s.dataDeviceManagerID, s.seatID
	s.mu.Unlock()
	w := wlMsgWriter{}
	w.putUint32(id)
	w.putUint32(seat)
	logErr("purego/wayland: get_data_device", s.send(mgr, wlDataDevMgrOpGetDataDevice, w))
}

func waylandClipboardDispatch(s *waylandSurface, objectID uint32, opcode uint16, p []byte) bool {
	s.mu.Lock()
	dd := s.dataDeviceID
	s.mu.Unlock()
	st := waylandClipboardOf(s)
	st.mu.Lock()
	src := st.sourceID
	_, isOffer := st.offers[objectID]
	st.mu.Unlock()
	switch {
	case dd != 0 && objectID == dd:
		waylandHandleDataDevice(s, st, opcode, p)
		return true
	case src != 0 && objectID == src:
		waylandHandleDataSource(s, st, opcode, p)
		return true
	case isOffer:
		waylandHandleDataOffer(st, objectID, opcode, p)
		return true
	}
	return false
}

func waylandHandleDataDevice(s *waylandSurface, st *wlClipboard, opcode uint16, p []byte) {
	r := wlParser{p: p}
	switch opcode {
	case wlDataDeviceEventDataOffer:
		id, err := r.uint32()
		if err != nil || id == 0 {
			return
		}
		st.mu.Lock()
		st.offers[id] = make(map[string]struct{})
		st.mu.Unlock()
	case wlDataDeviceEventSelection:
		id, err := r.uint32()
		if err != nil {
			return
		}
		st.mu.Lock()
		old := st.selection
		st.selection = id
		wait := st.gotSelection
		st.gotSelection = nil
		st.mu.Unlock()
		if old != 0 && old != id {
			logErr("purego/wayland: data_offer.destroy", s.send(old, wlDataOfferOpDestroy, wlMsgWriter{}))
			st.mu.Lock()
			delete(st.offers, old)
			st.mu.Unlock()
		}
		if wait != nil {
			close(wait)
		}
	}
}

func waylandHandleDataSource(s *waylandSurface, st *wlClipboard, opcode uint16, _ []byte) {
	switch opcode {
	case wlDataSourceEventSend:
		fd, ok := s.takePendingFD()
		if !ok {
			return
		}
		st.mu.Lock()
		text := st.text
		st.mu.Unlock()
		go waylandWriteDataSource(fd, text)
	case wlDataSourceEventCancel:
		st.mu.Lock()
		st.sourceID = 0
		st.mu.Unlock()
	}
}

func waylandHandleDataOffer(st *wlClipboard, objectID uint32, opcode uint16, p []byte) {
	if opcode != wlDataOfferEventOffer {
		return
	}
	r := wlParser{p: p}
	mime, err := r.str()
	if err != nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.offers[objectID] == nil {
		st.offers[objectID] = make(map[string]struct{})
	}
	st.offers[objectID][mime] = struct{}{}
}

func (s *waylandSurface) SetClipboardText(text string) error {
	waylandEnsureDataDevice(s)
	s.mu.Lock()
	mgr := s.dataDeviceManagerID
	dd := s.dataDeviceID
	s.mu.Unlock()
	if mgr == 0 || dd == 0 {
		return errors.New("purego/wayland: compositor did not advertise wl_data_device_manager")
	}
	st := waylandClipboardOf(s)
	st.mu.Lock()
	old := st.sourceID
	serial := st.serial
	st.mu.Unlock()
	if old != 0 {
		logErr("purego/wayland: data_source.destroy", s.send(old, wlDataSourceOpDestroy, wlMsgWriter{}))
	}
	id := s.allocID()
	if err := s.send(mgr, wlDataDevMgrOpCreateDataSource, wlMsgWriter{}.putU(id)); err != nil {
		return err
	}
	mime := wlMsgWriter{}
	mime.putString(wlClipboardMime)
	if err := s.send(id, wlDataSourceOpOffer, mime); err != nil {
		return err
	}
	sel := wlMsgWriter{}
	sel.putUint32(id)
	sel.putUint32(serial)
	if err := s.send(dd, wlDataDeviceOpSetSelection, sel); err != nil {
		return err
	}
	st.mu.Lock()
	st.sourceID = id
	st.text = text
	st.mu.Unlock()
	return nil
}

func (s *waylandSurface) ClipboardText() (string, error) {
	st := waylandClipboardOf(s)
	st.mu.Lock()
	offer := st.selection
	mimes := st.offers[offer]
	local := st.text
	owned := st.sourceID != 0
	st.mu.Unlock()
	if offer == 0 {
		if owned {
			return local, nil
		}
		return "", nil
	}
	if _, ok := mimes[wlClipboardMime]; !ok {
		if owned {
			return local, nil
		}
		return "", nil
	}
	text, err := waylandReceiveOffer(s, offer, wlClipboardMime)
	if err != nil {
		if owned {
			return local, nil
		}
		return "", err
	}
	return text, nil
}

func waylandWriteDataSource(fd int, text string) {
	f := os.NewFile(uintptr(fd), "wl-data-source")
	defer f.Close()
	if err := f.SetWriteDeadline(time.Now().Add(waylandClipboardTimeout)); err != nil {
		logErr("purego/wayland: clipboard write deadline", err)
	}
	if _, err := f.WriteString(text); err != nil {
		logErr("purego/wayland: clipboard write", err)
	}
}

func waylandReceiveOffer(s *waylandSurface, offer uint32, mime string) (string, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return "", err
	}
	req := wlMsgWriter{}
	req.putString(mime)
	if err := s.sendFD(offer, wlDataOfferOpReceive, req, int(pw.Fd())); err != nil {
		_ = pr.Close() // close is idempotent
		_ = pw.Close() // close is idempotent
		return "", err
	}
	_ = pw.Close() // close is idempotent
	defer pr.Close()
	if err := pr.SetReadDeadline(time.Now().Add(waylandClipboardTimeout)); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(pr, waylandOfferMaxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > waylandOfferMaxBytes {
		return "", errors.New("purego/wayland: data offer exceeds size limit")
	}
	return string(b), nil
}
