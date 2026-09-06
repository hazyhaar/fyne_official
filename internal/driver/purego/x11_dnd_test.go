//go:build purego && linux

package purego

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
)

func TestUriListToFileURIs(t *testing.T) {
	uris := uriListToFileURIs("file:///tmp/purego-dnd-a\r\n# comment\nfile:///tmp/purego-dnd-b\n")
	if len(uris) != 2 {
		t.Fatalf("got %d URIs, want 2", len(uris))
	}
	if uris[0].Path() != "/tmp/purego-dnd-a" || uris[1].Path() != "/tmp/purego-dnd-b" {
		t.Fatalf("paths = %q, %q", uris[0].Path(), uris[1].Path())
	}
}

func TestHandleDropEventCallsOnDropped(t *testing.T) {
	w := newWindow("drop")
	var gotPos fyne.Position
	var gotN int
	w.SetOnDropped(func(pos fyne.Position, items []fyne.URI) {
		gotPos = pos
		gotN = len(items)
	})
	w.handleEvent(dropEvent{Position: fyne.NewPos(10, 20), URIs: make([]fyne.URI, 2)})
	if gotPos != fyne.NewPos(10, 20) || gotN != 2 {
		t.Fatalf("OnDropped got pos=%v n=%d, want (10,20) n=2", gotPos, gotN)
	}
}

func TestXdndAwarePropertySet(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })

	peer := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = peer.Close() })

	aware, err := peer.internAtom("XdndAware")
	if err != nil {
		t.Fatalf("internAtom(XdndAware): %v", err)
	}
	got := getPropertyCARD32(t, peer, s.winID, aware)
	if got != xdndVersion {
		t.Fatalf("XdndAware = %d, want %d", got, xdndVersion)
	}
}

