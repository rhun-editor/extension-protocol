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

func TestSnippetRegexUnicodeSetsOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/snippet-regex-sets.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Source, Flags, Input string
		Start                int
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
		result, err := m.find(row.Start)
		if err != nil {
			t.Fatal(err)
		}
		if got := matchSpans(result); !reflect.DeepEqual(got, row.Captures) {
			t.Errorf("%q/%s input%q: got%v want%v", row.Source, row.Flags, row.Input, got, row.Captures)
		}
	}
}

func TestSnippetRegexUnicodeSetsLimitsAndOwnership(t *testing.T) {
	source := `[\q{ab|abc|a|}&&[\q{ab|abc|a|}]]`
	ast := mustRegex(t, source, "iv")
	work := maxSnippetRegexCompileWork
	want, err := compileSnippetRegexASTBudget(ast, &work)
	if err != nil {
		t.Fatal(err)
	}
	used := maxSnippetRegexCompileWork - work
	for _, amount := range []int{0, 1, used - 1} {
		left := amount
		if partial, err := compileSnippetRegexASTBudget(ast, &left); err == nil || partial != nil || left < 0 {
			t.Fatalf("accepted budget %d: %v", amount, err)
		}
	}
	left := used
	if exact, err := compileSnippetRegexASTBudget(ast, &left); err != nil || left != 0 || !reflect.DeepEqual(exact, want) {
		t.Fatal("exact work", left, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				got, err := compileSnippetRegexAST(ast, maxSnippetRegexCompileWork)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Error("concurrent compile", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	before := append([]string(nil), snippetEmojiProperties["RGI_Emoji"]...)
	p := mustCompileRegex(t, `\p{RGI_Emoji}`, "iv")
	for i := range p.ranges {
		p.ranges[i] = snippetRuneRange{}
	}
	if !reflect.DeepEqual(before, snippetEmojiProperties["RGI_Emoji"]) {
		t.Fatal("mutated data")
	}
	for _, source := range []string{`[\q{` + strings.Repeat("a", 5000) + `}]`,
		"[" + strings.Repeat(`\p{Lowercase_Letter}`, 128) + "]"} {
		if partial, err := compileSnippetRegex(source, "iv"); err == nil || partial != nil {
			t.Fatal("accepted excessive set compilation")
		}
	}
	program, err := PrepareSnippetTemplate(`${1:abc} ${1/([\q{ab|abc|a|}])/${1:/upcase}/v} ${NAME/[\q{ab|abc|a|}]/${0:/upcase}/v}`, SnippetContext{Variables: map[string]string{"NAME": "abc"}})
	if err != nil || program.Snippet.Text != "abc abc ABC" || len(program.Transforms) != 1 {
		t.Fatal(program, err)
	}
	for _, source := range []string{`😀 ${NAME/[a--]/x/v}`, `😀 ${NAME/a/x/uv}`} {
		partial, err := PrepareSnippetTemplate(source, SnippetContext{})
		var detail *SnippetExpansionError
		if !errors.As(err, &detail) || partial.Snippet.Text != "" || detail.Offset < 5 {
			t.Fatal("source error", partial, err)
		}
	}
}

func TestSnippetRegexUnicodeSetsTemplateSlash(t *testing.T) {
	for _, source := range []string{`${NAME/[\/]/x/v}`, `${NAME/[\q{\/|a}]/x/v}`} {
		program, err := PrepareSnippetTemplate(source, SnippetContext{Variables: map[string]string{"NAME": "/"}})
		if err != nil || program.Snippet.Text != "x" {
			t.Fatal(source, program, err)
		}
	}
	source := `😀 ${NAME/[\/&&]/x/v}`
	_, err := PrepareSnippetTemplate(source, SnippetContext{})
	var detail *SnippetExpansionError
	if !errors.As(err, &detail) || detail.Offset != strings.Index(source, "]") {
		t.Fatal("v raw-source offset", err)
	}
	program, err := PrepareSnippetTemplate(`${1:/} ${1/([\q{\/|ab}])/${1:/upcase}/v}`, SnippetContext{})
	if err != nil || program.Snippet.Text != "/ /" || len(program.Transforms) != 1 {
		t.Fatal(program, err)
	}
}

func TestSnippetRegexUnicodeSetsParse(t *testing.T) {
	for _, source := range []string{`[]`, `[^]`, `[a-z]`, `[a&&b&&c]`, `[a--b--c]`,
		`[[[a-c][d-f]]&&[^e]]`, `[^^]`, `[\p{RGI_Emoji}--\q{ab|}]`, `[^\q{a|b}]`,
		`[^\q{ab}&&a]`, `[^a--\q{ab}]`, `[\q{a\|b|\{\}|\&\!\#\%\,\:\;\<\=\>\@\x60\~}]`} {
		if _, err := parseSnippetRegex(source, "v"); err != nil {
			t.Errorf("%s: %v", source, err)
		}
	}
	for _, source := range []string{`[a&&]`, `[&&a]`, `[a&&bc]`, `[ab&&c]`, `[a--b&&c]`,
		`[a&&b--c]`, `[a-b-c]`, `[-]`, `[a-]`, `[z-a]`, `[a-\d]`, `[\d-a]`,
		`[()]`, `[/]`, `[!!]`, `[##]`, `[$$]`, `[%%]`, `[**]`, `[++ ]`, `[,,]`,
		`[..]`, `[::]`, `[;;]`, `[<<]`, `[==]`, `[>>]`, `[??]`, `[@@]`, `[a^^]`,
		"[``]", `[~~]`, `[^\q{}]`, `[^\q{ab}--\q{ab}]`, `[^\p{RGI_Emoji}]`,
		`[\P{RGI_Emoji}]`, `[\q{\d}]`, `[\q{\p{L}}]`, `[\q{[}]`, `[\q{ab]`} {
		if ast, err := parseSnippetRegex(source, "v"); err == nil || ast != nil {
			t.Errorf("accepted %s", source)
		}
	}
	for _, flags := range []string{"uv", "vu", "vv"} {
		_, err := parseSnippetRegex("", flags)
		var detail *snippetRegexError
		if !errors.As(err, &detail) || detail.Part != "options" || detail.Offset != 1 {
			t.Fatalf("%s: %v", flags, err)
		}
	}
	for _, source := range []string{strings.Repeat("[", 33) + "a" + strings.Repeat("]", 33),
		strings.Repeat("[", 32) + "a" + strings.Repeat("]", 32) + "*",
		`[\q{` + strings.Repeat("a|", maxSnippetRegexNodes) + "}]"} {
		if _, err := parseSnippetRegex(source, "v"); err == nil {
			t.Fatal("accepted excessive set", len(source))
		}
	}
}

func TestSnippetRegexUnicodeSetsCompileMatch(t *testing.T) {
	for _, tc := range []struct {
		source, flags, input string
		span                 []int
	}{
		{`[[a-z]&&[^aeiou]]+`, "v", "boat", []int{0, 1}},
		{`[[a-z]--[aeiou]]+`, "v", "eat", []int{2, 3}},
		{`[\q{ab|abc|a|}]c`, "v", "abc", []int{0, 3}},
		{`[\q{ab|abc|a|}]`, "v", "abcd", []int{0, 3}},
		{`[\q{ab|}]`, "v", "x", []int{0, 0}},
		{`[\q{Ab|Kſ}]`, "iv", "kS", []int{0, 2}},
		{`[\q{ab|cd}&&\q{cd|ef}]`, "v", "abcd", []int{2, 4}},
		{`[\q{ab|cd}--\q{cd|ef}]`, "v", "cdab", []int{2, 4}},
		{`[^\q{ab}&&a]`, "v", "a", []int{0, 1}},
		{`[\P{Lowercase_Letter}]`, "iv", "aA", nil},
		{`[\P{Lowercase_Letter}]`, "iu", "aA", []int{0, 1}},
		// ECMAScript folds the operand before v complements. Node 26's ASCII
		// property shortcut omits this closure, so these are spec-derived.
		{`\p{ASCII}`, "iv", "K", []int{0, 3}},
		{`\p{ASCII}`, "iv", "ſ", []int{0, 2}},
		{`\P{ASCII}`, "iv", "Kſ", nil},
		// Singleton q strings fold too, independently of engine shortcuts.
		{`[\q{a}]`, "iv", "A", []int{0, 1}},
		{`[\q{K}]`, "iv", "K", []int{0, 3}},
		{`[^\q{ſ}]`, "iv", "Sſs", nil},
		{`(?<=[\q{ab|abc}])c`, "v", "abc", []int{2, 3}},
		{`\p{Emoji_Keycap_Sequence}`, "v", "x1\ufe0f\u20e3", []int{1, 8}},
		{`[\p{RGI_Emoji_ZWJ_Sequence}]`, "v", "🏳️‍🌈", []int{0, 14}},
	} {
		t.Run(tc.source+"/"+tc.flags, func(t *testing.T) {
			p := mustCompileRegex(t, tc.source, tc.flags)
			if p.flags&128 != 0 || p.flags&uint32(rxUnicode) == 0 {
				t.Fatal("wire flags", p.flags)
			}
			m := mustRegexMatcher(t, p, tc.input)
			got, err := m.find(0)
			if err != nil {
				t.Fatal(err)
			}
			var span []int
			if got != nil {
				span = []int{got.start, got.end}
			}
			if !reflect.DeepEqual(span, tc.span) {
				t.Fatalf("got %v want %v", span, tc.span)
			}
		})
	}
}

func TestSnippetRegexUnicodeSetsWholeRGI(t *testing.T) {
	for _, source := range []string{`\p{RGI_Emoji}`, `(?<=\p{RGI_Emoji})`, `[\p{RGI_Emoji}]`} {
		for _, flags := range []string{"v", "iv"} {
			p := mustCompileRegex(t, source, flags)
			t.Logf("%s: code %d classes %d ranges %d", source, len(p.code), len(p.classes), len(p.ranges))
			other := mustCompileRegex(t, source, flags)
			if !reflect.DeepEqual(p, other) {
				t.Fatal("nondeterministic compilation")
			}
			for _, word := range snippetEmojiProperties["RGI_Emoji"] {
				m := mustRegexMatcher(t, p, word)
				start := 0
				if strings.HasPrefix(source, "(?<=") {
					start = len(word)
				}
				match, err := m.find(start)
				if err != nil || match == nil {
					t.Fatalf("%s %q: %v", source, word, err)
				}
				if strings.HasPrefix(source, "(?<=") {
					if match.start != len(word) || match.end != len(word) {
						t.Fatalf("backward %q: %v", word, match)
					}
				} else if match.start != 0 || match.end != len(word) {
					t.Fatalf("forward %q: %v", word, match)
				}
			}
		}
	}
}
