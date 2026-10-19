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

func TestSnippetRegexNamesOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/snippet-regex-names.json")
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

func TestSnippetRegexNamesBudgetsAndPublicPreparation(t *testing.T) {
	source := `(?:(?<é>a)|(?<\u00e9>b))\k<é>`
	ast := mustRegex(t, source, "v")
	budget := maxSnippetRegexCompileWork
	want, err := compileSnippetRegexASTBudget(ast, &budget)
	if err != nil {
		t.Fatal(err)
	}
	used := maxSnippetRegexCompileWork - budget
	for _, work := range []int{0, 1, used - 1} {
		if partial, err := compileSnippetRegexAST(ast, work); err == nil || partial != nil {
			t.Fatal("accepted compile quota", work)
		}
	}
	if exact, err := compileSnippetRegexAST(ast, used); err != nil || !reflect.DeepEqual(exact, want) {
		t.Fatal("exact quota", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 16; j++ {
				got, err := compileSnippetRegexAST(ast, maxSnippetRegexCompileWork)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Error("concurrent compilation", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(ast.root.children[1].aliasGroups, []int{1, 2}) {
		t.Fatal("mutated aliases")
	}
	group := strings.TrimSuffix(strings.Repeat(`(?<x>a)|`, 64), "|")
	if _, err := compileSnippetRegex("(?:"+group+")\\k<x>", "u"); err != nil {
		t.Fatal("maximum captures", err)
	}
	if partial, err := compileSnippetRegex("(?:"+group+")"+strings.Repeat(`\k<x>`, 128), "u"); err == nil || partial != nil {
		t.Fatal("unbounded alias expansion")
	}
	program, err := PrepareSnippetTemplate(`${1:bb} ${1/(?:(?<é>a)|(?<\u00e9>b))\k<é>/${0:/upcase}/v} ${NAME/(?:(?<字>a)|(?<字>b))\k<字>/${0:/upcase}/u}`, SnippetContext{Variables: map[string]string{"NAME": "aa"}})
	if err != nil || program.Snippet.Text != "bb bb AA" || len(program.Transforms) != 1 {
		t.Fatal(program, err)
	}
	bad := `😀 ${NAME/(?<é>a)(?<\u00e9>b)/x/v}`
	partial, err := PrepareSnippetTemplate(bad, SnippetContext{})
	var detail *SnippetExpansionError
	if !errors.As(err, &detail) || detail.Offset != strings.Index(bad, `(?<\u00e9>`) || partial.Snippet.Text != "" {
		t.Fatal("source mapping", partial, err)
	}
	compiled, err := CompileSnippetTransform(`(?<é>a)(?<é>b)`, "x", "v")
	var compileDetail *SnippetTransformCompileError
	if !errors.As(err, &compileDetail) || compileDetail.Part != "source" || compileDetail.Offset != 8 || compiled.Validate() == nil {
		t.Fatal("partial public transform", err)
	}
}

func TestSnippetRegexNamesIdentifiersAndLimits(t *testing.T) {
	for _, flags := range []string{"", "u", "v"} {
		for _, zeros := range []int{0, 6, 65000} {
			source := `(?<\u{` + strings.Repeat("0", zeros) + `61}>a)\k<a>`
			ast := mustRegex(t, source, flags)
			if ast.names["a"] != 1 {
				t.Fatal("leading zero name", flags, zeros)
			}
		}
	}
	for _, source := range []string{`\u{00000061}`, `[\u{00000061}]`, `[\q{\u{00000061}}]`} {
		p := mustCompileRegex(t, source, "v")
		m := mustRegexMatcher(t, p, "a")
		match, err := m.find(0)
		if err != nil || match == nil || match.end != 1 {
			t.Fatal(source, match, err)
		}
	}
	for _, source := range []string{`(?<\u{000110000}>a)`, `(?<\u{ffffffffffffffffffffffff}>a)`} {
		if ast, err := parseSnippetRegex(source, "v"); err == nil || ast != nil {
			t.Fatal("accepted name overflow")
		}
	}
	for _, name := range []string{strings.Repeat("a", 64), strings.Repeat("é", 32), strings.Repeat("𐐀", 16), strings.Repeat(`\u0061`, 64)} {
		ast := mustRegex(t, "(?<"+name+">a)\\k<"+name+">", "")
		if ast.captures != 1 || len(ast.names) != 1 {
			t.Fatal(ast)
		}
	}
	for _, name := range []string{strings.Repeat("a", 65), strings.Repeat("é", 33), strings.Repeat("𐐀", 17), strings.Repeat(`\u0061`, 65)} {
		source := "(?<" + name + ">a)"
		_, err := parseSnippetRegex(source, "")
		var detail *snippetRegexError
		want := 3 + 64
		if strings.HasPrefix(name, `\u`) {
			want = 3 + 6*64
		}
		if !errors.As(err, &detail) || detail.Offset != want {
			t.Fatal(name, err, want)
		}
	}
	for _, tc := range []struct {
		source string
		offset int
	}{
		{`(?<😀>a)`, 3}, {`(?<a😀>a)`, 4}, {`(?<\x61>a)`, 3}, {`(?<é>a)(?<\u00e9>b)`, 8},
		{`(?<x>a)(?<x>b)`, 7}, {`(?<é>a)\k<missing>`, 8},
	} {
		_, err := parseSnippetRegex(tc.source, "v")
		var detail *snippetRegexError
		if !errors.As(err, &detail) || detail.Offset != tc.offset {
			t.Fatalf("%s: %v want%d", tc.source, err, tc.offset)
		}
	}
	// Names use exact decoded code points, without Unicode normalization.
	ast := mustRegex(t, `(?<é>a)(?<é>b)\k<\u00e9>\k<é>`, "u")
	if len(ast.names) != 2 || ast.names["é"] != 1 || ast.names["é"] != 2 {
		t.Fatal(ast.names)
	}
	alias := mustRegex(t, `(?:(?<é>a)|(?<\u00e9>b))\k<é>`, "v")
	ref := alias.root.children[1]
	if ref.group != 1 || !reflect.DeepEqual(ref.aliasGroups, []int{1, 2}) {
		t.Fatal(ref)
	}
}
