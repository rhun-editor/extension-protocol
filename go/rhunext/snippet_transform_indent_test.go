package rhunext

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type snippetTransformIndentVector struct {
	Input, Prefix, Output string
	Width                 int
	Spaces                bool
}

func snippetTransformIndentVectors(t testing.TB) []snippetTransformIndentVector {
	t.Helper()
	b, e := os.ReadFile("testdata/snippet-transform-indent.json")
	if e != nil {
		t.Fatal(e)
	}
	var vs []snippetTransformIndentVector
	if e = json.Unmarshal(b, &vs); e != nil {
		t.Fatal(e)
	}
	return vs
}
func TestSnippetTransformIndentVectors(t *testing.T) {
	for _, v := range snippetTransformIndentVectors(t) {
		got, e := normalizeSnippetTransform(v.Input, v.Prefix, v.Width, v.Spaces, nil)
		if e != nil || got != v.Output {
			t.Fatalf("%+v got%q %v", v, got, e)
		}
	}
}
func TestSnippetTransformIndentWorkAndErrors(t *testing.T) {
	for _, v := range []struct {
		input, prefix string
		work          int
	}{{"", "", 1}, {"a", "", 5}, {"\n", "\t", 11}, {"a\r\n", "  ", 18}} {
		budget := &snippetRegexMatchBudget{v.work}
		if _, e := normalizeSnippetTransform(v.input, v.prefix, 4, true, budget); e != nil || budget.remaining != 0 {
			t.Fatalf("%+v %v remaining%d", v, e, budget.remaining)
		}
		if out, e := normalizeSnippetTransform(v.input, v.prefix, 4, true, &snippetRegexMatchBudget{v.work - 1}); e == nil || out != "" {
			t.Fatal("partial quota output")
		}
	}
	for _, n := range []int{-1, maxSnippetRegexMatchWork + 1} {
		if out, e := normalizeSnippetTransform("a", "", 4, true, &snippetRegexMatchBudget{n}); out != "" || e == nil {
			t.Fatal("invalid quota")
		}
	}
	for _, width := range []int{-1, 0, 17} {
		if out, e := normalizeSnippetTransform("a", "", width, true, nil); out != "" || e == nil {
			t.Fatal("invalid width")
		}
	}
	for _, prefix := range []string{"x", "\n", "\r", "\u00a0", "\xff", strings.Repeat(" ", MaxSnippetBody+1)} {
		out, e := normalizeSnippetTransform("a", prefix, 4, true, nil)
		var detail *snippetTransformIndentError
		if out != "" || !errors.As(e, &detail) || detail.Offset != -1 {
			t.Fatal("prefix", out, e)
		}
	}
	for _, input := range []string{"a\xff", "é\xf0\x90"} {
		out, e := normalizeSnippetTransform(input, "", 4, true, nil)
		var detail *snippetTransformIndentError
		if out != "" || !errors.As(e, &detail) || detail.Offset != strings.IndexFunc(input, func(r rune) bool { return r == utf8.RuneError }) {
			t.Fatal("input", out, e)
		}
	}
	if out, e := normalizeSnippetTransform(strings.Repeat("a", MaxSnippetBody+1), "", 4, true, nil); out != "" || e == nil {
		t.Fatal("oversize")
	}
	// A large tab prefix expands beyond the output cap without allocating it.
	out, e := normalizeSnippetTransform("\n", strings.Repeat("\t", MaxSnippetBody/16+1), 16, true, nil)
	if out != "" || e == nil || !strings.Contains(e.Error(), "output byte limit") {
		t.Fatal("expanded cap", e)
	}
	budget := &snippetRegexMatchBudget{10}
	for i := 0; i < 2; i++ {
		if _, e := normalizeSnippetTransform("a", "", 4, true, budget); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := normalizeSnippetTransform("a", "", 4, true, budget); e == nil {
		t.Fatal("refreshed quota")
	}
	x := snippetTransformIndenter{budget: &snippetRegexMatchBudget{10}}
	x.fill(' ', 11)
	if x.err == nil || x.out.Len() != 0 || x.budget.remaining != 10 {
		t.Fatal("fill quota")
	}
	x = snippetTransformIndenter{budget: &snippetRegexMatchBudget{100}}
	x.fill(' ', MaxSnippetBody+1)
	if x.err == nil || x.out.Len() != 0 || x.budget.remaining != 100 {
		t.Fatal("fill cap")
	}
}
func TestSnippetTransformIndentConcurrent(t *testing.T) {
	vs := snippetTransformIndentVectors(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, v := range vs {
				if out, e := normalizeSnippetTransform(v.Input, v.Prefix, v.Width, v.Spaces, nil); e != nil || out != v.Output {
					t.Errorf("%q %v", out, e)
				}
			}
		}()
	}
	wg.Wait()
}
func FuzzSnippetTransformIndent(f *testing.F) {
	for _, v := range snippetTransformIndentVectors(f) {
		f.Add(v.Input, v.Prefix, uint8(v.Width), v.Spaces, uint32(100000))
	}
	f.Fuzz(func(t *testing.T, input, prefix string, width uint8, spaces bool, work uint32) {
		if len(input)+len(prefix) > 16384 {
			t.Skip()
		}
		n := int(work % 200001)
		budget := &snippetRegexMatchBudget{n}
		out, e := normalizeSnippetTransform(input, prefix, int(width), spaces, budget)
		if budget.remaining < 0 || budget.remaining > n {
			t.Fatal("budget")
		}
		if e != nil {
			if out != "" {
				t.Fatal("partial")
			}
			return
		}
		if !utf8.ValidString(out) || len(out) > MaxSnippetBody {
			t.Fatal("output")
		}
		again, e := normalizeSnippetTransform(input, prefix, int(width), spaces, &snippetRegexMatchBudget{n})
		if e != nil || out != again {
			t.Fatal("determinism")
		}
	})
}
