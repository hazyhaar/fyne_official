//go:build purego

package purego

import "testing"

// TestLookupNameHostileGroupFallsBackToFirstGroup pins that a modifiers
// event carrying an out-of-range (or, on 32-bit hosts, sign-flipping)
// group index selects group 0 instead of indexing out of range.
func TestLookupNameHostileGroupFallsBackToFirstGroup(t *testing.T) {
	km := &xkbKeymap{keys: map[string]xkbKey{"AD01": {groups: [][]uint32{{0x71}}}}}
	for _, group := range []uint32{1, 0x7fffffff, 0x80000000, 0xffffffff} {
		if got := km.lookupName("AD01", 0, group); got != 0x71 {
			t.Fatalf("group %#x: lookupName = %#x, want %#x (group 0)", group, got, 0x71)
		}
	}
}
