package rhunext

import (
	"encoding/binary"
	"fmt"
)

const (
	maxSnippetRegexProgramBytes = 256 << 10
	maxSnippetRegexInstructions = 8192
	maxSnippetRegexClasses      = 256
	maxSnippetRegexRanges       = 8192
	maxSnippetRegexRepeats      = 256
	rxProgramMagic              = 0x58524e52
	rxProgramVersion            = 1
	rxProgramUnicode            = 0x00100000
	rxProgramHeader             = 48
	rxAllFlags                  = uint32(rxGlobal | rxIgnoreCase | rxMultiline | rxDotAll | rxUnicode | rxSticky | rxIndices)
)
const (
	rxMatch uint32 = iota
	rxChar
	rxDot
	rxSet
	rxAssertion
	rxSave
	rxClear
	rxSplit
	rxJump
	rxReference
	rxRepeatStart
	rxRepeatTest
	rxRepeatNext
	rxLookCall
	rxScopedAssertion
	rxScopedReference
)

type snippetRegexInstruction struct{ op, x, y, z uint32 }
type snippetRegexClass struct{ start, count uint32 }
type snippetRegexRepeat struct{ min, max, flags, reserved uint32 }
type snippetRegexProgram struct {
	flags, captures, entry uint32
	code                   []snippetRegexInstruction
	classes                []snippetRegexClass
	ranges                 []snippetRuneRange
	repeats                []snippetRegexRepeat
}
type snippetRegexProgramError struct {
	Offset  int
	Message string
}

func (e *snippetRegexProgramError) Error() string {
	return fmt.Sprintf("regex program byte %d: %s", e.Offset, e.Message)
}
func rxProgramError(at int, message string) error { return &snippetRegexProgramError{at, message} }

