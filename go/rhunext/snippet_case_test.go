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

type snippetCaseVector struct {
	Op            uint32
	Input, Output string
}

func snippetCaseVectors(t testing.TB) []snippetCaseVector {
	t.Helper()
	b, e := os.ReadFile("testdata/snippet-case.json")
	if e != nil {
		t.Fatal(e)
	}
	var v []snippetCaseVector
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func TestSnippetCaseVectors(t *testing.T) {
	for _, v := range snippetCaseVectors(t) {
		t.Run(v.Input, func(t *testing.T) {
			got, e := snippetCaseConvert(v.Input, v.Op, nil)
			if e != nil || got != v.Output {
				t.Fatalf("op%d got%q want%q error%v", v.Op, got, v.Output, e)
			}
		})
	}
}
func TestSnippetCaseBudgets(t *testing.T) {
	// Reserve validation, decoding, lookup and output before each action.
	for _, tc := range []struct {
		input string
		op    uint32
		work  int
	}{{"", 2, 1}, {"a", 2, 24}, {"ß", 2, 27}, {"éclair", 4, 42}} {
		budget := &snippetRegexMatchBudget{tc.work}
		out, e := snippetCaseConvert(tc.input, tc.op, budget)
		if e != nil || budget.remaining != 0 {
			t.Fatalf("%q %q %v remaining%d", tc.input, out, e, budget.remaining)
		}
		short := &snippetRegexMatchBudget{tc.work - 1}
		if out, e = snippetCaseConvert(tc.input, tc.op, short); e == nil || out != "" {
			t.Fatal("partial result on exhausted work")
		}
	}
	for _, n := range []int{-1, maxSnippetRegexMatchWork + 1} {
		if out, e := snippetCaseConvert("a", 2, &snippetRegexMatchBudget{n}); e == nil || out != "" {
			t.Fatal("invalid budget")
		}
	}
	for _, op := range []uint32{0, 1, 9, ^uint32(0)} {
		if out, e := snippetCaseConvert("a", op, nil); e == nil || out != "" {
			t.Fatal("invalid operation")
		}
	}
	for _, input := range []string{"a\xff", "é\xf0\x90", "\xed\xa0\x80"} {
		out, e := snippetCaseConvert(input, 2, nil)
		var detail *snippetCaseError
		if out != "" || !errors.As(e, &detail) || detail.Offset != strings.IndexFunc(input, func(r rune) bool { return r == utf8.RuneError }) {
			t.Fatalf("%q %v", out, e)
		}
	}
	if out, e := snippetCaseConvert(strings.Repeat("a", MaxSnippetBody+1), 2, nil); e == nil || out != "" {
		t.Fatal("oversize input")
	}
	// Builder guards also apply to snake intermediates and expanding case maps.
	c := snippetCaseConverter{budget: &snippetRegexMatchBudget{100}}
	var b strings.Builder
	c.append(&b, "ab", 1)
	if c.err == nil || b.Len() != 0 || c.budget.remaining != 100 {
		t.Fatal("append cap")
	}
	c = snippetCaseConverter{budget: &snippetRegexMatchBudget{100}}
	b.WriteString("a")
	c.append(&b, "bc", 2)
	if c.err == nil || b.String() != "a" {
		t.Fatal("append overflow")
	}
	c = snippetCaseConverter{budget: &snippetRegexMatchBudget{1}}
	b.Reset()
	c.append(&b, "ab", 100)
	if c.err == nil || b.Len() != 0 || c.budget.remaining != 1 {
		t.Fatal("append quota")
	}
	// An expanded mapping cannot publish a partial scalar beyond the final cap.
	c = snippetCaseConverter{budget: &snippetRegexMatchBudget{100}}
	b.Reset()
	b.WriteString(strings.Repeat("x", MaxSnippetBody-1))
	c.mapped(&b, 'ß', "ß", true)
	if c.err == nil || b.Len() != MaxSnippetBody-1 {
		t.Fatal("expanded final cap")
	}
	var detail *snippetCaseError
	if !errors.As(c.err, &detail) || detail.Offset != -1 {
		t.Fatal("intermediate error offset")
	}
	c = snippetCaseConverter{budget: &snippetRegexMatchBudget{100}}
	b.Reset()
	b.WriteString(strings.Repeat("x", 2*MaxSnippetBody))
	c.append(&b, "_", 2*MaxSnippetBody)
	if c.err == nil || b.Len() != 2*MaxSnippetBody {
		t.Fatal("snake intermediate cap")
	}
	budget := &snippetRegexMatchBudget{48}
	for i := 0; i < 2; i++ {
		if _, e := snippetCaseConvert("a", 2, budget); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := snippetCaseConvert("a", 2, budget); e == nil {
		t.Fatal("refreshed shared budget")
	}
}
func TestSnippetCaseLinearAndConcurrent(t *testing.T) {
	for _, v := range []snippetCaseVector{{3, "ΑΣ" + strings.Repeat("\u0345", 10000), "ας" + strings.Repeat("\u0345", 10000)}, {8, strings.Repeat("A", 10000) + "!", strings.Repeat("a", 10000) + "!"}} {
		budget := &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
		out, e := snippetCaseConvert(v.Input, v.Op, budget)
		if e != nil || out != v.Output || maxSnippetRegexMatchWork-budget.remaining > 150*len(v.Input)+100 {
			t.Fatalf("linear conversion: %v", e)
		}
	}
	vectors := snippetCaseVectors(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, v := range vectors {
				if got, e := snippetCaseConvert(v.Input, v.Op, nil); e != nil || got != v.Output {
					t.Errorf("concurrent %q %v", got, e)
				}
			}
		}()
	}
	wg.Wait()
}
func FuzzSnippetCase(f *testing.F) {
	for _, v := range snippetCaseVectors(f) {
		f.Add(v.Input, uint8(v.Op), uint32(100000))
	}
	f.Fuzz(func(t *testing.T, input string, op uint8, work uint32) {
		if len(input) > 65536 {
			t.Skip()
		}
		budget := &snippetRegexMatchBudget{int(work % 100001)}
		out, e := snippetCaseConvert(input, uint32(op), budget)
		if budget.remaining < 0 || budget.remaining > int(work%100001) {
			t.Fatal("budget")
		}
		if e != nil {
			if out != "" {
				t.Fatal("partial output")
			}
			return
		}
		if !utf8.ValidString(out) || len(out) > MaxSnippetBody {
			t.Fatal("invalid output")
		}
		again, e := snippetCaseConvert(input, uint32(op), &snippetRegexMatchBudget{int(work % 100001)})
		if e != nil || again != out {
			t.Fatal("nondeterministic")
		}
	})
}
