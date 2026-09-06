//go:build purego

package purego

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

const (
	xkbEvdevCodeOffset = 8

	xkbModShiftBit   uint32 = 1 << 0
	xkbModLockBit    uint32 = 1 << 1
	xkbModControlBit uint32 = 1 << 2
	xkbMod1Bit       uint32 = 1 << 3
	xkbMod2Bit       uint32 = 1 << 4
	xkbMod3Bit       uint32 = 1 << 5
	xkbMod4Bit       uint32 = 1 << 6
	xkbMod5Bit       uint32 = 1 << 7

	xkbDeadGrave    = 0xfe50
	xkbMultiKey     = 0xff20
	xkbISOLeftTab   = 0xfe20
	xkbISONextGroup = 0xfe08
	xkbGreekOMEGA   = 0x7c9
	xkbVoidSymbol   = 0xffffff
	xkbLatin1Named  = 0xa0
	xkbHexBase      = 16
	xkbUnicodeULen  = 4
	xkbMaxTypeLevel = 8
	xkbGroupPrefix  = "Group"
	xkbLevelPrefix  = "Level"
	xkbTokAssign    = "="
)

type xkbKeymap struct {
	codeToName map[uint32]string
	nameToCode map[string]uint32
	types      map[string]xkbType
	keys       map[string]xkbKey
	vmod       map[string]uint32
}

type xkbType struct {
	mods uint32
	maps []xkbTypeEntry
}

type xkbTypeEntry struct {
	mods  uint32
	level int
}

type xkbKey struct {
	typ    string
	groups [][]uint32
}

func parseXkbKeymap(text string) (*xkbKeymap, error) {
	km := &xkbKeymap{
		codeToName: make(map[uint32]string),
		nameToCode: make(map[string]uint32),
		types:      builtinXkbTypes(),
		keys:       make(map[string]xkbKey),
		vmod:       defaultXkbVMods(),
	}
	sc := &xkbScanner{src: text}
	for {
		sc.skip()
		if sc.eof() {
			return km, nil
		}
		id := sc.ident()
		if id == "" {
			sc.pos++
			continue
		}
		switch id {
		case "xkb_keymap":
			sc.skip()
			if sc.peek() == '"' {
				_ = sc.quoted()
				sc.skip()
			}
			if sc.peek() == '{' {
				sc.pos++
			}
		case "xkb_keycodes":
			parseXkbKeycodes(km, sc.sectionBody())
		case "xkb_types":
			parseXkbTypes(km, sc.sectionBody())
		case "xkb_symbols":
			parseXkbSymbols(km, sc.sectionBody())
		case "xkb_compatibility", "xkb_geometry":
			_ = sc.sectionBody()
		default:
			sc.skipItem()
		}
	}
}

func defaultXkbVMods() map[string]uint32 {
	return map[string]uint32{
		"Shift":      xkbModShiftBit,
		"Lock":       xkbModLockBit,
		"Control":    xkbModControlBit,
		"Mod1":       xkbMod1Bit,
		"Mod2":       xkbMod2Bit,
		"Mod3":       xkbMod3Bit,
		"Mod4":       xkbMod4Bit,
		"Mod5":       xkbMod5Bit,
		"NumLock":    xkbMod2Bit,
		"Alt":        xkbMod1Bit,
		"LevelThree": xkbMod5Bit,
		"AltGr":      xkbMod5Bit,
		"Super":      xkbMod4Bit,
		"Meta":       xkbMod4Bit,
		"Hyper":      xkbMod4Bit,
		"LevelFive":  xkbMod3Bit,
		"ScrollLock": xkbMod3Bit,
	}
}