func rxProgramSize(instructions, classes, ranges, repeats uint64) (int, error) {
	if instructions == 0 || instructions > maxSnippetRegexInstructions {
		return 0, rxProgramError(20, "instruction count out of bounds")
	}
	if classes > maxSnippetRegexClasses {
		return 0, rxProgramError(24, "class count out of bounds")
	}
	if ranges > maxSnippetRegexRanges {
		return 0, rxProgramError(28, "range count out of bounds")
	}
	if repeats > maxSnippetRegexRepeats {
		return 0, rxProgramError(32, "repeat count out of bounds")
	}
	// Counts are bounded before multiplication and conversion on every platform.
	size := uint64(rxProgramHeader) + instructions*16 + classes*8 + ranges*8 + repeats*16
	if size > maxSnippetRegexProgramBytes {
		return 0, rxProgramError(40, "program byte limit exceeded")
	}
	return int(size), nil
}
func (p *snippetRegexProgram) validate() error {
	if p == nil {
		return rxProgramError(0, "nil program")
	}
	if _, err := rxProgramSize(uint64(len(p.code)), uint64(len(p.classes)), uint64(len(p.ranges)), uint64(len(p.repeats))); err != nil {
		return err
	}
	if p.flags & ^rxAllFlags != 0 {
		return rxProgramError(12, "unsupported flags")
	}
	if p.captures > maxSnippetRegexCaptures {
		return rxProgramError(16, "capture count out of bounds")
	}
	if uint64(p.entry) >= uint64(len(p.code)) {
		return rxProgramError(36, "entry out of bounds")
	}
	slots := 2 * (p.captures + 1)
	target := func(pc uint32) bool { return uint64(pc) < uint64(len(p.code)) }
	for i, in := range p.code {
		at := rxProgramHeader + i*16
		fallthroughPC := true
		switch in.op {
		case rxMatch:
			if in.x|in.y|in.z != 0 {
				return rxProgramError(at+4, "reserved operands")
			}
			fallthroughPC = false
		case rxChar:
			if !snippetUnicodeScalar(rune(in.x)) {
				return rxProgramError(at+4, "invalid character")
			}
			if in.y|in.z != 0 {
				return rxProgramError(at+8, "reserved operands")
			}
		case rxDot:
			if in.x|in.y|in.z != 0 {
				return rxProgramError(at+4, "reserved operands")
			}
		case rxSet:
			if uint64(in.x) >= uint64(len(p.classes)) {
				return rxProgramError(at+4, "class out of bounds")
			}
			if in.y|in.z != 0 {
				return rxProgramError(at+8, "reserved operands")
			}
		case rxAssertion:
			if in.x < 1 || in.x > 4 {
				return rxProgramError(at+4, "unknown assertion")
			}
			if in.y|in.z != 0 {
				return rxProgramError(at+8, "reserved operands")
			}
		case rxScopedAssertion, rxScopedReference:
			mask := uint32(rxIgnoreCase | rxMultiline)
			if in.op == rxScopedAssertion {
				if in.x < 1 || in.x > 4 {
					return rxProgramError(at+4, "unknown assertion")
				}
			} else {
				if in.x < 1 || in.x > p.captures {
					return rxProgramError(at+4, "reference out of bounds")
				}
				mask = uint32(rxIgnoreCase)
			}
			if in.y&^mask != 0 {
				return rxProgramError(at+8, "unknown scoped flags")
			}
			if in.z != 0 {
				return rxProgramError(at+12, "reserved operand")
			}
		case rxSave:
			if in.x < 2 || in.x >= slots {
				return rxProgramError(at+4, "capture slot out of bounds")
			}
			if in.y|in.z != 0 {
				return rxProgramError(at+8, "reserved operands")
			}
		case rxClear:
			if in.x < 2 || in.x&1 != 0 || in.y == 0 || in.y&1 != 0 || uint64(in.x)+uint64(in.y) > uint64(slots) {
				return rxProgramError(at+4, "capture span out of bounds")
			}
			if in.z != 0 {
				return rxProgramError(at+12, "reserved operand")
			}
		case rxSplit:
			if !target(in.x) {
				return rxProgramError(at+4, "branch out of bounds")
			}
			if !target(in.y) {
				return rxProgramError(at+8, "branch out of bounds")
			}
			if in.z != 0 {
				return rxProgramError(at+12, "reserved operand")
			}
			fallthroughPC = false
		case rxJump:
			if !target(in.x) {
				return rxProgramError(at+4, "branch out of bounds")
			}
			if in.y|in.z != 0 {
				return rxProgramError(at+8, "reserved operands")
			}
			fallthroughPC = false
		case rxReference:
			if in.x < 1 || in.x > p.captures {
				return rxProgramError(at+4, "reference out of bounds")
			}
			if in.y|in.z != 0 {
				return rxProgramError(at+8, "reserved operands")
			}
		case rxRepeatStart:
			if uint64(in.x) >= uint64(len(p.repeats)) {
				return rxProgramError(at+4, "repeat out of bounds")
			}
			if in.y|in.z != 0 {
				return rxProgramError(at+8, "reserved operands")
			}
		case rxRepeatTest:
			if uint64(in.x) >= uint64(len(p.repeats)) {
				return rxProgramError(at+4, "repeat out of bounds")
			}
			if !target(in.y) {
				return rxProgramError(at+8, "branch out of bounds")
			}
			if !target(in.z) {
				return rxProgramError(at+12, "branch out of bounds")
			}
			fallthroughPC = false
		case rxRepeatNext:
			if uint64(in.x) >= uint64(len(p.repeats)) {
				return rxProgramError(at+4, "repeat out of bounds")
			}
			if !target(in.y) {
				return rxProgramError(at+8, "branch out of bounds")
			}
			if in.z != 0 {
				return rxProgramError(at+12, "reserved operand")
			}
			fallthroughPC = false
		case rxLookCall:
			if !target(in.x) {
				return rxProgramError(at+4, "branch out of bounds")
			}
			if !target(in.y) {
				return rxProgramError(at+8, "branch out of bounds")
			}
			if in.z & ^uint32(3) != 0 {
				return rxProgramError(at+12, "unknown look flags")
			}
			fallthroughPC = false
		default:
			return rxProgramError(at, "unknown opcode")
		}
		if fallthroughPC && i+1 == len(p.code) {
			return rxProgramError(at, "fallthrough outside program")
		}
	}
	classAt := rxProgramHeader + len(p.code)*16
	rangeAt := classAt + len(p.classes)*8
	next := uint64(0)
	for i, c := range p.classes {
		if uint64(c.start) != next || uint64(c.count) > uint64(len(p.ranges))-next {
			return rxProgramError(classAt+i*8, "invalid class span")
		}
		for j := uint64(0); j < uint64(c.count); j++ {
			r := p.ranges[next+j]
			if !snippetUnicodeScalar(r.lo) || !snippetUnicodeScalar(r.hi) || r.lo > r.hi || r.lo < 0xd800 && r.hi > 0xdfff {
				return rxProgramError(rangeAt+int(next+j)*8, "invalid scalar range")
			}
			if j > 0 && p.ranges[next+j-1].hi+1 >= r.lo {
				return rxProgramError(rangeAt+int(next+j)*8, "noncanonical range order")
			}
		}
		next += uint64(c.count)
	}
	if next != uint64(len(p.ranges)) {
		return rxProgramError(rangeAt+int(next)*8, "unowned ranges")
	}
	repeatAt := rangeAt + len(p.ranges)*8
	for i, r := range p.repeats {
		if r.min > 0x7fffffff {
			return rxProgramError(repeatAt+i*16, "minimum out of bounds")
		}
		if r.max != 0xffffffff && (r.max > 0x7fffffff || r.max < r.min) {
			return rxProgramError(repeatAt+i*16+4, "maximum out of bounds")
		}
		if r.flags & ^uint32(1) != 0 {
			return rxProgramError(repeatAt+i*16+8, "unknown repeat flags")
		}
		if r.reserved != 0 {
			return rxProgramError(repeatAt+i*16+12, "reserved repeat operand")
		}
	}
	return nil
}
func (p *snippetRegexProgram) encode() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	size, _ := rxProgramSize(uint64(len(p.code)), uint64(len(p.classes)), uint64(len(p.ranges)), uint64(len(p.repeats)))
	out := make([]byte, size)
	at := 0
	put := func(v uint32) { binary.LittleEndian.PutUint32(out[at:at+4], v); at += 4 }
	for _, v := range []uint32{rxProgramMagic, rxProgramVersion, rxProgramUnicode, p.flags, p.captures, uint32(len(p.code)), uint32(len(p.classes)), uint32(len(p.ranges)), uint32(len(p.repeats)), p.entry, uint32(size), 0} {
		put(v)
	}
	for _, in := range p.code {
		put(in.op)
		put(in.x)
		put(in.y)
		put(in.z)
	}
	for _, c := range p.classes {
		put(c.start)
		put(c.count)
	}
	for _, r := range p.ranges {
		put(uint32(r.lo))
		put(uint32(r.hi))
	}
	for _, r := range p.repeats {
		put(r.min)
		put(r.max)
		put(r.flags)
		put(r.reserved)
	}
	return out, nil
}
func decodeSnippetRegexProgram(data []byte) (*snippetRegexProgram, error) {
	if len(data) < rxProgramHeader {
		return nil, rxProgramError(len(data), "truncated header")
	}
	if len(data) > maxSnippetRegexProgramBytes {
		return nil, rxProgramError(40, "program byte limit exceeded")
	}
	get := func(at int) uint32 { return binary.LittleEndian.Uint32(data[at : at+4]) }
	for _, v := range []struct {
		at   int
		want uint32
	}{{0, rxProgramMagic}, {4, rxProgramVersion}, {8, rxProgramUnicode}, {44, 0}} {
		if get(v.at) != v.want {
			return nil, rxProgramError(v.at, "invalid header")
		}
	}
	size, err := rxProgramSize(uint64(get(20)), uint64(get(24)), uint64(get(28)), uint64(get(32)))
	if err != nil {
		return nil, err
	}
	if uint64(get(40)) != uint64(size) || len(data) != size {
		return nil, rxProgramError(40, "length mismatch")
	}
	if get(12) & ^rxAllFlags != 0 {
		return nil, rxProgramError(12, "unsupported flags")
	}
	if get(16) > maxSnippetRegexCaptures {
		return nil, rxProgramError(16, "capture count out of bounds")
	}
	if get(36) >= get(20) {
		return nil, rxProgramError(36, "entry out of bounds")
	}
	p := &snippetRegexProgram{flags: get(12), captures: get(16), entry: get(36), code: make([]snippetRegexInstruction, int(get(20))), classes: make([]snippetRegexClass, int(get(24))), ranges: make([]snippetRuneRange, int(get(28))), repeats: make([]snippetRegexRepeat, int(get(32)))}
	at := rxProgramHeader
	for i := range p.code {
		p.code[i] = snippetRegexInstruction{get(at), get(at + 4), get(at + 8), get(at + 12)}
		at += 16
	}
	for i := range p.classes {
		p.classes[i] = snippetRegexClass{get(at), get(at + 4)}
		at += 8
	}
	for i := range p.ranges {
		p.ranges[i] = snippetRuneRange{rune(get(at)), rune(get(at + 4))}
		at += 8
	}
	for i := range p.repeats {
		p.repeats[i] = snippetRegexRepeat{get(at), get(at + 4), get(at + 8), get(at + 12)}
		at += 16
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}
