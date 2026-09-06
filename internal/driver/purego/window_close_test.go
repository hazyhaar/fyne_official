//go:build purego

package purego

import (
	"testing"
)

func TestCloseInterceptCallingCloseDoesNotRecurse(t *testing.T) {
	w := newWindow("close-intercept")
	n := 0
	w.SetCloseIntercept(func() {
		n++
		if n > 8 {
			t.Fatal("SetCloseIntercept recursed through Close")
			return
		}
		w.Close()
	})
	w.handleEvent(closeEvent{})
	if n != 1 {
		t.Fatalf("intercept calls = %d, want 1", n)
	}
	if !w.closed {
		t.Fatal("window was not closed after intercept called Close")
	}
}

func TestCloseEventWithoutInterceptCloses(t *testing.T) {
	w := newWindow("close-wm")
	w.handleEvent(closeEvent{})
	if !w.closed {
		t.Fatal("window-manager close request did not close the window")
	}
}

func TestExplicitCloseIgnoresIntercept(t *testing.T) {
	w := newWindow("close-explicit")
	w.SetCloseIntercept(func() {
		t.Fatal("explicit Close invoked the intercept")
	})
	w.Close()
	if !w.closed {
		t.Fatal("explicit Close did not close the window")
	}
}