func TestHarnessXdndDropDeliversTwoURIs(t *testing.T) {
	s := openX11ForTest(t, 200, 100)
	t.Cleanup(func() { _ = s.Close() })

	w := newWindow("xdnd-drop")
	w.surface = s
	w.shown = true

	got := make(chan dropEvent, 1)
	w.SetOnDropped(func(pos fyne.Position, items []fyne.URI) {
		got <- dropEvent{Position: pos, URIs: items}
	})

	src := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = src.Close() })
	srcWin := src.winID

	enter, err := src.internAtom("XdndEnter")
	if err != nil {
		t.Fatalf("internAtom(XdndEnter): %v", err)
	}
	position, err := src.internAtom("XdndPosition")
	if err != nil {
		t.Fatalf("internAtom(XdndPosition): %v", err)
	}
	drop, err := src.internAtom("XdndDrop")
	if err != nil {
		t.Fatalf("internAtom(XdndDrop): %v", err)
	}
	selection, err := src.internAtom("XdndSelection")
	if err != nil {
		t.Fatalf("internAtom(XdndSelection): %v", err)
	}
	uriList, err := src.internAtom(xdndURIListAtomName)
	if err != nil {
		t.Fatalf("internAtom(text/uri-list): %v", err)
	}
	action, err := src.internAtom("XdndActionCopy")
	if err != nil {
		t.Fatalf("internAtom(XdndActionCopy): %v", err)
	}

	if err := xdndSetSelectionOwner(src, srcWin, selection); err != nil {
		t.Fatalf("SetSelectionOwner: %v", err)
	}
	if err := waitSelectionOwnerOn(src, srcWin, selection); err != nil {
		t.Fatalf("waitSelectionOwner: %v", err)
	}

	payload := []byte("file:///tmp/purego-dnd-a\r\nfile:///tmp/purego-dnd-b\r\n")
	go xdndSourceRespond(src, selection, uriList, payload)

	beforeDrop := countX11ClientSockets(t)
	const dropX, dropY = 40, 60
	const xdndVersionShift = 24
	flags := uint32(xdndVersion) << xdndVersionShift
	coords := uint32(dropX)<<xdndCoordShift | uint32(dropY)

	_ = src.sendXdndClientMessage(s.winID, enter, [xdndClientDataCount]uint32{
		srcWin, flags, uriList, 0, 0,
	})
	_ = src.sendXdndClientMessage(s.winID, position, [xdndClientDataCount]uint32{
		srcWin, 0, coords, 0, action,
	})
	_ = src.sendXdndClientMessage(s.winID, drop, [xdndClientDataCount]uint32{
		srcWin, 0, 0, 0, 0,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.drainEvents()
		select {
		case ev := <-got:
			if int(ev.Position.X) != dropX || int(ev.Position.Y) != dropY {
				t.Fatalf("drop position = %v, want (%d,%d)", ev.Position, dropX, dropY)
			}
			if len(ev.URIs) != 2 {
				t.Fatalf("got %d URIs, want 2", len(ev.URIs))
			}
			wantA := storage.NewFileURI(filepath.Clean("/tmp/purego-dnd-a"))
			wantB := storage.NewFileURI(filepath.Clean("/tmp/purego-dnd-b"))
			if ev.URIs[0].Path() != wantA.Path() || ev.URIs[1].Path() != wantB.Path() {
				t.Fatalf("URIs = %q, %q", ev.URIs[0].Path(), ev.URIs[1].Path())
			}
			afterDrop := countX11ClientSockets(t)
			if afterDrop != beforeDrop {
				t.Fatalf("XDND drop opened an X11 socket: before=%d after=%d", beforeDrop, afterDrop)
			}
			return
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatal("OnDropped did not fire after XDND Enter/Position/Drop")
}

func xdndSetSelectionOwner(s *x11Surface, win, selection uint32) error {
	req := make([]byte, setSelectionOwnerReqLen)
	req[0] = opSetSelectionOwner
	binary.LittleEndian.PutUint16(req[2:4], setSelectionOwnerReqWords)
	binary.LittleEndian.PutUint32(req[4:8], win)
	binary.LittleEndian.PutUint32(req[8:12], selection)
	return s.writeRequest(req)
}

func xdndSourceRespond(s *x11Surface, selection, uriList uint32, payload []byte) {
	id, ch := s.subscribeX11Event(eventCodeSelectionRequest, s.winID)
	defer s.unsubscribeX11Event(id)
	for {
		buf, err := waitX11Event(ch, s.done, 5*time.Second)
		if err != nil {
			return
		}
		requestor := binary.LittleEndian.Uint32(buf[12:16])
		sel := binary.LittleEndian.Uint32(buf[16:20])
		target := binary.LittleEndian.Uint32(buf[20:24])
		property := binary.LittleEndian.Uint32(buf[24:28])
		if property == atomNone {
			property = target
		}
		if sel != selection || target != uriList {
			xdndSendSelectionNotify(s, requestor, sel, target, atomNone)
			continue
		}
		if !xdndChangeProperty(s, requestor, property, uriList, propFormat8, payload) {
			property = atomNone
		}
		xdndSendSelectionNotify(s, requestor, sel, target, property)
	}
}

func xdndChangeProperty(s *x11Surface, window, property, typ uint32, format byte, value []byte) bool {
	const changePropertyHeaderWords = 6
	pad := (setupWordSize - len(value)%setupWordSize) % setupWordSize
	reqWords := changePropertyHeaderWords + (len(value)+pad)/setupWordSize
	req := make([]byte, reqWords*setupWordSize)
	req[0] = opChangeProperty
	req[1] = propModeReplace
	binary.LittleEndian.PutUint16(req[2:4], uint16(reqWords)) //nolint:gosec // uri-list payload is two short paths
	binary.LittleEndian.PutUint32(req[4:8], window)
	binary.LittleEndian.PutUint32(req[8:12], property)
	binary.LittleEndian.PutUint32(req[12:16], typ)
	req[changePropertyFormatOffset] = format
	units := len(value)
	if format == propFormat32 {
		units /= 4
	}
	binary.LittleEndian.PutUint32(req[20:24], uint32(units)) //nolint:gosec // uri-list payload is two short paths
	copy(req[24:], value)
	return s.writeRequest(req) == nil
}

func xdndSendSelectionNotify(s *x11Surface, requestor, selection, target, property uint32) {
	event := make([]byte, coreEventSize)
	event[0] = eventCodeSelectionNotify
	binary.LittleEndian.PutUint32(event[8:12], requestor)
	binary.LittleEndian.PutUint32(event[12:16], selection)
	binary.LittleEndian.PutUint32(event[16:20], target)
	binary.LittleEndian.PutUint32(event[20:24], property)
	req := make([]byte, sendEventReqLen)
	req[0] = opSendEvent
	binary.LittleEndian.PutUint16(req[2:4], uint16(sendEventReqWords))
	binary.LittleEndian.PutUint32(req[4:8], requestor)
	copy(req[sendEventHeaderLen:], event)
	_ = s.writeRequest(req)
}

func getPropertyCARD32(t *testing.T, s *x11Surface, window, property uint32) uint32 {
	t.Helper()
	_, extra, err := s.getPropertyOnce(window, property, false)
	if err != nil {
		t.Fatalf("GetProperty: %v", err)
	}
	if len(extra) < 4 {
		t.Fatal("XdndAware property missing")
	}
	return binary.LittleEndian.Uint32(extra[:4])
}

func TestXdndStatusRefusesNonURI(t *testing.T) {
	s := openX11ForTest(t, 200, 100)
	t.Cleanup(func() { _ = s.Close() })

	src := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = src.Close() })
	srcWin := src.winID

	enter, err := src.internAtom("XdndEnter")
	if err != nil {
		t.Fatalf("internAtom(XdndEnter): %v", err)
	}
	position, err := src.internAtom("XdndPosition")
	if err != nil {
		t.Fatalf("internAtom(XdndPosition): %v", err)
	}
	status, err := src.internAtom("XdndStatus")
	if err != nil {
		t.Fatalf("internAtom(XdndStatus): %v", err)
	}
	png, err := src.internAtom("image/png")
	if err != nil {
		t.Fatalf("internAtom(image/png): %v", err)
	}
	action, err := src.internAtom("XdndActionCopy")
	if err != nil {
		t.Fatalf("internAtom(XdndActionCopy): %v", err)
	}

	const xdndVersionShift = 24
	flags := uint32(xdndVersion) << xdndVersionShift
	coords := uint32(40)<<xdndCoordShift | uint32(60)

	cmID, cmCh := src.subscribeX11Event(eventCodeClientMessage, 0)
	t.Cleanup(func() { src.unsubscribeX11Event(cmID) })

	_ = src.sendXdndClientMessage(s.winID, enter, [xdndClientDataCount]uint32{
		srcWin, flags, png, 0, 0,
	})
	_ = src.sendXdndClientMessage(s.winID, position, [xdndClientDataCount]uint32{
		srcWin, 0, coords, 0, action,
	})

	data := readXdndClientMessage(t, src, cmCh, status)
	if data[1]&xdndStatusAccept != 0 {
		t.Fatal("XdndStatus accept bit set, want 0 for a non-URI type")
	}
	if data[4] != 0 {
		t.Fatalf("XdndStatus action = %d, want None (0) when the drop is refused", data[4])
	}
}

