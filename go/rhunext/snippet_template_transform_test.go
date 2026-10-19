package rhunext

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func templateTransformHarness(t testing.TB, source string) (*snippetExpander, *stNode) {
	t.Helper()
	tree, err := ParseSnippetTemplate(source)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tree.nodes {
		if tree.nodes[i].kind == stTransform {
			return &snippetExpander{source: source, compileWork: maxSnippetRegexMatchWork}, &tree.nodes[i]
		}
	}
	t.Fatal("no transform")
	return nil, nil
}
func TestSnippetTemplateTransformSourceMapping(t *testing.T) {
	source := "😀 ${1/a\\/b\\x41é/x/ig}"
	e, n := templateTransformHarness(t, source)
	raw := source[n.transform.regexFrom:n.transform.regexTo]
	if raw != "a\\/b\\x41é" || n.transform.regex != "a/b\\x41é" || source[n.transform.optionsFrom:n.to-1] != "ig" {
		t.Fatal(n.transform)
	}
	// Every decoded byte has a hand-derived original position, including the
	// collapsed slash, preserved escape bytes and UTF-8 byte boundaries.
	base := n.transform.regexFrom
	for decoded, original := range []int{0, 1, 3, 4, 5, 6, 7, 8, 9, 10} {
		if got := snippetTransformSourceOffset(source, n.transform, decoded); got != base+original {
			t.Fatal(decoded, got, base+original)
		}
	}
	if _, err := e.compiledTransform(n); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		source, marker string
		eof            bool
	}{
		{"😀 ${1/a\\/b[/x/}", "/x/}", true},
		{"é ${1/a\\/b/x/ii}", "ii}", false},
		{"prefix ${1/a\\/b\\xG0/x/u}", "G0", false},
	} {
		e, n := templateTransformHarness(t, tc.source)
		_, err := e.compiledTransform(n)
		var diagnostic *SnippetExpansionError
		if !errors.As(err, &diagnostic) {
			t.Fatal(tc, err)
		}
		want := strings.Index(tc.source, tc.marker)
		if tc.marker == "ii}" {
			want++
		}
		if diagnostic.Offset != want {
			t.Fatalf("%q: %+v want %d", tc.source, diagnostic, want)
		}
	}
}
func TestSnippetTemplateTransformCompilationCacheAndLimits(t *testing.T) {
	e, n := templateTransformHarness(t, "${1/a/x/}")
	first, err := e.compiledTransform(n)
	if err != nil {
		t.Fatal(err)
	}
	work, bytes := e.compileWork, e.transformBytes
	second, err := e.compiledTransform(n)
	if err != nil || first != second || work != e.compileWork || bytes != e.transformBytes {
		t.Fatal("compile cache", err)
	}
	// A separate reachable transform cannot reset the preparation's budget.
	next := *n
	transform := *n.transform
	next.transform = &transform
	e.compileWork = 1
	if _, err = e.compiledTransform(&next); err == nil {
		t.Fatal("shared compile budget reset")
	}
	e, n = templateTransformHarness(t, "${1/a/x/}")
	e.transformBytes = MaxSnippetBody - 100
	if _, err = e.compiledTransform(n); err == nil || len(e.compiled) != 0 || e.transformBytes != MaxSnippetBody-100 {
		t.Fatal("cache byte preflight", err)
	}
	ast, err := parseSnippetRegex("a", "")
	if err != nil {
		t.Fatal(err)
	}
	budget := 100
	if _, err := compileSnippetRegexASTBudget(ast, &budget); err != nil || budget >= 100 {
		t.Fatal("compile budget not returned", budget, err)
	}
}

