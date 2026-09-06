//go:build purego

package purego

import (
	"testing"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/internal/cache"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// TestHandleKeyRoutesToFocusedWidget is the regression test for when
// handleKey called only the canvas-level
// OnTypedRune/OnTypedKey callbacks, which are nil for an ordinary
// application window, instead of checking w.canvas.Focused() first the way
// internal/driver/glfw/window.go's processCharInput/processKeyEvent do. A
// key press therefore never reached a focused widget.Entry — this test
// fails on that exact scenario if handleKey regresses to canvas-only
// dispatch.
func TestHandleKeyRoutesToFocusedWidget(t *testing.T) {
	test.NewApp()
	w := newWindow("test")
	entry := widget.NewEntry()
	w.SetContent(entry)
	w.canvas.Resize(fyne.NewSize(200, 200))
	w.canvas.Focus(entry)

	if w.canvas.Focused() != entry {
		t.Fatal("entry did not become focused; the test setup, not handleKey, is broken")
	}

	w.handleKey(keyEvent{Pressed: true, KeySym: 0x61})

	if entry.Text != "a" {
		t.Fatalf("entry.Text = %q after a key press with the entry focused, want %q", entry.Text, "a")
	}
}

// TestHandleKeyFallsBackToCanvasCallback confirms the other half of the
// fix: with nothing focused, handleKey must still reach the canvas-level
// OnTypedRune/OnTypedKey callbacks exactly as before — this is what an
// application relies on outside of any focused widget (e.g. window-level
// key handling registered directly on the canvas).
func TestHandleKeyFallsBackToCanvasCallback(t *testing.T) {
	test.NewApp()
	w := newWindow("test")
	w.canvas.Resize(fyne.NewSize(200, 200))

	var gotRune rune
	var gotKey fyne.KeyName
	w.canvas.SetOnTypedRune(func(r rune) { gotRune = r })
	w.canvas.SetOnTypedKey(func(k *fyne.KeyEvent) { gotKey = k.Name })

	w.handleKey(keyEvent{Pressed: true, KeySym: 0x61})

	if gotRune != 'a' {
		t.Fatalf("canvas OnTypedRune got %q, want 'a'", gotRune)
	}
	if gotKey != fyne.KeyA {
		t.Fatalf("canvas OnTypedKey got %q, want %q", gotKey, fyne.KeyA)
	}
}

// TestHandleKeyControlCTriggersCopyShortcut checks the shortcut half of the
// fix's ordering (glfw's triggersShortcut runs before TypedKey): Ctrl+C with
// something focused must reach the canvas's registered ShortcutCopy
// handler, not TypedKey.
func TestHandleKeyControlCTriggersCopyShortcut(t *testing.T) {
	test.NewApp()
	w := newWindow("test")
	w.canvas.Resize(fyne.NewSize(200, 200))

	triggered := false
	w.canvas.AddShortcut(&fyne.ShortcutCopy{}, func(fyne.Shortcut) { triggered = true })

	w.handleKey(keyEvent{Pressed: true, KeySym: 0x63, Modifiers: modControl})

	if !triggered {
		t.Fatal("Ctrl+C did not trigger the registered ShortcutCopy handler")
	}
}

func TestHandleKeyCustomShortcutCtrlShiftK(t *testing.T) {
	test.NewApp()
	w := newWindow("test")
	w.canvas.Resize(fyne.NewSize(200, 200))

	triggered := false
	w.canvas.AddShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyK,
		Modifier: fyne.KeyModifierControl | fyne.KeyModifierShift,
	}, func(fyne.Shortcut) { triggered = true })

	w.handleKey(keyEvent{Pressed: true, KeySym: 0x6b, Modifiers: modControl | modShift})

	if !triggered {
		t.Fatal("Ctrl+Shift+K did not trigger the registered CustomShortcut handler")
	}
}

