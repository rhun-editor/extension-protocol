package rhunext

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func mustRegex(t *testing.T, source, flags string) *snippetRegex {
	t.Helper()
	r, err := parseSnippetRegex(source, flags)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestSnippetRegexASTGoldens(t *testing.T) {
	cases := []struct {
		source   string
		root     *snippetRegexNode
		captures int
	}{
		{"", &snippetRegexNode{kind: rxEmpty}, 0},
		{"a|bc", &snippetRegexNode{kind: rxAlternative, from: 0, to: 4, children: []*snippetRegexNode{
			{kind: rxLiteral, from: 0, to: 1, lo: 'a'},
			{kind: rxSequence, from: 2, to: 4, children: []*snippetRegexNode{{kind: rxLiteral, from: 2, to: 3, lo: 'b'}, {kind: rxLiteral, from: 3, to: 4, lo: 'c'}}}}}, 0},
		{"(a){2,4}?", &snippetRegexNode{kind: rxRepeat, from: 0, to: 9, min: 2, max: 4, lazy: true, children: []*snippetRegexNode{
			{kind: rxCapture, from: 0, to: 3, group: 1, children: []*snippetRegexNode{{kind: rxLiteral, from: 1, to: 2, lo: 'a'}}}}}, 1},
		{`\uD83D\uDE00`, &snippetRegexNode{kind: rxLiteral, from: 0, to: 12, lo: '😀'}, 0},
		{`[a-c\d]`, &snippetRegexNode{kind: rxClass, from: 0, to: 7, children: []*snippetRegexNode{{kind: rxRange, from: 1, to: 4, lo: 'a', hi: 'c'}, {kind: rxShorthand, from: 4, to: 6, text: "d"}}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			r := mustRegex(t, tc.source, "")
			if !reflect.DeepEqual(r.root, tc.root) || r.captures != tc.captures || r.source != tc.source {
				t.Fatalf("root %#v != %#v; captures%d", r.root, tc.root, r.captures)
			}
		})
	}
}
func TestSnippetRegexAssertionsGroupsReferences(t *testing.T) {
	r := mustRegex(t, `(?<word>[a-z]+)(?:\s|_)*\k<word>`, "gimsuyd")
	if r.flags != rxGlobal|rxIgnoreCase|rxMultiline|rxDotAll|rxUnicode|rxSticky|rxIndices || r.captures != 1 || r.names["word"] != 1 {
		t.Fatalf("%+v", r)
	}
	c := r.root.children
	if len(c) != 3 || c[0].kind != rxCapture || c[0].group != 1 || c[1].kind != rxRepeat || c[2].kind != rxBackref || c[2].group != 1 {
		t.Fatalf("%+v", c)
	}
	for _, tc := range []struct {
		source             string
		negative, backward bool
	}{
		{"(?=a)", false, false}, {"(?!a)", true, false}, {"(?<=a)", false, true}, {"(?<!a)", true, true},
	} {
		n := mustRegex(t, tc.source, "").root
		if n.kind != rxLook || n.negative != tc.negative || n.backward != tc.backward {
			t.Fatalf("%s: %+v", tc.source, n)
		}
	}
	r = mustRegex(t, `\2(\k<later>)(?<later>x)`, "")
	if r.root.children[0].group != 2 || r.root.children[1].children[0].group != 2 {
		t.Fatal("forward reference resolution")
	}
	r = mustRegex(t, `^\b.\B$`, "")
	for i, want := range []string{"start", "word", "", "word", "end"} {
		if r.root.children[i].text != want {
			t.Fatalf("assertion%d", i)
		}
	}
	if !r.root.children[3].negative || r.root.children[2].kind != rxAny {
		t.Fatal("assertion flags")
	}
	// Quantified groups may contain assertions, but direct assertions are not quantified.
	mustRegex(t, "(?:^)*", "")
}
func TestSnippetRegexClassAndEscapeGoldens(t *testing.T) {
	for _, source := range []string{`[]`, `[^]`, `[-a]`, `[a-]`, `[--a]`, `[a-b-c]`, `[\]\-\\]`, `[\b\0\n]`, `[\p{L}\P{Script_Extensions=Greek}\W]`} {
		mustRegex(t, source, "")
	}
	r := mustRegex(t, `[\p{L}\P{Script_Extensions=Greek}\W]`, "")
	if r.root.children[0].kind != rxProperty || r.root.children[0].text != "L" || !r.root.children[1].negative || r.root.children[1].text != "Script_Extensions=Greek" || !r.root.children[2].negative {
		t.Fatal("class predicates")
	}
	for _, tc := range []struct {
		source string
		want   rune
	}{
		{`\n`, '\n'}, {`\r`, '\r'}, {`\t`, '\t'}, {`\f`, '\f'}, {`\v`, '\v'}, {`\0`, 0}, {"\x00", 0}, {"é", 'é'}, {"�", '�'}, {`\cA`, 1}, {`\cz`, 26}, {`\x2f`, '/'}, {`\u0061`, 'a'}, {`\u{10FFFF}`, utf8.MaxRune}, {`\u{1f600}`, '😀'},
	} {
		n := mustRegex(t, tc.source, "").root
		if n.kind != rxLiteral || n.lo != tc.want {
			t.Fatalf("%q: %+v", tc.source, n)
		}
	}
	for _, source := range []string{`\^`, `\$`, `\\`, `\.`, `\*`, `\+`, `\?`, `\(`, `\)`, `\[`, `\]`, `\{`, `\}`, `\|`, `\/`, `\-`} {
		n := mustRegex(t, source, "").root
		if n.lo != rune(source[1]) {
			t.Fatal(source)
		}
	}
	for _, tc := range []struct {
		source   string
		min, max int
		lazy     bool
	}{
		{"a*", 0, -1, false}, {"a+?", 1, -1, true}, {"a??", 0, 1, true}, {"a{0}", 0, 0, false}, {"a{0002,}", 2, -1, false}, {"a{2,4}?", 2, 4, true}, {"a{2147483647}", 2147483647, 2147483647, false},
	} {
		n := mustRegex(t, tc.source, "").root
		if n.min != tc.min || n.max != tc.max || n.lazy != tc.lazy || n.kind != rxRepeat {
			t.Fatalf("%s: %+v", tc.source, n)
		}
	}
	r = mustRegex(t, "|a||", "")
	if len(r.root.children) != 4 || r.root.children[0].kind != rxEmpty || r.root.children[2].kind != rxEmpty {
		t.Fatal("empty alternatives")
	}
}
func TestSnippetRegexErrorsOriginsAndOffsets(t *testing.T) {
	for _, tc := range []struct {
		source, flags, part string
		offset              int
	}{
		{"", "gg", "options", 1}, {"a", "x", "options", 0}, {"a", "ih", "options", 1}, {"a", "i😀", "options", 1},
		{"a)", "", "source", 1}, {"(", "", "source", 1}, {"[a", "", "source", 2}, {"\\", "", "source", 1},
		{`\9`, "u", "source", 0}, {`\k<missing>`, "u", "source", 0}, {`a{3,2}`, "", "source", 1},
		{`\uD800`, "", "source", 0}, {`\uDC00`, "", "source", 0}, {`\u{110000}`, "", "source", 0},
		{"a\xff", "", "source", 1}, {`\p{1}`, "u", "source", 3},
	} {
		r, err := parseSnippetRegex(tc.source, tc.flags)
		var diagnostic *snippetRegexError
		if r != nil || !errors.As(err, &diagnostic) || diagnostic.Part != tc.part || diagnostic.Offset != tc.offset {
			t.Fatalf("%q/%q: %+v %v want%s:%d", tc.source, tc.flags, r, err, tc.part, tc.offset)
		}
	}
	for _, source := range []string{
		`*a`, `+`, `?`, `a**`, `a++`, `a?*`, `a{1}{2}`, `a{}`, `a{,1}`, `a{1`, `a{1,2`, `a{1,2,3}`, `a{2147483648}`, `\2147483648`,
		`^*`, `$?`, `\b+`, `(?=a)*`, `(?!a)?`, `(?<=a)+`, `(?<!a){2}`, `(?x:a)`, `(?`,
		`(?<x>a)(?<x>b)`, `(?<>a)`, `(?<1x>a)`, `(?<😀>a)`, `\k<>`, `\k<x`,
		`[z-a]`, `[a-\d]`, `[\d-a]`, `[\1]`, `[\k<x>]`, `[\B]`, `[\01]`, `[`,
		`\a`, `\é`, `\01`, `\x1`, `\xQ0`, `\u00`, `\u{}`, `\u{000110000}`, `\uD800\u0041`, `\uD800\u{DC00}`, `\u{D800}`, `\c1`,
		`\pL`, `\p{}`, `\p{L`, `\p{=L}`, `\p{L=}`, `\p{A=B=C}`, `\p{Script=1}`, `\p{White Space}`, `]`, `}`, `{`,
	} {
		r, err := parseSnippetRegex(source, "u")
		if err == nil || r != nil {
			t.Fatalf("accepted %q", source)
		}
	}
}
func TestSnippetRegexBoundsAndOwnership(t *testing.T) {
	// Source length and quantifier magnitude do not expand into repeated nodes.
	source := "a{" + strings.Repeat("0", maxSnippetRegexBytes-4) + "1}"
	if len(source) != maxSnippetRegexBytes {
		t.Fatal(len(source))
	}
	r := mustRegex(t, source, "")
	if r.root.min != 1 || r.root.max != 1 || len(r.root.children) != 1 {
		t.Fatal("quantifier unrolled")
	}
	if r, err := parseSnippetRegex(source+"x", ""); err == nil || r != nil {
		t.Fatal("source cap")
	}
	mustRegex(t, strings.Repeat("a", maxSnippetRegexNodes-1), "")
	if r, err := parseSnippetRegex(strings.Repeat("a", maxSnippetRegexNodes), ""); err == nil || r != nil {
		t.Fatal("node cap")
	}
	mustRegex(t, strings.Repeat("()", maxSnippetRegexCaptures), "")
	if r, err := parseSnippetRegex(strings.Repeat("()", maxSnippetRegexCaptures+1), ""); err == nil || r != nil {
		t.Fatal("capture cap")
	}
	for _, extra := range []string{"", "a*", "[a]"} {
		levels := maxSnippetRegexDepth
		if extra != "" {
			levels--
		} else {
			extra = "a"
		}
		mustRegex(t, strings.Repeat("(?:", levels)+extra+strings.Repeat(")", levels), "")
		if r, err := parseSnippetRegex(strings.Repeat("(?:", levels+1)+extra+strings.Repeat(")", levels+1), ""); err == nil || r != nil {
			t.Fatal("depth cap")
		}
	}
	name := strings.Repeat("a", maxSnippetRegexToken)
	mustRegex(t, "(?<"+name+">x)\\k<"+name+">", "")
	mustRegex(t, `\p{`+name+`}`, "u")
	for _, source := range []string{"(?<" + name + "a>x)", `\p{` + name + `a}`} {
		if r, err := parseSnippetRegex(source, "u"); err == nil || r != nil {
			t.Fatal("token cap")
		}
	}
	r = mustRegex(t, `(?<x>a|b){1000000}?\k<x>`, "iu")
	snapshot := fmt.Sprintf("%#v", r)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				other, err := parseSnippetRegex(r.source, "iu")
				if err != nil || !reflect.DeepEqual(r, other) {
					t.Error("concurrent parse differed")
				}
			}
		}()
	}
	wg.Wait()
	if fmt.Sprintf("%#v", r) != snapshot {
		t.Fatal("tree changed")
	}
}
func FuzzSnippetRegexParser(f *testing.F) {
	f.Add(`(?im:(a)\1$)(?-i:b)`, "u")
	f.Add(`(a)\18*[a-\d](?=(a))?a`, "")
	f.Add(`(?:(?<é>a)|(?<\u00e9>b))+\k<é>`, "v")
	for _, s := range []string{`[[a-z]&&[^aeiou]]`, `[\q{ab|a|}]`, `[\p{RGI_Emoji}--a]`} {
		f.Add(s, "v")
	}
	for _, source := range []string{"", `(?<word>\w+)\k<word>`, `(?<=a)[^b]{1,4}?`, `[\p{L}\P{Script=Greek}]`, `\uD83D\uDE00`} {
		f.Add(source, "iu")
	}
	f.Fuzz(func(t *testing.T, source, flags string) {
		r, err := parseSnippetRegex(source, flags)
		if err != nil {
			var diagnostic *snippetRegexError
			if r != nil || !errors.As(err, &diagnostic) || diagnostic.Offset < 0 {
				t.Fatal("invalid error")
			}
			input := source
			if diagnostic.Part == "options" {
				input = flags
			} else if diagnostic.Part != "source" {
				t.Fatal("invalid origin")
			}
			if diagnostic.Offset > len(input) {
				t.Fatal("offset outside input")
			}
			return
		}
		count := 0
		var walk func(*snippetRegexNode)
		walk = func(n *snippetRegexNode) {
			count++
			if count > maxSnippetRegexNodes || n.from < 0 || n.to < n.from || n.to > len(source) {
				t.Fatal("invalid node")
			}
			if n.kind == rxBackref && (n.group < 1 || n.group > r.captures) {
				t.Fatal("unresolved ref")
			}
			for _, child := range n.children {
				walk(child)
			}
		}
		walk(r.root)
		second, err := parseSnippetRegex(source, flags)
		if err != nil || !reflect.DeepEqual(r, second) {
			t.Fatal("nondeterministic parse")
		}
	})
}
