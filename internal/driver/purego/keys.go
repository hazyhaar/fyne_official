//go:build purego

package purego

const (
	coreKeysymCols = 4
	coreGroup2     = 2

	modifierShift   = 0x0001
	modifierLock    = 0x0002
	modifierControl = 0x0004
	modifierAlt     = 0x0008
	modifierSuper   = 0x0040
)

func selectCoreKeysym(syms [coreKeysymCols]uint32, state, modeSwitchMask, numLockMask uint16, lockMeaning uint32) uint32 {
	offset := 0
	if modeSwitchMask != 0 && state&modeSwitchMask != 0 && (syms[coreGroup2] != 0 || syms[coreGroup2+1] != 0) {
		offset = coreGroup2
	}
	ks1 := syms[offset]
	ks2 := syms[offset+1]
	if ks2 == 0 {
		_, ks2 = keysymConvertCase(ks1)
	}
	shift := state&modifierShift != 0
	lock := state&modifierLock != 0
	num := numLockMask != 0 && state&numLockMask != 0
	if num && (keysymIsKeypad(ks1) || keysymIsKeypad(ks2)) {
		if shift {
			return ks1
		}
		return ks2
	}
	if lock && lockMeaning == xkCapsLock {
		lower, upper := keysymConvertCase(ks1)
		if lower != upper {
			if shift {
				return lower
			}
			return upper
		}
	}
	if shift || (lock && lockMeaning == xkShiftLock) {
		return ks2
	}
	return ks1
}
