package rhunext

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSnippetExpansionGoldenPrograms(t *testing.T) {
	for _, tc := range []struct {
		source, text string
		context      map[string]string
		fields       []Field
		choices      []SnippetChoice
	}{
		{source: "${1:one} $1", text: "one one", fields: []Field{{1, 0, 3, 0}, {1, 4, 7, 0}}},
		{source: "$5 ${5:é${2:x}} $2", text: "éx éx x", fields: []Field{{2, 0, 3, 0}, {1, 2, 3, 1}, {2, 4, 7, 0}, {1, 8, 9, 0}}},
		{source: "$9 ${9|a,b|}", text: "a a", fields: []Field{{1, 0, 1, 0}, {1, 2, 3, 0}}, choices: []SnippetChoice{{1, []string{"a", "b"}}}},
		{source: "${1:} $1 ${1:late} ${1:ignored${2:oops}}", text: "late late late late", fields: []Field{{1, 0, 4, 0}, {1, 5, 9, 0}, {1, 10, 14, 0}, {1, 15, 19, 0}}},
		{source: "${2147483647:x} $NO $2", text: "x NO ", fields: []Field{{2, 0, 1, 0}, {3, 2, 4, 0}, {1, 5, 5, 0}}},
		{source: "${A:hello ${2:world}}/$A/$A/$EMPTY/${EMPTY:fallback}", text: "hello world/A/A//", context: map[string]string{"EMPTY": ""}, fields: []Field{{1, 6, 11, 0}, {2, 12, 13, 0}, {2, 14, 15, 0}}},
		{source: "${HIDDEN:${2:from hidden}}$2", text: "supplied from hidden", context: map[string]string{"HIDDEN": "supplied "}, fields: []Field{{1, 9, 20, 0}}},
		{source: "${1:x} ${0:hello ${2:y}} $1", text: "x hello y x", fields: []Field{{1, 0, 1, 0}, {0, 2, 9, 0}, {2, 8, 9, 0}, {1, 10, 11, 0}}},
		{source: "${1:a${0:end}} $1", text: "aend aend", fields: []Field{{1, 0, 4, 0}, {0, 1, 4, 1}, {1, 5, 9, 0}}},
		{source: "${0:result}", text: "result", fields: []Field{{0, 0, 6, 0}}},
		{source: "${1|,x|}$1", text: "", fields: []Field{{1, 0, 0, 0}, {1, 0, 0, 0}}, choices: []SnippetChoice{{1, []string{"", "x"}}}},
		{source: `\$1 ${1|a\,b,c\|d,\\|}`, text: "$1 a,b", fields: []Field{{1, 3, 6, 0}}, choices: []SnippetChoice{{1, []string{"a,b", "c|d", `\`}}}},
		{source: "$VALUE ${VALUE:ignored} ${UNSET:} $0", text: "$1\\x $1\\x  ", context: map[string]string{"VALUE": "$1\\x"}, fields: []Field{{0, 11, 11, 0}}},
	} {
		t.Run(tc.source, func(t *testing.T) {
			parsed, err := ParseSnippetTemplate(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			p, err := parsed.Prepare(SnippetContext{Variables: tc.context})
			if err != nil {
				t.Fatal(err)
			}
			want := SnippetProgram{Snippet: PreparedSnippet{Text: tc.text, Fields: tc.fields}, Choices: tc.choices}
			if !reflect.DeepEqual(p, want) {
				t.Fatalf("got %#v\nwant %#v", p, want)
			}
			if p.Validate() != nil {
				t.Fatal("compiler returned invalid native program")
			}
			again, err := PrepareSnippetTemplate(tc.source, SnippetContext{Variables: tc.context})
			if err != nil || !reflect.DeepEqual(again, want) {
				t.Fatal(again, err)
			}
		})
	}
}

func TestSnippetExpansionErrorsReturnNoProgram(t *testing.T) {
	for _, source := range []string{"${1:$1}", "${1:$2}${2:$1}", "${1:${2:$1}}", "$0 $0", "${0|a,b|}", "${0/a/b/g}", "${VAR/[/b/}"} {
		p, err := PrepareSnippetTemplate(source, SnippetContext{})
		var expansion *SnippetExpansionError
		if !errors.As(err, &expansion) || expansion.Offset < 0 || expansion.Offset >= len(source) || !reflect.DeepEqual(p, SnippetProgram{}) {
			t.Fatalf("%q: %#v %v", source, p, err)
		}
	}
	var absent *SnippetTemplate
	if p, err := absent.Prepare(SnippetContext{}); err == nil || !reflect.DeepEqual(p, SnippetProgram{}) {
		t.Fatal(p, err)
	}
	if _, err := PrepareSnippetTemplate("${bad", SnippetContext{}); err == nil {
		t.Fatal("syntax error lost")
	}
	for _, variables := range []map[string]string{{"": "x"}, {"1bad": "x"}, {"a-b": "x"}, {"é": "x"}, {"A": string([]byte{255})}, {"A": strings.Repeat("x", MaxSnippetBody)}} {
		p, err := PrepareSnippetTemplate("$A", SnippetContext{Variables: variables})
		var expansion *SnippetExpansionError
		if !errors.As(err, &expansion) || expansion.Offset != -1 || !reflect.DeepEqual(p, SnippetProgram{}) {
			t.Fatal(p, err)
		}
	}
}

func TestSnippetExpansionOnlyEvaluatesReachableDefaults(t *testing.T) {
	for _, source := range []string{"${V:${1:$1}}", "${V:${1/a/b/}}", "${V:$0$0}", "${1:x}${1:${2:$2}}"} {
		p, err := PrepareSnippetTemplate(source, SnippetContext{Variables: map[string]string{"V": "literal"}})
		if err != nil {
			t.Fatal(source, err)
		}
		if p.Snippet.Text != "literal" && p.Snippet.Text != "xx" {
			t.Fatal(p)
		}
	}
	// A later reference can make a definition in an otherwise hidden fallback reachable.
	if _, err := PrepareSnippetTemplate("${V:${1:$1}}$1", SnippetContext{Variables: map[string]string{"V": "literal"}}); err == nil {
		t.Fatal("reachable hidden cycle accepted")
	}
}

func dependencyChain(n int, leafFirst bool) string {
	nodes := make([]string, n)
	for i := 1; i <= n; i++ {
		if i == n {
			nodes[i-1] = fmt.Sprintf("${%d:x}", i)
		} else {
			nodes[i-1] = fmt.Sprintf("${%d:$%d}", i, i+1)
		}
	}
	if leafFirst {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			nodes[i], nodes[j] = nodes[j], nodes[i]
		}
	}
	return strings.Join(nodes, "")
}

func TestSnippetExpansionDependencyHeightIndependentOfCacheOrder(t *testing.T) {
	for _, leafFirst := range []bool{false, true} {
		for _, n := range []int{32, 33} {
			p, err := PrepareSnippetTemplate(dependencyChain(n, leafFirst), SnippetContext{})
			if (err == nil) != (n == 32) {
				t.Fatalf("height=%d leafFirst=%v: %v", n, leafFirst, err)
			}
			if err == nil && p.Validate() != nil {
				t.Fatal(p)
			}
		}
	}
	nested := "x"
	for i := 32; i >= 1; i-- {
		nested = fmt.Sprintf("${%d:%s}", i, nested)
	}
	p, err := PrepareSnippetTemplate(nested, SnippetContext{})
	if err != nil || len(p.Snippet.Fields) != 32 {
		t.Fatal(p, err)
	}
}

func TestSnippetExpansionNativeAndContextLimits(t *testing.T) {
	for _, n := range []int{512, 513} {
		p, err := PrepareSnippetTemplate(strings.Repeat("$1", n), SnippetContext{})
		if (err == nil) != (n == 512) {
			t.Fatal(n, err)
		}
		if err == nil && len(p.Snippet.Fields) != 512 {
			t.Fatal(len(p.Snippet.Fields))
		}
	}
	for _, n := range []int{256, 257} {
		var source strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&source, "$%d ", i)
		}
		_, err := PrepareSnippetTemplate(source.String(), SnippetContext{})
		if (err == nil) != (n == 256) {
			t.Fatal(n, err)
		}
		variables := map[string]string{}
		for i := 1; i <= n; i++ {
			variables[fmt.Sprintf("V%d", i)] = ""
		}
		_, err = PrepareSnippetTemplate("", SnippetContext{Variables: variables})
		if (err == nil) != (n == 256) {
			t.Fatal(n, err)
		}
	}
	for _, source := range []string{
		strings.Repeat("x", MaxSnippetBody),
		"${1:" + strings.Repeat("x", 1<<20) + "}$1$1$1$1",
		"${1|x," + strings.Repeat("y", MaxSnippetOptionBytes+1) + "|}",
	} {
		if p, err := PrepareSnippetTemplate(source, SnippetContext{}); err == nil || !reflect.DeepEqual(p, SnippetProgram{}) {
			t.Fatal("limit bypass", err)
		}
	}
	if _, err := PrepareSnippetTemplate(strings.Repeat("x", MaxSnippetBody-64), SnippetContext{}); err != nil {
		t.Fatal(err)
	}
}

func TestSnippetExpansionImmutableConcurrentReuse(t *testing.T) {
	tree, err := ParseSnippetTemplate("$V ${1|a,b|} $1 $UNKNOWN ${0:end}")
	if err != nil {
		t.Fatal(err)
	}
	first, err := tree.Prepare(SnippetContext{Variables: map[string]string{"V": "literal"}})
	if err != nil {
		t.Fatal(err)
	}
	first.Snippet.Fields[0].End = 999
	first.Choices[0].Options[0] = "mutated"
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := tree.Prepare(SnippetContext{Variables: map[string]string{"V": "literal"}})
			if err != nil || p.Snippet.Text != "literal a a UNKNOWN end" || p.Choices[0].Options[0] != "a" {
				t.Error(p, err)
			}
		}()
	}
	wg.Wait()
}

func FuzzSnippetExpansion(f *testing.F) {
	for _, s := range []string{"${1:x} $1 ${0:end}", "$1 ${1:${2:x}} $2", "${V:${1|a,b|}}", "${1:$2}${2:$1}", "${0:é}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 8192 {
			t.Skip()
		}
		p, err := PrepareSnippetTemplate(source, SnippetContext{Variables: map[string]string{"V": "literal"}})
		if err == nil {
			if why := p.Validate(); why != nil {
				t.Fatal(why)
			}
		} else if !reflect.DeepEqual(p, SnippetProgram{}) {
			t.Fatal("partial program on error")
		}
	})
}

func TestSnippetExpansionVariableFallbacksDoNotMakeDepthCacheDependent(t *testing.T) {
	for _, n := range []int{23, 32, 33} {
		for _, leafFirst := range []bool{false, true} {
			nodes := make([]string, n)
			for i := 1; i <= n; i++ {
				if i == n {
					nodes[i-1] = fmt.Sprintf("${%d:x}", i)
				} else {
					nodes[i-1] = fmt.Sprintf("${%d:${A:${B:$%d}}}", i, i+1)
				}
			}
			if leafFirst {
				for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
					nodes[i], nodes[j] = nodes[j], nodes[i]
				}
			}
			p, err := PrepareSnippetTemplate(strings.Join(nodes, ""), SnippetContext{})
			if (err == nil) != (n <= 32) {
				t.Fatalf("height=%d leafFirst=%v: %v", n, leafFirst, err)
			}
			if err == nil {
				if why := p.Validate(); why != nil {
					t.Fatal(why)
				}
				if len(p.Snippet.Fields) != 2*n-1 || p.Snippet.Text != strings.Repeat("x", n) {
					t.Fatal(p)
				}
			}
		}
	}
}
