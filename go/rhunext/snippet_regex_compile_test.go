package rhunext

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func mustCompileRegex(t *testing.T, source, options string) *snippetRegexProgram {
	t.Helper()
	p, e := compileSnippetRegex(source, options)
	if e != nil {
		t.Fatalf("%q/%q: %v", source, options, e)
	}
	return p
}
func TestSnippetRegexCompilerInstructionGoldens(t *testing.T) {
	cases := []struct {
		source, flags string
		captures      uint32
		code          []snippetRegexInstruction
		repeats       []snippetRegexRepeat
	}{
		{"", "", 0, []snippetRegexInstruction{{rxMatch, 0, 0, 0}}, nil},
		{"a😀", "", 0, []snippetRegexInstruction{{rxChar, 'a', 0, 0}, {rxChar, '😀', 0, 0}, {rxMatch, 0, 0, 0}}, nil},
		{"a|b", "", 0, []snippetRegexInstruction{{rxSplit, 1, 3, 0}, {rxChar, 'a', 0, 0}, {rxJump, 4, 0, 0}, {rxChar, 'b', 0, 0}, {rxMatch, 0, 0, 0}}, nil},
		{"|a|", "", 0, []snippetRegexInstruction{{rxSplit, 1, 2, 0}, {rxJump, 5, 0, 0}, {rxSplit, 3, 5, 0}, {rxChar, 'a', 0, 0}, {rxJump, 5, 0, 0}, {rxMatch, 0, 0, 0}}, nil},
		{`\1(a)`, "", 1, []snippetRegexInstruction{{rxReference, 1, 0, 0}, {rxClear, 2, 2, 0}, {rxSave, 2, 0, 0}, {rxChar, 'a', 0, 0}, {rxSave, 3, 0, 0}, {rxMatch, 0, 0, 0}}, nil},
		{"(a){2,4}?", "", 1, []snippetRegexInstruction{{rxRepeatStart, 0, 0, 0}, {rxRepeatTest, 0, 2, 8}, {rxClear, 2, 2, 0}, {rxClear, 2, 2, 0}, {rxSave, 2, 0, 0}, {rxChar, 'a', 0, 0}, {rxSave, 3, 0, 0}, {rxRepeatNext, 0, 1, 0}, {rxMatch, 0, 0, 0}}, []snippetRegexRepeat{{2, 4, 1, 0}}},
		{"(?:)*", "", 0, []snippetRegexInstruction{{rxRepeatStart, 0, 0, 0}, {rxRepeatTest, 0, 2, 3}, {rxRepeatNext, 0, 1, 0}, {rxMatch, 0, 0, 0}}, []snippetRegexRepeat{{0, 0xffffffff, 0, 0}}},
		{"(?<=a(b))c", "", 1, []snippetRegexInstruction{{rxLookCall, 3, 1, 2}, {rxChar, 'c', 0, 0}, {rxMatch, 0, 0, 0}, {rxClear, 2, 2, 0}, {rxSave, 3, 0, 0}, {rxChar, 'b', 0, 0}, {rxSave, 2, 0, 0}, {rxChar, 'a', 0, 0}, {rxMatch, 0, 0, 0}}, nil},
		{"(?<!(?=ab)c)d", "", 0, []snippetRegexInstruction{{rxLookCall, 3, 1, 3}, {rxChar, 'd', 0, 0}, {rxMatch, 0, 0, 0}, {rxChar, 'c', 0, 0}, {rxLookCall, 6, 5, 0}, {rxMatch, 0, 0, 0}, {rxChar, 'a', 0, 0}, {rxChar, 'b', 0, 0}, {rxMatch, 0, 0, 0}}, nil},
		{`^\b.\B$`, "gmsyd", 0, []snippetRegexInstruction{{rxAssertion, 1, 0, 0}, {rxAssertion, 3, 0, 0}, {rxDot, 0, 0, 0}, {rxAssertion, 4, 0, 0}, {rxAssertion, 2, 0, 0}, {rxMatch, 0, 0, 0}}, nil},
		{"KſΣ", "iu", 0, []snippetRegexInstruction{{rxChar, 'k', 0, 0}, {rxChar, 's', 0, 0}, {rxChar, 'σ', 0, 0}, {rxMatch, 0, 0, 0}}, nil},
		{"KſΣ", "i", 0, []snippetRegexInstruction{{rxChar, 'K', 0, 0}, {rxChar, 'ſ', 0, 0}, {rxChar, 'Σ', 0, 0}, {rxMatch, 0, 0, 0}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.source+tc.flags, func(t *testing.T) {
			p := mustCompileRegex(t, tc.source, tc.flags)
			if !reflect.DeepEqual(p.code, tc.code) || !reflect.DeepEqual(p.repeats, tc.repeats) || p.captures != tc.captures {
				t.Fatalf("code=%#v\nrepeats=%#v captures%d", p.code, p.repeats, p.captures)
			}
			mustRegexBytes(t, p)
		})
	}
}
func compiledClass(t *testing.T, source, flags string) []snippetRuneRange {
	t.Helper()
	p := mustCompileRegex(t, source, flags)
	if p.code[0].op != rxSet {
		t.Fatal("not a class")
	}
	d := p.classes[p.code[0].x]
	return p.ranges[d.start : d.start+d.count]
}
func TestSnippetRegexCompilerPredicateGoldens(t *testing.T) {
	cases := []struct {
		source, flags string
		want          []snippetRuneRange
	}{
		{`[]`, "", nil}, {`[^]`, "", []snippetRuneRange{{0, 0xd7ff}, {0xe000, 0x10ffff}}},
		{`[d-fa-ce-g]`, "", []snippetRuneRange{{'a', 'g'}}},
		{`[\uD7FF-\uE000]`, "", []snippetRuneRange{{0xd7ff, 0xd7ff}, {0xe000, 0xe000}}},
		{`[\u{0}-\u{10ffff}]`, "", []snippetRuneRange{{0, 0xd7ff}, {0xe000, 0x10ffff}}},
		{`\d`, "iu", []snippetRuneRange{{'0', '9'}}},
		{`[k]`, "iu", []snippetRuneRange{{'K', 'K'}, {'k', 'k'}, {'K', 'K'}}},
		{`[k]`, "i", []snippetRuneRange{{'K', 'K'}, {'k', 'k'}}},
		{`[s]`, "iu", []snippetRuneRange{{'S', 'S'}, {'s', 's'}, {'ſ', 'ſ'}}},
		{`[s]`, "i", []snippetRuneRange{{'S', 'S'}, {'s', 's'}}},
		{`[ß]`, "iu", []snippetRuneRange{{'ß', 'ß'}, {'ẞ', 'ẞ'}}},
		{`[ß]`, "i", []snippetRuneRange{{'ß', 'ß'}}},
		{`[Σ]`, "iu", []snippetRuneRange{{'Σ', 'Σ'}, {'ς', 'σ'}}},
		{`[Σ]`, "i", []snippetRuneRange{{'Σ', 'Σ'}, {'ς', 'σ'}}},
		{`[Ω]`, "iu", []snippetRuneRange{{'Ω', 'Ω'}, {'ω', 'ω'}, {'Ω', 'Ω'}}},
		{`[Ω]`, "i", []snippetRuneRange{{'Ω', 'Ω'}, {'ω', 'ω'}}},
		{`\p{Cs}`, "u", nil},
		{`\p{Script=Katakana_Or_Hiragana}`, "u", nil},
		{`\p{ASCII}`, "", []snippetRuneRange{{0, 127}}},
		{`\P{ASCII}`, "", []snippetRuneRange{{128, 0xd7ff}, {0xe000, 0x10ffff}}},
		{`\s`, "", []snippetRuneRange{{9, 13}, {32, 32}, {0xa0, 0xa0}, {0x1680, 0x1680}, {0x2000, 0x200a}, {0x2028, 0x2029}, {0x202f, 0x202f}, {0x205f, 0x205f}, {0x3000, 0x3000}, {0xfeff, 0xfeff}}},
	}
	for _, tc := range cases {
		t.Run(tc.source+tc.flags, func(t *testing.T) {
			a := compiledClass(t, tc.source, tc.flags)
			if len(a) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(a, tc.want) {
				t.Fatalf("%#v != %#v", a, tc.want)
			}
		})
	}
}
func TestSnippetRegexCompilerPredicateSemantics(t *testing.T) {
	cases := []struct {
		source, flags string
		yes, no       string
	}{
		{`\w`, "iu", "aZ0_Kſ", "éß-"}, {`\W`, "iu", "éß-", "aZ0_Kſ"}, {`\W`, "i", "Kſé-", "aZ0_"},
		{`[^k]`, "iu", "aß", "KkK"}, {`[^\W]`, "iu", "aZ0_Kſ", "é-"},
		{`\s`, "iu", "\t\r\n \ufeff\u2028", "\u0085x"},
		// Unicode-u property complement happens before case closure, unlike [^...].
		{`\P{Lowercase_Letter}`, "iu", "aAſK1", ""},
		{`[^\p{Lowercase_Letter}]`, "iu", "1-", "aAſK"},
		{`\p{sc=Greek}`, "u", "Ωα", "aА"}, {`\p{scx=Hira}`, "u", "あー", "aА"},
	}
	for _, tc := range cases {
		a := compiledClass(t, tc.source, tc.flags)
		for _, cp := range tc.yes {
			if !snippetUnicodeContains(a, cp) {
				t.Errorf("%s/%s missing %U", tc.source, tc.flags, cp)
			}
		}
		for _, cp := range tc.no {
			if snippetUnicodeContains(a, cp) {
				t.Errorf("%s/%s includes %U", tc.source, tc.flags, cp)
			}
		}
	}
	// Equivalent classes share one immutable descriptor, including empty predicates.
	p := mustCompileRegex(t, `[a-c][cba]\p{Cs}[]`, "")
	if len(p.classes) != 2 || p.code[0].x != p.code[1].x || p.code[2].x != p.code[3].x {
		t.Fatalf("intern %+v %+v", p.classes, p.code)
	}
}
func TestSnippetRegexCompilerLimits(t *testing.T) {
	for _, source := range []string{`\p{NoSuchProperty}`, `a\p{CE}`, strings.Repeat("a?", 257), strings.Repeat("a|", 2731) + "a"} {
		p, e := compileSnippetRegex(source, "u")
		if p != nil || e == nil {
			t.Fatalf("accepted length%d %.32q", len(source), source)
		}
	}
	for _, tc := range []struct {
		s  string
		at int
	}{{`a\p{NoSuchProperty}`, 1}, {`[a\p{CE}]`, 2}} {
		p, e := compileSnippetRegex(tc.s, "u")
		var diagnostic *snippetRegexError
		if p != nil || !errors.As(e, &diagnostic) || diagnostic.Part != "source" || diagnostic.Offset != tc.at {
			t.Fatalf("%+v %v", p, e)
		}
	}
	a := mustRegex(t, "[a-z]", "iu")
	if p, e := compileSnippetRegexAST(a, 8); p != nil || e == nil {
		t.Fatal("work limit")
	}
	for _, source := range []string{"a{0}", "a{2147483647}", "a{0,2147483647}"} {
		p := mustCompileRegex(t, source, "")
		if len(p.code) != 5 || len(p.repeats) != 1 {
			t.Fatalf("unrolled %s", source)
		}
	}
	// Individual class and range caps are tested directly without spending parser or work budgets first.
	n := &snippetRegexNode{from: 7}
	c := snippetRegexCompiler{left: maxSnippetRegexCompileWork, intern: map[uint64][]uint32{}}
	for i := 0; i < maxSnippetRegexClasses; i++ {
		c.class(n, []snippetRuneRange{{rune(i * 2), rune(i * 2)}})
	}
	if c.err != nil {
		t.Fatal(c.err)
	}
	c.class(n, []snippetRuneRange{{10000, 10000}})
	if c.err == nil {
		t.Fatal("class cap")
	}
	c = snippetRegexCompiler{left: maxSnippetRegexCompileWork, intern: map[uint64][]uint32{}}
	ranges := make([]snippetRuneRange, maxSnippetRegexRanges)
	for i := range ranges {
		ranges[i] = snippetRuneRange{rune(i * 2), rune(i * 2)}
	}
	c.class(n, ranges)
	if c.err != nil {
		t.Fatal(c.err)
	}
	c.class(n, []snippetRuneRange{{30000, 30000}})
	if c.err == nil {
		t.Fatal("range cap")
	}
	c = snippetRegexCompiler{left: maxSnippetRegexCompileWork}
	if r := c.add(n, make([]snippetRuneRange, maxSnippetRegexTempRanges), []snippetRuneRange{{0, 0}}); r != nil || c.err == nil {
		t.Fatal("temporary cap")
	}
	c = snippetRegexCompiler{left: maxSnippetRegexCompileWork}
	if r := c.normalize(n, make([]snippetRuneRange, 100000)); r != nil || c.err == nil {
		t.Fatal("sort work cap")
	}
}
func TestSnippetRegexCompilerCaptureClearAndDirections(t *testing.T) {
	p := mustCompileRegex(t, `((a)|(b(?=(c))))*`, "")
	// The repeat and outer capture both clear all four lexical capture pairs.
	if p.code[2] != (snippetRegexInstruction{rxClear, 2, 8, 0}) || p.code[3] != (snippetRegexInstruction{rxClear, 2, 8, 0}) {
		t.Fatalf("%+v", p.code)
	}
	p = mustCompileRegex(t, `(?<=a|bc)`, "")
	want := []snippetRegexInstruction{{rxLookCall, 2, 1, 2}, {rxMatch, 0, 0, 0}, {rxSplit, 3, 5, 0}, {rxChar, 'a', 0, 0}, {rxJump, 7, 0, 0}, {rxChar, 'c', 0, 0}, {rxChar, 'b', 0, 0}, {rxMatch, 0, 0, 0}}
	if !reflect.DeepEqual(p.code, want) {
		t.Fatalf("%#v", p.code)
	}
}
func TestSnippetRegexCompilerOwnershipConcurrency(t *testing.T) {
	const source = `(?<a>[a-z]+)(?<=x\k<a>)\p{scx=Greek}{0,3}?`
	ast := mustRegex(t, source, "iu")
	before := fmt.Sprintf("%#v", ast.root)
	p, e := compileSnippetRegexAST(ast, maxSnippetRegexCompileWork)
	if e != nil {
		t.Fatal(e)
	}
	want := mustRegexBytes(t, p)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				q, e := compileSnippetRegexAST(ast, maxSnippetRegexCompileWork)
				if e != nil {
					t.Error(e)
					return
				}
				b, e := q.encode()
				if e != nil || !bytes.Equal(want, b) {
					t.Errorf("nondeterministic %v", e)
				}
			}
		}()
	}
	wg.Wait()
	if fmt.Sprintf("%#v", ast.root) != before {
		t.Fatal("mutated AST")
	}
	// Compiled class ranges do not borrow generated data.
	prop, _ := snippetUnicodeProperty("ASCII")
	old := prop[0]
	q := mustCompileRegex(t, `\p{ASCII}`, "")
	q.ranges[0].hi = 1
	if prop[0] != old {
		t.Fatal("generated table alias")
	}
}
func FuzzSnippetRegexCompile(f *testing.F) {
	f.Add(`(?im:(a)\1$)(?-i:b)`, "u")
	f.Add(`(a)\18*[a-\d](?=(a))?a`, "")
	f.Add(`(?:(?<é>a)|(?<\u00e9>b))+\k<é>`, "v")
	for _, s := range []string{`[[a-z]&&[^aeiou]]`, `[\q{ab|a|}]`, `\p{RGI_Emoji}`, `(?<=[\q{ab|bc}])`} {
		f.Add(s, "iv")
	}
	for _, s := range []string{"", `(?<=ab)(c|d){2,4}?\1`, `[a-z\p{L}]`, `\P{Lowercase_Letter}`, "😀|K", `(?:)*`} {
		f.Add(s, "iu")
	}
	f.Fuzz(func(t *testing.T, source, flags string) {
		p, e := compileSnippetRegex(source, flags)
		if e != nil {
			if p != nil {
				t.Fatal("partial program")
			}
			return
		}
		b, e := p.encode()
		if e != nil {
			t.Fatal(e)
		}
		q, e := decodeSnippetRegexProgram(b)
		if e != nil {
			t.Fatal(e)
		}
		out, e := q.encode()
		if e != nil || !bytes.Equal(b, out) {
			t.Fatalf("roundtrip %v", e)
		}
	})
}
