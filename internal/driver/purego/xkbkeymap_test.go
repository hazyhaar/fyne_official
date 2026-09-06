//go:build purego

package purego

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	testXkbAD01 = "AD01"
	testXkbAE02 = "AE02"
	testXkbAE05 = "AE05"
)

func TestParseXkbKeymapCageAndFrench(t *testing.T) {
	text := loadCageKeymap(t)
	km, err := parseXkbKeymap(text)
	if err != nil {
		t.Fatalf("parseXkbKeymap(cage): %v", err)
	}
	syms := km.group1Syms(testXkbAD01)
	if len(syms) < 2 || syms[0] != 'q' || syms[1] != 'Q' {
		t.Fatalf("cage <AD01> symbols = %v, want [q Q]", fmtKeysyms(syms))
	}

	fr, err := parseXkbKeymap(frenchSyntheticKeymap)
	if err != nil {
		t.Fatalf("parseXkbKeymap(fr): %v", err)
	}
	ad01 := fr.group1Syms(testXkbAD01)
	if len(ad01) < 2 || ad01[0] != 'a' || ad01[1] != 'A' {
		t.Fatalf("fr <AD01> symbols = %v, want [a A]", fmtKeysyms(ad01))
	}
	ae02 := fr.group1Syms(testXkbAE02)
	if len(ae02) < 1 || ae02[0] != 0xe9 {
		t.Fatalf("fr <AE02> level 0 = 0x%X, want eacute 0xE9", firstSym(ae02))
	}
}

func TestParseXkbKeysymUnicodeAndHex(t *testing.T) {
	if got := parseXkbKeysymName("U20AC"); got != unicodeKeysymBase|0x20ac {
		t.Fatalf("U20AC = 0x%X, want 0x%X", got, unicodeKeysymBase|0x20ac)
	}
	if got := parseXkbKeysymName("0x1000020ac"); got != unicodeKeysymBase|0x20ac {
		t.Fatalf("0x1000020ac = 0x%X, want 0x%X", got, unicodeKeysymBase|0x20ac)
	}
	if got := parseXkbKeysymName("EuroSign"); got != xkEuroSign {
		t.Fatalf("EuroSign = 0x%X, want 0x%X", got, xkEuroSign)
	}
	if got := parseXkbKeysymName("Up"); got != xkUp {
		t.Fatalf("Up = 0x%X, want 0x%X (must not parse as U+p)", got, xkUp)
	}
}

func TestXkbLookupShiftLockAltGr(t *testing.T) {
	km, err := parseXkbKeymap(frenchSyntheticKeymap)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := km.lookupName(testXkbAD01, 0, 0); got != 'a' {
		t.Fatalf("AD01 none = 0x%X, want a", got)
	}
	if got := km.lookupName(testXkbAD01, xkbModShiftBit, 0); got != 'A' {
		t.Fatalf("AD01 Shift = 0x%X, want A", got)
	}
	if got := km.lookupName(testXkbAD01, xkbModLockBit, 0); got != 'A' {
		t.Fatalf("AD01 Lock = 0x%X, want A", got)
	}
	if got := km.lookupName(testXkbAE05, xkbMod5Bit, 0); got != xkEuroSign {
		t.Fatalf("AE05 AltGr = 0x%X, want EuroSign", got)
	}
	const evdevAD01 = 16
	if got := km.lookupEvdev(evdevAD01, 0, 0); got != 'a' {
		t.Fatalf("evdev %d = 0x%X, want a", evdevAD01, got)
	}
}

func loadCageKeymap(t *testing.T) string {
	t.Helper()
	path := filepath.Join("testdata", "cage_keymap.xkb")
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		return string(data)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	text, err := compileUsKeymap()
	if err != nil {
		t.Fatalf("compile us keymap for testdata: %v", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %s (%d bytes) via setxkbmap|xkbcomp; cage seat has no keyboard capability", path, len(text))
	return text
}

func compileUsKeymap() (string, error) {
	printOut, err := exec.Command("setxkbmap", "-layout", "us", "-print").Output()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("xkbcomp", "-xkb", "-", "-")
	cmd.Stdin = bytes.NewReader(printOut)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func firstSym(syms []uint32) uint32 {
	if len(syms) == 0 {
		return 0
	}
	return syms[0]
}

func fmtKeysyms(syms []uint32) string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = fmt.Sprintf("0x%X", s)
	}
	return fmt.Sprintf("%v", out)
}

const frenchSyntheticKeymap = `xkb_keymap {
xkb_keycodes "evdev" {
    minimum = 8;
    maximum = 255;
    <AD01> = 24;
    <AE02> = 11;
    <AE05> = 14;
};
xkb_types "complete" {
    virtual_modifiers NumLock,Alt,LevelThree;
    type "ONE_LEVEL" { modifiers= none; };
    type "TWO_LEVEL" { modifiers= Shift; map[Shift]= Level2; };
    type "ALPHABETIC" { modifiers= Shift+Lock; map[Shift]= Level2; map[Lock]= Level2; };
    type "FOUR_LEVEL" {
        modifiers= Shift+LevelThree;
        map[Shift]= Level2;
        map[LevelThree]= Level3;
        map[Shift+LevelThree]= Level4;
    };
    type "FOUR_LEVEL_ALPHABETIC" {
        modifiers= Shift+Lock+LevelThree;
        map[Shift]= Level2;
        map[Lock]= Level2;
        map[LevelThree]= Level3;
        map[Shift+LevelThree]= Level4;
        map[Lock+LevelThree]= Level4;
        map[Shift+Lock+LevelThree]= Level3;
    };
    type "FOUR_LEVEL_SEMIALPHABETIC" {
        modifiers= Shift+Lock+LevelThree;
        map[Shift]= Level2;
        map[Lock]= Level2;
        map[LevelThree]= Level3;
        map[Shift+LevelThree]= Level4;
        map[Lock+LevelThree]= Level3;
        map[Shift+Lock+LevelThree]= Level4;
    };
    type "KEYPAD" { modifiers= Shift+NumLock; map[NumLock]= Level2; };
};
xkb_compatibility "complete" { };
xkb_symbols "fr" {
    key <AD01> { [ a, A ] };
    key <AE02> { [ eacute, 2 ] };
    key <AE05> { type= "FOUR_LEVEL", [ 5, percent, EuroSign, U20AC ] };
};
};
`
