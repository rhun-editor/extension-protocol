package rhunext

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSnippetRegexLegacyOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/snippet-regex-legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Source, Flags, Input string
		Accepted             bool
		Captures             [][]int
	}
	if err = json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		p, err := compileSnippetRegex(row.Source, row.Flags)
		if !row.Accepted {
			if err == nil {
				t.Errorf("accepted %q/%s", row.Source, row.Flags)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q/%s: %v", row.Source, row.Flags, err)
			continue
		}
		m := mustRegexMatcher(t, p, row.Input)
		result, err := m.find(0)
		if err != nil {
			t.Fatal(err)
		}
		if got := matchSpans(result); !reflect.DeepEqual(got, row.Captures) {
			t.Errorf("%q/%s input%q: got%v want%v", row.Source, row.Flags, row.Input, got, row.Captures)
		}
	}
}

func TestSnippetRegexLegacyContracts(t *testing.T) {
	// These scalar/property extensions were already native noflag syntax.
	for _, tc := range []struct{ source, input string }{
		{`\p{L}`, "é"}, {`\P{L}`, "1"}, {`\u{61}`, "a"},
		{`\u{00000061}`, "a"}, {`\uD801\uDC00`, "𐐀"}, {".", "😀"},
	} {
		p := mustCompileRegex(t, tc.source, "")
		m := mustRegexMatcher(t, p, tc.input)
		match, err := m.find(0)
		if err != nil || match == nil || match.start != 0 || match.end != len(tc.input) {
			t.Fatal(tc, match, err)
		}
	}
	for _, source := range []string{`\uD800`, `\u{D800}`, `\u{110000}`, strings.Repeat("\\8", maxSnippetRegexNodes), "a{" + strings.Repeat("9", 100) + "}"} {
		if p, err := compileSnippetRegex(source, ""); p != nil || err == nil {
			t.Fatal("accepted invalid/resource", len(source), err)
		}
	}
	// Malformed very long braces are literals, rather than numeric overflow.
	p := mustCompileRegex(t, "a{"+strings.Repeat("0", 100)+"x}", "")
	if len(p.repeats) != 0 {
		t.Fatal("malformed quantifier")
	}
	for _, source := range []string{`(?<\u00GG>a)`, `(?<\u{G0}>a)`, `(?<x>a)\k<\u00GG>`, `(?<x>a)\k<\u{G0}>`} {
		if p, err := compileSnippetRegex(source, ""); p != nil || err == nil {
			t.Fatal("loose name escapes", source)
		}
	}
	ast := mustRegex(t, `(a)\18*[a-\d](?=(a))?a`, "")
	want, err := compileSnippetRegexAST(ast, maxSnippetRegexCompileWork)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				got, err := compileSnippetRegexAST(ast, maxSnippetRegexCompileWork)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Error("concurrent", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	program, err := PrepareSnippetTemplate(`${1:aa} ${1/(\141+)/${1:/upcase}/} ${NAME/(\xZZ)/${1:/upcase}/}`, SnippetContext{Variables: map[string]string{"NAME": "xZZ"}})
	if err != nil || program.Snippet.Text != "aa aa XZZ" || len(program.Transforms) != 1 {
		t.Fatal(program, err)
	}
	source := `😀 ${NAME/a{3,2}/x/}`
	partial, err := PrepareSnippetTemplate(source, SnippetContext{})
	var detail *SnippetExpansionError
	if !errors.As(err, &detail) || detail.Offset != strings.Index(source, "{3,2}") || partial.Snippet.Text != "" {
		t.Fatal("source offset", partial, err)
	}
}

func TestSnippetRegexLegacyHelpers(t *testing.T) {
	for _, tc := range []struct {
		source   string
		captures int
		named    bool
	}{
		{`\1(a)`, 1, false}, {`\12[(](a)(?<é>b)(?:c)(?=d)(?<=e)(?!f)(?<!g)`, 2, true},
		{`\(\)[()](a)\[b\](c)`, 2, false}, {`[\](](a)[(](?<x>b)`, 2, true},
		{strings.Repeat("()", 100), 65, false},
	} {
		p := snippetRegexParser{s: tc.source}
		p.legacyCensus()
		if p.legacyCaptures != tc.captures || p.legacyNamed != tc.named {
			t.Fatal(tc, p.legacyCaptures, p.legacyNamed)
		}
		p.s = ""
		p.legacyCensus()
		if p.legacyCaptures != tc.captures || p.legacyNamed != tc.named {
			t.Fatal("uncached census")
		}
	}
	for _, text := range []string{"{0}", "{1,}", "{1,3}", "{01}", "{" + strings.Repeat("9", 64000) + "}"} {
		p := snippetRegexParser{s: text}
		if !p.bracedQuantifier(0) {
			t.Fatal(text)
		}
	}
	for _, text := range []string{"{", "{}", "{,1}", "{1,2,3}", "{1", "{1,x}", "{x}", "{" + strings.Repeat("9", 64000) + "x}"} {
		p := snippetRegexParser{s: text}
		if p.bracedQuantifier(0) {
			t.Fatal(text)
		}
	}
	for _, tc := range []struct {
		digits      string
		inClass     bool
		kind        snippetRegexKind
		value, next int
	}{
		{"18*", false, rxLiteral, 1, 1}, {"18(a)", false, rxLiteral, 1, 1},
		{"1(a)", false, rxBackref, 1, 1}, {"1(a)", true, rxLiteral, 1, 1},
		{"01(a)", false, rxLiteral, 1, 2}, {"08(a)", false, rxLiteral, 0, 1},
		{"377", false, rxLiteral, 255, 3}, {"400", false, rxLiteral, 32, 2},
		{"777", false, rxLiteral, 63, 2}, {"89", false, rxLiteral, 8 + '0', 1},
	} {
		p := snippetRegexParser{s: tc.digits}
		n := p.legacyDecimal(0, tc.inClass)
		value := int(n.lo)
		if n.kind == rxBackref {
			value = n.group
		}
		if n.kind != tc.kind || value != tc.value || p.pos != tc.next {
			t.Fatal(tc, n, p.pos)
		}
	}
}