// TestHandleKeyShortcutPrefersFocusedWidget is the regression test for the
// regression once seen in this code: handleKey gave a Ctrl shortcut to
// the canvas's registered handler even when a focused widget implementing
// fyne.Shortcutable (widget.Entry does, for Copy/Paste/Cut) exists, the
// opposite of internal/driver/glfw/window.go:885-899's order. This test sets
// up both a focused Entry with selected text to copy and a canvas-level
// ShortcutCopy handler, and requires the Entry's own TypedShortcut to run,
// not the canvas one — exactly the ordering glfw uses so a focused Entry's
// Ctrl+C/V/X is never stolen by a window-level shortcut.
func TestHandleKeyShortcutPrefersFocusedWidget(t *testing.T) {
	test.NewApp()
	w := newWindow("test")
	entry := widget.NewEntry()
	entry.SetText("hello")
	entry.DoubleTapped(&fyne.PointEvent{}) // select the word under the cursor, giving Copy something to act on
	w.SetContent(entry)
	w.canvas.Resize(fyne.NewSize(200, 200))
	w.canvas.Focus(entry)

	canvasShortcutFired := false
	w.canvas.AddShortcut(&fyne.ShortcutCopy{}, func(fyne.Shortcut) { canvasShortcutFired = true })

	w.handleKey(keyEvent{Pressed: true, KeySym: 0x63, Modifiers: modControl})

	if canvasShortcutFired {
		t.Fatal("the canvas-level ShortcutCopy handler fired even though the focused Entry implements fyne.Shortcutable; it should have taken priority")
	}
	if got := w.Clipboard().Content(); got != "hello" {
		t.Fatalf("clipboard content after Ctrl+C on the focused Entry = %q, want %q", got, "hello")
	}
}

// TestClipboardIsStableAcrossCalls is the regression test for the
// regression once seen in this code: controlShortcut built a fresh
// NewClipboard() on every keystroke, so content copied on one Ctrl+C was
// gone by the next Ctrl+V (a brand new, empty in-memory clipboard every
// time). window.Clipboard() must return the same instance across calls when
// falling back to the process-local clipboard.
func TestClipboardIsStableAcrossCalls(t *testing.T) {
	test.NewApp()
	w := newWindow("test")

	first := w.Clipboard()
	first.SetContent("kept")

	second := w.Clipboard()
	if got := second.Content(); got != "kept" {
		t.Fatalf("second Clipboard() call sees content %q, want %q (window.Clipboard() is not reusing one instance)", got, "kept")
	}
}

var _ desktop.Keyable = (*keyableRecorder)(nil)

type keyableRecorder struct {
	fynecanvas.Rectangle
	seq []string
}

func (*keyableRecorder) FocusGained()   {}
func (*keyableRecorder) FocusLost()     {}
func (*keyableRecorder) TypedRune(rune) {}

func (k *keyableRecorder) TypedKey(*fyne.KeyEvent) {
	k.seq = append(k.seq, "TypedKey")
}

func (k *keyableRecorder) KeyDown(*fyne.KeyEvent) {
	k.seq = append(k.seq, "KeyDown")
}

func (k *keyableRecorder) KeyUp(*fyne.KeyEvent) {
	k.seq = append(k.seq, "KeyUp")
}

func TestHandleKeyKeyableDownTypedUp(t *testing.T) {
	test.NewApp()
	w := newWindow("keyable")
	rec := &keyableRecorder{}
	w.SetContent(rec)
	w.canvas.Resize(fyne.NewSize(200, 200))
	w.canvas.Focus(rec)

	if w.canvas.Focused() != rec {
		t.Fatal("keyable widget did not become focused")
	}

	w.handleKey(keyEvent{Pressed: true, KeySym: 0x61})
	w.handleKey(keyEvent{Pressed: false, KeySym: 0x61})

	want := []string{"KeyDown", "TypedKey", "KeyUp"}
	if len(rec.seq) != len(want) {
		t.Fatalf("event sequence = %v, want %v", rec.seq, want)
	}
	for i := range want {
		if rec.seq[i] != want[i] {
			t.Fatalf("event sequence = %v, want %v", rec.seq, want)
		}
	}
}