func builtinXkbTypes() map[string]xkbType {
	shift := xkbModShiftBit
	lock := xkbModLockBit
	num := xkbMod2Bit
	l3 := xkbMod5Bit
	return map[string]xkbType{
		"ONE_LEVEL": {mods: 0},
		"TWO_LEVEL": {mods: shift, maps: []xkbTypeEntry{{mods: shift, level: 1}}},
		"ALPHABETIC": {mods: shift | lock, maps: []xkbTypeEntry{
			{mods: shift, level: 1},
			{mods: lock, level: 1},
		}},
		"KEYPAD": {mods: shift | num, maps: []xkbTypeEntry{
			{mods: shift, level: 1},
			{mods: num, level: 1},
		}},
		"FOUR_LEVEL": {mods: shift | l3, maps: []xkbTypeEntry{
			{mods: shift, level: 1},
			{mods: l3, level: 2},
			{mods: shift | l3, level: 3},
		}},
		"FOUR_LEVEL_ALPHABETIC": {mods: shift | lock | l3, maps: []xkbTypeEntry{
			{mods: shift, level: 1},
			{mods: lock, level: 1},
			{mods: l3, level: 2},
			{mods: shift | l3, level: 3},
			{mods: lock | l3, level: 3},
			{mods: shift | lock | l3, level: 2},
		}},
		"FOUR_LEVEL_SEMIALPHABETIC": {mods: shift | lock | l3, maps: []xkbTypeEntry{
			{mods: shift, level: 1},
			{mods: lock, level: 1},
			{mods: l3, level: 2},
			{mods: shift | l3, level: 3},
			{mods: lock | l3, level: 2},
			{mods: shift | lock | l3, level: 3},
		}},
	}
}

func parseXkbKeycodes(km *xkbKeymap, body string) {
	sc := &xkbScanner{src: body}
	for {
		sc.skip()
		if sc.eof() {
			return
		}
		if sc.peek() == '<' {
			name := sc.keyName()
			sc.skip()
			if !sc.consume(xkbTokAssign) {
				sc.skipItem()
				continue
			}
			sc.skip()
			n, ok := sc.number()
			if ok && name != "" {
				km.nameToCode[name] = n
				km.codeToName[n] = name
			}
			sc.skipToSemi()
			continue
		}
		sc.skipItem()
	}
}

func parseXkbTypes(km *xkbKeymap, body string) {
	sc := &xkbScanner{src: body}
	for {
		sc.skip()
		if sc.eof() {
			return
		}
		id := sc.ident()
		switch id {
		case "virtual_modifiers":
			sc.skipVModList(km)
		case "type":
			parseXkbTypeDef(km, sc)
		default:
			if id == "" {
				sc.pos++
				continue
			}
			sc.skipItem()
		}
	}
}

func parseXkbTypeDef(km *xkbKeymap, sc *xkbScanner) {
	sc.skip()
	name := sc.quoted()
	if name == "" {
		name = sc.ident()
	}
	body := sc.sectionBody()
	if name == "" || body == "" {
		return
	}
	km.types[name] = parseXkbTypeBody(km, body)
}

func parseXkbTypeBody(km *xkbKeymap, body string) xkbType {
	sc := &xkbScanner{src: body}
	var t xkbType
	for {
		sc.skip()
		if sc.eof() {
			return t
		}
		id := sc.ident()
		switch id {
		case "modifiers":
			sc.skip()
			sc.consume(xkbTokAssign)
			t.mods = km.parseModMask(sc.modExpr())
		case "map":
			sc.skip()
			mask := km.parseModMask(sc.bracketExpr())
			sc.skip()
			sc.consume(xkbTokAssign)
			sc.skip()
			level := parseXkbLevel(sc.ident())
			t.maps = append(t.maps, xkbTypeEntry{mods: mask, level: level})
			sc.skipToSemi()
		default:
			if id == "" {
				sc.pos++
				continue
			}
			sc.skipItem()
		}
	}
}

func parseXkbSymbols(km *xkbKeymap, body string) {
	sc := &xkbScanner{src: body}
	for {
		sc.skip()
		if sc.eof() {
			return
		}
		id := sc.ident()
		if id == "key" {
			parseXkbKeyDef(km, sc)
			continue
		}
		if id == "" {
			sc.pos++
			continue
		}
		sc.skipItem()
	}
}

func parseXkbKeyDef(km *xkbKeymap, sc *xkbScanner) {
	sc.skip()
	name := sc.keyName()
	body := sc.sectionBody()
	if name == "" || body == "" {
		return
	}
	km.keys[name] = parseXkbKeyBody(body)
}