func TestXdndFinishedHonorsMove(t *testing.T) {
	s := openX11ForTest(t, 200, 100)
	t.Cleanup(func() { _ = s.Close() })

	w := newWindow("xdnd-move")
	w.surface = s
	w.shown = true
	got := make(chan dropEvent, 1)
	w.SetOnDropped(func(pos fyne.Position, items []fyne.URI) {
		got <- dropEvent{Position: pos, URIs: items}
	})

	src := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = src.Close() })
	srcWin := src.winID

	enter, err := src.internAtom("XdndEnter")
	if err != nil {
		t.Fatalf("internAtom(XdndEnter): %v", err)
	}
	position, err := src.internAtom("XdndPosition")
	if err != nil {
		t.Fatalf("internAtom(XdndPosition): %v", err)
	}
	drop, err := src.internAtom("XdndDrop")
	if err != nil {
		t.Fatalf("internAtom(XdndDrop): %v", err)
	}
	finished, err := src.internAtom("XdndFinished")
	if err != nil {
		t.Fatalf("internAtom(XdndFinished): %v", err)
	}
	selection, err := src.internAtom("XdndSelection")
	if err != nil {
		t.Fatalf("internAtom(XdndSelection): %v", err)
	}
	uriList, err := src.internAtom(xdndURIListAtomName)
	if err != nil {
		t.Fatalf("internAtom(text/uri-list): %v", err)
	}
	move, err := src.internAtom("XdndActionMove")
	if err != nil {
		t.Fatalf("internAtom(XdndActionMove): %v", err)
	}

	if err := xdndSetSelectionOwner(src, srcWin, selection); err != nil {
		t.Fatalf("SetSelectionOwner: %v", err)
	}
	if err := waitSelectionOwnerOn(src, srcWin, selection); err != nil {
		t.Fatalf("waitSelectionOwner: %v", err)
	}

	watch := newXdndSourceWatch(finished)
	payload := []byte("file:///tmp/purego-dnd-move\r\n")
	go xdndSourceRespondWatch(src, selection, uriList, payload, watch)

	const xdndVersionShift = 24
	flags := uint32(xdndVersion) << xdndVersionShift
	coords := uint32(40)<<xdndCoordShift | uint32(60)

	_ = src.sendXdndClientMessage(s.winID, enter, [xdndClientDataCount]uint32{
		srcWin, flags, uriList, 0, 0,
	})
	_ = src.sendXdndClientMessage(s.winID, position, [xdndClientDataCount]uint32{
		srcWin, 0, coords, 0, move,
	})
	_ = src.sendXdndClientMessage(s.winID, drop, [xdndClientDataCount]uint32{
		srcWin, 0, 0, 0, 0,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.drainEvents()
		select {
		case <-got:
			data := watch.waitFinished(t, time.Until(deadline))
			if data[2] != move {
				t.Fatalf("XdndFinished action = %d, want XdndActionMove %d", data[2], move)
			}
			return
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatal("OnDropped did not fire for XdndActionMove")
}

func TestXdndINCRLargeSelection(t *testing.T) {
	s := openX11ForTest(t, 200, 100)
	t.Cleanup(func() { _ = s.Close() })

	w := newWindow("xdnd-incr")
	w.surface = s
	w.shown = true
	got := make(chan dropEvent, 1)
	w.SetOnDropped(func(pos fyne.Position, items []fyne.URI) {
		got <- dropEvent{Position: pos, URIs: items}
	})

	src := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = src.Close() })
	srcWin := src.winID

	enter, err := src.internAtom("XdndEnter")
	if err != nil {
		t.Fatalf("internAtom(XdndEnter): %v", err)
	}
	position, err := src.internAtom("XdndPosition")
	if err != nil {
		t.Fatalf("internAtom(XdndPosition): %v", err)
	}
	drop, err := src.internAtom("XdndDrop")
	if err != nil {
		t.Fatalf("internAtom(XdndDrop): %v", err)
	}
	selection, err := src.internAtom("XdndSelection")
	if err != nil {
		t.Fatalf("internAtom(XdndSelection): %v", err)
	}
	uriList, err := src.internAtom(xdndURIListAtomName)
	if err != nil {
		t.Fatalf("internAtom(text/uri-list): %v", err)
	}
	action, err := src.internAtom("XdndActionCopy")
	if err != nil {
		t.Fatalf("internAtom(XdndActionCopy): %v", err)
	}

	if err := xdndSetSelectionOwner(src, srcWin, selection); err != nil {
		t.Fatalf("SetSelectionOwner: %v", err)
	}
	if err := waitSelectionOwnerOn(src, srcWin, selection); err != nil {
		t.Fatalf("waitSelectionOwner: %v", err)
	}

	incr, err := src.internAtom("INCR")
	if err != nil {
		t.Fatalf("internAtom(INCR): %v", err)
	}

	const minINCR = 256*1024 + 1
	line := []byte("file:///tmp/purego-dnd-incr-item\r\n")
	payload := bytes.Repeat(line, (minINCR+len(line)-1)/len(line))
	wantN := bytes.Count(payload, []byte("\n"))
	go xdndSourceRespondINCR(src, selection, uriList, incr, payload)

	const xdndVersionShift = 24
	flags := uint32(xdndVersion) << xdndVersionShift
	coords := uint32(40)<<xdndCoordShift | uint32(60)

	_ = src.sendXdndClientMessage(s.winID, enter, [xdndClientDataCount]uint32{
		srcWin, flags, uriList, 0, 0,
	})
	_ = src.sendXdndClientMessage(s.winID, position, [xdndClientDataCount]uint32{
		srcWin, 0, coords, 0, action,
	})
	_ = src.sendXdndClientMessage(s.winID, drop, [xdndClientDataCount]uint32{
		srcWin, 0, 0, 0, 0,
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.drainEvents()
		select {
		case ev := <-got:
			if len(ev.URIs) != wantN {
				t.Fatalf("INCR drop got %d URIs, want %d", len(ev.URIs), wantN)
			}
			if ev.URIs[0].Path() != "/tmp/purego-dnd-incr-item" {
				t.Fatalf("first URI path = %q", ev.URIs[0].Path())
			}
			return
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatal("OnDropped did not fire for INCR selection > 256 KiB")
}

func TestXdndINCRImmediateFirstChunk(t *testing.T) {
	const minINCR = 256*1024 + 1
	line := []byte("file:///tmp/purego-dnd-incr-immediate\r\n")
	payload := bytes.Repeat(line, (minINCR+len(line)-1)/len(line))
	wantN := bytes.Count(payload, []byte("\n"))
	for i := 0; i < 20; i++ {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			xdndINCRImmediateOnce(t, payload, wantN)
		})
	}
}

func xdndINCRImmediateOnce(t *testing.T, payload []byte, wantN int) {
	t.Helper()
	s := openX11ForTest(t, 200, 100)
	t.Cleanup(func() { _ = s.Close() })

	w := newWindow("xdnd-incr-immediate")
	w.surface = s
	w.shown = true
	got := make(chan dropEvent, 1)
	w.SetOnDropped(func(pos fyne.Position, items []fyne.URI) {
		got <- dropEvent{Position: pos, URIs: items}
	})

	src := openX11ForTest(t, 1, 1, withoutMap())
	t.Cleanup(func() { _ = src.Close() })
	srcWin := src.winID

	enter, err := src.internAtom("XdndEnter")
	if err != nil {
		t.Fatalf("internAtom(XdndEnter): %v", err)
	}
	position, err := src.internAtom("XdndPosition")
	if err != nil {
		t.Fatalf("internAtom(XdndPosition): %v", err)
	}
	drop, err := src.internAtom("XdndDrop")
	if err != nil {
		t.Fatalf("internAtom(XdndDrop): %v", err)
	}
	selection, err := src.internAtom("XdndSelection")
	if err != nil {
		t.Fatalf("internAtom(XdndSelection): %v", err)
	}
	uriList, err := src.internAtom(xdndURIListAtomName)
	if err != nil {
		t.Fatalf("internAtom(text/uri-list): %v", err)
	}
	action, err := src.internAtom("XdndActionCopy")
	if err != nil {
		t.Fatalf("internAtom(XdndActionCopy): %v", err)
	}
	incr, err := src.internAtom("INCR")
	if err != nil {
		t.Fatalf("internAtom(INCR): %v", err)
	}

	if err := xdndSetSelectionOwner(src, srcWin, selection); err != nil {
		t.Fatalf("SetSelectionOwner: %v", err)
	}
	if err := waitSelectionOwnerOn(src, srcWin, selection); err != nil {
		t.Fatalf("waitSelectionOwner: %v", err)
	}

	go xdndSourceRespondINCR(src, selection, uriList, incr, payload)

	const xdndVersionShift = 24
	flags := uint32(xdndVersion) << xdndVersionShift
	coords := uint32(40)<<xdndCoordShift | uint32(60)

	_ = src.sendXdndClientMessage(s.winID, enter, [xdndClientDataCount]uint32{
		srcWin, flags, uriList, 0, 0,
	})
	_ = src.sendXdndClientMessage(s.winID, position, [xdndClientDataCount]uint32{
		srcWin, 0, coords, 0, action,
	})
	_ = src.sendXdndClientMessage(s.winID, drop, [xdndClientDataCount]uint32{
		srcWin, 0, 0, 0, 0,
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.drainEvents()
		select {
		case ev := <-got:
			if len(ev.URIs) != wantN {
				t.Fatalf("INCR drop got %d URIs, want %d", len(ev.URIs), wantN)
			}
			if ev.URIs[0].Path() != "/tmp/purego-dnd-incr-immediate" {
				t.Fatalf("first URI path = %q", ev.URIs[0].Path())
			}
			return
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatal("OnDropped did not fire for immediate-chunk INCR selection")
}

func readXdndClientMessage(t *testing.T, s *x11Surface, ch <-chan []byte, wantType uint32) [xdndClientDataCount]uint32 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		buf, err := waitX11Event(ch, s.done, time.Until(deadline))
		if err != nil {
			t.Fatalf("reading ClientMessage: %v", err)
		}
		typ := binary.LittleEndian.Uint32(buf[xdndEventTypeOffset : xdndEventTypeOffset+xdndEventDataWord])
		if typ != wantType {
			continue
		}
		var data [xdndClientDataCount]uint32
		for i := 0; i < xdndClientDataCount; i++ {
			data[i] = xdndData(buf, i)
		}
		return data
	}
	t.Fatal("timed out waiting for ClientMessage")
	return [xdndClientDataCount]uint32{}
}

type xdndSourceWatch struct {
	finished uint32
	ch       chan [xdndClientDataCount]uint32
}

func newXdndSourceWatch(finished uint32) *xdndSourceWatch {
	return &xdndSourceWatch{finished: finished, ch: make(chan [xdndClientDataCount]uint32, 1)}
}

func (w *xdndSourceWatch) waitFinished(t *testing.T, d time.Duration) [xdndClientDataCount]uint32 {
	t.Helper()
	select {
	case data := <-w.ch:
		return data
	case <-time.After(d):
		t.Fatal("timed out waiting for XdndFinished")
		return [xdndClientDataCount]uint32{}
	}
}

func xdndSourceRespondWatch(s *x11Surface, selection, uriList uint32, payload []byte, watch *xdndSourceWatch) {
	reqID, reqCh := s.subscribeX11Event(eventCodeSelectionRequest, s.winID)
	defer s.unsubscribeX11Event(reqID)
	cmID, cmCh := s.subscribeX11Event(eventCodeClientMessage, 0)
	defer s.unsubscribeX11Event(cmID)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-timer.C:
			return
		case buf := <-cmCh:
			if watch == nil {
				continue
			}
			typ := binary.LittleEndian.Uint32(buf[xdndEventTypeOffset : xdndEventTypeOffset+xdndEventDataWord])
			if typ != watch.finished {
				continue
			}
			var data [xdndClientDataCount]uint32
			for i := 0; i < xdndClientDataCount; i++ {
				data[i] = xdndData(buf, i)
			}
			select {
			case watch.ch <- data:
			default:
			}
			return
		case buf := <-reqCh:
			requestor := binary.LittleEndian.Uint32(buf[12:16])
			sel := binary.LittleEndian.Uint32(buf[16:20])
			target := binary.LittleEndian.Uint32(buf[20:24])
			property := binary.LittleEndian.Uint32(buf[24:28])
			if property == atomNone {
				property = target
			}
			if sel != selection || target != uriList {
				xdndSendSelectionNotify(s, requestor, sel, target, atomNone)
				continue
			}
			if !xdndChangeProperty(s, requestor, property, uriList, propFormat8, payload) {
				property = atomNone
			}
			xdndSendSelectionNotify(s, requestor, sel, target, property)
		}
	}
}

