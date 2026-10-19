package rhunext

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func snippetRegexEmitBytes(out *bytes.Buffer, data []byte) {
	if len(data) == 0 {
		out.WriteString(".byte 0\n")
		return
	}
	uniform := len(data) > 16
	for _, b := range data {
		if b != data[0] {
			uniform = false
			break
		}
	}
	if uniform {
		fmt.Fprintf(out, ".zero %d,%d\n", len(data), data[0])
		return
	}
	for at, b := range data {
		if at%16 == 0 {
			out.WriteString(".byte ")
		} else {
			out.WriteByte(',')
		}
		fmt.Fprintf(out, "0x%02x", b)
		if at%16 == 15 || at+1 == len(data) {
			out.WriteByte('\n')
		}
	}
}
func snippetRegexNativeLimits() ([]byte, error) {
	cases := []struct {
		source, input, flags string
		p                    *snippetRegexProgram
		work, code           int
		message              string
	}{
		{source: "a*", input: strings.Repeat("a", 5000), work: maxSnippetRegexMatchWork, code: 5, message: "branch limit"},
		{source: "(a){14000}", input: strings.Repeat("a", 14000), work: maxSnippetRegexMatchWork, code: 6, message: "trail limit"},
		{p: &snippetRegexProgram{code: []snippetRegexInstruction{{rxLookCall, 0, 1, 0}, {rxMatch, 0, 0, 0}}}, work: maxSnippetRegexMatchWork, code: 7, message: "look call limit"},
		{p: &snippetRegexProgram{code: []snippetRegexInstruction{{rxSplit, 0, 1, 0}, {rxMatch, 0, 0, 0}}}, work: maxSnippetRegexMatchWork, code: 5, message: "branch limit"},
		{p: &snippetRegexProgram{code: []snippetRegexInstruction{{rxJump, 0, 0, 0}}}, work: 1000, code: 4, message: "work limit"},
		{p: &snippetRegexProgram{code: []snippetRegexInstruction{{rxRepeatTest, 0, 1, 1}, {rxMatch, 0, 0, 0}}, repeats: []snippetRegexRepeat{{0, 0xffffffff, 0, 0}}}, work: 10000, code: 8, message: "repeat state"},
		{p: &snippetRegexProgram{code: []snippetRegexInstruction{{rxRepeatNext, 0, 1, 0}, {rxMatch, 0, 0, 0}}, repeats: []snippetRegexRepeat{{0, 0xffffffff, 0, 0}}}, work: 10000, code: 8, message: "repeat state"},
		{p: &snippetRegexProgram{captures: 1, code: []snippetRegexInstruction{{rxSave, 2, 0, 0}, {rxMatch, 0, 0, 0}}}, work: 10000, code: 8, message: "capture"},
		{p: &snippetRegexProgram{captures: 1, code: []snippetRegexInstruction{{rxSave, 3, 0, 0}, {rxChar, 'a', 0, 0}, {rxSave, 2, 0, 0}, {rxMatch, 0, 0, 0}}}, input: "a", work: 10000, code: 8, message: "capture"},
		{source: "(a|aa)*b", input: strings.Repeat("a", 32), work: 100000, code: 4, message: "work limit"},
		{source: `(a{100})\1`, flags: "iu", input: strings.Repeat("a", 200), work: 4500, code: 4, message: "work limit"},
	}
	var out, rows bytes.Buffer
	for i, c := range cases {
		p := c.p
		var e error
		if p == nil {
			p, e = compileSnippetRegex(c.source, c.flags)
			if e != nil {
				return nil, e
			}
		}
		data, e := p.encode()
		if e != nil {
			return nil, e
		}
		m, e := newSnippetRegexMatcher(p, c.input, &snippetRegexMatchBudget{c.work})
		if e != nil {
			return nil, e
		}
		result, e := m.find(0)
		var diagnostic *snippetRegexMatchError
		if result != nil || !errors.As(e, &diagnostic) || !strings.Contains(e.Error(), c.message) {
			return nil, fmt.Errorf("limit%d: %v", i, e)
		}
		fmt.Fprintf(&out, ".p2align 3\n.Lrxlimit_program_%d:\n", i)
		snippetRegexEmitBytes(&out, data)
		fmt.Fprintf(&out, ".Lrxlimit_input_%d:\n", i)
		snippetRegexEmitBytes(&out, []byte(c.input))
		fmt.Fprintf(&rows, ".quad .Lrxlimit_program_%d,%d,.Lrxlimit_input_%d,%d,0,%d,%d,%d,%d\n", i, len(data), i, len(c.input), c.work, c.code, diagnostic.PC, m.budget.remaining)
	}
	out.WriteString(".p2align 3\n.Lrxlimit_vectors:\n")
	out.Write(rows.Bytes())
	out.WriteString(".quad 0,0,0,0,0,0,0,0,0\n")
	return out.Bytes(), nil
}
func snippetRegexWriteExport(target string, data []byte) error {
	file, e := os.CreateTemp(filepath.Dir(target), ".regex-vectors-*")
	if e != nil {
		return e
	}
	defer os.Remove(file.Name())
	if _, e = file.Write(data); e != nil {
		file.Close()
		return e
	}
	if e = file.Chmod(0644); e != nil {
		file.Close()
		return e
	}
	if e = file.Close(); e != nil {
		return e
	}
	return os.Rename(file.Name(), target)
}