func parseXkbKeyBody(body string) xkbKey {
	sc := &xkbScanner{src: body}
	var k xkbKey
	for {
		sc.skip()
		if sc.eof() {
			break
		}
		switch {
		case sc.peek() == '[':
			k.groups = append(k.groups, sc.keysymList())
		case sc.consume("symbols"):
			sc.skip()
			_ = sc.bracketExpr()
			sc.skip()
			sc.consume(xkbTokAssign)
			sc.skip()
			k.groups = append(k.groups, sc.keysymList())
		case sc.consume("type"):
			sc.skip()
			if sc.peek() == '[' {
				_ = sc.bracketExpr()
			}
			sc.skip()
			sc.consume(xkbTokAssign)
			sc.skip()
			if t := sc.quoted(); t != "" {
				k.typ = t
			} else {
				k.typ = sc.ident()
			}
			sc.skipToSemiOrComma()
		default:
			if sc.ident() == "" {
				sc.pos++
			} else {
				sc.skipItem()
			}
		}
	}
	if k.typ == "" && len(k.groups) > 0 {
		k.typ = inferXkbType(k.groups[0])
	}
	return k
}

func inferXkbType(syms []uint32) string {
	switch len(syms) {
	case 0, 1:
		return "ONE_LEVEL"
	case 2:
		if xkbKeysymIsLetter(syms[0]) {
			return "ALPHABETIC"
		}
		return "TWO_LEVEL"
	default:
		if xkbKeysymIsLetter(syms[0]) {
			return "FOUR_LEVEL_ALPHABETIC"
		}
		return "FOUR_LEVEL"
	}
}

func xkbKeysymIsLetter(ks uint32) bool {
	r := keysymToRune(ks)
	return r != 0 && unicode.IsLetter(r)
}

func (km *xkbKeymap) parseModMask(expr string) uint32 {
	expr = strings.TrimSpace(expr)
	if expr == "" || expr == "none" || expr == "None" {
		return 0
	}
	var mask uint32
	for _, part := range strings.Split(expr, "+") {
		name := strings.TrimSpace(part)
		if name == "" || name == "none" || name == "None" {
			continue
		}
		if bit, ok := km.vmod[name]; ok {
			mask |= bit
		}
	}
	return mask
}

func parseXkbLevel(s string) int {
	if strings.HasPrefix(s, xkbLevelPrefix) {
		n, err := strconv.Atoi(s[len(xkbLevelPrefix):])
		if err == nil && n > 0 && n <= xkbMaxTypeLevel {
			return n - 1
		}
	}
	n, err := strconv.Atoi(s)
	if err == nil && n > 0 && n <= xkbMaxTypeLevel {
		return n - 1
	}
	return 0
}

func (km *xkbKeymap) lookupEvdev(evdev uint32, realMods uint32, group uint32) uint32 {
	if km == nil {
		return 0
	}
	name := km.codeToName[evdev+xkbEvdevCodeOffset]
	return km.lookupName(name, realMods, group)
}

func (km *xkbKeymap) lookupName(name string, realMods uint32, group uint32) uint32 {
	if km == nil || name == "" {
		return 0
	}
	key, ok := km.keys[name]
	if !ok || len(key.groups) == 0 {
		return 0
	}
	// Compare before converting: on 32-bit hosts int(group) of a hostile
	// value such as 0x80000000 is negative.
	g := 0
	if group <= math.MaxInt32 && int(group) < len(key.groups) {
		g = int(group)
	}
	syms := key.groups[g]
	if len(syms) == 0 {
		return 0
	}
	typName := key.typ
	if typName == "" {
		typName = inferXkbType(syms)
	}
	level := 0
	if t, ok := km.types[typName]; ok {
		level = t.levelFor(realMods)
	}
	if level < 0 || level >= len(syms) {
		level = len(syms) - 1
	}
	return syms[level]
}

func (t xkbType) levelFor(realMods uint32) int {
	masked := realMods & t.mods
	for _, e := range t.maps {
		if e.mods == masked {
			return e.level
		}
	}
	return 0
}

func (km *xkbKeymap) group1Syms(name string) []uint32 {
	if km == nil {
		return nil
	}
	key, ok := km.keys[name]
	if !ok || len(key.groups) == 0 {
		return nil
	}
	return key.groups[0]
}

type xkbScanner struct {
	src string
	pos int
}

func (sc *xkbScanner) eof() bool { return sc.pos >= len(sc.src) }

func (sc *xkbScanner) peek() byte {
	if sc.eof() {
		return 0
	}
	return sc.src[sc.pos]
}

