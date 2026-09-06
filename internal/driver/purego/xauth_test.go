//go:build purego && linux

package purego

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeXauthFile builds a minimal Xauthority file with one FamilyLocal entry
// for displayNum, following the format documented in xauth.go.
func writeXauthFile(t *testing.T, displayNum, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Xauthority")

	var buf []byte
	putField := func(b []byte) {
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(b))) //nolint:gosec // test data, always short
		buf = append(buf, lenBuf[:]...)
		buf = append(buf, b...)
	}
	var famBuf [2]byte
	binary.BigEndian.PutUint16(famBuf[:], xauthFamilyLocal)
	buf = append(buf, famBuf[:]...)
	putField([]byte("localhost"))
	putField([]byte(displayNum))
	putField([]byte(name))
	putField(data)

	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// TestLookupXauthFindsMatchingDisplay is the regression test for defect (1):
// before xauth.go existed, openX11 never read $XAUTHORITY at all, so every
// connection through xvfb-run (which always writes a MIT-MAGIC-COOKIE-1
// entry there) failed connection setup with "server refused connection
// setup (status 0)". This test fails if lookupXauth stops parsing the
// Xauthority binary format or stops matching on display number.
func TestLookupXauthFindsMatchingDisplay(t *testing.T) {
	cookie := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	path := writeXauthFile(t, "99", "MIT-MAGIC-COOKIE-1", cookie)
	t.Setenv("XAUTHORITY", path)

	name, data, ok := lookupXauth("99")
	if !ok {
		t.Fatal("lookupXauth(99) = not found, want a match")
	}
	if string(name) != "MIT-MAGIC-COOKIE-1" {
		t.Fatalf("name = %q, want MIT-MAGIC-COOKIE-1", name)
	}
	if len(data) != len(cookie) {
		t.Fatalf("data length = %d, want %d", len(data), len(cookie))
	}
	for i := range cookie {
		if data[i] != cookie[i] {
			t.Fatalf("data[%d] = %#x, want %#x", i, data[i], cookie[i])
		}
	}

	if _, _, ok := lookupXauth("42"); ok {
		t.Fatal("lookupXauth(42) unexpectedly matched an entry written for display 99")
	}
}

// TestLookupXauthNoFileFallsBack confirms the "keep working without a
// cookie" half of the fix: a missing/unreadable Xauthority file must not be
// an error, only a signal to send an unauthenticated connection setup, as
// this backend always did before this file existed.
func TestLookupXauthNoFileFallsBack(t *testing.T) {
	t.Setenv("XAUTHORITY", filepath.Join(t.TempDir(), "does-not-exist"))
	if _, _, ok := lookupXauth("0"); ok {
		t.Fatal("lookupXauth with no Xauthority file = found an entry, want none")
	}
}
