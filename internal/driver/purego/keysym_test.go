//go:build purego

package purego

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

func TestKeysymToKey(t *testing.T) {
	tests := []struct {
		family string
		ks     uint32
		name   fyne.KeyName
		r      rune
	}{
		{"latin1-letter", 0x61, fyne.KeyA, 'a'},
		{"latin1-letter", 0x41, fyne.KeyA, 'A'},
		{"latin1-digit", 0x31, fyne.Key1, '1'},
		{"latin1-space", 0x20, fyne.KeySpace, ' '},
		{"latin1-eacute", 0xe9, "", 'é'},
		{"unicode-euro", unicodeKeysymBase + 0x20ac, "", '€'},
		{"unicode-arrow", unicodeKeysymBase + 0x2192, "", '→'},
		{"legacy-euro", 0x20ac, "", '€'},
		{"legacy-arrow", 0x08fd, "", '→'},
		{"unknown", 0x01, "", 0},
		{"function-return", 0xff0d, fyne.KeyReturn, 0},
		{"function-escape", 0xff1b, fyne.KeyEscape, 0},
		{"function-backspace", 0xff08, fyne.KeyBackspace, 0},
		{"function-tab", 0xff09, fyne.KeyTab, 0},
		{"function-left", 0xff51, fyne.KeyLeft, 0},
		{"function-up", 0xff52, fyne.KeyUp, 0},
		{"function-right", 0xff53, fyne.KeyRight, 0},
		{"function-down", 0xff54, fyne.KeyDown, 0},
		{"function-home", 0xff50, fyne.KeyHome, 0},
		{"function-end", 0xff57, fyne.KeyEnd, 0},
		{"function-pageup", 0xff55, fyne.KeyPageUp, 0},
		{"function-pagedown", 0xff56, fyne.KeyPageDown, 0},
		{"function-insert", 0xff63, fyne.KeyInsert, 0},
		{"function-delete", 0xffff, fyne.KeyDelete, 0},
		{"function-f1", 0xffbe, fyne.KeyF1, 0},
		{"function-f12", 0xffc9, fyne.KeyF12, 0},
		{"keypad-0", 0xffb0, fyne.Key0, '0'},
		{"keypad-9", 0xffb9, fyne.Key9, '9'},
		{"keypad-enter", 0xff8d, fyne.KeyEnter, 0},
		{"keypad-add", 0xffab, fyne.KeyPlus, '+'},
		{"mod-shift-l", 0xffe1, desktop.KeyShiftLeft, 0},
		{"mod-shift-r", 0xffe2, desktop.KeyShiftRight, 0},
		{"mod-control-l", 0xffe3, desktop.KeyControlLeft, 0},
		{"mod-alt-l", 0xffe9, desktop.KeyAltLeft, 0},
		{"mod-super-l", 0xffeb, desktop.KeySuperLeft, 0},
		{"mod-caps", 0xffe5, desktop.KeyCapsLock, 0},
		{"mod-numlock", 0xff7f, fyne.KeyName("NumLock"), 0},
	}
	for _, tc := range tests {
		t.Run(tc.family, func(t *testing.T) {
			name, r := keysymToKey(tc.ks)
			if name != tc.name || r != tc.r {
				t.Fatalf("keysymToKey(0x%X) = (%q, %q), want (%q, %q)", tc.ks, name, r, tc.name, tc.r)
			}
		})
	}
}