func (sc *xkbScanner) skip() {
	for !sc.eof() {
		switch sc.src[sc.pos] {
		case ' ', '\t', '\n', '\r':
			sc.pos++
		case '/':
			if sc.pos+1 < len(sc.src) && sc.src[sc.pos+1] == '/' {
				sc.skipLine()
				continue
			}
			if sc.pos+1 < len(sc.src) && sc.src[sc.pos+1] == '*' {
				sc.skipBlockComment()
				continue
			}
			return
		default:
			return
		}
	}
}

func (sc *xkbScanner) skipLine() {
	for !sc.eof() {
		c := sc.src[sc.pos]
		sc.pos++
		if c == '\n' {
			return
		}
	}
}

func (sc *xkbScanner) skipBlockComment() {
	sc.pos += 2
	for sc.pos+1 < len(sc.src) {
		if sc.src[sc.pos] == '*' && sc.src[sc.pos+1] == '/' {
			sc.pos += 2
			return
		}
		sc.pos++
	}
	sc.pos = len(sc.src)
}

func (sc *xkbScanner) consume(s string) bool {
	if strings.HasPrefix(sc.src[sc.pos:], s) {
		sc.pos += len(s)
		return true
	}
	return false
}

func (sc *xkbScanner) ident() string {
	if sc.eof() {
		return ""
	}
	c := sc.src[sc.pos]
	if !isXkbIdentStart(c) {
		return ""
	}
	start := sc.pos
	sc.pos++
	for !sc.eof() && isXkbIdentPart(sc.src[sc.pos]) {
		sc.pos++
	}
	return sc.src[start:sc.pos]
}

func isXkbIdentStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isXkbIdentPart(c byte) bool {
	return isXkbIdentStart(c) || (c >= '0' && c <= '9')
}

func (sc *xkbScanner) quoted() string {
	if sc.peek() != '"' {
		return ""
	}
	sc.pos++
	start := sc.pos
	for !sc.eof() && sc.src[sc.pos] != '"' {
		sc.pos++
	}
	s := sc.src[start:sc.pos]
	if !sc.eof() {
		sc.pos++
	}
	return s
}

func (sc *xkbScanner) keyName() string {
	if sc.peek() != '<' {
		return ""
	}
	sc.pos++
	start := sc.pos
	for !sc.eof() && sc.src[sc.pos] != '>' {
		sc.pos++
	}
	s := sc.src[start:sc.pos]
	if !sc.eof() {
		sc.pos++
	}
	return s
}

func (sc *xkbScanner) number() (uint32, bool) {
	if sc.eof() {
		return 0, false
	}
	if strings.HasPrefix(sc.src[sc.pos:], "0x") || strings.HasPrefix(sc.src[sc.pos:], "0X") {
		sc.pos += 2
		start := sc.pos
		for !sc.eof() && isHexByte(sc.src[sc.pos]) {
			sc.pos++
		}
		if start == sc.pos {
			return 0, false
		}
		v, err := strconv.ParseUint(sc.src[start:sc.pos], xkbHexBase, 32)
		return uint32(v), err == nil
	}
	if sc.src[sc.pos] < '0' || sc.src[sc.pos] > '9' {
		return 0, false
	}
	start := sc.pos
	for !sc.eof() && sc.src[sc.pos] >= '0' && sc.src[sc.pos] <= '9' {
		sc.pos++
	}
	v, err := strconv.ParseUint(sc.src[start:sc.pos], 10, 32)
	return uint32(v), err == nil
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (sc *xkbScanner) sectionBody() string {
	sc.skip()
	if sc.peek() == '"' {
		_ = sc.quoted()
		sc.skip()
	}
	if sc.peek() != '{' {
		sc.skipItem()
		return ""
	}
	return sc.balanced('{', '}')
}

func (sc *xkbScanner) balanced(open, end byte) string {
	if sc.peek() != open {
		return ""
	}
	sc.pos++
	start := sc.pos
	depth := 1
	for !sc.eof() && depth > 0 {
		c := sc.src[sc.pos]
		switch c {
		case '"':
			_ = sc.quoted()
		case '/':
			if sc.pos+1 < len(sc.src) && sc.src[sc.pos+1] == '/' {
				sc.skipLine()
			} else if sc.pos+1 < len(sc.src) && sc.src[sc.pos+1] == '*' {
				sc.skipBlockComment()
			} else {
				sc.pos++
			}
		case open:
			depth++
			sc.pos++
		case end:
			depth--
			if depth == 0 {
				body := sc.src[start:sc.pos]
				sc.pos++
				return body
			}
			sc.pos++
		default:
			sc.pos++
		}
	}
	return sc.src[start:]
}

func (sc *xkbScanner) bracketExpr() string {
	sc.skip()
	if sc.peek() != '[' {
		return ""
	}
	return strings.TrimSpace(sc.balanced('[', ']'))
}

func (sc *xkbScanner) modExpr() string {
	sc.skip()
	start := sc.pos
	for !sc.eof() {
		c := sc.src[sc.pos]
		if c == ';' || c == ',' || c == '{' || c == '}' {
			break
		}
		sc.pos++
	}
	return strings.TrimSpace(sc.src[start:sc.pos])
}

func (sc *xkbScanner) keysymList() []uint32 {
	inner := sc.bracketExpr()
	if inner == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, parseXkbKeysymName(p))
	}
	return out
}

