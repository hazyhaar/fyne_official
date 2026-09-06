//go:build purego && linux

package purego

import (
	"sync"

	"fyne.io/fyne/v2"
)

const (
	wlURIListMime = "text/uri-list"

	wlDataDeviceEventEnter  uint16 = 1
	wlDataDeviceEventLeave  uint16 = 2
	wlDataDeviceEventMotion uint16 = 3
	wlDataDeviceEventDrop   uint16 = 4

	wlDataOfferOpAccept     uint16 = 0
	wlDataOfferOpSetActions uint16 = 4
	wlDataOfferOpFinish     uint16 = 3

	wlDataOfferEventSourceActions uint16 = 1
	wlDataOfferEventAction        uint16 = 2

	wlDndActionNone uint32 = 0
	wlDndActionCopy uint32 = 1
)

type wlDnd struct {
	mu     sync.Mutex
	offer  uint32
	serial uint32
	action uint32
	x, y   int
	active bool
}

func waylandDndOf(s *waylandSurface) *wlDnd {
	return &s.state.dnd
}

func waylandDndDispatch(s *waylandSurface, objectID uint32, opcode uint16, p []byte) bool {
	s.mu.Lock()
	dd := s.dataDeviceID
	s.mu.Unlock()
	if dd != 0 && objectID == dd {
		switch opcode {
		case wlDataDeviceEventEnter:
			waylandDndEnter(s, p)
			return true
		case wlDataDeviceEventLeave:
			waylandDndLeave(s)
			return true
		case wlDataDeviceEventMotion:
			waylandDndMotion(s, p)
			return true
		case wlDataDeviceEventDrop:
			waylandDndDrop(s)
			return true
		default:
			return false
		}
	}
	st := waylandDndOf(s)
	st.mu.Lock()
	offer := st.offer
	st.mu.Unlock()
	if offer == 0 || objectID != offer {
		return false
	}
	return waylandDndOfferEvent(st, opcode, p)
}

func waylandDndOfferEvent(st *wlDnd, opcode uint16, p []byte) bool {
	switch opcode {
	case wlDataOfferEventSourceActions:
		return true
	case wlDataOfferEventAction:
		r := wlParser{p: p}
		action, err := r.uint32()
		if err != nil {
			return true
		}
		st.mu.Lock()
		st.action = action
		st.mu.Unlock()
		return true
	default:
		return false
	}
}

func waylandOfferAccept(s *waylandSurface, offer, serial uint32, mime string) {
	req := wlMsgWriter{}
	req.putUint32(serial)
	if mime == "" {
		req.putUint32(0)
	} else {
		req.putString(mime)
	}
	logErr("purego/wayland: data_offer.accept", s.send(offer, wlDataOfferOpAccept, req))
}

func waylandOfferSetActionsCopy(s *waylandSurface, offer uint32) {
	req := wlMsgWriter{}
	req.putUint32(wlDndActionCopy)
	req.putUint32(wlDndActionCopy)
	logErr("purego/wayland: data_offer.set_actions", s.send(offer, wlDataOfferOpSetActions, req))
}

func waylandForgetOffer(s *waylandSurface, offer uint32) {
	if offer == 0 {
		return
	}
	logErr("purego/wayland: data_offer.destroy", s.send(offer, wlDataOfferOpDestroy, wlMsgWriter{}))
	clip := waylandClipboardOf(s)
	clip.mu.Lock()
	delete(clip.offers, offer)
	clip.mu.Unlock()
}

func waylandOfferHasURIList(s *waylandSurface, offer uint32) bool {
	st := waylandClipboardOf(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.offers[offer][wlURIListMime]
	return ok
}

func waylandDndEnter(s *waylandSurface, p []byte) {
	r := wlParser{p: p}
	serial, err1 := r.uint32()
	_, err2 := r.uint32()
	x, err3 := r.fixed()
	y, err4 := r.fixed()
	offer, err5 := r.uint32()
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
		return
	}
	st := waylandDndOf(s)
	st.mu.Lock()
	st.offer = offer
	st.serial = serial
	st.x, st.y = x, y
	st.active = offer != 0
	st.mu.Unlock()
	if offer == 0 {
		return
	}
	if !waylandOfferHasURIList(s, offer) {
		waylandOfferAccept(s, offer, serial, "")
		return
	}
	waylandOfferAccept(s, offer, serial, wlURIListMime)
	waylandOfferSetActionsCopy(s, offer)
}

func waylandDndLeave(s *waylandSurface) {
	st := waylandDndOf(s)
	st.mu.Lock()
	offer := st.offer
	st.active = false
	st.offer = 0
	st.mu.Unlock()
	waylandForgetOffer(s, offer)
}

func waylandDndMotion(s *waylandSurface, p []byte) {
	r := wlParser{p: p}
	if _, err := r.uint32(); err != nil {
		return
	}
	x, err1 := r.fixed()
	y, err2 := r.fixed()
	if err1 != nil || err2 != nil {
		return
	}
	st := waylandDndOf(s)
	st.mu.Lock()
	st.x, st.y = x, y
	st.mu.Unlock()
}

func waylandDndDrop(s *waylandSurface) {
	st := waylandDndOf(s)
	st.mu.Lock()
	offer, serial, x, y := st.offer, st.serial, st.x, st.y
	active := st.active
	st.active = false
	st.mu.Unlock()
	if !active || offer == 0 || !waylandOfferHasURIList(s, offer) {
		waylandForgetOffer(s, offer)
		return
	}
	go waylandDndReceive(s, offer, serial, x, y)
}

func waylandDndReceive(s *waylandSurface, offer, serial uint32, x, y int) {
	accept := wlMsgWriter{}
	accept.putUint32(serial)
	accept.putString(wlURIListMime)
	logErr("purego/wayland: data_offer.accept", s.send(offer, wlDataOfferOpAccept, accept))

	text, err := waylandReceiveOffer(s, offer, wlURIListMime)
	logErr("purego/wayland: data_offer.finish", s.send(offer, wlDataOfferOpFinish, wlMsgWriter{}))
	waylandForgetOffer(s, offer)
	if err != nil {
		return
	}
	uris := uriListToFileURIs(text)
	if len(uris) == 0 {
		return
	}
	select {
	case s.events <- dropEvent{Position: fyne.NewPos(float32(x), float32(y)), URIs: uris}:
	case <-s.done:
	}
}