func TestHandleKeyCanvasOnKeyDownUp(t *testing.T) {
	test.NewApp()
	w := newWindow("keys")
	w.canvas.Resize(fyne.NewSize(200, 200))
	var down, up fyne.KeyName
	w.canvas.SetOnKeyDown(func(e *fyne.KeyEvent) { down = e.Name })
	w.canvas.SetOnKeyUp(func(e *fyne.KeyEvent) { up = e.Name })
	w.handleKey(keyEvent{Pressed: true, Name: fyne.KeyA})
	w.handleKey(keyEvent{Pressed: false, Name: fyne.KeyA})
	if down != fyne.KeyA {
		t.Fatalf("OnKeyDown got %q, want %q", down, fyne.KeyA)
	}
	if up != fyne.KeyA {
		t.Fatalf("OnKeyUp got %q, want %q", up, fyne.KeyA)
	}
	if w.canvas.OnKeyDown() == nil || w.canvas.OnKeyUp() == nil {
		t.Fatal("OnKeyDown/OnKeyUp getters returned nil after Set")
	}
}

func TestHandleKeyPhysicalAndRepeat(t *testing.T) {
	test.NewApp()
	w := newWindow("phys")
	w.canvas.Resize(fyne.NewSize(200, 200))
	var got *fyne.KeyEvent
	w.canvas.SetOnKeyDown(func(e *fyne.KeyEvent) { got = e })
	w.handleKey(keyEvent{Pressed: true, Name: fyne.KeyA, Scancode: 38, Repeat: true})
	if got == nil {
		t.Fatal("OnKeyDown was not called")
	}
	if got.Physical.ScanCode != 38 {
		t.Fatalf("Physical.ScanCode = %d, want 38", got.Physical.ScanCode)
	}
	if !got.Repeat {
		t.Fatal("Repeat = false, want true")
	}
}

func TestHandleKeyTabMovesFocusBetweenEntries(t *testing.T) {
	test.NewApp()
	w := newWindow("tab")
	e1 := widget.NewEntry()
	e2 := widget.NewEntry()
	w.SetContent(container.NewVBox(e1, e2))
	w.canvas.Resize(fyne.NewSize(200, 200))
	w.canvas.Focus(e1)
	if w.canvas.Focused() != e1 {
		t.Fatal("setup: e1 did not become focused")
	}
	w.handleKey(keyEvent{Pressed: true, Name: fyne.KeyTab})
	if w.canvas.Focused() != e2 {
		t.Fatalf("Tab focused %v, want e2", w.canvas.Focused())
	}
	w.handleKey(keyEvent{Pressed: true, Name: fyne.KeyTab, Modifiers: modShift})
	if w.canvas.Focused() != e1 {
		t.Fatalf("Shift+Tab focused %v, want e1", w.canvas.Focused())
	}
}

func TestCloseReleasesWidgetRenderer(t *testing.T) {
	test.NewApp()
	w := newWindow("close")
	e := widget.NewEntry()
	w.SetContent(e)
	w.canvas.Resize(fyne.NewSize(200, 100))
	w.canvas.EnsureMinSize()
	if !cache.IsRendered(e) {
		t.Fatal("setup: entry should have a renderer after EnsureMinSize")
	}
	if cache.GetCanvasForObject(e) != w.canvas {
		t.Fatal("setup: entry should be registered on the canvas")
	}
	overlay := fynecanvas.NewRectangle(theme.Color(theme.ColorNameBackground))
	w.canvas.Overlays().Add(overlay)
	if w.canvas.Overlays().Top() != overlay {
		t.Fatal("setup: overlay was not added")
	}
	w.Close()
	if cache.IsRendered(e) {
		t.Fatal("renderer still registered after Close")
	}
	if cache.GetCanvasForObject(e) != nil {
		t.Fatal("canvas still registered for widget after Close")
	}
	if w.canvas.Overlays().Top() != nil {
		t.Fatal("overlay still present after Close")
	}
}