func (sc *xkbScanner) skipToSemi() {
	for !sc.eof() && sc.src[sc.pos] != ';' {
		if sc.peek() == '{' {
			_ = sc.balanced('{', '}')
			continue
		}
		sc.pos++
	}
	if !sc.eof() {
		sc.pos++
	}
}

func (sc *xkbScanner) skipToSemiOrComma() {
	for !sc.eof() {
		c := sc.src[sc.pos]
		if c == ';' || c == ',' {
			sc.pos++
			return
		}
		if c == '{' {
			_ = sc.balanced('{', '}')
			continue
		}
		sc.pos++
	}
}

func (sc *xkbScanner) skipItem() {
	sc.skip()
	for !sc.eof() {
		c := sc.src[sc.pos]
		switch c {
		case '{':
			_ = sc.balanced('{', '}')
			sc.skip()
			if sc.peek() == ';' {
				sc.pos++
			}
			return
		case ';':
			sc.pos++
			return
		case '"':
			_ = sc.quoted()
		default:
			sc.pos++
		}
	}
}

func (sc *xkbScanner) skipVModList(km *xkbKeymap) {
	sc.skip()
	for !sc.eof() && sc.peek() != ';' {
		sc.skip()
		name := sc.ident()
		if name == "" {
			if sc.peek() == ',' {
				sc.pos++
				continue
			}
			break
		}
		if _, ok := km.vmod[name]; !ok {
			km.vmod[name] = 0
		}
		sc.skip()
		if sc.peek() == ',' {
			sc.pos++
		}
	}
	sc.skipToSemi()
}

func parseXkbKeysymName(name string) uint32 {
	if name == "NoSymbol" || name == "VoidSymbol" {
		return 0
	}
	if v, ok := xkbKeysymNames[name]; ok {
		return v
	}
	if len(name) == 1 {
		return uint32(name[0])
	}
	if ks, ok := parseXkbUnicodeName(name); ok {
		return ks
	}
	if strings.HasPrefix(name, "0x") || strings.HasPrefix(name, "0X") {
		return parseXkbHexKeysym(name[2:])
	}
	return 0
}

func parseXkbHexKeysym(hex string) uint32 {
	v, err := strconv.ParseUint(hex, xkbHexBase, 64)
	if err != nil || v == 0 {
		return 0
	}
	if v <= uint64(^uint32(0)) {
		return uint32(v)
	}
	return unicodeKeysymBase | uint32(v&uint64(unicodeCodepointMask))
}

