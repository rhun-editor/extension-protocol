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

func formatSnapshot() SnippetSnapshot {
	return SnippetSnapshot{Version: 1, Position: Pos{1, 3}, TabWidth: 4, InsertSpaces: true, CurrentLine: "  a", Indent: "  ", SelectedText: "a\n  b\n"}
}
func TestSnippetFormatLiteralGoldens(t *testing.T) {
	s := formatSnapshot()
	for _, tc := range []struct {
		name, source, want string
		keep               bool
	}{
		{"line endings", "\tfoo\r\n\tbar\rbaz\n", "    foo\n    bar\n  baz\n  ", false},
		{"keep", "\tfoo\r\n\tbar\rbaz\n", "\tfoo\n\tbar\nbaz\n", true},
		{"empty group at line start", "\n${1:}\tfoo$0", "\n  \tfoo", false},
		{"node boundary avoids double indent", "\n${1:\tfoo}$0", "\n  \tfoo", false},
		{"unicode and NUL", "\t😀\x00\r\né", "    😀\x00\n  é", false},
		{"resolved variable EOL supplies context", "$X\tfoo", "x\n    foo", false},
		{"literal override", "$X", "x\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := variableProgram(t, tc.source, s, SnippetVariableOptions{KeepWhitespace: tc.keep, Variables: map[string]string{"X": "x\n"}})
			if p.Snippet.Text != tc.want {
				t.Fatalf("%q != %q", p.Snippet.Text, tc.want)
			}
		})
	}
	for _, width := range []int{1, 4, 16} {
		for _, spaces := range []bool{false, true} {
			s.TabWidth = width
			s.InsertSpaces = spaces
			want := "\tX\n"
			if spaces {
				want = strings.Repeat(" ", width) + "X\n"
			}
			columns := (2/width + 1) * width
			indent := strings.Repeat("\t", columns/width) + strings.Repeat(" ", columns%width)
			if spaces {
				indent = strings.Repeat(" ", columns)
			}
			want += indent + "Y"
			p := variableProgram(t, "\tX\n\tY", s, SnippetVariableOptions{})
			if p.Snippet.Text != want {
				t.Fatalf("width%d spaces%v: %q != %q", width, spaces, p.Snippet.Text, want)
			}
		}
	}
}
func TestSnippetFormatFieldsAndVariantGolden(t *testing.T) {
	p := variableProgram(t, "${1:\tx}\n$1$0", formatSnapshot(), SnippetVariableOptions{})
	want := SnippetProgram{Snippet: PreparedSnippet{Text: "    x\n  \tx", Fields: []Field{{1, 0, 5, 0}, {1, 8, 10, 0}, {0, 10, 10, 0}}}, InitialVariants: []int{2}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("%+v != %+v", p, want)
	}
	// Forward defaults and nested fields keep exact ownership while mirror provenance flattens.
	p = variableProgram(t, "$1\n${1:\t${2:x}}${0:done}", formatSnapshot(), SnippetVariableOptions{})
	want = SnippetProgram{Snippet: PreparedSnippet{Text: "    x\n  \txdone", Fields: []Field{{1, 0, 5, 0}, {2, 4, 5, 1}, {1, 8, 10, 0}, {0, 10, 14, 0}}}, InitialVariants: []int{3}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("%+v != %+v", p, want)
	}
}
func TestSnippetFormatCapturedSelectionGoldens(t *testing.T) {
	s := formatSnapshot()
	p := variableProgram(t, "\t${1:$SELECTION}\n$1$0", s, SnippetVariableOptions{})
	want := SnippetProgram{Snippet: PreparedSnippet{Text: "    a\n    b\n  \n  a\n  b\n", Fields: []Field{{1, 4, 14, 0}, {1, 17, 23, 0}, {0, 23, 23, 0}}}, InitialVariants: []int{2}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("%+v != %+v", p, want)
	}
	for _, name := range []string{"SELECTION", "TM_SELECTED_TEXT"} {
		p = variableProgram(t, "\t$"+name, s, SnippetVariableOptions{})
		if p.Snippet.Text != "    a\n    b\n  " {
			t.Fatalf("%q", p.Snippet.Text)
		}
		p = variableProgram(t, "\t$"+name, s, SnippetVariableOptions{Variables: map[string]string{name: s.SelectedText}})
		if p.Snippet.Text != "    a\n  b\n" {
			t.Fatalf("override changed: %q", p.Snippet.Text)
		}
		p = variableProgram(t, "\t$"+name, s, SnippetVariableOptions{KeepWhitespace: true})
		if p.Snippet.Text != "\ta\n\t  b\n\t" {
			t.Fatalf("keep changed: %q", p.Snippet.Text)
		}
	}
	s.CurrentLine = "  code"
	s.Position.Col = 7
	p = variableProgram(t, "\t$SELECTION", s, SnippetVariableOptions{})
	if p.Snippet.Text != "    a\n    b\n  " {
		t.Fatalf("external code prefix: %q", p.Snippet.Text)
	}
	s = formatSnapshot()
	s.SelectedText = "a\r\n b\rc\n"
	p = variableProgram(t, "\t$SELECTION", s, SnippetVariableOptions{})
	if p.Snippet.Text != "    a\r\n   b\r  c\n  " {
		t.Fatalf("selection EOL spelling: %q", p.Snippet.Text)
	}
	s.CurrentLine = "\ta"
	s.Indent = "\t"
	s.Position.Col = 2
	p = variableProgram(t, "\n$SELECTION", s, SnippetVariableOptions{})
	// Prefix matching is byte-based; normalized spaces do not match a captured tab.
	if p.Snippet.Text != "\n    a\r\n     b\r    c\n    " {
		t.Fatalf("tab mismatch: %q", p.Snippet.Text)
	}
}
func TestSnippetFormatPayloadsAndRawPreparationStayLiteral(t *testing.T) {
	source := "\t${1|a\r\n\tb,c|} $1 $X $UNFAMILIAR"
	p := variableProgram(t, source, formatSnapshot(), SnippetVariableOptions{Variables: map[string]string{"X": "\r\n\t$1"}})
	if p.Snippet.Text != "    a\r\n\tb a\r\n\tb \r\n\t$1 UNFAMILIAR" {
		t.Fatalf("%q", p.Snippet.Text)
	}
	if p.Choices[0].Options[0] != "a\r\n\tb" || len(p.InitialVariants) != 0 {
		t.Fatalf("%+v", p)
	}
	raw, err := PrepareSnippetTemplate("${1:\tx\r\n}\n$1", SnippetContext{})
	if err != nil || raw.Snippet.Text != "\tx\r\n\n\tx\r\n" || len(raw.InitialVariants) != 0 {
		t.Fatalf("%+v %v", raw, err)
	}
}
func TestSnippetFormatLimitsAndConcurrentReuse(t *testing.T) {
	s := formatSnapshot()
	source := "x" + strings.Repeat("x", MaxSnippetBody-100)
	p := variableProgram(t, source, s, SnippetVariableOptions{})
	b, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	source += strings.Repeat("x", MaxSnippetBody-len(b))
	p = variableProgram(t, source, s, SnippetVariableOptions{})
	b, err = p.Encode()
	if err != nil || len(b) != MaxSnippetBody {
		t.Fatalf("%d %v", len(b), err)
	}
	for _, source := range []string{source + "x", strings.Repeat("\t", MaxSnippetBody/4+1), "\n" + strings.Repeat("x", MaxSnippetBody-2)} {
		p, err := PrepareSnippetTemplateWithSnapshot(source, s, SnippetVariableOptions{})
		var expansion *SnippetExpansionError
		if !errors.As(err, &expansion) || !reflect.DeepEqual(p, SnippetProgram{}) {
			t.Fatalf("partial/missing error %d %+v %v", len(source), p, err)
		}
	}
	template, err := ParseSnippetTemplate("\t${1:$SELECTION}\n$1$0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				p, err := template.PrepareWithSnapshot(s, SnippetVariableOptions{})
				if err != nil || p.Snippet.Text != "    a\n    b\n  \n  a\n  b\n" {
					t.Errorf("%+v %v", p, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
func TestSnippetFormatPartBudgets(t *testing.T) {
	// Expanded unresolved layout has its own marker bound. Raw preparation stays
	// within its existing byte and field limits.
	source := "${1:" + strings.Repeat("${X:a}", 1024) + "}"
	for i := 2; i <= 7; i++ {
		source += fmt.Sprintf("${%d:$%d$%d}", i, i-1, i-1)
	}
	p, err := PrepareSnippetTemplate(source, SnippetContext{Variables: map[string]string{"X": "x"}})
	if err != nil || p.Validate() != nil {
		t.Fatalf("raw path: %v", err)
	}
	p, err = PrepareSnippetTemplateWithSnapshot(source, formatSnapshot(), SnippetVariableOptions{Variables: map[string]string{"X": "x"}})
	if err == nil || !strings.Contains(err.Error(), "format layout parts") || !reflect.DeepEqual(p, SnippetProgram{}) {
		t.Fatalf("%+v %v", p, err)
	}
	// Replaying a compact default also reaches the layout bound before rendering.
	source = "${1:" + strings.Repeat("${X:a}", 128) + "}" + strings.Repeat("$1", 256)
	p, err = PrepareSnippetTemplateWithSnapshot(source, formatSnapshot(), SnippetVariableOptions{Variables: map[string]string{"X": "x"}})
	if err == nil || !strings.Contains(err.Error(), "format layout parts") || !reflect.DeepEqual(p, SnippetProgram{}) {
		t.Fatalf("%+v %v", p, err)
	}
}

func FuzzSnippetFormat(f *testing.F) {
	for _, source := range []string{"${1:\tx}\n$1$0", "\t${1:$SELECTION}\n$1", "${1|x,y|}${0:end}", "\r\n${1:${2:}}$2"} {
		f.Add(source, "a\n  b\n", uint8(4), true, false)
	}
	f.Fuzz(func(t *testing.T, source, selection string, width uint8, spaces, keep bool) {
		if len(source) > 8192 || len(selection) > 8192 || !utf8.ValidString(selection) {
			t.Skip()
		}
		template, err := ParseSnippetTemplate(source)
		if err != nil {
			return
		}
		before, rawErr := template.Prepare(SnippetContext{})
		s := formatSnapshot()
		s.TabWidth = int(width%16) + 1
		s.InsertSpaces = spaces
		s.SelectedText = selection
		p, err := template.PrepareWithSnapshot(s, SnippetVariableOptions{KeepWhitespace: keep})
		if err != nil {
			if !reflect.DeepEqual(p, SnippetProgram{}) {
				t.Fatal("partial program")
			}
		} else if err = p.Validate(); err != nil {
			t.Fatal(err)
		}
		after, afterErr := template.Prepare(SnippetContext{})
		if !reflect.DeepEqual(before, after) || fmt.Sprint(rawErr) != fmt.Sprint(afterErr) {
			t.Fatal("template mutated")
		}
	})
}
