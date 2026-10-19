package rhunext

import "unicode/utf8"

const (
	maxSnippetRegexCompileWork = 1 << 20
	maxSnippetRegexTempRanges  = 0x110000/2 + 1552
)

type snippetRegexLookFixup struct {
	pc    int
	node  *snippetRegexNode
	flags uint32
}
type snippetRegexCompiler struct {
	p           snippetRegexProgram
	left        int
	err         error
	looks       []snippetRegexLookFixup
	intern      map[uint64][]uint32
	folds       []snippetFoldEntry
	foldsReady  bool
	unicodeSets bool
	setFolds    map[rune][]rune
	globalFlags uint32
}

func compileSnippetRegex(source, options string) (*snippetRegexProgram, error) {
	ast, err := parseSnippetRegex(source, options)
	if err != nil {
		return nil, err
	}
	return compileSnippetRegexAST(ast, maxSnippetRegexCompileWork)
}
func compileSnippetRegexAST(ast *snippetRegex, work int) (*snippetRegexProgram, error) {
	return compileSnippetRegexASTBudget(ast, &work)
}
func compileSnippetRegexASTBudget(ast *snippetRegex, work *int) (*snippetRegexProgram, error) {
	c := snippetRegexCompiler{left: *work, p: snippetRegexProgram{flags: uint32(ast.flags), captures: uint32(ast.captures)}, intern: map[uint64][]uint32{}}
	if ast.flags&rxUnicodeSets != 0 {
		c.unicodeSets = true
		c.p.flags = c.p.flags&^uint32(rxUnicodeSets) | uint32(rxUnicode)
	}
	c.globalFlags = c.p.flags
	defer func() { *work = c.left }()
	c.compile(ast.root, false)
	c.emit(ast.root, rxMatch, 0, 0, 0)
	for i := 0; i < len(c.looks) && c.err == nil; i++ {
		fix := c.looks[i]
		flags := c.p.flags
		c.p.flags = fix.flags
		c.p.code[fix.pc].x = uint32(len(c.p.code))
		c.compile(fix.node.children[0], fix.node.backward)
		c.emit(fix.node, rxMatch, 0, 0, 0)
		c.p.flags = flags
	}
	if c.err != nil {
		return nil, c.err
	}
	if err := c.p.validate(); err != nil {
		return nil, &snippetRegexError{"source", ast.root.from, "invalid compiled program: " + err.Error()}
	}
	return &c.p, nil
}
func (c *snippetRegexCompiler) fail(n *snippetRegexNode, message string) {
	if c.err == nil {
		c.err = &snippetRegexError{"source", n.from, message}
	}
}
func (c *snippetRegexCompiler) spend(n *snippetRegexNode, amount int) bool {
	if c.err != nil {
		return false
	}
	if amount < 0 || amount > c.left {
		c.fail(n, "compile work limit exceeded")
		return false
	}
	c.left -= amount
	return true
}
func (c *snippetRegexCompiler) emit(n *snippetRegexNode, op, x, y, z uint32) int {
	at := len(c.p.code)
	if !c.spend(n, 1) {
		return at
	}
	if at == maxSnippetRegexInstructions {
		c.fail(n, "instruction limit exceeded")
		return at
	}
	c.p.code = append(c.p.code, snippetRegexInstruction{op, x, y, z})
	return at
}
func (c *snippetRegexCompiler) patch(pc int, x, y, z uint32) {
	if c.err == nil {
		c.p.code[pc].x = x
		c.p.code[pc].y = y
		c.p.code[pc].z = z
	}
}
func (c *snippetRegexCompiler) captureSpan(n *snippetRegexNode) (int, int) {
	if !c.spend(n, 1) {
		return 0, 0
	}
	lo, hi := 0, 0
	if n.kind == rxCapture {
		lo = n.group
		hi = n.group
	}
	for _, child := range n.children {
		a, b := c.captureSpan(child)
		if a != 0 && (lo == 0 || a < lo) {
			lo = a
		}
		if b > hi {
			hi = b
		}
		if c.err != nil {
			break
		}
	}
	return lo, hi
}
func (c *snippetRegexCompiler) clear(n *snippetRegexNode) {
	lo, hi := c.captureSpan(n)
	if lo != 0 {
		c.emit(n, rxClear, uint32(2*lo), uint32(2*(hi-lo+1)), 0)
	}
}
func (c *snippetRegexCompiler) compile(n *snippetRegexNode, backward bool) {
	if !c.spend(n, 1) {
		return
	}
	switch n.kind {
	case rxEmpty:
	case rxLiteral:
		if (c.p.flags^c.globalFlags)&uint32(rxIgnoreCase) == 0 {
			c.emit(n, rxChar, uint32(snippetRegexCanonical(n.lo, c.p.flags)), 0, 0)
		} else {
			ranges := c.closeSetCase(n, c.one(n, nil, n.lo, n.lo))
			c.emit(n, rxSet, c.class(n, ranges), 0, 0)
		}
	case rxAny:
		if (c.p.flags^c.globalFlags)&uint32(rxDotAll) == 0 {
			c.emit(n, rxDot, 0, 0, 0)
		} else {
			ranges := []snippetRuneRange{{0, 0xd7ff}, {0xe000, 0x10ffff}}
			if c.p.flags&uint32(rxDotAll) == 0 {
				ranges = c.setRanges(n, ranges, []snippetRuneRange{{10, 10}, {13, 13}, {0x2028, 0x2029}}, true)
			}
			c.emit(n, rxSet, c.class(n, ranges), 0, 0)
		}
	case rxAssert:
		value := uint32(1)
		switch n.text {
		case "end":
			value = 2
		case "word":
			value = 3
			if n.negative {
				value = 4
			}
		}
		mask := uint32(rxMultiline)
		if value >= 3 {
			mask = uint32(rxIgnoreCase)
		}
		if (c.p.flags^c.globalFlags)&mask == 0 {
			c.emit(n, rxAssertion, value, 0, 0)
		} else {
			c.emit(n, rxScopedAssertion, value, c.p.flags&uint32(rxIgnoreCase|rxMultiline), 0)
		}
	case rxBackref:
		if len(n.aliasGroups) == 0 {
			c.compileReference(n, n.group)
		} else {
			// Only one disjoint definition can be present. Missing captures
			// match empty, so a sequence selects the participating capture.
			for _, group := range n.aliasGroups {
				c.compileReference(n, group)
			}
		}
	case rxSequence:
		for i := 0; i < len(n.children) && c.err == nil; i++ {
			index := i
			if backward {
				index = len(n.children) - 1 - i
			}
			c.compile(n.children[index], backward)
		}
	case rxAlternative:
		var jumps []int
		for i, child := range n.children {
			if c.err != nil {
				break
			}
			if i+1 == len(n.children) {
				c.compile(child, backward)
				break
			}
			split := c.emit(n, rxSplit, 0, 0, 0)
			body := len(c.p.code)
			c.compile(child, backward)
			jumps = append(jumps, c.emit(n, rxJump, 0, 0, 0))
			c.patch(split, uint32(body), uint32(len(c.p.code)), 0)
		}
		for _, jump := range jumps {
			c.patch(jump, uint32(len(c.p.code)), 0, 0)
		}
	case rxCapture:
		c.clear(n)
		start, end := uint32(2*n.group), uint32(2*n.group+1)
		if backward {
			start, end = end, start
		}
		c.emit(n, rxSave, start, 0, 0)
		c.compile(n.children[0], backward)
		c.emit(n, rxSave, end, 0, 0)
	case rxGroup:
		c.compileGroup(n, backward)
	case rxLook:
		flags := uint32(0)
		if n.negative {
			flags |= 1
		}
		if n.backward {
			flags |= 2
		}
		pc := c.emit(n, rxLookCall, 0, uint32(len(c.p.code)+1), flags)
		if c.err == nil {
			c.looks = append(c.looks, snippetRegexLookFixup{pc, n, c.p.flags})
		}
	case rxRepeat:
		if len(c.p.repeats) == maxSnippetRegexRepeats {
			c.fail(n, "repeat limit exceeded")
			return
		}
		id := uint32(len(c.p.repeats))
		flags := uint32(0)
		if n.lazy {
			flags = 1
		}
		c.p.repeats = append(c.p.repeats, snippetRegexRepeat{uint32(n.min), uint32(n.max), flags, 0})
		c.emit(n, rxRepeatStart, id, 0, 0)
		test := c.emit(n, rxRepeatTest, id, 0, 0)
		body := len(c.p.code)
		c.clear(n.children[0])
		c.compile(n.children[0], backward)
		c.emit(n, rxRepeatNext, id, uint32(test), 0)
		c.patch(test, id, uint32(body), uint32(len(c.p.code)))
	case rxClass, rxShorthand, rxProperty:
		if c.unicodeSets {
			c.compileSet(n, c.setValue(n), backward)
			return
		}
		ranges := c.predicate(n)
		id := c.class(n, ranges)
		c.emit(n, rxSet, id, 0, 0)
	case rxSetUnion, rxSetIntersection, rxSetSubtraction, rxStrings:
		c.compileSet(n, c.setValue(n), backward)
	default:
		c.fail(n, "unsupported regex node")
	}
}