func parseXkbUnicodeName(name string) (uint32, bool) {
	rest := ""
	switch {
	case strings.HasPrefix(name, "U+") && len(name) > 2:
		rest = name[2:]
	case len(name) > xkbUnicodeULen && name[0] == 'U':
		rest = name[1:]
	default:
		return 0, false
	}
	for i := 0; i < len(rest); i++ {
		if !isHexByte(rest[i]) {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(rest, xkbHexBase, 32)
	if err != nil || v == 0 {
		return 0, false
	}
	if v <= latin1Max {
		return uint32(v), true
	}
	return unicodeKeysymBase | uint32(v), true
}

var xkbKeysymNames = map[string]uint32{}

func init() {
	for c := byte('a'); c <= 'z'; c++ {
		xkbKeysymNames[string(c)] = uint32(c)
	}
	for c := byte('A'); c <= 'Z'; c++ {
		xkbKeysymNames[string(c)] = uint32(c)
	}
	for c := byte('0'); c <= '9'; c++ {
		xkbKeysymNames[string(c)] = uint32(c)
	}
	for name, v := range xkbNamedKeysyms {
		xkbKeysymNames[name] = v
	}
	dead := []string{
		"grave", "acute", "circumflex", "tilde", "macron", "breve", "abovedot",
		"diaeresis", "abovering", "doubleacute", "caron", "cedilla", "ogonek",
		"iota", "voiced_sound", "semivoiced_sound", "belowdot", "hook", "horn",
		"stroke", "abovecomma", "abovereversedcomma", "doublegrave", "belowring",
		"belowmacron", "belowcircumflex", "belowtilde", "belowbreve", "belowdiaeresis",
		"invertedbreve", "belowcomma", "currency",
	}
	for i, n := range dead {
		xkbKeysymNames["dead_"+n] = xkbDeadGrave + uint32(i)
	}
	for i, c := range []byte("0123456789") {
		xkbKeysymNames["KP_"+string(c)] = xkKP0 + uint32(i)
	}
}

var xkbNamedKeysyms = map[string]uint32{
	"space":            ' ',
	"exclam":           '!',
	"quotedbl":         '"',
	"numbersign":       '#',
	"dollar":           '$',
	"percent":          '%',
	"ampersand":        '&',
	"apostrophe":       '\'',
	"quoteright":       '\'',
	"parenleft":        '(',
	"parenright":       ')',
	"asterisk":         '*',
	"plus":             '+',
	"comma":            ',',
	"minus":            '-',
	"period":           '.',
	"slash":            '/',
	"colon":            ':',
	"semicolon":        ';',
	"less":             '<',
	"equal":            '=',
	"greater":          '>',
	"question":         '?',
	"at":               '@',
	"bracketleft":      '[',
	"backslash":        '\\',
	"bracketright":     ']',
	"asciicircum":      '^',
	"underscore":       '_',
	"grave":            '`',
	"quoteleft":        '`',
	"braceleft":        '{',
	"bar":              '|',
	"braceright":       '}',
	"asciitilde":       '~',
	"Return":           xkReturn,
	"Linefeed":         0xff0a,
	"Tab":              xkTab,
	"Escape":           xkEscape,
	"BackSpace":        xkBackSpace,
	"Delete":           xkDelete,
	"Insert":           xkInsert,
	"Home":             xkHome,
	"End":              xkEnd,
	"Page_Up":          xkPageUp,
	"Page_Down":        xkPageDown,
	"Left":             xkLeft,
	"Up":               xkUp,
	"Right":            xkRight,
	"Down":             xkDown,
	"F1":               xkF1,
	"F2":               xkF2,
	"F3":               xkF3,
	"F4":               xkF4,
	"F5":               xkF5,
	"F6":               xkF6,
	"F7":               xkF7,
	"F8":               xkF8,
	"F9":               xkF9,
	"F10":              xkF10,
	"F11":              xkF11,
	"F12":              xkF12,
	"Shift_L":          xkShiftL,
	"Shift_R":          xkShiftR,
	"Control_L":        xkControlL,
	"Control_R":        xkControlR,
	"Caps_Lock":        xkCapsLock,
	"Shift_Lock":       xkShiftLock,
	"Alt_L":            xkAltL,
	"Alt_R":            xkAltR,
	"Super_L":          xkSuperL,
	"Super_R":          xkSuperR,
	"Num_Lock":         xkNumLock,
	"Mode_switch":      xkModeSwitch,
	"ISO_Level3_Shift": xkISOLevel3Shift,
	"ISO_Left_Tab":     xkbISOLeftTab,
	"ISO_Next_Group":   xkbISONextGroup,
	"Multi_key":        xkbMultiKey,
	"KP_Space":         xkKPSpace,
	"KP_Tab":           xkKPTab,
	"KP_Enter":         xkKPEnter,
	"KP_Home":          xkKPHome,
	"KP_Left":          xkKPLeft,
	"KP_Up":            xkKPUp,
	"KP_Right":         xkKPRight,
	"KP_Down":          xkKPDown,
	"KP_Page_Up":       xkKPPageUp,
	"KP_Page_Down":     xkKPPageDown,
	"KP_End":           xkKPEnd,
	"KP_Insert":        xkKPInsert,
	"KP_Delete":        xkKPDelete,
	"KP_Multiply":      xkKPMultiply,
	"KP_Add":           xkKPAdd,
	"KP_Separator":     xkKPSeparator,
	"KP_Subtract":      xkKPSubtract,
	"KP_Decimal":       xkKPDecimal,
	"KP_Divide":        xkKPDivide,
	"KP_Equal":         xkKPEqual,
	"EuroSign":         xkEuroSign,
	"Greek_OMEGA":      xkbGreekOMEGA,
	"VoidSymbol":       xkbVoidSymbol,
	"nobreakspace":     xkbLatin1Named,
	"exclamdown":       0xa1,
	"cent":             0xa2,
	"sterling":         0xa3,
	"currency":         0xa4,
	"yen":              0xa5,
	"brokenbar":        0xa6,
	"section":          0xa7,
	"diaeresis":        0xa8,
	"copyright":        0xa9,
	"ordfeminine":      0xaa,
	"guillemotleft":    0xab,
	"notsign":          0xac,
	"hyphen":           0xad,
	"registered":       0xae,
	"macron":           0xaf,
	"degree":           0xb0,
	"plusminus":        0xb1,
	"twosuperior":      0xb2,
	"threesuperior":    0xb3,
	"acute":            0xb4,
	"mu":               0xb5,
	"paragraph":        0xb6,
	"periodcentered":   0xb7,
	"cedilla":          0xb8,
	"onesuperior":      0xb9,
	"masculine":        0xba,
	"guillemotright":   0xbb,
	"onequarter":       0xbc,
	"onehalf":          0xbd,
	"threequarters":    0xbe,
	"questiondown":     0xbf,
	"Agrave":           0xc0,
	"Aacute":           0xc1,
	"Acircumflex":      0xc2,
	"Atilde":           0xc3,
	"Adiaeresis":       0xc4,
	"Aring":            0xc5,
	"AE":               0xc6,
	"Ccedilla":         0xc7,
	"Egrave":           0xc8,
	"Eacute":           0xc9,
	"Ecircumflex":      0xca,
	"Ediaeresis":       0xcb,
	"Igrave":           0xcc,
	"Iacute":           0xcd,
	"Icircumflex":      0xce,
	"Idiaeresis":       0xcf,
	"ETH":              0xd0,
	"Ntilde":           0xd1,
	"Ograve":           0xd2,
	"Oacute":           0xd3,
	"Ocircumflex":      0xd4,
	"Otilde":           0xd5,
	"Odiaeresis":       0xd6,
	"multiply":         0xd7,
	"Ooblique":         0xd8,
	"Oslash":           0xd8,
	"Ugrave":           0xd9,
	"Uacute":           0xda,
	"Ucircumflex":      0xdb,
	"Udiaeresis":       0xdc,
	"Yacute":           0xdd,
	"THORN":            0xde,
	"ssharp":           0xdf,
	"agrave":           0xe0,
	"aacute":           0xe1,
	"acircumflex":      0xe2,
	"atilde":           0xe3,
	"adiaeresis":       0xe4,
	"aring":            0xe5,
	"ae":               0xe6,
	"ccedilla":         0xe7,
	"egrave":           0xe8,
	"eacute":           0xe9,
	"ecircumflex":      0xea,
	"ediaeresis":       0xeb,
	"igrave":           0xec,
	"iacute":           0xed,
	"icircumflex":      0xee,
	"idiaeresis":       0xef,
	"eth":              0xf0,
	"ntilde":           0xf1,
	"ograve":           0xf2,
	"oacute":           0xf3,
	"ocircumflex":      0xf4,
	"otilde":           0xf5,
	"odiaeresis":       0xf6,
	"division":         0xf7,
	"oslash":           0xf8,
	"ugrave":           0xf9,
	"uacute":           0xfa,
	"ucircumflex":      0xfb,
	"udiaeresis":       0xfc,
	"yacute":           0xfd,
	"thorn":            0xfe,
	"ydiaeresis":       0xff,
}
