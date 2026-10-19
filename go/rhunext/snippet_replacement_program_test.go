package rhunext

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func mustReplacementBytes(t *testing.T, p *snippetReplacementProgram) []byte {
	t.Helper()
	b, e := p.encode()
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func replacementFixture() *snippetReplacementProgram {
	return &snippetReplacementProgram{code: []snippetReplacementInstruction{
		{replacementLiteral, 0, "é\x00", ""}, {replacementCapture, 0, "", ""},
		{replacementUpper, 1, "", ""}, {replacementLower, 2, "", ""}, {replacementCapitalize, 3, "", ""}, {replacementCamel, 4, "", ""}, {replacementPascal, 5, "", ""}, {replacementSnake, 6, "", ""}, {replacementKebab, 0x7fffffff, "", ""},
		{replacementIf, 1, "yes", ""}, {replacementElse, 2, "", ""}, {replacementConditional, 3, "if", "else"},
	}}
}
func TestSnippetReplacementBinaryGolden(t *testing.T) {
	p := &snippetReplacementProgram{code: []snippetReplacementInstruction{{replacementLiteral, 0, "é", ""}}}
	want, e := hex.DecodeString("524e5246010000000000100001000000020000003a0000000000000000000000000000000000000000000000020000000000000000000000c3a9")
	if e != nil {
		t.Fatal(e)
	}
	b := mustReplacementBytes(t, p)
	if !bytes.Equal(b, want) {
		t.Fatalf("%x !=%x", b, want)
	}
	decoded, e := decodeSnippetReplacement(b)
	if e != nil {
		t.Fatal(e)
	}
	b[len(b)-1] = 0
	if decoded.code[0].a != "é" {
		t.Fatal("inputalias")
	}
	encoded := mustReplacementBytes(t, decoded)
	encoded[len(encoded)-1] = 0
	if decoded.code[0].a != "é" {
		t.Fatal("outputalias")
	}
	b = mustReplacementBytes(t, replacementFixture())
	q, e := decodeSnippetReplacement(b)
	if e != nil || !reflect.DeepEqual(q, replacementFixture()) {
		t.Fatal(q, e)
	}
	empty := mustReplacementBytes(t, &snippetReplacementProgram{})
	if len(empty) != 32 {
		t.Fatal("empty size")
	}
	if _, e := decodeSnippetReplacement(empty); e != nil {
		t.Fatal(e)
	}
}
func TestSnippetReplacementCompile(t *testing.T) {
	template, e := ParseSnippetTemplate(`${1/a/é\$0$0${1:/upcase}${2:/downcase}${3:/capitalize}${4:/camelcase}${5:/pascalcase}${6:/snakecase}${7:/kebabcase}${1:+yes}${2:-}${3:?if:else}/}`)
	if e != nil {
		t.Fatal(e)
	}
	parts := template.nodes[0].transform.format
	p, e := compileSnippetReplacement(parts)
	if e != nil {
		t.Fatal(e)
	}
	want := replacementFixture()
	want.code[0].a = "é$0"
	want.code[8].index = 7
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("%#v != %#v", p, want)
	}
	parts[0].text = "changed"
	if p.code[0].a != "é$0" {
		t.Fatal("AST alias")
	}
	invalidParts := []stFormat{{kind: stCase, text: "missing", from: 7}, {kind: stCapture, index: -1, from: 7}, {kind: stFormatText, index: 1, from: 7}, {kind: 255, from: 7}, {kind: stFormatText, text: "\xff", from: 7}}
	if strconv.IntSize == 64 {
		index := uint64(0x80000000)
		invalidParts = append(invalidParts, stFormat{kind: stCapture, index: int(index), from: 7})
	}
	for _, part := range invalidParts {
		p, e := compileSnippetReplacement([]stFormat{part})
		var diagnostic *SnippetExpansionError
		if p != nil || !errors.As(e, &diagnostic) || diagnostic.Offset != 7 {
			t.Fatal(p, e)
		}
	}
	parts = make([]stFormat, maxSnippetReplacementRecords+1)
	parts[maxSnippetReplacementRecords].from = 99
	if p, e := compileSnippetReplacement(parts); p != nil || e == nil {
		t.Fatal("record cap")
	}
}
func TestSnippetReplacementMalformed(t *testing.T) {
	b := mustReplacementBytes(t, replacementFixture())
	blob := 32 + 12*24
	cases := []struct {
		at     int
		value  uint32
		offset int
	}{
		{0, 0, 0}, {4, 2, 4}, {8, 0, 8}, {12, 8193, 12}, {16, 0xffffffff, 20}, {20, 0, 20}, {24, 1, 24}, {28, 1, 28},
		{32, 12, 32}, {36, 1, 36}, {40, 1, 40}, {44, 0xffffffff, 44}, {48, 1, 48}, {52, 1, 48},
		{60, 0x80000000, 60}, {64, 1, 64}, {68, 1, 64}, {72, 1, 72}, {76, 1, 72},
		{32 + 9*24 + 8, 0, 32 + 9*24 + 8}, {32 + 10*24 + 8, 0, 32 + 10*24 + 8}, {32 + 11*24 + 16, 0, 32 + 11*24 + 16},
	}
	for _, tc := range cases {
		data := bytes.Clone(b)
		binary.LittleEndian.PutUint32(data[tc.at:], tc.value)
		p, e := decodeSnippetReplacement(data)
		var diagnostic *snippetReplacementProgramError
		if p != nil || !errors.As(e, &diagnostic) || diagnostic.Offset != tc.offset {
			t.Fatalf("at%d got%v %v want%d", tc.at, p, e, tc.offset)
		}
	}
	for n := 0; n < len(b); n++ {
		if p, e := decodeSnippetReplacement(b[:n]); p != nil || e == nil {
			t.Fatalf("truncated %d", n)
		}
	}
	if p, e := decodeSnippetReplacement(append(bytes.Clone(b), 0)); p != nil || e == nil {
		t.Fatal("tailbytes")
	}
	if p, e := decodeSnippetReplacement(make([]byte, maxSnippetReplacementBytes+1)); p != nil || e == nil {
		t.Fatal("bytecap")
	}
	bad := bytes.Clone(b)
	bad[blob] = 0xff
	p, e := decodeSnippetReplacement(bad)
	var diagnostic *snippetReplacementProgramError
	if p != nil || !errors.As(e, &diagnostic) || diagnostic.Offset != blob {
		t.Fatal("UTF8", p, e)
	}
	// Two valid whole-blob bytes may not be split into separate text records.
	split := mustReplacementBytes(t, &snippetReplacementProgram{code: []snippetReplacementInstruction{{replacementLiteral, 0, "é", ""}, {replacementLiteral, 0, "", ""}}})
	binary.LittleEndian.PutUint32(split[44:], 1)
	binary.LittleEndian.PutUint32(split[64:], 1)
	binary.LittleEndian.PutUint32(split[68:], 1)
	p, e = decodeSnippetReplacement(split)
	if p != nil || !errors.As(e, &diagnostic) || diagnostic.Offset != 44 {
		t.Fatal("split", p, e)
	}
	tail := mustReplacementBytes(t, &snippetReplacementProgram{code: []snippetReplacementInstruction{{replacementLiteral, 0, "a", ""}}})
	binary.LittleEndian.PutUint32(tail[44:], 0)
	p, e = decodeSnippetReplacement(tail)
	if p != nil || !errors.As(e, &diagnostic) || diagnostic.Offset != 56 {
		t.Fatal("unowned", p, e)
	}
}
func TestSnippetReplacementTypedLimits(t *testing.T) {
	cases := []*snippetReplacementProgram{nil, {code: []snippetReplacementInstruction{{op: 12}}}, {code: []snippetReplacementInstruction{{op: replacementCapture, index: 0x80000000}}}, {code: []snippetReplacementInstruction{{op: replacementCapture, a: "x"}}}, {code: []snippetReplacementInstruction{{op: replacementIf, b: "x"}}}, {code: []snippetReplacementInstruction{{a: "\xff"}}}, {code: []snippetReplacementInstruction{{op: replacementConditional, b: "\xff"}}}, {code: make([]snippetReplacementInstruction, maxSnippetReplacementRecords+1)}}
	for _, p := range cases {
		if b, e := p.encode(); b != nil || e == nil {
			t.Fatal("typedaccepted")
		}
	}
	p := &snippetReplacementProgram{code: []snippetReplacementInstruction{{a: strings.Repeat("x", maxSnippetReplacementBytes-56)}}}
	b := mustReplacementBytes(t, p)
	if len(b) != maxSnippetReplacementBytes {
		t.Fatal("exact cap")
	}
	if _, e := decodeSnippetReplacement(b); e != nil {
		t.Fatal(e)
	}
	p.code[0].a += "x"
	if b, e := p.encode(); b != nil || e == nil {
		t.Fatal("bytecap")
	}
	if p, e := compileSnippetReplacement([]stFormat{{kind: stFormatText, text: p.code[0].a}}); p != nil || e == nil {
		t.Fatal("compile bytecap")
	}
	p = &snippetReplacementProgram{code: make([]snippetReplacementInstruction, maxSnippetReplacementRecords)}
	b = mustReplacementBytes(t, p)
	if _, e := decodeSnippetReplacement(b); e != nil {
		t.Fatal(e)
	}
}
func TestSnippetReplacementConcurrency(t *testing.T) {
	p := replacementFixture()
	want := mustReplacementBytes(t, p)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				b, e := p.encode()
				if e != nil || !bytes.Equal(b, want) {
					t.Error("encode", e)
				}
				q, e := decodeSnippetReplacement(b)
				if e != nil || !reflect.DeepEqual(q, p) {
					t.Error("decode", e)
				}
			}
		}()
	}
	wg.Wait()
}
func FuzzSnippetReplacementDecode(f *testing.F) {
	b, e := replacementFixture().encode()
	if e != nil {
		f.Fatal(e)
	}
	f.Add(b)
	f.Add([]byte("RNRF"))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, e := decodeSnippetReplacement(data)
		if e != nil {
			if p != nil {
				t.Fatal("partial")
			}
			return
		}
		b, e := p.encode()
		if e != nil || !bytes.Equal(data, b) {
			t.Fatal("roundtrip", e)
		}
	})
}
func FuzzSnippetReplacementCompile(f *testing.F) {
	f.Add(`${1/a/${1:+yes}${1:-no}/}`)
	f.Fuzz(func(t *testing.T, source string) {
		template, e := ParseSnippetTemplate(source)
		if e != nil {
			return
		}
		var visit func([]stNode)
		visit = func(nodes []stNode) {
			for _, node := range nodes {
				if node.transform != nil {
					p, e := compileSnippetReplacement(node.transform.format)
					if e != nil {
						if p != nil {
							t.Fatal("partial")
						}
						continue
					}
					b, e := p.encode()
					if e != nil {
						t.Fatal(e)
					}
					if _, e = decodeSnippetReplacement(b); e != nil {
						t.Fatal(e)
					}
				}
				visit(node.children)
			}
		}
		visit(template.nodes)
	})
}