func xdndSourceRespondINCR(s *x11Surface, selection, uriList, incr uint32, payload []byte) {
	id, ch := s.subscribeX11Event(eventCodeSelectionRequest, s.winID)
	defer s.unsubscribeX11Event(id)
	buf, err := waitX11Event(ch, s.done, 5*time.Second)
	if err != nil {
		return
	}
	requestor := binary.LittleEndian.Uint32(buf[12:16])
	sel := binary.LittleEndian.Uint32(buf[16:20])
	target := binary.LittleEndian.Uint32(buf[20:24])
	property := binary.LittleEndian.Uint32(buf[24:28])
	if property == atomNone {
		property = target
	}
	if sel != selection || target != uriList {
		xdndSendSelectionNotify(s, requestor, sel, target, atomNone)
		return
	}
	if err := xdndSelectPropertyChange(s, requestor); err != nil {
		xdndSendSelectionNotify(s, requestor, sel, target, atomNone)
		return
	}
	propID, propCh := s.subscribeX11Event(eventCodePropertyNotify, requestor)
	defer s.unsubscribeX11Event(propID)
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(len(payload))) //nolint:gosec // INCR size estimate is the payload length in bytes
	if !xdndChangeProperty(s, requestor, property, incr, propFormat32, size) {
		xdndSendSelectionNotify(s, requestor, sel, target, atomNone)
		return
	}
	xdndSendSelectionNotify(s, requestor, sel, target, property)
	const chunk = 32 * 1024
	off := 0
	for off < len(payload) {
		if err := xdndWaitPropertyDeleted(s, propCh, requestor, property); err != nil {
			return
		}
		end := off + chunk
		if end > len(payload) {
			end = len(payload)
		}
		if !xdndChangeProperty(s, requestor, property, uriList, propFormat8, payload[off:end]) {
			return
		}
		off = end
	}
	if err := xdndWaitPropertyDeleted(s, propCh, requestor, property); err != nil {
		return
	}
	_ = xdndChangeProperty(s, requestor, property, uriList, propFormat8, nil)
}

func xdndSelectPropertyChange(s *x11Surface, window uint32) error {
	req := make([]byte, changeAttrsReqLen)
	req[0] = opChangeWindowAttrs
	binary.LittleEndian.PutUint16(req[2:4], changeAttrsReqWords)
	binary.LittleEndian.PutUint32(req[4:8], window)
	binary.LittleEndian.PutUint32(req[8:12], cwEventMask)
	binary.LittleEndian.PutUint32(req[12:16], eventMaskPropertyChange)
	return s.writeRequest(req)
}

func xdndWaitPropertyDeleted(s *x11Surface, ch <-chan []byte, window, property uint32) error {
	for {
		buf, err := waitX11Event(ch, s.done, 5*time.Second)
		if err != nil {
			return err
		}
		win := binary.LittleEndian.Uint32(buf[4:8])
		atom := binary.LittleEndian.Uint32(buf[8:12])
		if win == window && atom == property && buf[propertyNotifyStateOffset] == propertyStateDeleted {
			return nil
		}
	}
}
