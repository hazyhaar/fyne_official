//go:build purego

package purego

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	fynecanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
)

type modifierClickRec struct {
	*fynecanvas.Rectangle
	mods []fyne.KeyModifier
}

func (m *modifierClickRec) MouseDown(e *desktop.MouseEvent) {
	m.mods = append(m.mods, e.Modifier)
}

func (m *modifierClickRec) MouseUp(e *desktop.MouseEvent) {
	m.mods = append(m.mods, e.Modifier)
}

func TestHandlePointerControlClickSetsModifier(t *testing.T) {
	test.NewApp()
	w := newWindow("ctrl-click")
	rec := &modifierClickRec{Rectangle: fynecanvas.NewRectangle(color.White)}
	rec.SetMinSize(fyne.NewSize(200, 200))
	w.SetContent(rec)
	w.canvas.Resize(fyne.NewSize(200, 200))

	w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 1, Pressed: true, Modifiers: modControl})
	w.handlePointer(pointerEvent{X: 50, Y: 50, Button: 1, Pressed: false, Modifiers: modControl})
	if len(rec.mods) < 1 {
		t.Fatal("MouseDown was not delivered")
	}
	if rec.mods[0] != fyne.KeyModifierControl {
		t.Fatalf("MouseEvent.Modifier = %v, want ControlModifier", rec.mods[0])
	}
}

type focusHook struct {
	*fynecanvas.Rectangle
	gained, lost int
}

func (f *focusHook) FocusGained()          { f.gained++ }
func (f *focusHook) FocusLost()            { f.lost++ }
func (*focusHook) TypedRune(rune)          {}
func (*focusHook) TypedKey(*fyne.KeyEvent) {}

func TestHandleFocusEventNotifiesCanvas(t *testing.T) {
	test.NewApp()
	w := newWindow("focus-event")
	h := &focusHook{Rectangle: fynecanvas.NewRectangle(color.White)}
	h.SetMinSize(fyne.NewSize(200, 200))
	w.SetContent(h)
	w.canvas.Resize(fyne.NewSize(200, 200))
	w.canvas.Focus(h)
	h.gained, h.lost = 0, 0

	w.handleEvent(focusEvent{Gained: false})
	if h.lost != 1 {
		t.Fatalf("FocusLost count = %d, want 1", h.lost)
	}
	w.handleEvent(focusEvent{Gained: true})
	if h.gained != 1 {
		t.Fatalf("FocusGained count = %d, want 1", h.gained)
	}
}
