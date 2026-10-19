package rhunext

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// These opt-in exports never run external commands or write repository files.
func snippetCaseNativeVectors(t testing.TB) []byte {
	t.Helper()
	var data, rows bytes.Buffer
	type vector struct {
		snippetCaseVector
		budget int
	}
	var cases []vector
	for _, v := range snippetCaseVectors(t) {
		budget := &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
		if _, e := snippetCaseConvert(v.Input, v.Op, budget); e != nil {
			t.Fatal(e)
		}
		used := maxSnippetRegexMatchWork - budget.remaining
		cases = append(cases, vector{v, maxSnippetRegexMatchWork}, vector{v, used}, vector{v, used - 1})
	}
	// A deterministic mixed-script corpus also compares exact processing work.
	alphabet := []rune("ABCaéÉΣΑσς中ǅ12!_- \u0345\u0085\ufeff𐐨𐐀")
	seed := uint32(19)
	for i := 0; i < 512; i++ {
		seed = seed*1664525 + 1013904223
		n := seed % 24
		var s strings.Builder
		for j := uint32(0); j < n; j++ {
			seed = seed*1664525 + 1013904223
			s.WriteRune(alphabet[int(seed)%len(alphabet)])
		}
		for op := uint32(2); op <= 8; op++ {
			v := snippetCaseVector{Op: op, Input: s.String()}
			cases = append(cases, vector{v, maxSnippetRegexMatchWork})
			if i < 128 {
				budget := &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
				if _, e := snippetCaseConvert(v.Input, v.Op, budget); e != nil {
					t.Fatal(e)
				}
				used := maxSnippetRegexMatchWork - budget.remaining
				seed = seed*1664525 + 1013904223
				cases = append(cases, vector{v, int(seed % uint32(used+1))})
			}
		}
	}
	for _, input := range []string{"a\xff", "é\xf0\x90", "\xed\xa0\x80", "\xc0\xaf"} {
		cases = append(cases, vector{snippetCaseVector{Op: 2, Input: input}, maxSnippetRegexMatchWork})
	}
	for _, op := range []uint32{0, 1, 9, ^uint32(0)} {
		cases = append(cases, vector{snippetCaseVector{Op: op, Input: "a"}, maxSnippetRegexMatchWork})
	}
	cases = append(cases, vector{snippetCaseVector{Op: 2, Input: strings.Repeat("x", MaxSnippetBody+1)}, maxSnippetRegexMatchWork})
	for i, v := range cases {
		budget := &snippetRegexMatchBudget{v.budget}
		out, e := snippetCaseConvert(v.Input, v.Op, budget)
		code, offset, success := 0, -1, 1
		if e != nil {
			success = 0
			var detail *snippetCaseError
			if !errors.As(e, &detail) {
				t.Fatal(e)
			}
			offset = detail.Offset
			switch {
			case strings.Contains(detail.Message, "operation"):
				code = 10
			case strings.Contains(detail.Message, "work"):
				code = 4
			case strings.Contains(detail.Message, "output"):
				code = 11
			default:
				code = 2
			}
		}
		fmt.Fprintf(&data, ".Lsc_input_%d:\n", i)
		snippetRegexEmitBytes(&data, []byte(v.Input))
		fmt.Fprintf(&data, ".Lsc_output_%d:\n", i)
		snippetRegexEmitBytes(&data, []byte(out))
		fmt.Fprintf(&rows, ".quad .Lsc_input_%d,%d,%d,%d,.Lsc_output_%d,%d,%d,%d,%d,%d\n", i, len(v.Input), v.Op, v.budget, i, len(out), code, offset, budget.remaining, success)
	}
	data.WriteString(".p2align 3\n.Lsc_vectors:\n")
	data.Write(rows.Bytes())
	data.WriteString(".quad 0,0,0,0,0,0,0,0,0,0\n")
	return data.Bytes()
}
func TestSnippetCaseNativeVectorExport(t *testing.T) {
	a := snippetCaseNativeVectors(t)
	b := snippetCaseNativeVectors(t)
	if !bytes.Equal(a, b) {
		t.Fatal("nondeterministic export")
	}
	if path := os.Getenv("RHUN_CASE_ASM_OUT"); path != "" {
		if e := snippetRegexWriteExport(path, a); e != nil {
			t.Fatal(e)
		}
	}
}
