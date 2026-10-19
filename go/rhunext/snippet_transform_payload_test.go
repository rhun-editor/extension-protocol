package rhunext

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func simpleTransformPayload(t testing.TB) *snippetTransformPayload {
	t.Helper()
	r, e := compileSnippetRegex("a", "")
	if e != nil {
		t.Fatal(e)
	}
	return &snippetTransformPayload{[]snippetTransformBinding{{1, r, snippetTransformFormat(t, "x")}}}
}
func TestSnippetTransformPayloadBinary(t *testing.T) {
	p := simpleTransformPayload(t)
	data, e := p.encode()
	if e != nil {
		t.Fatal(e)
	}
	want := []uint32{snippetTransformPayloadMagic, 1, rxProgramUnicode, 1, 193, 0, 0, 0, 1, 56, 80, 136, 57, 0}
	for i, n := range want {
		if got := binary.LittleEndian.Uint32(data[i*4:]); got != n {
			t.Fatalf("word%d %d want%d", i, got, n)
		}
	}
	if len(data) != 193 {
		t.Fatal("length")
	}
	decoded, e := decodeSnippetTransformPayload(data)
	if e != nil {
		t.Fatal(e)
	}
	again, e := decoded.encode()
	if e != nil || !bytes.Equal(data, again) {
		t.Fatal("roundtrip", e)
	}
	for i := range data {
		data[i] = 0
	}
	b := decoded.bindings[0]
	got, e := executeSnippetTransform(b.regex, b.format, "aba", nil)
	if e != nil || got != "xba" {
		t.Fatal("retained input", got, e)
	}
	empty, e := (&snippetTransformPayload{}).encode()
	if e != nil || len(empty) != 32 {
		t.Fatal(e)
	}
	if p, e := decodeSnippetTransformPayload(empty); e != nil || len(p.bindings) != 0 {
		t.Fatal(e)
	}
}
func TestSnippetTransformPayloadErrors(t *testing.T) {
	base, e := simpleTransformPayload(t).encode()
	if e != nil {
		t.Fatal(e)
	}
	check := func(data []byte, want int) {
		t.Helper()
		p, e := decodeSnippetTransformPayload(data)
		var detail *snippetTransformPayloadError
		if p != nil || !errors.As(e, &detail) || detail.Offset != want {
			t.Fatalf("got%v %v want%d", p, e, want)
		}
	}
	for i := 0; i < 32; i++ {
		check(base[:i], i)
	}
	for _, v := range []struct {
		at    int
		value uint32
		want  int
	}{{0, 0, 0}, {4, 2, 4}, {8, 0, 8}, {20, 1, 20}, {24, 1, 24}, {28, 1, 28}, {12, 513, 12}, {12, 0xffffffff, 12}, {16, 192, 16}, {32, 0, 32}, {32, 513, 32}, {52, 1, 52}, {36, 57, 36}, {40, 0xffffffff, 40}, {44, 137, 44}, {48, 0xffffffff, 48}, {56, 0, 56}, {136, 0, 136}, {136 + 56, 0xff, 136 + 56}} {
		data := bytes.Clone(base)
		if v.at == 192 {
			data[v.at] = byte(v.value)
		} else {
			binary.LittleEndian.PutUint32(data[v.at:], v.value)
		}
		check(data, v.want)
	}
	data := append(bytes.Clone(base), 0)
	binary.LittleEndian.PutUint32(data[16:], uint32(len(data)))
	check(data, 16)
	two := simpleTransformPayload(t)
	two.bindings = append(two.bindings, snippetTransformBinding{3, two.bindings[0].regex, two.bindings[0].format})
	data, e = two.encode()
	if e != nil {
		t.Fatal(e)
	}
	binary.LittleEndian.PutUint32(data[56:], 1)
	check(data, 56)
	nested := bytes.Clone(base)
	nested[56] = 0
	p, e := decodeSnippetTransformPayload(nested)
	var pe *snippetRegexProgramError
	if p != nil || !errors.As(e, &pe) {
		t.Fatal("nested regex cause", e)
	}
	nested = bytes.Clone(base)
	nested[136] = 0
	p, e = decodeSnippetTransformPayload(nested)
	var re *snippetReplacementProgramError
	if p != nil || !errors.As(e, &re) {
		t.Fatal("nested replacement cause", e)
	}
	var nilPayload *snippetTransformPayload
	if b, e := nilPayload.encode(); b != nil || e == nil {
		t.Fatal("nil")
	}
	for _, p := range []*snippetTransformPayload{{bindings: make([]snippetTransformBinding, 513)}, {bindings: []snippetTransformBinding{{field: 0}}}, {bindings: []snippetTransformBinding{{field: 513}}}, {bindings: []snippetTransformBinding{{field: 1}}}} {
		if b, e := p.encode(); b != nil || e == nil {
			t.Fatal("invalid typed payload")
		}
	}
	p = simpleTransformPayload(t)
	p.bindings[0].format = nil
	if b, e := p.encode(); b != nil || e == nil {
		t.Fatal("nil format")
	}
	p = simpleTransformPayload(t)
	p.bindings[0].regex.code[0].op = 99
	if b, e := p.encode(); b != nil || e == nil {
		t.Fatal("bad regex")
	}
	p = simpleTransformPayload(t)
	p.bindings[0].format.code[0].a = strings.Repeat("x", MaxSnippetBody)
	if b, e := p.encode(); b != nil || e == nil {
		t.Fatal("format length")
	}
	p = simpleTransformPayload(t)
	p.bindings[0].format.code[0].a = strings.Repeat("x", MaxSnippetBody-replacementHeader-replacementRecord)
	if b, e := p.encode(); b != nil || e == nil {
		t.Fatal("aggregate length")
	}
	p = simpleTransformPayload(t)
	p.bindings = append(p.bindings, p.bindings[0])
	if b, e := p.encode(); b != nil || e == nil {
		t.Fatal("duplicate")
	}
	p.bindings[0].field = 2
	if b, e := p.encode(); b != nil || e == nil {
		t.Fatal("order")
	}
}
func TestSnippetTransformPayloadLimitsConcurrent(t *testing.T) {
	p := simpleTransformPayload(t)
	one := p.bindings[0]
	p.bindings = nil
	for i := 1; i <= MaxSnippetFields; i++ {
		b := one
		b.field = i
		p.bindings = append(p.bindings, b)
	}
	data, e := p.encode()
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := decodeSnippetTransformPayload(data)
	if e != nil || len(decoded.bindings) != 512 {
		t.Fatal(e)
	}
	before := bytes.Clone(data)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			again, e := decoded.encode()
			if e != nil || !reflect.DeepEqual(before, again) {
				t.Errorf("concurrent %v", e)
			}
		}()
	}
	wg.Wait()
	if !bytes.Equal(data, before) {
		t.Fatal("mutated input")
	}
}
func FuzzSnippetTransformPayload(f *testing.F) {
	data, e := simpleTransformPayload(f).encode()
	if e != nil {
		f.Fatal(e)
	}
	f.Add(data)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxSnippetBody+1 {
			t.Skip()
		}
		p, e := decodeSnippetTransformPayload(data)
		if e != nil {
			if p != nil {
				t.Fatal("partial")
			}
			return
		}
		out, e := p.encode()
		if e != nil || !bytes.Equal(out, data) {
			t.Fatal("noncanonical accepted", e)
		}
	})
}
