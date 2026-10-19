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

type snippetTransformVector struct{ Source, Flags, Format, Input, Output string }

func snippetTransformVectors(t testing.TB) []snippetTransformVector {
	t.Helper()
	b, e := os.ReadFile("testdata/snippet-transform.json")
	if e != nil {
		t.Fatal(e)
	}
	var vs []snippetTransformVector
	if e = json.Unmarshal(b, &vs); e != nil {
		t.Fatal(e)
	}
	return vs
}
func snippetTransformFormat(t testing.TB, s string) *snippetReplacementProgram {
	t.Helper()
	tree, e := ParseSnippetTemplate("${1/a/" + s + "/}")
	if e != nil {
		t.Fatal(e)
	}
	if len(tree.nodes) != 1 || tree.nodes[0].transform == nil {
		t.Fatal("missing transform")
	}
	p, e := compileSnippetReplacement(tree.nodes[0].transform.format)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestSnippetTransformVectors(t *testing.T) {
	for _, v := range snippetTransformVectors(t) {
		t.Run(v.Source+"/"+v.Format, func(t *testing.T) {
			p := mustCompileRegex(t, v.Source, v.Flags)
			f := snippetTransformFormat(t, v.Format)
			got, e := executeSnippetTransform(p, f, v.Input, nil)
			if e != nil || got != v.Output {
				t.Fatalf("input%q got%q want%q error%v", v.Input, got, v.Output, e)
			}
		})
	}
}
func TestSnippetTransformWork(t *testing.T) {
	for _, v := range []struct {
		source, flags, format, input, output string
		work                                 int
	}{{"a", "", "x", "a", "x", 150}, {"", "g", "x", "", "x", 128}, {"a", "", "", "b", "b", 128}} {
		p := mustCompileRegex(t, v.source, v.flags)
		f := snippetTransformFormat(t, v.format)
		budget := &snippetRegexMatchBudget{v.work}
		out, e := executeSnippetTransform(p, f, v.input, budget)
		if e != nil || out != v.output || budget.remaining != 0 {
			t.Fatalf("%q got%q %v remaining%d", v.source, out, e, budget.remaining)
		}
		if out, e = executeSnippetTransform(p, f, v.input, &snippetRegexMatchBudget{v.work - 1}); e == nil || out != "" {
			t.Fatal("partial quota output")
		}
	}
	p := mustCompileRegex(t, "a", "")
	f := snippetTransformFormat(t, "x")
	budget := &snippetRegexMatchBudget{300}
	for i := 0; i < 2; i++ {
		if _, e := executeSnippetTransform(p, f, "a", budget); e != nil {
			t.Fatal(e)
		}
	}
	if budget.remaining != 0 {
		t.Fatal("shared budget")
	}
	if out, e := executeSnippetTransform(p, f, "a", budget); e == nil || out != "" {
		t.Fatal("refreshed budget")
	}
	// The base and aggregate text reservations fail at the same boundaries used
	// by native encoded programs, before any UTF-8 validation or matching.
	for _, v := range []struct{ work, want int }{{55, 55}, {56, 0}} {
		budget := &snippetRegexMatchBudget{v.work}
		out, e := executeSnippetTransform(p, f, "a", budget)
		if out != "" || e == nil || budget.remaining != v.want {
			t.Fatal("format reserve", out, e, budget.remaining)
		}
	}
}
func TestSnippetTransformErrors(t *testing.T) {
	p := mustCompileRegex(t, "a", "")
	f := snippetTransformFormat(t, "x")
	check := func(out string, e error, stage string, record int) {
		t.Helper()
		var detail *snippetTransformError
		if out != "" || !errors.As(e, &detail) || detail.Stage != stage || detail.Record != record || errors.Unwrap(e) != detail.Cause {
			t.Fatalf("%q %v", out, e)
		}
	}
	out, e := executeSnippetTransform(nil, f, "a", nil)
	check(out, e, "arguments", -1)
	out, e = executeSnippetTransform(p, nil, "a", nil)
	check(out, e, "arguments", -1)
	for _, n := range []int{-1, maxSnippetRegexMatchWork + 1} {
		out, e = executeSnippetTransform(p, f, "a", &snippetRegexMatchBudget{n})
		check(out, e, "arguments", -1)
	}
	bad := &snippetReplacementProgram{code: []snippetReplacementInstruction{{op: 99}}}
	out, e = executeSnippetTransform(p, bad, "a", nil)
	check(out, e, "format", 0)
	var pe *snippetReplacementProgramError
	if !errors.As(e, &pe) {
		t.Fatal("lost program error")
	}
	bad = &snippetReplacementProgram{code: []snippetReplacementInstruction{{op: replacementLiteral, a: "\xff"}}}
	out, e = executeSnippetTransform(p, bad, "a", nil)
	check(out, e, "format", 0)
	bad = &snippetReplacementProgram{code: make([]snippetReplacementInstruction, maxSnippetReplacementRecords+1)}
	out, e = executeSnippetTransform(p, bad, "a", nil)
	check(out, e, "format", -1)
	bad = &snippetReplacementProgram{code: []snippetReplacementInstruction{{op: replacementLiteral, a: strings.Repeat("x", MaxSnippetBody)}}}
	out, e = executeSnippetTransform(p, bad, "a", nil)
	check(out, e, "format", 0)
	out, e = executeSnippetTransform(p, f, "\xff", nil)
	check(out, e, "match", -1)
	var me *snippetRegexMatchError
	if !errors.As(e, &me) {
		t.Fatal("lost matcher error")
	}
	out, e = executeSnippetTransform(&snippetRegexProgram{}, f, "a", nil)
	check(out, e, "match", -1)
	out, e = executeSnippetTransform(p, f, strings.Repeat("a", MaxSnippetBody+1), nil)
	check(out, e, "match", -1)
	// Full format validation, matcher initialization/match and record dispatch
	// leave too little work for the case operation's validation reservation.
	f = snippetTransformFormat(t, "${0:/upcase}")
	budget := &snippetRegexMatchBudget{149}
	out, e = executeSnippetTransform(p, f, "a", budget)
	check(out, e, "case", 0)
	var ce *snippetCaseError
	if !errors.As(e, &ce) {
		t.Fatal("lost case error")
	}
	// Two matches can grow output past its cap even when each format is valid.
	p = mustCompileRegex(t, "a", "g")
	f = &snippetReplacementProgram{code: []snippetReplacementInstruction{{op: replacementLiteral, a: strings.Repeat("x", MaxSnippetBody-replacementHeader-replacementRecord)}}}
	out, e = executeSnippetTransform(p, f, "aa", nil)
	check(out, e, "output", 0)
}
func TestSnippetTransformImmutableConcurrent(t *testing.T) {
	p := mustCompileRegex(t, "(a)|(b)", "g")
	f := snippetTransformFormat(t, "${1:+A}${2:+B}")
	beforeP, e := p.encode()
	if e != nil {
		t.Fatal(e)
	}
	beforeF, e := f.encode()
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				got, e := executeSnippetTransform(p, f, "abba", nil)
				if e != nil || got != "ABBA" {
					t.Errorf("%q %v", got, e)
				}
			}
		}()
	}
	wg.Wait()
	afterP, _ := p.encode()
	afterF, _ := f.encode()
	if !reflect.DeepEqual(beforeP, afterP) || !reflect.DeepEqual(beforeF, afterF) {
		t.Fatal("mutated program")
	}
}
func FuzzSnippetTransform(f *testing.F) {
	for _, v := range snippetTransformVectors(f) {
		f.Add(v.Source, v.Flags, v.Format, v.Input, uint32(100000))
	}
	f.Fuzz(func(t *testing.T, source, flags, format, input string, work uint32) {
		if len(source)+len(format)+len(input) > 16384 {
			t.Skip()
		}
		p, e := compileSnippetRegex(source, flags)
		if e != nil {
			return
		}
		tree, e := ParseSnippetTemplate("${1/a/" + format + "/}")
		if e != nil || len(tree.nodes) != 1 || tree.nodes[0].transform == nil {
			return
		}
		r, e := compileSnippetReplacement(tree.nodes[0].transform.format)
		if e != nil {
			return
		}
		quota := int(work % 200001)
		budget := &snippetRegexMatchBudget{quota}
		out, e := executeSnippetTransform(p, r, input, budget)
		if budget.remaining < 0 || budget.remaining > quota {
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
		again, e := executeSnippetTransform(p, r, input, &snippetRegexMatchBudget{quota})
		if e != nil || again != out {
			t.Fatal("determinism")
		}
	})
}
