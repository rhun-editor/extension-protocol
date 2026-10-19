package rhunext

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func snippetTransformIndentNativeVectors(t testing.TB) []byte {
	t.Helper()
	type vector struct {
		v    snippetTransformIndentVector
		work int
	}
	var cases []vector
	for _, v := range snippetTransformIndentVectors(t) {
		b := &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
		if _, e := normalizeSnippetTransform(v.Input, v.Prefix, v.Width, v.Spaces, b); e != nil {
			t.Fatal(e)
		}
		used := maxSnippetRegexMatchWork - b.remaining
		cases = append(cases, vector{v, maxSnippetRegexMatchWork}, vector{v, used}, vector{v, used - 1})
	}
	seed := uint32(41)
	alphabet := []rune("ab \t\r\n中𐐨\u2028\u00a0")
	for i := 0; i < 512; i++ {
		seed = seed*1664525 + 1013904223
		var input, prefix strings.Builder
		n := seed % 48
		for j := uint32(0); j < n; j++ {
			seed = seed*1664525 + 1013904223
			input.WriteRune(alphabet[int(seed)%len(alphabet)])
		}
		for j := 0; j < i%9; j++ {
			seed = seed*1664525 + 1013904223
			if seed&1 == 0 {
				prefix.WriteByte('\t')
			} else {
				prefix.WriteByte(' ')
			}
		}
		v := snippetTransformIndentVector{Input: input.String(), Prefix: prefix.String(), Width: 1 + i%16, Spaces: i%3 != 0}
		b := &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
		if _, e := normalizeSnippetTransform(v.Input, v.Prefix, v.Width, v.Spaces, b); e != nil {
			t.Fatal(e)
		}
		used := maxSnippetRegexMatchWork - b.remaining
		seed = seed*1664525 + 1013904223
		cases = append(cases, vector{v, maxSnippetRegexMatchWork}, vector{v, int(seed % uint32(used+1))})
	}
	for _, v := range []snippetTransformIndentVector{{Input: "a\xff", Width: 4}, {Input: "é\xf0\x90", Width: 4}, {Input: "\xed\xa0\x80", Width: 4}, {Input: "a", Prefix: "\n", Width: 4}, {Input: "a", Prefix: "\u00a0", Width: 4}, {Input: "a", Width: 0}, {Input: "a", Width: 17}, {Input: strings.Repeat("a", MaxSnippetBody+1), Width: 4}, {Prefix: strings.Repeat(" ", MaxSnippetBody+1), Width: 4}, {Input: "\n", Prefix: strings.Repeat("\t", MaxSnippetBody/16+1), Width: 16, Spaces: true}} {
		cases = append(cases, vector{v, maxSnippetRegexMatchWork})
	}
	var data, rows bytes.Buffer
	for i, tc := range cases {
		v := tc.v
		b := &snippetRegexMatchBudget{tc.work}
		out, e := normalizeSnippetTransform(v.Input, v.Prefix, v.Width, v.Spaces, b)
		code, offset := 0, -1
		if e != nil {
			var detail *snippetTransformIndentError
			if !errors.As(e, &detail) {
				t.Fatal(e)
			}
			offset = detail.Offset
			switch {
			case strings.Contains(detail.Message, "work limit"):
				code = 4
			case strings.Contains(detail.Message, "output byte limit"):
				code = 11
			case strings.Contains(detail.Message, "prefix") || strings.Contains(detail.Message, "width"):
				code = 10
			default:
				code = 2
			}
		}
		fmt.Fprintf(&data, ".Lti_input_%d:\n", i)
		snippetRegexEmitBytes(&data, []byte(v.Input))
		fmt.Fprintf(&data, ".Lti_prefix_%d:\n", i)
		snippetRegexEmitBytes(&data, []byte(v.Prefix))
		fmt.Fprintf(&data, ".Lti_output_%d:\n", i)
		snippetRegexEmitBytes(&data, []byte(out))
		spaces := 0
		if v.Spaces {
			spaces = 1
		}
		fmt.Fprintf(&data, ".p2align 3\n.Lti_desc_%d:\n.quad .Lti_input_%d,%d,.Lti_prefix_%d,%d,%d,%d\n", i, i, len(v.Input), i, len(v.Prefix), v.Width, spaces)
		fmt.Fprintf(&rows, ".quad .Lti_desc_%d,%d,.Lti_output_%d,%d,%d,%d,%d\n", i, tc.work, i, len(out), code, offset, b.remaining)
	}
	var result bytes.Buffer
	result.WriteString(".p2align 3\n.Lti_vectors:\n")
	result.Write(rows.Bytes())
	result.WriteString(".quad 0,0,0,0,0,0,0\n")
	result.Write(data.Bytes())
	return result.Bytes()
}
func TestSnippetTransformIndentNativeVectorExport(t *testing.T) {
	a := snippetTransformIndentNativeVectors(t)
	b := snippetTransformIndentNativeVectors(t)
	if !bytes.Equal(a, b) {
		t.Fatal("nondeterministic export")
	}
	if path := os.Getenv("RHUN_TRANSFORM_INDENT_ASM_OUT"); path != "" {
		if e := snippetRegexWriteExport(path, a); e != nil {
			t.Fatal(e)
		}
	}
}