// This test exports only when explicitly requested. Normal SDK and editor builds need no generator.
func snippetRegexNativeVectors(vectors []snippetRegexVector) ([]byte, error) {
	var out bytes.Buffer
	for i, v := range vectors {
		p, e := compileSnippetRegex(v.Source, v.Flags)
		if e != nil {
			return nil, e
		}
		program, e := p.encode()
		if e != nil {
			return nil, e
		}
		m, e := newSnippetRegexMatcher(p, v.Input, nil)
		if e != nil {
			return nil, e
		}
		result, e := m.find(v.Start)
		if e != nil {
			return nil, e
		}
		if (result != nil) != (v.Captures != nil) {
			return nil, fmt.Errorf("vector %d result mismatch", i)
		}
		if v.Captures != nil && len(v.Captures) != int(p.captures)+1 {
			return nil, fmt.Errorf("vector %d capture count", i)
		}
		fmt.Fprintf(&out, ".p2align 3\n.Lrxmatch_program_%d:\n", i)

		snippetRegexEmitBytes(&out, program)
		fmt.Fprintf(&out, ".Lrxmatch_input_%d:\n", i)
		snippetRegexEmitBytes(&out, []byte(v.Input))
		fmt.Fprintf(&out, ".p2align 3\n.Lrxmatch_captures_%d:\n", i)
		if v.Captures == nil {
			out.WriteString(".quad 0,0\n")
		} else {
			for _, c := range v.Captures {
				fmt.Fprintf(&out, ".quad %d,%d\n", c[0], c[1])
			}
		}
	}
	out.WriteString(".p2align 3\n.Lrxmatch_vectors:\n")
	for i, v := range vectors {
		p, e := compileSnippetRegex(v.Source, v.Flags)
		if e != nil {
			return nil, e
		}
		program, e := p.encode()
		if e != nil {
			return nil, e
		}
		m, e := newSnippetRegexMatcher(p, v.Input, nil)
		if e != nil {
			return nil, e
		}
		r, e := m.find(v.Start)
		if e != nil {
			return nil, e
		}
		matched := 0
		if r != nil {
			matched = 1
		}
		fmt.Fprintf(&out, ".quad .Lrxmatch_program_%d,%d,.Lrxmatch_input_%d,%d,%d,%d,%d,.Lrxmatch_captures_%d,%d\n", i, len(program), i, len(v.Input), v.Start, matched, p.captures+1, i, m.budget.remaining)
	}
	out.WriteString(".quad 0,0,0,0,0,0,0,0,0\n")
	return out.Bytes(), nil
}
func TestSnippetRegexNativeVectorExport(t *testing.T) {
	vectors := regexVectors(t)
	first, e := snippetRegexNativeVectors(vectors)
	if e != nil {
		t.Fatal(e)
	}
	second, e := snippetRegexNativeVectors(vectors)
	if e != nil || !bytes.Equal(first, second) {
		t.Fatal("nondeterministic vectors", e)
	}
	if target := os.Getenv("RHUN_REGEX_ASM_OUT"); target != "" {
		if e := snippetRegexWriteExport(target, first); e != nil {
			t.Fatal(e)
		}
	}
	limits, e := snippetRegexNativeLimits()
	if e != nil {
		t.Fatal(e)
	}
	again, e := snippetRegexNativeLimits()
	if e != nil || !bytes.Equal(limits, again) {
		t.Fatal("nondeterministic limits", e)
	}
	if target := os.Getenv("RHUN_REGEX_LIMITS_ASM_OUT"); target != "" {
		if e := snippetRegexWriteExport(target, limits); e != nil {
			t.Fatal(e)
		}
	}
}
