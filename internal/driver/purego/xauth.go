//go:build purego && linux

package purego

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
)

// Xauthority family codes (X Window System Protocol, Xau library): the
// family a cookie entry applies to. FamilyLocal covers Unix-domain socket
// connections (what this backend always uses, see openX11); FamilyWild
// matches any family and is used by some setups (notably xvfb-run) instead
// of writing a FamilyLocal entry.
const (
	xauthFamilyLocal = 256
	xauthFamilyWild  = 65535
)

// xauthEntry is one cookie record from an Xauthority file.
type xauthEntry struct {
	family        uint16
	address       []byte
	displayNumber []byte
	name          []byte
	data          []byte
}

// readXauthEntries parses the binary Xauthority file format: a sequence of
// records, each five fields in order (family, address, display number,
// protocol name, protocol data), every field but family prefixed by its
// length as a big-endian uint16. family itself is a bare big-endian uint16,
// not length-prefixed.
func readXauthEntries(path string) ([]xauthEntry, error) {
	f, err := os.Open(path) //nolint:gosec // path is $XAUTHORITY or ~/.Xauthority, not request input
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var entries []xauthEntry
	for {
		family, err := readBE16(r)
		if err != nil {
			if err == io.EOF {
				return entries, nil
			}
			return entries, nil
		}
		address, err := readBE16Field(r)
		if err != nil {
			return entries, nil
		}
		displayNumber, err := readBE16Field(r)
		if err != nil {
			return entries, nil
		}
		name, err := readBE16Field(r)
		if err != nil {
			return entries, nil
		}
		data, err := readBE16Field(r)
		if err != nil {
			return entries, nil
		}
		entries = append(entries, xauthEntry{family: family, address: address, displayNumber: displayNumber, name: name, data: data})
	}
}

const byteBits = 8

func readBE16(r *bufio.Reader) (uint16, error) {
	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return uint16(b[0])<<byteBits | uint16(b[1]), nil
}

func readBE16Field(r *bufio.Reader) ([]byte, error) {
	n, err := readBE16(r)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// lookupXauth finds the authorization name/data this client should present
// for displayNum, following the same file $XAUTHORITY (or ~/.Xauthority)
// that xvfb-run and every other X client read. It returns ok=false when no
// file or no matching entry exists, in which case openX11 falls back to
// unauthenticated connection setup, exactly as before this file existed.
func lookupXauth(displayNum string) (name, data []byte, ok bool) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, false
		}
		path = filepath.Join(home, ".Xauthority")
	}
	entries, err := readXauthEntries(path)
	if err != nil {
		return nil, nil, false
	}
	for _, e := range entries {
		if (e.family == xauthFamilyLocal || e.family == xauthFamilyWild) && string(e.displayNumber) == displayNum {
			return e.name, e.data, true
		}
	}
	// A FamilyWild entry with no display number applies to every display; xvfb-run
	// itself writes exactly this kind of entry, so it must be tried too.
	for _, e := range entries {
		if e.family == xauthFamilyWild {
			return e.name, e.data, true
		}
	}
	return nil, nil, false
}
