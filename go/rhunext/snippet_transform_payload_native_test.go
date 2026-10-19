package rhunext

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
)

func snippetTransformPayloadValidationCases(t testing.TB) [][]byte {
	t.Helper()
	one, e := simpleTransformPayload(t).encode()
	if e != nil {
		t.Fatal(e)
	}
	empty, e := (&snippetTransformPayload{}).encode()
	if e != nil {
		t.Fatal(e)
	}
	p := simpleTransformPayload(t)
	b := p.bindings[0]
	b.field = 3
	p.bindings = append(p.bindings, b)
	two, e := p.encode()
	if e != nil {
		t.Fatal(e)
	}
	mirror := bytes.Clone(one)
	binary.LittleEndian.PutUint32(mirror[32:], 2)
	final := bytes.Clone(one)
	binary.LittleEndian.PutUint32(final[32:], 4)
	far := bytes.Clone(one)
	binary.LittleEndian.PutUint32(far[32:], 5)
	cases := [][]byte{empty, one, two, mirror, final, far}
	for i := 0; i < len(one); i++ {
		cases = append(cases, bytes.Clone(one[:i]))
	}
	for _, base := range [][]byte{one, two} {
		for i := range base {
			bad := bytes.Clone(base)
			bad[i] ^= 0xff
			cases = append(cases, bad)
		}
	}
	tail := append(bytes.Clone(one), 0)
	binary.LittleEndian.PutUint32(tail[16:], uint32(len(tail)))
	cases = append(cases, tail, make([]byte, MaxSnippetBody+1))
	maximum := simpleTransformPayload(t)
	first := maximum.bindings[0]
	maximum.bindings = nil
	for field := 1; field <= MaxSnippetFields; field++ {
		b := first
		b.field = field
		maximum.bindings = append(maximum.bindings, b)
	}
	maxData, e := maximum.encode()
	if e != nil {
		t.Fatal(e)
	}
	cases = append(cases, maxData)
	return append(cases, snippetRegexScopedPayloadCases(t)...)
}

func snippetTransformPayloadNativeVectors(t testing.TB) []byte {
	t.Helper()
	cases := snippetTransformPayloadValidationCases(t)
	var data, rows bytes.Buffer
	maximumWritten := false
	for i, b := range cases {
		decoded, e := decodeSnippetTransformPayload(b)
		valid, offset := 1, 0
		if e != nil {
			valid = 0
			var detail *snippetTransformPayloadError
			if !errors.As(e, &detail) {
				t.Fatal(e)
			}
			offset = detail.Offset
		}
		if e == nil && len(decoded.bindings) == MaxSnippetFields {
			if maximumWritten {
				t.Fatal("ambiguous maximum transform fixture")
			}
			maximumWritten = true
			data.WriteString(".Lstp_max:\n")
		}
		fmt.Fprintf(&data, ".Lstp_data_%d:\n", i)
		snippetRegexEmitBytes(&data, b)
		fmt.Fprintf(&rows, ".quad .Lstp_data_%d,%d,%d,%d\n", i, len(b), valid, offset)
	}
	if !maximumWritten {
		t.Fatal("missing maximum transform fixture")
	}
	var result bytes.Buffer
	result.WriteString(".p2align 3\n.Lstp_vectors:\n")
	result.Write(rows.Bytes())
	result.WriteString(".quad 0,0,0,0\n")
	result.Write(data.Bytes())
	return result.Bytes()
}
func TestSnippetTransformPayloadNativeVectorExport(t *testing.T) {
	a := snippetTransformPayloadNativeVectors(t)
	b := snippetTransformPayloadNativeVectors(t)
	if !bytes.Equal(a, b) {
		t.Fatal("nondeterministic export")
	}
	if path := os.Getenv("RHUN_TRANSFORM_PAYLOAD_ASM_OUT"); path != "" {
		if e := snippetRegexWriteExport(path, a); e != nil {
			t.Fatal(e)
		}
	}
}