func TestSnippetTemplateTransformActualVSCodeFixtures(t *testing.T) {
	var cases []struct {
		Source, Text, Selected string
		Variables              map[string]string
		Snapshot, Keep         bool
		Fields                 []struct {
			Group, Start, End int
			Transform         bool
		}
	}
	raw, err := os.ReadFile("testdata/snippet-template-transform.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Source+fmt.Sprint(tc.Snapshot, tc.Keep, tc.Selected), func(t *testing.T) {
			var p SnippetProgram
			var err error
			if tc.Snapshot {
				s := formatSnapshot()
				s.SelectedText = tc.Selected
				p, err = PrepareSnippetTemplateWithSnapshot(tc.Source, s, SnippetVariableOptions{Variables: tc.Variables, KeepWhitespace: tc.Keep})
			} else {
				p, err = PrepareSnippetTemplate(tc.Source, SnippetContext{Variables: tc.Variables})
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Snippet.Text != tc.Text || len(p.Snippet.Fields) != len(tc.Fields) {
				t.Fatalf("got %q %+v, want %q %+v", p.Snippet.Text, p.Snippet.Fields, tc.Text, tc.Fields)
			}
			count := 0
			for i, f := range tc.Fields {
				got := p.Snippet.Fields[i]
				if got.Group != f.Group || got.Start != f.Start || got.End != f.End {
					t.Fatalf("field%d: %+v want %+v", i, got, f)
				}
				if f.Transform {
					count++
					found := false
					for _, binding := range p.Transforms {
						if binding.Field == i+1 {
							found = true
						}
					}
					if !found {
						t.Fatal("missing binding", i)
					}
				}
			}
			if len(p.Transforms) != count {
				t.Fatal("extra binding", p.Transforms)
			}
		})
	}
}
func TestSnippetTemplateTransformReachabilityAndNoPartial(t *testing.T) {
	context := SnippetContext{Variables: map[string]string{"IGNORE": "value"}}
	for _, source := range []string{"${IGNORE:${MISSING/[/bad/}}", "${IGNORE:${1:${MISSING/[/bad/}}}"} {
		p, err := PrepareSnippetTemplate(source, context)
		if err != nil || p.Snippet.Text != "value" {
			t.Fatal(p, err)
		}
	}
	for _, source := range []string{"${IGNORE:${1:${MISSING/[/bad/}}} $1", "${NAME/[/bad/}", "${0/a/x/}"} {
		p, err := PrepareSnippetTemplate(source, context)
		if err == nil || !reflect.DeepEqual(p, SnippetProgram{}) {
			t.Fatal("partial or accepted", p, err)
		}
	}
}
func TestSnippetTemplateTransformConcurrentPrepare(t *testing.T) {
	source := "${1:${NAME/(.*)/${1:/upcase}/}} ${1/(.*)/${1:/downcase}/}"
	tree, err := ParseSnippetTemplate(source)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 10; j++ {
				p, err := tree.Prepare(SnippetContext{Variables: map[string]string{"NAME": "É"}})
				if err != nil || p.Snippet.Text != "É É" || len(p.Transforms) != 1 || p.Transforms[0].Field != 2 {
					t.Error(p, err)
					return
				}
			}
		}()
	}
	group.Wait()
	if tree.Source() != source {
		t.Fatal("mutated source")
	}
}

func TestSnippetTemplateTransformsSharedExecutionBudgetAndFieldLimit(t *testing.T) {
	input := strings.Repeat("a", 4096)
	compiled, err := CompileSnippetTransform("a", "x", "g")
	if err != nil {
		t.Fatal(err)
	}
	rawRegex, rawFormat := compiled.Programs()
	regex, err := decodeSnippetRegexProgram(rawRegex)
	if err != nil {
		t.Fatal(err)
	}
	format, err := decodeSnippetReplacement(rawFormat)
	if err != nil {
		t.Fatal(err)
	}
	budget := snippetRegexMatchBudget{maxSnippetRegexMatchWork}
	if _, err = executeSnippetTransform(regex, format, input, &budget); err != nil {
		t.Fatal(err)
	}
	used := maxSnippetRegexMatchWork - budget.remaining
	count := maxSnippetRegexMatchWork/used + 1
	source := strings.Repeat("${NAME/a/x/g}", count)
	context := SnippetContext{Variables: map[string]string{"NAME": input}}
	for _, snapshot := range []bool{false, true} {
		var p SnippetProgram
		if snapshot {
			p, err = PrepareSnippetTemplateWithSnapshot(source, formatSnapshot(), SnippetVariableOptions{Variables: context.Variables})
		} else {
			p, err = PrepareSnippetTemplate(source, context)
		}
		if err == nil || !strings.Contains(err.Error(), "work limit") || !reflect.DeepEqual(p, SnippetProgram{}) {
			t.Fatal("aggregate execution budget", count, p, err)
		}
		short := strings.Repeat("${NAME/a/x/g}", max(1, count/2))
		if snapshot {
			p, err = PrepareSnippetTemplateWithSnapshot(short, formatSnapshot(), SnippetVariableOptions{Variables: context.Variables})
		} else {
			p, err = PrepareSnippetTemplate(short, context)
		}
		if err != nil {
			t.Fatal("individually valid batch", err)
		}
	}
	for _, count := range []int{512, 513} {
		for _, snapshot := range []bool{false, true} {
			source := strings.Repeat("${1/a/x/}", count)
			var p SnippetProgram
			if snapshot {
				p, err = PrepareSnippetTemplateWithSnapshot(source, formatSnapshot(), SnippetVariableOptions{})
			} else {
				p, err = PrepareSnippetTemplate(source, SnippetContext{})
			}
			if (err == nil) != (count == 512) {
				t.Fatal(count, snapshot, err)
			}
			if err == nil && len(p.Transforms) != count {
				t.Fatal("binding count", len(p.Transforms))
			}
			if err != nil && !reflect.DeepEqual(p, SnippetProgram{}) {
				t.Fatal("partial program")
			}
		}
	}
}

func TestSnippetRawDefaultVariableTransformRunsOnce(t *testing.T) {
	input := strings.Repeat("a", 786432)
	context := SnippetContext{Variables: map[string]string{"NAME": input}}
	direct, err := PrepareSnippetTemplate("${NAME/z/x/}", context)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"${1:${NAME/z/x/}}", "${1:${NAME/z/x/}} $1"} {
		p, err := PrepareSnippetTemplate(source, context)
		if err != nil {
			t.Fatal(source, err)
		}
		want := direct.Snippet.Text
		if strings.HasSuffix(source, " $1") {
			want += " " + want
		}
		if p.Snippet.Text != want {
			t.Fatal("cached transform changed text")
		}
	}
}
