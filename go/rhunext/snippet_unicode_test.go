package rhunext

import (
	"reflect"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestSnippetUnicodeFoldAndCaseGoldens(t *testing.T) {
	for _, tc := range []struct{ from, to rune }{
		{'A', 'a'}, {'z', 'z'}, {'K', 'k'}, {'ſ', 's'}, {'Σ', 'σ'}, {'ς', 'σ'}, {'İ', 'İ'}, {'ı', 'ı'}, {'I', 'i'}, {'ß', 'ß'}, {'ẞ', 'ß'}, {0x1c89, 0x1c8a}, {0x10d50, 0x10d70}, {0x10400, 0x10428}, {-1, -1}, {0xd800, 0xd800}, {0x110000, 0x110000},
	} {
		if got := snippetUnicodeFold(tc.from); got != tc.to {
			t.Fatalf("fold%x ->%x want%x", tc.from, got, tc.to)
		}
	}
	for _, tc := range []struct {
		cp    rune
		upper bool
		want  string
	}{
		{'a', true, "A"}, {'A', false, "a"}, {'ß', true, "SS"}, {'ẞ', false, "ß"}, {'İ', false, "i\u0307"}, {'Σ', false, "σ"}, {'ς', true, "Σ"}, {'ﬃ', true, "FFI"}, {0x10d50, false, string(rune(0x10d70))}, {0x1c8a, true, string(rune(0x1c89))}, {'1', true, ""}, {'A', true, ""}, {-1, true, ""}, {0xd800, false, ""},
	} {
		if got := snippetUnicodeCase(tc.cp, tc.upper); got != tc.want {
			t.Fatalf("case%x upper%v: %q != %q", tc.cp, tc.upper, got, tc.want)
		}
	}
}
func TestSnippetUnicodePropertyGoldens(t *testing.T) {
	for _, tc := range []struct {
		expr string
		cp   rune
		yes  bool
	}{
		{"L", 'é', true}, {"Letter", 'Σ', true}, {"Lu", 'A', true}, {"gc=Uppercase_Letter", 'a', false}, {"General_Category=LC", 'ǅ', true},
		{"sc=Grek", 'Σ', true}, {"Script=Greek", 'a', false}, {"Script_Extensions=Hiragana", 'ー', true}, {"scx=Kana", 'ー', true}, {"sc=Hira", 'ー', false}, {"sc=Common", 'ー', true},
		{"scx=Common", 'ー', false}, {"Script=Unknown", 0x378, true}, {"scx=Zzzz", 0x378, true}, {"Script=Katakana_Or_Hiragana", 'あ', false},
		{"Cn", 0x378, true}, {"Assigned", 0x378, false}, {"Assigned", 0x10d50, true}, {"Emoji", '😀', true}, {"ExtPict", '😀', true}, {"Bidi_M", '(', true},
		{"ASCII", 127, true}, {"ASCII", 128, false}, {"Any", 0x10ffff, true}, {"Any", 0xd800, false}, {"Cs", 0xd800, false}, {"White_Space", 0x85, true}, {"Cased", 0x1c89, true}, {"CI", 0x301, true},
		{"CWKCF", 0x200b, true}, {"AHex", 'f', true}, {"AHex", 'g', false},
	} {
		ranges, ok := snippetUnicodeProperty(tc.expr)
		if !ok || snippetUnicodeContains(ranges, tc.cp) != tc.yes {
			t.Fatalf("%s U+%04X =%v want%v valid%v", tc.expr, tc.cp, snippetUnicodeContains(ranges, tc.cp), tc.yes, ok)
		}
	}
	for _, expr := range []string{"", "Greek", "script=Greek", "sc=greek", "sc=Old Persian", "IsGreek", "gc=Lu=Ll", "Any=Yes", "Composition_Exclusion", "CE", "kEH_NoMirror", "kEH_NoRotate", "Full_Composition_Exclusion", "gc=", "sc=", "space ", "\x00"} {
		if _, ok := snippetUnicodeProperty(expr); ok {
			t.Fatalf("accepted %q", expr)
		}
	}
}
func TestSnippetUnicodeTableInvariants(t *testing.T) {
	if snippetUnicodeVersion != "16.0.0" {
		t.Fatal(snippetUnicodeVersion)
	}
	for key, ranges := range snippetUnicodeProperties {
		previous := rune(-2)
		for _, r := range ranges {
			if !snippetUnicodeScalar(r.lo) || !snippetUnicodeScalar(r.hi) || r.lo > r.hi || r.lo <= previous+1 || r.lo <= 0xdfff && r.hi >= 0xd800 {
				t.Fatalf("bad range %s %+v", key, r)
			}
			previous = r.hi
			if !snippetUnicodeContains(ranges, r.lo) || !snippetUnicodeContains(ranges, r.hi) {
				t.Fatalf("inclusive endpoints %s", key)
			}
		}
	}
	for alias, key := range snippetUnicodeAliases {
		if _, ok := snippetUnicodeProperties[key]; !ok {
			t.Fatalf("dangling alias %s ->%s", alias, key)
		}
	}
	var previous rune = -1
	for _, entry := range snippetUnicodeFolds {
		if entry.from <= previous || !snippetUnicodeScalar(entry.from) || !snippetUnicodeScalar(entry.to) || entry.from == entry.to || snippetUnicodeFold(entry.to) != entry.to || snippetUnicodeFold(entry.from) != entry.to {
			t.Fatalf("bad fold %+v", entry)
		}
		previous = entry.from
	}
	for _, table := range [][]snippetCaseEntry{snippetUnicodeUpper[:], snippetUnicodeLower[:]} {
		previous = -1
		for _, entry := range table {
			count := utf8.RuneCountInString(entry.text)
			if entry.from <= previous || !snippetUnicodeScalar(entry.from) || !utf8.ValidString(entry.text) || count < 1 || count > 3 || len(entry.text) > 12 || entry.text == string(entry.from) {
				t.Fatalf("bad case %+v", entry)
			}
			previous = entry.from
		}
	}
	// Identity lookups outside entries, including scalar endpoints and gaps.
	i := 0
	for cp := rune(0); cp <= utf8.MaxRune; cp++ {
		if i < len(snippetUnicodeFolds) && snippetUnicodeFolds[i].from == cp {
			i++
			continue
		}
		if snippetUnicodeFold(cp) != cp {
			t.Fatalf("unexpected fold%x", cp)
		}
	}
	ranges, _ := snippetUnicodeProperty("Cased")
	before := append([]snippetRuneRange(nil), ranges...)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				r, ok := snippetUnicodeProperty("Cased")
				if !ok || !snippetUnicodeContains(r, 'Σ') || snippetUnicodeFold('K') != 'k' {
					t.Error("concurrent lookup")
				}
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(ranges, before) {
		t.Fatal("shared table changed")
	}
}
