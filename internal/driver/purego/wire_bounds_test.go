//go:build purego && linux

package purego

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func TestX11ReplyExtraBytesRejectsHostileLength(t *testing.T) {
	header := make([]byte, coreEventSize)
	header[0] = replyStatusReply
	binary.LittleEndian.PutUint32(header[4:8], 0xffffffff)
	allocs := testing.AllocsPerRun(50, func() {
		_, _ = x11ReplyExtraBytes(header)
	})
	if _, err := x11ReplyExtraBytes(header); err == nil {
		t.Fatal("0xFFFFFFFF extra words must be refused")
	}
	if allocs > 8 {
		t.Fatalf("AllocsPerRun = %v, want no giant allocation", allocs)
	}
}

func TestX11ReplyExtraBytesRejectsSixteenMillionWords(t *testing.T) {
	header := make([]byte, coreEventSize)
	header[0] = replyStatusReply
	binary.LittleEndian.PutUint32(header[4:8], 16_000_000)
	n, err := x11ReplyExtraBytes(header)
	if err == nil {
		t.Fatalf("16e6 extra words accepted as %d bytes", n)
	}
}

func TestX11ChangePropertyWordsRefuses300KiB(t *testing.T) {
	_, err := x11ChangePropertyWords(300 * 1024)
	if err == nil {
		t.Fatal("300 KiB ChangeProperty payload must be refused")
	}
	if _, err := x11ChangePropertyWords(16); err != nil {
		t.Fatalf("16-byte payload refused: %v", err)
	}
}

func TestChangePropertyRefusesOverCard16Limit(t *testing.T) {
	owner := &clipboardOwner{}
	payload := make([]byte, 300*1024)
	allocs := testing.AllocsPerRun(20, func() {
		_ = owner.changeProperty(1, 2, 3, propFormat8, payload)
	})
	if owner.changeProperty(1, 2, 3, propFormat8, payload) {
		t.Fatal("changeProperty with 300 KiB returned true; want refusal")
	}
	if allocs > 8 {
		t.Fatalf("AllocsPerRun = %v, want no giant request allocation", allocs)
	}
}

func TestXdndParseAtomListHostileValueLen(t *testing.T) {
	extra := make([]byte, 16)
	allocs := testing.AllocsPerRun(50, func() {
		_, _ = xdndParseAtomList(extra, 0xffffffff)
	})
	out, err := xdndParseAtomList(extra, 0xffffffff)
	if err == nil || out != nil {
		t.Fatalf("valueLen 0xFFFFFFFF: out=%v err=%v, want error", out, err)
	}
	if allocs > 8 {
		t.Fatalf("AllocsPerRun = %v, want no giant atom slice", allocs)
	}
}

func TestXdndParseAtomListDeclaredOverLimit(t *testing.T) {
	extra := make([]byte, maxAtomListBytes+64)
	_, err := xdndParseAtomList(extra, uint32(maxAtomListCount+1))
	if err == nil {
		t.Fatal("atom list longer than maxAtomListCount must be refused")
	}
}

func TestSetTitleRefusesOverCard16Limit(t *testing.T) {
	s := openX11ForTest(t, 32, 32)
	t.Cleanup(func() { _ = s.Close() })
	title := strings.Repeat("A", 300*1024)
	if err := s.SetTitle(title); err == nil {
		t.Fatal("SetTitle with a 300 KiB title returned no error")
	}
	if err := s.SetTitle("ok"); err != nil {
		t.Fatalf("short title after refusal: %v", err)
	}
}

func TestCard16FromIntRejectsOverflow(t *testing.T) {
	if _, err := card16FromInt(0x10000); err == nil {
		t.Fatal("0x10000 must not fit in CARD16")
	}
	if _, err := intFromUint32(0xffffffff); err == nil && ^uint(0) == 0xffffffff {
		t.Fatal("0xFFFFFFFF must not fit in 32-bit int")
	}
	v, err := card16FromInt(math.MaxUint16)
	if err != nil || v != math.MaxUint16 {
		t.Fatalf("got %d, %v; want 65535, nil", v, err)
	}
}

func TestX11ParseSetupExtraTruncated(t *testing.T) {
	if _, err := x11ParseSetupExtra(nil); err == nil {
		t.Fatal("empty extra must be refused")
	}
	if _, err := x11ParseSetupExtra(make([]byte, 22)); err == nil {
		t.Fatal("prefix-only extra must be refused")
	}
	const (
		screenRecordOffset    = 32
		truncatedAtRootVisual = 32
		screenRecordMinSize   = 40
	)
	if _, err := x11ParseSetupExtra(make([]byte, screenRecordOffset+truncatedAtRootVisual)); err == nil {
		t.Fatal("SCREEN truncated at root-visual must be refused")
	}
	ok := make([]byte, screenRecordOffset+screenRecordMinSize)
	if _, err := x11ParseSetupExtra(ok); err != nil {
		t.Fatalf("minimal SCREEN refused: %v", err)
	}
}

func FuzzX11Reply(f *testing.F) {
	f.Add([]byte{1, 0, 1, 0, 0, 0, 0, 0})
	over := make([]byte, 8)
	binary.LittleEndian.PutUint32(over[4:8], 0xffffffff)
	f.Add(over)
	f.Add([]byte{1})
	f.Add([]byte{})
	f.Add(make([]byte, 22))
	f.Add(make([]byte, 64))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = x11ReplyExtraBytes(data)
		_, _ = x11ParseSetupExtra(data)
	})
}
