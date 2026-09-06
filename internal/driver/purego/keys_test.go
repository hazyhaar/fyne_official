//go:build purego

package purego

import "testing"

func TestSelectCoreKeysym(t *testing.T) {
	const (
		modeSwitch = uint16(1 << 7)
		numLock    = uint16(1 << 4)
	)
	letter := [coreKeysymCols]uint32{0x61, 0x41}
	digit := [coreKeysymCols]uint32{0x31, 0x21}
	emptyShift := [coreKeysymCols]uint32{0x61, 0}
	altGr := [coreKeysymCols]uint32{0x61, 0x41, 0xe9, 0xc9}
	keypad := [coreKeysymCols]uint32{xkKPHome, xkKP0 + 7}

	tests := []struct {
		name        string
		syms        [coreKeysymCols]uint32
		state       uint16
		mode, num   uint16
		lockMeaning uint32
		want        uint32
	}{
		{"plain-a", letter, 0, 0, 0, xkCapsLock, 0x61},
		{"shift-a", letter, modifierShift, 0, 0, xkCapsLock, 0x41},
		{"caps-a", letter, modifierLock, 0, 0, xkCapsLock, 0x41},
		{"caps-shift-a", letter, modifierShift | modifierLock, 0, 0, xkCapsLock, 0x61},
		{"empty-col1-shift", emptyShift, modifierShift, 0, 0, 0, 0x41},
		{"digit", digit, 0, 0, 0, xkCapsLock, 0x31},
		{"shift-digit", digit, modifierShift, 0, 0, xkCapsLock, 0x21},
		{"caps-digit", digit, modifierLock, 0, 0, xkCapsLock, 0x31},
		{"mode-switch", altGr, modeSwitch, modeSwitch, 0, 0, 0xe9},
		{"mode-switch-shift", altGr, modeSwitch | modifierShift, modeSwitch, 0, 0, 0xc9},
		{"kp-nav", keypad, 0, 0, numLock, 0, xkKPHome},
		{"kp-numlock", keypad, numLock, 0, numLock, 0, xkKP0 + 7},
		{"kp-numlock-shift", keypad, numLock | modifierShift, 0, numLock, 0, xkKPHome},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectCoreKeysym(tc.syms, tc.state, tc.mode, tc.num, tc.lockMeaning)
			if got != tc.want {
				t.Fatalf("selectCoreKeysym = 0x%X, want 0x%X", got, tc.want)
			}
		})
	}
}
