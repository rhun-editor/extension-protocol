package rhunext

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
)

func regexProgramFixture() *snippetRegexProgram {
	return &snippetRegexProgram{flags: rxAllFlags, captures: 1, code: []snippetRegexInstruction{
		{rxChar, 'a', 0, 0}, {rxDot, 0, 0, 0}, {rxSet, 0, 0, 0}, {rxAssertion, 1, 0, 0}, {rxSave, 2, 0, 0}, {rxClear, 2, 2, 0}, {rxSplit, 7, 8, 0}, {rxJump, 8, 0, 0}, {rxReference, 1, 0, 0}, {rxRepeatStart, 0, 0, 0}, {rxRepeatTest, 0, 11, 13}, {rxRepeatNext, 0, 10, 0}, {rxLookCall, 13, 13, 3}, {rxMatch, 0, 0, 0}}, classes: []snippetRegexClass{{0, 1}}, ranges: []snippetRuneRange{{'a', 'z'}}, repeats: []snippetRegexRepeat{{0, 0xffffffff, 1, 0}}}
}
func mustRegexBytes(t *testing.T, p *snippetRegexProgram) []byte {
	t.Helper()
	b, e := p.encode()
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestSnippetRegexProgramBinaryGolden(t *testing.T) {
	p := &snippetRegexProgram{code: []snippetRegexInstruction{{rxChar, 'a', 0, 0}, {rxMatch, 0, 0, 0}}}
	want, e := hex.DecodeString("524e525801000000000010000000000000000000020000000000000000000000000000000000000050000000000000000100000061000000000000000000000000000000000000000000000000000000")
	if e != nil {
		t.Fatal(e)
	}
	b := mustRegexBytes(t, p)
	if !bytes.Equal(b, want) {
		t.Fatalf("%x != %x", b, want)
	}
	decoded, e := decodeSnippetRegexProgram(b)
	if e != nil {
		t.Fatal(e)
	}
	if again := mustRegexBytes(t, decoded); !bytes.Equal(b, again) {
		t.Fatal("round trip")
	}
	b[52] = 'z'
	if decoded.code[0].x != 'a' {
		t.Fatal("decoder aliases input")
	}
	encoded := mustRegexBytes(t, decoded)
	encoded[52] = 'q'
	if decoded.code[0].x != 'a' {
		t.Fatal("encoder aliases program")
	}
}
func TestSnippetRegexProgramAllOperands(t *testing.T) {
	p := regexProgramFixture()
	b := mustRegexBytes(t, p)
	q, e := decodeSnippetRegexProgram(b)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(p, q) {
		t.Fatalf("%#v != %#v", p, q)
	}
	for _, opcode := range []uint32{rxAssertion, rxSave, rxLookCall} {
		for _, value := range map[uint32][]uint32{rxAssertion: {1, 2, 3, 4}, rxSave: {2, 3}, rxLookCall: {0, 1, 2, 3}}[opcode] {
			q := regexProgramFixture()
			for i, in := range q.code {
				if in.op == opcode {
					if opcode == rxLookCall {
						q.code[i].z = value
					} else {
						q.code[i].x = value
					}
				}
			}
			mustRegexBytes(t, q)
		}
	}
	// Empty sets may share the next range offset; all ranges still belong to exactly one set.
	q = &snippetRegexProgram{code: []snippetRegexInstruction{{rxSet, 0, 0, 0}, {rxMatch, 0, 0, 0}}, classes: []snippetRegexClass{{0, 0}, {0, 1}, {1, 0}}, ranges: []snippetRuneRange{{0, 0xd7ff}}}
	mustRegexBytes(t, q)
	// Structural validation deliberately accepts bounded-execution cycles.
	mustRegexBytes(t, &snippetRegexProgram{code: []snippetRegexInstruction{{rxJump, 0, 0, 0}}})
}
func TestSnippetRegexProgramMalformed(t *testing.T) {
	base := mustRegexBytes(t, regexProgramFixture())
	classAt := 48 + 14*16
	rangeAt := classAt + 8
	repeatAt := rangeAt + 8
	cases := []struct {
		name  string
		at    int
		value uint32
	}{
		{"magic", 0, 0}, {"format", 4, 2}, {"unicode", 8, 0}, {"flags", 12, 128}, {"captures", 16, 65}, {"zero-code", 20, 0}, {"code-limit", 20, 8193}, {"class-limit", 24, 257}, {"range-limit", 28, 8193}, {"repeat-limit", 32, 257}, {"entry", 36, 14}, {"length", 40, 0}, {"reserved", 44, 1},
		{"op", 48, 16}, {"char", 52, 0xd800}, {"huge-char", 52, 0xffffffff}, {"char-y", 56, 1}, {"char-z", 60, 1}, {"dot-x", 68, 1}, {"set-index", 84, 1}, {"set-y", 88, 1}, {"assert-zero", 100, 0}, {"assert-five", 100, 5}, {"assert-y", 104, 1}, {"save-zero", 116, 0}, {"save-high", 116, 4}, {"save-y", 120, 1}, {"clear-odd", 132, 3}, {"clear-countzero", 136, 0}, {"clear-countodd", 136, 1}, {"clear-high", 136, 4}, {"clear-overflow", 132, 0xfffffffe}, {"clear-z", 140, 1},
		{"split-x", 148, 14}, {"split-y", 152, 14}, {"split-z", 156, 1}, {"jump-x", 164, 14}, {"jump-y", 168, 1}, {"ref-zero", 180, 0}, {"ref-high", 180, 2}, {"ref-y", 184, 1}, {"start-index", 196, 1}, {"start-y", 200, 1}, {"test-index", 212, 1}, {"test-body", 216, 14}, {"test-exit", 220, 14}, {"next-index", 228, 1}, {"next-target", 232, 14}, {"next-z", 236, 1}, {"look-sub", 244, 14}, {"look-next", 248, 14}, {"look-flags", 252, 4}, {"match-x", 260, 1}, {"match-y", 264, 1}, {"match-z", 268, 1},
		{"class-start", classAt, 1}, {"class-count", classAt + 4, 2}, {"range-surrogate", rangeAt, 0xd800}, {"range-reversed", rangeAt, 'z' + 1}, {"range-huge", rangeAt + 4, 0x110000}, {"repeat-min", repeatAt, 0x80000000}, {"repeat-max", repeatAt + 4, 0x80000000}, {"repeat-flags", repeatAt + 8, 2}, {"repeat-reserved", repeatAt + 12, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bytes.Clone(base)
			binary.LittleEndian.PutUint32(b[tc.at:], tc.value)
			p, e := decodeSnippetRegexProgram(b)
			var diagnostic *snippetRegexProgramError
			if p != nil || !errors.As(e, &diagnostic) {
				t.Fatalf("accepted: %+v %v", p, e)
			}
			if diagnostic.Offset < 0 || diagnostic.Offset > len(b) {
				t.Fatal(diagnostic)
			}
		})
	}
	for i := 0; i < len(base); i++ {
		if p, e := decodeSnippetRegexProgram(base[:i]); p != nil || e == nil {
			t.Fatalf("truncation %d", i)
		}
	}
	for _, b := range [][]byte{append(bytes.Clone(base), 0), make([]byte, maxSnippetRegexProgramBytes+1)} {
		if p, e := decodeSnippetRegexProgram(b); p != nil || e == nil {
			t.Fatal("accepted length")
		}
	}
}
func TestSnippetRegexProgramTypedRejection(t *testing.T) {
	cases := []*snippetRegexProgram{nil, {}, {code: []snippetRegexInstruction{{rxChar, 'a', 0, 0}}}, {code: []snippetRegexInstruction{{rxMatch, 0, 0, 0}}, ranges: []snippetRuneRange{{0, 0}}},
		{code: []snippetRegexInstruction{{rxMatch, 0, 0, 0}}, classes: []snippetRegexClass{{0, 1}}, ranges: []snippetRuneRange{{0xd7ff, 0xe000}}},
		{code: []snippetRegexInstruction{{rxMatch, 0, 0, 0}}, classes: []snippetRegexClass{{0, 2}}, ranges: []snippetRuneRange{{1, 2}, {3, 4}}},
		{code: []snippetRegexInstruction{{rxMatch, 0, 0, 0}}, classes: []snippetRegexClass{{0, 2}}, ranges: []snippetRuneRange{{3, 4}, {1, 2}}},
		{code: []snippetRegexInstruction{{rxMatch, 0, 0, 0}}, repeats: []snippetRegexRepeat{{2, 1, 0, 0}}},
	}
	for i, p := range cases {
		if b, e := p.encode(); b != nil || e == nil {
			t.Fatalf("case%d accepted", i)
		}
	}
	p := &snippetRegexProgram{captures: 64, code: make([]snippetRegexInstruction, maxSnippetRegexInstructions), classes: make([]snippetRegexClass, maxSnippetRegexClasses), repeats: make([]snippetRegexRepeat, maxSnippetRegexRepeats)}
	// A terminal at every position is structurally valid.
	mustRegexBytes(t, p)
	p.code = append(p.code, snippetRegexInstruction{})
	if b, e := p.encode(); b != nil || e == nil {
		t.Fatal("instruction cap")
	}
}
func FuzzSnippetRegexProgramDecode(f *testing.F) {
	for _, p := range []*snippetRegexProgram{regexProgramFixture(), {code: []snippetRegexInstruction{{rxMatch, 0, 0, 0}}}} {
		b, e := p.encode()
		if e != nil {
			f.Fatal(e)
		}
		f.Add(b)
	}
	f.Add([]byte("RNRX"))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, e := decodeSnippetRegexProgram(b)
		if e != nil {
			if p != nil {
				t.Fatal("partial program")
			}
			return
		}
		out, e := p.encode()
		if e != nil || !bytes.Equal(out, b) {
			t.Fatalf("round trip %v", e)
		}
	})
}
