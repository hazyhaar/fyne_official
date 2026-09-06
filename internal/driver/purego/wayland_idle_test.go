//go:build purego && linux

package purego

import (
	"encoding/binary"
	"os"
	"sync"
	"testing"
)

func TestWaylandIdleInhibitMissingIsNoOp(t *testing.T) {
	wlIdleLogOnce = sync.Once{}
	wlIdleMu.Lock()
	wlIdleLogged = false
	wlIdleMu.Unlock()

	s := &waylandSurface{
		done:      make(chan struct{}),
		surfaceID: 5,
		nextID:    10,
	}
	t.Cleanup(func() { close(s.done) })

	waylandSetIdleInhibit(s, true)
	waylandSetIdleInhibit(s, true)
	if !waylandIdleInhibitLogged() {
		t.Fatal("missing idle-inhibit global was not logged")
	}
	st := waylandIdleOf(s)
	st.mu.Lock()
	id := st.inhibitorID
	st.mu.Unlock()
	if id != 0 {
		t.Fatalf("inhibitor id = %d, want 0 when the global is absent", id)
	}
}

func TestWaylandIdleInhibitSendsCreateInhibitor(t *testing.T) {
	client, server := unixSocketPair(t)
	defer client.Close()
	defer server.Close()

	s := &waylandSurface{
		conn:             client,
		done:             make(chan struct{}),
		idleInhibitMgrID: 7,
		surfaceID:        3,
		nextID:           20,
	}
	t.Cleanup(func() { close(s.done) })

	waylandSetIdleInhibit(s, true)
	obj, op, payload := readWlMessage(t, server)
	if obj != 7 || op != zwpIdleInhibitMgrOpCreateInhibitor {
		t.Fatalf("create_inhibitor sent object=%d opcode=%d, want 7/%d", obj, op, zwpIdleInhibitMgrOpCreateInhibitor)
	}
	if len(payload) < 8 {
		t.Fatalf("create_inhibitor payload %v too short", payload)
	}
	newID := binary.LittleEndian.Uint32(payload[0:4])
	surf := binary.LittleEndian.Uint32(payload[4:8])
	if surf != 3 {
		t.Fatalf("create_inhibitor surface=%d, want 3", surf)
	}
	st := waylandIdleOf(s)
	st.mu.Lock()
	got := st.inhibitorID
	st.mu.Unlock()
	if got != newID {
		t.Fatalf("recorded inhibitor %d, wire new_id %d", got, newID)
	}

	waylandSetIdleInhibit(s, false)
	obj, op, payload = readWlMessage(t, server)
	if obj != newID || op != zwpIdleInhibitorOpDestroy {
		t.Fatalf("destroy sent object=%d opcode=%d, want %d/%d", obj, op, newID, zwpIdleInhibitorOpDestroy)
	}
	if len(payload) != 0 {
		t.Fatalf("destroy payload = %v, want empty", payload)
	}
}

func TestWaylandIdleInhibitUnderCage(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("purego/wayland: WAYLAND_DISPLAY unset, Wayland tests run in the Wayland suite only")
	}
	s := openWaylandForTest(t)
	d := NewDriver().(*driverImpl)
	w := newWindow("idle")
	w.driver = d
	w.surface = s
	w.shown = true
	d.mu.Lock()
	d.windows = append(d.windows, w)
	d.mu.Unlock()

	s.mu.Lock()
	mgr := s.idleInhibitMgrID
	s.mu.Unlock()
	if mgr == 0 {
		// cage (and many nested compositors) do not advertise
		// zwp_idle_inhibit_manager_v1. The path is a once-only log and a no-op.
		wlIdleLogOnce = sync.Once{}
		wlIdleMu.Lock()
		wlIdleLogged = false
		wlIdleMu.Unlock()
		d.SetDisableScreenBlanking(true)
		if !waylandIdleInhibitLogged() {
			t.Fatal("cage without idle-inhibit did not log the no-op")
		}
		return
	}
	d.SetDisableScreenBlanking(true)
	st := waylandIdleOf(s)
	st.mu.Lock()
	id := st.inhibitorID
	st.mu.Unlock()
	if id == 0 {
		t.Fatal("idle inhibitor was not created despite advertised manager")
	}
	d.SetDisableScreenBlanking(false)
}