// Scalar consumption is native to rhun, while case equivalence keeps the i/iu distinction.
func snippetRegexCanonical(cp rune, flags uint32) rune {
	if flags&uint32(rxIgnoreCase) == 0 {
		return cp
	}
	if flags&uint32(rxUnicode) != 0 {
		return snippetUnicodeFold(cp)
	}
	text := snippetUnicodeCase(cp, true)
	if text == "" {
		return cp
	}
	r, n := utf8.DecodeRuneInString(text)
	if n != len(text) || cp >= 128 && r < 128 {
		return cp
	}
	return r
}
func (c *snippetRegexCompiler) add(n *snippetRegexNode, dst, src []snippetRuneRange) []snippetRuneRange {
	if len(src) > maxSnippetRegexTempRanges-len(dst) {
		c.fail(n, "temporary range limit exceeded")
		return nil
	}
	if !c.spend(n, len(src)) {
		return nil
	}
	return append(dst, src...)
}
func (c *snippetRegexCompiler) one(n *snippetRegexNode, dst []snippetRuneRange, lo, hi rune) []snippetRuneRange {
	if lo > hi {
		return dst
	}
	return c.add(n, dst, []snippetRuneRange{{lo, hi}})
}
func (c *snippetRegexCompiler) normalize(n *snippetRegexNode, input []snippetRuneRange) []snippetRuneRange {
	count := len(input)
	levels := 0
	for k := 1; k < count; k *= 2 {
		levels++
	}
	// Bottom-up mergesort has at most count*levels comparisons and writes.
	if count > maxSnippetRegexTempRanges {
		c.fail(n, "temporary range limit exceeded")
		return nil
	}
	if !c.spend(n, count+2*count*levels+count) {
		return nil
	}
	a := append([]snippetRuneRange(nil), input...)
	tmp := make([]snippetRuneRange, count)
	for width := 1; width < count; width *= 2 {
		for start := 0; start < count; start += 2 * width {
			mid := min(start+width, count)
			end := min(start+2*width, count)
			i, j := start, mid
			for at := start; at < end; at++ {
				if i < mid && (j == end || a[i].lo < a[j].lo || a[i].lo == a[j].lo && a[i].hi <= a[j].hi) {
					tmp[at] = a[i]
					i++
				} else {
					tmp[at] = a[j]
					j++
				}
			}
		}
		a, tmp = tmp, a
	}
	used := 0
	for _, r := range a {
		if used > 0 && a[used-1].hi+1 >= r.lo {
			if r.hi > a[used-1].hi {
				a[used-1].hi = r.hi
			}
		} else {
			a[used] = r
			used++
		}
	}
	return a[:used]
}
func (c *snippetRegexCompiler) complement(n *snippetRegexNode, a []snippetRuneRange) []snippetRuneRange {
	var out []snippetRuneRange
	for _, domain := range []snippetRuneRange{{0, 0xd7ff}, {0xe000, utf8.MaxRune}} {
		next := domain.lo
		for _, r := range a {
			if !c.spend(n, 1) {
				return nil
			}
			if r.hi < domain.lo {
				continue
			}
			if r.lo > domain.hi {
				break
			}
			if r.lo > next {
				out = c.one(n, out, next, r.lo-1)
			}
			if r.hi >= next {
				next = r.hi + 1
			}
			if next > domain.hi {
				break
			}
		}
		if next <= domain.hi {
			out = c.one(n, out, next, domain.hi)
		}
	}
	return out
}
func (c *snippetRegexCompiler) contains(n *snippetRegexNode, a []snippetRuneRange, cp rune) bool {
	lo, hi := 0, len(a)
	for lo < hi {
		if !c.spend(n, 1) {
			return false
		}
		mid := lo + (hi-lo)/2
		if a[mid].hi < cp {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo < len(a) && a[lo].lo <= cp
}
func (c *snippetRegexCompiler) mappings(n *snippetRegexNode) []snippetFoldEntry {
	if c.foldsReady {
		return c.folds
	}
	c.foldsReady = true
	if c.p.flags&uint32(rxUnicode) != 0 {
		c.folds = snippetUnicodeFolds[:]
		return c.folds
	}
	if !c.spend(n, len(snippetUnicodeUpper)) {
		return nil
	}
	for _, entry := range snippetUnicodeUpper {
		r, size := utf8.DecodeRuneInString(entry.text)
		if size == len(entry.text) && !(entry.from >= 128 && r < 128) {
			c.folds = append(c.folds, snippetFoldEntry{entry.from, r})
		}
	}
	return c.folds
}
func (c *snippetRegexCompiler) closeCase(n *snippetRegexNode, a []snippetRuneRange) []snippetRuneRange {
	if c.p.flags&uint32(rxIgnoreCase) == 0 || c.err != nil {
		return a
	}
	folds := c.mappings(n)
	var extra []snippetRuneRange
	for _, f := range folds {
		if !c.spend(n, 1) {
			return nil
		}
		if c.contains(n, a, f.from) {
			extra = c.one(n, extra, f.to, f.to)
		}
	}
	reps := c.normalize(n, c.add(n, extra, a))
	extra = nil
	for _, f := range folds {
		if !c.spend(n, 1) {
			return nil
		}
		if c.contains(n, reps, f.to) {
			extra = c.one(n, extra, f.from, f.from)
		}
	}
	return c.normalize(n, c.add(n, extra, reps))
}
func (c *snippetRegexCompiler) predicate(n *snippetRegexNode) []snippetRuneRange {
	if !c.spend(n, 1) {
		return nil
	}
	var a []snippetRuneRange
	switch n.kind {
	case rxLiteral:
		a = c.one(n, nil, n.lo, n.lo)
	case rxRange:
		// Literal ranges crossing the surrogate hole contain only scalar values.
		if n.lo < 0xd800 {
			a = c.one(n, a, n.lo, min(n.hi, 0xd7ff))
		}
		if n.hi > 0xdfff {
			a = c.one(n, a, max(n.lo, 0xe000), n.hi)
		}
	case rxShorthand:
		switch n.text {
		case "d":
			a = c.one(n, a, '0', '9')
		case "w":
			a = c.add(n, a, []snippetRuneRange{{'0', '9'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}})
		case "s":
			for _, r := range snippetUnicodeProperties["bin:White_Space"] {
				if r.lo == 0x85 && r.hi == 0x85 {
					continue
				}
				a = c.add(n, a, []snippetRuneRange{r})
			}
			a = c.one(n, a, 0xfeff, 0xfeff)
		}
	case rxProperty:
		ranges, ok := snippetUnicodeProperty(n.text)
		if !ok {
			c.fail(n, "unknown Unicode property")
			return nil
		}
		a = c.add(n, a, ranges)
	case rxClass:
		for _, child := range n.children {
			if c.err != nil {
				break
			}
			a = c.add(n, a, c.predicate(child))
		}
		a = c.normalize(n, a)
		if n.negative {
			a = c.complement(n, a)
		}
		return a
	}
	a = c.normalize(n, a)
	if n.kind == rxProperty && n.negative {
		a = c.complement(n, a)
	}
	a = c.closeCase(n, a)
	if n.kind == rxShorthand && n.negative {
		a = c.complement(n, a)
	}
	return a
}
func (c *snippetRegexCompiler) class(n *snippetRegexNode, ranges []snippetRuneRange) uint32 {
	if !c.spend(n, len(ranges)) {
		return 0
	}
	hash := uint64(14695981039346656037)
	for _, r := range ranges {
		for _, v := range []rune{r.lo, r.hi} {
			hash ^= uint64(uint32(v))
			hash *= 1099511628211
		}
	}
	for _, id := range c.intern[hash] {
		d := c.p.classes[id]
		if int(d.count) != len(ranges) {
			continue
		}
		if !c.spend(n, len(ranges)) {
			return 0
		}
		equal := true
		for i, r := range ranges {
			if c.p.ranges[int(d.start)+i] != r {
				equal = false
				break
			}
		}
		if equal {
			return id
		}
	}
	if len(c.p.classes) == maxSnippetRegexClasses {
		c.fail(n, "class limit exceeded")
		return 0
	}
	if len(ranges) > maxSnippetRegexRanges-len(c.p.ranges) {
		c.fail(n, "range limit exceeded")
		return 0
	}
	if !c.spend(n, len(ranges)) {
		return 0
	}
	id := uint32(len(c.p.classes))
	c.p.classes = append(c.p.classes, snippetRegexClass{uint32(len(c.p.ranges)), uint32(len(ranges))})
	c.p.ranges = append(c.p.ranges, ranges...)
	c.intern[hash] = append(c.intern[hash], id)
	return id
}
