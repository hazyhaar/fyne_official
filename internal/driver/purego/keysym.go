//go:build purego

package purego

import (
	"unicode"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

const (
	latin1Min = 0x20
	latin1Max = 0xff

	unicodeKeysymMask      = 0xff000000
	unicodeKeysymBase      = 0x01000000
	unicodeCodepointMask   = 0x00ffffff
	xkRightArrowPublishing = 0x08fd
	xkEuroSign             = 0x20ac

	xkBackSpace = 0xff08
	xkTab       = 0xff09
	xkReturn    = 0xff0d
	xkEscape    = 0xff1b
	xkDelete    = 0xffff
	xkHome      = 0xff50
	xkLeft      = 0xff51
	xkUp        = 0xff52
	xkRight     = 0xff53
	xkDown      = 0xff54
	xkPageUp    = 0xff55
	xkPageDown  = 0xff56
	xkEnd       = 0xff57
	xkInsert    = 0xff63

	xkNumLock    = 0xff7f
	xkModeSwitch = 0xff7e

	xkKPSpace     = 0xff80
	xkKPTab       = 0xff89
	xkKPEnter     = 0xff8d
	xkKPHome      = 0xff95
	xkKPLeft      = 0xff96
	xkKPUp        = 0xff97
	xkKPRight     = 0xff98
	xkKPDown      = 0xff99
	xkKPPageUp    = 0xff9a
	xkKPPageDown  = 0xff9b
	xkKPEnd       = 0xff9c
	xkKPInsert    = 0xff9e
	xkKPDelete    = 0xff9f
	xkKPMultiply  = 0xffaa
	xkKPAdd       = 0xffab
	xkKPSeparator = 0xffac
	xkKPSubtract  = 0xffad
	xkKPDecimal   = 0xffae
	xkKPDivide    = 0xffaf
	xkKP0         = 0xffb0
	xkKP9         = 0xffb9
	xkKPEqual     = 0xffbd

	xkF1  = 0xffbe
	xkF2  = 0xffbf
	xkF3  = 0xffc0
	xkF4  = 0xffc1
	xkF5  = 0xffc2
	xkF6  = 0xffc3
	xkF7  = 0xffc4
	xkF8  = 0xffc5
	xkF9  = 0xffc6
	xkF10 = 0xffc7
	xkF11 = 0xffc8
	xkF12 = 0xffc9

	xkShiftL    = 0xffe1
	xkShiftR    = 0xffe2
	xkControlL  = 0xffe3
	xkControlR  = 0xffe4
	xkCapsLock  = 0xffe5
	xkShiftLock = 0xffe6
	xkAltL      = 0xffe9
	xkAltR      = 0xffea
	xkSuperL    = 0xffeb
	xkSuperR    = 0xffec

	xkISOLevel3Shift = 0xfe03
)

var keysymNames = map[uint32]fyne.KeyName{
	xkBackSpace:   fyne.KeyBackspace,
	xkTab:         fyne.KeyTab,
	xkReturn:      fyne.KeyReturn,
	xkEscape:      fyne.KeyEscape,
	xkDelete:      fyne.KeyDelete,
	xkHome:        fyne.KeyHome,
	xkLeft:        fyne.KeyLeft,
	xkUp:          fyne.KeyUp,
	xkRight:       fyne.KeyRight,
	xkDown:        fyne.KeyDown,
	xkPageUp:      fyne.KeyPageUp,
	xkPageDown:    fyne.KeyPageDown,
	xkEnd:         fyne.KeyEnd,
	xkInsert:      fyne.KeyInsert,
	xkNumLock:     fyne.KeyName("NumLock"),
	xkKPSpace:     fyne.KeySpace,
	xkKPTab:       fyne.KeyTab,
	xkKPEnter:     fyne.KeyEnter,
	xkKPHome:      fyne.KeyHome,
	xkKPLeft:      fyne.KeyLeft,
	xkKPUp:        fyne.KeyUp,
	xkKPRight:     fyne.KeyRight,
	xkKPDown:      fyne.KeyDown,
	xkKPPageUp:    fyne.KeyPageUp,
	xkKPPageDown:  fyne.KeyPageDown,
	xkKPEnd:       fyne.KeyEnd,
	xkKPInsert:    fyne.KeyInsert,
	xkKPDelete:    fyne.KeyDelete,
	xkKPMultiply:  fyne.KeyAsterisk,
	xkKPAdd:       fyne.KeyPlus,
	xkKPSeparator: fyne.KeyComma,
	xkKPSubtract:  fyne.KeyMinus,
	xkKPDecimal:   fyne.KeyPeriod,
	xkKPDivide:    fyne.KeySlash,
	xkKPEqual:     fyne.KeyEqual,
	xkShiftL:      desktop.KeyShiftLeft,
	xkShiftR:      desktop.KeyShiftRight,
	xkControlL:    desktop.KeyControlLeft,
	xkControlR:    desktop.KeyControlRight,
	xkCapsLock:    desktop.KeyCapsLock,
	xkAltL:        desktop.KeyAltLeft,
	xkAltR:        desktop.KeyAltRight,
	xkSuperL:      desktop.KeySuperLeft,
	xkSuperR:      desktop.KeySuperRight,
	xkF1:          fyne.KeyF1,
	xkF2:          fyne.KeyF2,
	xkF3:          fyne.KeyF3,
	xkF4:          fyne.KeyF4,
	xkF5:          fyne.KeyF5,
	xkF6:          fyne.KeyF6,
	xkF7:          fyne.KeyF7,
	xkF8:          fyne.KeyF8,
	xkF9:          fyne.KeyF9,
	xkF10:         fyne.KeyF10,
	xkF11:         fyne.KeyF11,
	xkF12:         fyne.KeyF12,
}

func keysymToKey(ks uint32) (fyne.KeyName, rune) {
	if ks >= xkKP0 && ks <= xkKP9 {
		digit := rune('0' + (ks - xkKP0))
		return fyne.KeyName(digit), digit
	}
	if name, ok := keysymNames[ks]; ok {
		return name, keysymFixedRune(ks)
	}
	r := keysymToRune(ks)
	return printableKeyName(r), r
}

func keysymFixedRune(ks uint32) rune {
	switch ks {
	case xkKPSpace:
		return ' '
	case xkKPMultiply:
		return '*'
	case xkKPAdd:
		return '+'
	case xkKPSeparator:
		return ','
	case xkKPSubtract:
		return '-'
	case xkKPDecimal:
		return '.'
	case xkKPDivide:
		return '/'
	case xkKPEqual:
		return '='
	}
	return 0
}

func keysymToRune(ks uint32) rune {
	if ks >= latin1Min && ks <= latin1Max {
		return rune(ks)
	}
	if ks&unicodeKeysymMask == unicodeKeysymBase {
		r := rune(ks & unicodeCodepointMask)
		if r != 0 && utf8.ValidRune(r) {
			return r
		}
		return 0
	}
	switch ks {
	case xkEuroSign:
		return '€'
	case xkRightArrowPublishing:
		return '→'
	}
	return 0
}

func printableKeyName(r rune) fyne.KeyName {
	if r >= 'a' && r <= 'z' {
		return fyne.KeyName(r - ('a' - 'A'))
	}
	if r >= 'A' && r <= 'Z' {
		return fyne.KeyName(r)
	}
	if r >= '0' && r <= '9' {
		return fyne.KeyName(r)
	}
	switch r {
	case ' ':
		return fyne.KeySpace
	case '\'':
		return fyne.KeyApostrophe
	case ',':
		return fyne.KeyComma
	case '-':
		return fyne.KeyMinus
	case '.':
		return fyne.KeyPeriod
	case '/':
		return fyne.KeySlash
	case '\\':
		return fyne.KeyBackslash
	case '[':
		return fyne.KeyLeftBracket
	case ']':
		return fyne.KeyRightBracket
	case ';':
		return fyne.KeySemicolon
	case '=':
		return fyne.KeyEqual
	case '*':
		return fyne.KeyAsterisk
	case '+':
		return fyne.KeyPlus
	case '`':
		return fyne.KeyBackTick
	}
	return ""
}

func keysymIsKeypad(ks uint32) bool {
	return ks >= xkKPSpace && ks <= xkKPEqual
}

func keysymConvertCase(ks uint32) (lower, upper uint32) {
	r := keysymToRune(ks)
	if r == 0 {
		return ks, ks
	}
	l := unicode.ToLower(r)
	u := unicode.ToUpper(r)
	if ks >= latin1Min && ks <= latin1Max {
		return uint32FromRune(l), uint32FromRune(u)
	}
	if ks&unicodeKeysymMask == unicodeKeysymBase {
		return unicodeKeysymBase | uint32FromRune(l), unicodeKeysymBase | uint32FromRune(u)
	}
	return ks, ks
}
