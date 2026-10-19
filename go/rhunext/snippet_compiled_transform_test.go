package rhunext

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestCompiledSnippetTransformFactoryAndOwnership(t *testing.T) {
	program, err := CompileSnippetTransform("(a)", `${1:/upcase}\/x`, "g")
	if err != nil {
		t.Fatal(err)
	}
	r, f := program.Programs()
	regex, err := decodeSnippetRegexProgram(r)
	if err != nil {
		t.Fatal(err)
	}
	format, err := decodeSnippetReplacement(f)
	if err != nil {
		t.Fatal(err)
	}
	out, err := executeSnippetTransform(regex, format, "aa", nil)
	if err != nil || out != "A/xA/x" {
		t.Fatal(out, err)
	}
	imported, err := NewCompiledSnippetTransform(r, f)
	if err != nil || !reflect.DeepEqual(imported, program) {
		t.Fatal(err)
	}
	for i := range r {
		r[i] = 0
	}
	for i := range f {
		f[i] = 0
	}
	if err := imported.Validate(); err != nil {
		t.Fatal("input retained", err)
	}
	r, f = imported.Programs()
	r[0] = 0
	f[0] = 0
	if err := imported.Validate(); err != nil {
		t.Fatal("output aliases", err)
	}
	for _, format := range []string{"", `\\`, "é\x00", `${0}${1:+yes}${2:?yes:no}${3:-fallback}`} {
		if p, e := CompileSnippetTransform("", format, ""); e != nil || p.Validate() != nil {
			t.Fatal(format, e)
		}
	}
}
func TestCompiledSnippetTransformDiagnostics(t *testing.T) {
	for _, v := range []struct {
		source, format, options, part string
		offset                        int
	}{
		{"(", "", "", "source", 1}, {"a", "", "gg", "options", 1},
		{"a", "abc/def", "", "format", 3}, {"a", "a\\", "", "format", 2},
		{"a", "\xff", "", "format", 0}, {"a", `${1:/missing}`, "", "format", 5},
	} {
		p, e := CompileSnippetTransform(v.source, v.format, v.options)
		var detail *SnippetTransformCompileError
		if p != (CompiledSnippetTransform{}) || !errors.As(e, &detail) || detail.Part != v.part || detail.Offset != v.offset {
			t.Fatalf("%+v =>%v", v, e)
		}
	}
	if p, e := CompileSnippetTransform("a", strings.Repeat("x", MaxSnippetBody+1), ""); p != (CompiledSnippetTransform{}) || e == nil {
		t.Fatal("format cap")
	}
	if e := (CompiledSnippetTransform{}).Validate(); e == nil {
		t.Fatal("zero accepted")
	}
	program, e := CompileSnippetTransform("a", "x", "")
	if e != nil {
		t.Fatal(e)
	}
	r, f := program.Programs()
	for _, v := range []struct {
		r, f []byte
		part string
		at   int
	}{{nil, f, "regex", 0}, {r, nil, "replacement", 0}, {make([]byte, maxSnippetRegexProgramBytes+1), f, "regex", 40}, {r, make([]byte, MaxSnippetBody), "program", 0}} {
		p, e := NewCompiledSnippetTransform(v.r, v.f)
		var detail *SnippetTransformCompileError
		if p != (CompiledSnippetTransform{}) || !errors.As(e, &detail) || detail.Part != v.part || detail.Offset != v.at {
			t.Fatal(detail, e)
		}
	}
	r[0] = 0
	_, e = NewCompiledSnippetTransform(r, f)
	var detail *SnippetTransformCompileError
	if !errors.As(e, &detail) || errors.Unwrap(e) == nil || detail.Part != "regex" || detail.Offset != 0 {
		t.Fatal(e)
	}
}
func TestDecodeSnippetTransformsOwnedAndConcurrent(t *testing.T) {
	raw, e := simpleTransformPayload(t).encode()
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := DecodeSnippetTransforms(raw)
	if e != nil || len(decoded) != 1 || decoded[0].Field != 1 {
		t.Fatal(decoded, e)
	}
	for i := range raw {
		raw[i] = 0
	}
	if e := decoded[0].Transform.Validate(); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r, f := decoded[0].Transform.Programs()
				p, e := NewCompiledSnippetTransform(r, f)
				if e != nil || p.Validate() != nil {
					t.Error(e)
				}
			}
		}()
	}
	wg.Wait()
	if p, e := DecodeSnippetTransforms([]byte("SNTX")); p != nil || e == nil {
		t.Fatal("partial decode")
	}
	var payloadError *SnippetTransformPayloadError
	_, e = DecodeSnippetTransforms(nil)
	if !errors.As(e, &payloadError) || payloadError.Offset != 0 {
		t.Fatal(e)
	}
	// Exported bytes remain canonical for payload construction by other SDKs.
	r, f := decoded[0].Transform.Programs()
	if !bytes.Equal(r, []byte(decoded[0].Transform.regex)) || !bytes.Equal(f, []byte(decoded[0].Transform.replacement)) {
		t.Fatal("program copy")
	}
}
