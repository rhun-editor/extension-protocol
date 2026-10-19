package rhunext

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func snippetTransformNativeVectors(t testing.TB) []byte {
	t.Helper()
	type vector struct {
		v      snippetTransformVector
		p      *snippetReplacementProgram
		budget int
	}
	var cases []vector
	for _, v := range snippetTransformVectors(t) {
		p, e := compileSnippetRegex(v.Source, v.Flags)
		if e != nil {
			t.Fatal(e)
		}
		f := snippetTransformFormat(t, v.Format)
		budget := &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
		if _, e = executeSnippetTransform(p, f, v.Input, budget); e != nil {
			t.Fatal(e)
		}
		used := maxSnippetRegexMatchWork - budget.remaining
		cases = append(cases, vector{v, nil, maxSnippetRegexMatchWork}, vector{v, nil, used}, vector{v, nil, used - 1})
	}
	seed := uint32(31)
	alphabet := []rune("abABΣ中𐐨 !")
	for i := 0; i < 64; i++ {
		seed = seed*1664525 + 1013904223
		var input strings.Builder
		for j := uint32(0); j < seed%17; j++ {
			seed = seed*1664525 + 1013904223
			input.WriteRune(alphabet[int(seed)%len(alphabet)])
		}
		for _, source := range []string{"a", "(a)|(b)", "a*", "(?=a)|$", "(.)"} {
			for _, format := range []string{"<$0>", "${1:?yes:no}${2:-fallback}", "${0:/upcase}", "${0:/kebabcase}"} {
				v := snippetTransformVector{Source: source, Flags: "g", Format: format, Input: input.String()}
				cases = append(cases, vector{v, nil, maxSnippetRegexMatchWork})
				p, e := compileSnippetRegex(source, "g")
				if e != nil {
					t.Fatal(e)
				}
				f := snippetTransformFormat(t, format)
				budget := &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
				if _, e = executeSnippetTransform(p, f, v.Input, budget); e != nil {
					t.Fatal(e)
				}
				used := maxSnippetRegexMatchWork - budget.remaining
				seed = seed*1664525 + 1013904223
				cases = append(cases, vector{v, nil, int(seed % uint32(used+1))})
			}
		}
	}
	for _, input := range []string{"a\xff", "\xed\xa0\x80", strings.Repeat("x", MaxSnippetBody+1)} {
		cases = append(cases, vector{snippetTransformVector{Source: "a", Format: "x", Input: input}, nil, maxSnippetRegexMatchWork})
	}
	cases = append(cases, vector{v: snippetTransformVector{Source: "a", Flags: "g", Input: "aa"}, p: &snippetReplacementProgram{code: []snippetReplacementInstruction{{op: replacementLiteral, a: strings.Repeat("x", MaxSnippetBody-replacementHeader-replacementRecord)}}}, budget: maxSnippetRegexMatchWork})
	cases = append(cases, vector{v: snippetTransformVector{Source: "a+", Input: strings.Repeat("a", 81)}, p: &snippetReplacementProgram{code: []snippetReplacementInstruction{{op: replacementLiteral, a: strings.Repeat("x", MaxSnippetBody-replacementHeader-2*replacementRecord)}, {op: replacementUpper}}}, budget: maxSnippetRegexMatchWork})
	var data, rows bytes.Buffer
	stages := map[string]int{"arguments": 1, "format": 2, "match": 3, "case": 4, "output": 5}
	for i, v := range cases {
		regex, e := compileSnippetRegex(v.v.Source, v.v.Flags)
		if e != nil {
			t.Fatal(e)
		}
		format := v.p
		if format == nil {
			format = snippetTransformFormat(t, v.v.Format)
		}
		r, e := regex.encode()
		if e != nil {
			t.Fatal(e)
		}
		f, e := format.encode()
		if e != nil {
			t.Fatal(e)
		}
		budget := &snippetRegexMatchBudget{v.budget}
		out, e := executeSnippetTransform(regex, format, v.v.Input, budget)
		code, stage, record := 0, 0, -1
		if e != nil {
			var detail *snippetTransformError
			if !errors.As(e, &detail) {
				t.Fatal(e)
			}
			stage = stages[detail.Stage]
			record = detail.Record
			message := detail.Cause.Error()
			switch {
			case strings.Contains(message, "work limit"):
				code = 4
			case strings.Contains(message, "output byte limit"):
				code = 11
			case strings.Contains(message, "branch limit"):
				code = 5
			case strings.Contains(message, "trail limit"):
				code = 6
			case strings.Contains(message, "look call limit"):
				code = 7
			case strings.Contains(message, "UTF-8") || strings.Contains(message, "input byte limit"):
				code = 2
			default:
				t.Fatalf("unmapped diagnostic: %v", e)
			}
		}
		fmt.Fprintf(&data, ".Ltf_regex_%d:\n", i)
		snippetRegexEmitBytes(&data, r)
		fmt.Fprintf(&data, ".Ltf_format_%d:\n", i)
		base := replacementHeader + len(format.code)*replacementRecord
		snippetRegexEmitBytes(&data, f[:base])
		if len(f) > base {
			snippetRegexEmitBytes(&data, f[base:])
		}
		fmt.Fprintf(&data, ".Ltf_input_%d:\n", i)
		snippetRegexEmitBytes(&data, []byte(v.v.Input))
		fmt.Fprintf(&data, ".Ltf_output_%d:\n", i)
		snippetRegexEmitBytes(&data, []byte(out))
		fmt.Fprintf(&data, ".p2align 3\n.Ltf_desc_%d:\n.quad .Ltf_regex_%d,%d,.Ltf_format_%d,%d,.Ltf_input_%d,%d\n", i, i, len(r), i, len(f), i, len(v.v.Input))
		fmt.Fprintf(&rows, ".quad .Ltf_desc_%d,%d,.Ltf_output_%d,%d,%d,%d,%d,%d\n", i, v.budget, i, len(out), code, stage, record, budget.remaining)
	}
	var result bytes.Buffer
	result.WriteString(".p2align 3\n.Ltf_vectors:\n")
	result.Write(rows.Bytes())
	result.WriteString(".quad 0,0,0,0,0,0,0,0\n")
	result.Write(data.Bytes())
	return result.Bytes()
}
func TestSnippetTransformNativeVectorExport(t *testing.T) {
	a := snippetTransformNativeVectors(t)
	b := snippetTransformNativeVectors(t)
	if !bytes.Equal(a, b) {
		t.Fatal("nondeterministic export")
	}
	if path := os.Getenv("RHUN_TRANSFORM_ASM_OUT"); path != "" {
		if e := snippetRegexWriteExport(path, a); e != nil {
			t.Fatal(e)
		}
	}
}
