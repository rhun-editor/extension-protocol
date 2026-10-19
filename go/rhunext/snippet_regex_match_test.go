package rhunext

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type snippetRegexVector struct {
	Source, Flags, Input string
	Start                int
	Captures             [][]int
}

func regexVectors(t testing.TB) []snippetRegexVector {
	t.Helper()
	b, e := os.ReadFile("testdata/snippet-regex.json")
	if e != nil {
		t.Fatal(e)
	}
	var vectors []snippetRegexVector
	if e := json.Unmarshal(b, &vectors); e != nil {
		t.Fatal(e)
	}
	return vectors
}
func mustRegexMatcher(t *testing.T, p *snippetRegexProgram, input string) *snippetRegexMatcher {
	t.Helper()
	m, e := newSnippetRegexMatcher(p, input, nil)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func matchSpans(result *snippetRegexMatch) [][]int {
	if result == nil {
		return nil
	}
	out := make([][]int, len(result.captures))
	for i, c := range result.captures {
		if c.present {
			out[i] = []int{c.start, c.end}
		} else {
			out[i] = []int{-1, -1}
		}
	}
	return out
}
func TestSnippetRegexMatcherSharedVectors(t *testing.T) {
	for _, v := range regexVectors(t) {
		t.Run(v.Source+"/"+v.Flags+"/"+v.Input, func(t *testing.T) {
			p := mustCompileRegex(t, v.Source, v.Flags)
			m := mustRegexMatcher(t, p, v.Input)
			result, e := m.find(v.Start)
			if e != nil {
				t.Fatal(e)
			}
			if got := matchSpans(result); !reflect.DeepEqual(got, v.Captures) {
				t.Fatalf("got%v want%v", got, v.Captures)
			}
			if result != nil && (result.start != v.Captures[0][0] || result.end != v.Captures[0][1]) {
				t.Fatal("whole match")
			}
		})
	}
}
func TestSnippetRegexMatcherInputAndBudgets(t *testing.T) {
	p := mustCompileRegex(t, "a", "")
	for _, input := range []string{"\xff", strings.Repeat("a", MaxSnippetBody+1)} {
		if m, e := newSnippetRegexMatcher(p, input, nil); m != nil || e == nil {
			t.Fatal("accepted input")
		}
	}
	if m, e := newSnippetRegexMatcher(nil, "", nil); m != nil || e == nil {
		t.Fatal("nil program")
	}
	for _, work := range []int{-1, 0, 1} {
		if m, e := newSnippetRegexMatcher(p, "", &snippetRegexMatchBudget{work}); m != nil || e == nil {
			t.Fatal("accepted budget")
		}
	}
	// Constructor accepts the exact input byte cap when it has sufficient work.
	mustRegexMatcher(t, p, strings.Repeat("a", MaxSnippetBody))
	for _, start := range []int{-1, 1, 3} {
		m := mustRegexMatcher(t, p, "é")
		if result, e := m.find(start); result != nil || e == nil {
			t.Fatal("accepted boundary")
		}
		if result, e := m.find(0); result != nil || e == nil {
			t.Fatal("nonsticky error")
		}
	}
	budget := &snippetRegexMatchBudget{200}
	m, e := newSnippetRegexMatcher(p, strings.Repeat("b", 100), budget)
	if e != nil {
		t.Fatal(e)
	}
	if result, e := m.find(0); result != nil || e == nil || budget.remaining < 0 {
		t.Fatal("unbounded candidate scan")
	}
	budget = &snippetRegexMatchBudget{100000}
	m, e = newSnippetRegexMatcher(mustCompileRegex(t, "(a|aa)*b", ""), strings.Repeat("a", 32), budget)
	if e != nil {
		t.Fatal(e)
	}
	if result, e := m.find(0); result != nil || e == nil {
		t.Fatal("unbounded backtracking")
	}
	budget = &snippetRegexMatchBudget{4500}
	m, e = newSnippetRegexMatcher(mustCompileRegex(t, `(a{100})\1`, "iu"), strings.Repeat("a", 200), budget)
	if e != nil {
		t.Fatal(e)
	}
	if result, e := m.find(0); result != nil || e == nil {
		t.Fatal("unbounded backref")
	}
}
func TestSnippetRegexMatcherStorageCaps(t *testing.T) {
	cases := []struct {
		p              *snippetRegexProgram
		input, message string
	}{
		{mustCompileRegex(t, "a*", ""), strings.Repeat("a", 5000), "branch limit"},
		{mustCompileRegex(t, "(a){14000}", ""), strings.Repeat("a", 14000), "trail limit"},
		{&snippetRegexProgram{code: []snippetRegexInstruction{{rxLookCall, 0, 1, 0}, {rxMatch, 0, 0, 0}}}, "", "look call limit"},
		{&snippetRegexProgram{code: []snippetRegexInstruction{{rxSplit, 0, 1, 0}, {rxMatch, 0, 0, 0}}}, "", "branch limit"},
	}
	for _, tc := range cases {
		m := mustRegexMatcher(t, tc.p, tc.input)
		result, e := m.find(0)
		if result != nil || e == nil || !strings.Contains(e.Error(), tc.message) {
			t.Fatalf("got%v %v want%s", result, e, tc.message)
		}
	}
}
func TestSnippetRegexMatcherMalformedState(t *testing.T) {
	cases := []*snippetRegexProgram{
		{captures: 1, code: []snippetRegexInstruction{{rxSave, 2, 0, 0}, {rxMatch, 0, 0, 0}}},
		{captures: 1, code: []snippetRegexInstruction{{rxSave, 3, 0, 0}, {rxChar, 'a', 0, 0}, {rxSave, 2, 0, 0}, {rxMatch, 0, 0, 0}}},
		{code: []snippetRegexInstruction{{rxRepeatTest, 0, 1, 1}, {rxMatch, 0, 0, 0}}, repeats: []snippetRegexRepeat{{0, 0xffffffff, 0, 0}}},
		{code: []snippetRegexInstruction{{rxRepeatNext, 0, 1, 0}, {rxMatch, 0, 0, 0}}, repeats: []snippetRegexRepeat{{0, 0xffffffff, 0, 0}}},
		{captures: 1, code: []snippetRegexInstruction{{rxSave, 3, 0, 0}, {rxChar, 'a', 0, 0}, {rxSave, 2, 0, 0}, {rxReference, 1, 0, 0}, {rxMatch, 0, 0, 0}}},
	}
	for _, p := range cases {
		m := mustRegexMatcher(t, p, "a")
		if result, e := m.find(0); result != nil || e == nil {
			t.Fatal("accepted invalid state")
		}
	}
	p := &snippetRegexProgram{code: []snippetRegexInstruction{{rxJump, 0, 0, 0}}}
	m, e := newSnippetRegexMatcher(p, "", &snippetRegexMatchBudget{1000})
	if e != nil {
		t.Fatal(e)
	}
	if result, e := m.find(0); result != nil || e == nil {
		t.Fatal("cycle")
	}
}
func TestSnippetRegexMatcherReuseAndOwnership(t *testing.T) {
	p := mustCompileRegex(t, "(a)|(b)", "")
	encoded := mustRegexBytes(t, p)
	m := mustRegexMatcher(t, p, "ab")
	a, e := m.find(0)
	if e != nil {
		t.Fatal(e)
	}
	b, e := m.find(1)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(matchSpans(a), [][]int{{0, 1}, {0, 1}, {-1, -1}}) || !reflect.DeepEqual(matchSpans(b), [][]int{{1, 2}, {-1, -1}, {1, 2}}) {
		t.Fatalf("%v %v", matchSpans(a), matchSpans(b))
	}
	a.captures[0].end = 100
	if b.captures[0].end != 2 {
		t.Fatal("result alias")
	}
	if result, e := m.find(2); result != nil || e != nil {
		t.Fatal(result, e)
	}
	if !reflect.DeepEqual(encoded, mustRegexBytes(t, p)) {
		t.Fatal("program mutation")
	}
	// Shared budgets span matcher constructions, rather than refreshing per search.
	budget := &snippetRegexMatchBudget{1000}
	m, e = newSnippetRegexMatcher(p, "ab", budget)
	if e != nil {
		t.Fatal(e)
	}
	_, e = m.find(0)
	if e != nil {
		t.Fatal(e)
	}
	left := budget.remaining
	_, e = newSnippetRegexMatcher(p, "ab", budget)
	if e != nil || budget.remaining >= left {
		t.Fatal("budget not shared")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, e := newSnippetRegexMatcher(p, "ab", nil)
			if e != nil {
				t.Error(e)
				return
			}
			for j := 0; j < 20; j++ {
				r, e := q.find(j % 2)
				if e != nil || r == nil || r.start != j%2 {
					t.Errorf("reuse %v %v", r, e)
				}
			}
		}()
	}
	wg.Wait()
}
func FuzzSnippetRegexMatcher(f *testing.F) {
	for _, v := range regexVectors(f) {
		f.Add(v.Source, v.Flags, v.Input, uint32(v.Start))
	}
	f.Fuzz(func(t *testing.T, source, flags, input string, start uint32) {
		if len(input) > 65536 || !utf8.ValidString(input) {
			return
		}
		p, e := compileSnippetRegex(source, flags)
		if e != nil {
			return
		}
		m, e := newSnippetRegexMatcher(p, input, &snippetRegexMatchBudget{100000})
		if e != nil {
			return
		}
		r, e := m.find(int(uint64(start) % uint64(len(input)+1)))
		if e != nil {
			if r != nil {
				t.Fatal("partial result")
			}
			return
		}
		if r != nil {
			for _, c := range r.captures {
				if c.present && (!m.boundary(c.start) || !m.boundary(c.end) || c.start > c.end) {
					t.Fatal("invalid capture")
				}
			}
		}
	})
}
func FuzzSnippetRegexMatcherBytecode(f *testing.F) {
	for _, p := range []*snippetRegexProgram{regexProgramFixture(), {code: []snippetRegexInstruction{{rxJump, 0, 0, 0}}}} {
		b, e := p.encode()
		if e != nil {
			f.Fatal(e)
		}
		f.Add(b, "a")
	}
	f.Fuzz(func(t *testing.T, b []byte, input string) {
		if len(input) > 65536 || !utf8.ValidString(input) {
			return
		}
		p, e := decodeSnippetRegexProgram(b)
		if e != nil {
			return
		}
		m, e := newSnippetRegexMatcher(p, input, &snippetRegexMatchBudget{100000})
		if e != nil {
			return
		}
		r, e := m.find(0)
		if r != nil && e != nil {
			t.Fatal("partial result")
		}
		var diagnostic *snippetRegexMatchError
		if e != nil && !errors.As(e, &diagnostic) {
			t.Fatalf("runtime error type %T", e)
		}
	})
}
