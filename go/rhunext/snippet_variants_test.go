package rhunext

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

func variantProgram() SnippetProgram {
	return SnippetProgram{Snippet: PreparedSnippet{Text: "a ABC", Fields: []Field{{1, 0, 1, 0}, {1, 2, 5, 0}, {0, 5, 5, 0}}}, InitialVariants: []int{2}, Choices: []SnippetChoice{{1, []string{"a", "next"}}}}
}
func TestInitialVariantsGoldenAndOwnership(t *testing.T) {
	p := variantProgram()
	want := "0 0 0 0 snippet 0 5 0 82\na ABCfield 1 0 1 0 0\nfield 1 2 5 0 0\nfield 0 5 5 0 0\nvariant 2 0\nchoice 1 2 9\n1\na4\nnext"
	b, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("%q", b)
	}
	if p.Snippet.Validate() == nil {
		t.Fatal("plain validator admitted variants")
	}
	it := (CompletionItem{Label: "label"}).WithSnippetProgram(p)
	p.InitialVariants[0] = 1
	p.Snippet.Fields[1].End = 3
	out := (CompletionList{Items: []CompletionItem{it}}).Encode()
	if !bytes.Contains(out, []byte("variant 2 0\n")) {
		t.Fatalf("builder borrowed input: %q", out)
	}
	it.Insert = "a aXX" // the declared field now differs too, still valid
	if !bytes.Contains((CompletionList{Items: []CompletionItem{it}}).Encode(), []byte("variant 2 0\n")) {
		t.Fatal("lost variant")
	}
	it.Insert = "a a"
	if bytes.Contains((CompletionList{Items: []CompletionItem{it}}).Encode(), []byte("field ")) {
		t.Fatal("text mutation did not revalidate")
	}
	it = it.WithSnippet(PreparedSnippet{Text: "plain"})
	if len(it.initialVariants) != 0 {
		t.Fatal("stale declarations")
	}
}
func TestInitialVariantsRejectAllMetadata(t *testing.T) {
	for name, change := range map[string]func(*SnippetProgram){
		"primary":   func(p *SnippetProgram) { p.InitialVariants = []int{1} },
		"final":     func(p *SnippetProgram) { p.InitialVariants = []int{3} },
		"missing":   func(p *SnippetProgram) { p.InitialVariants = []int{4} },
		"zero":      func(p *SnippetProgram) { p.InitialVariants = []int{0} },
		"negative":  func(p *SnippetProgram) { p.InitialVariants = []int{-1} },
		"huge":      func(p *SnippetProgram) { p.InitialVariants = []int{int(^uint(0) >> 1)} },
		"duplicate": func(p *SnippetProgram) { p.InitialVariants = []int{2, 2} },
		"too many":  func(p *SnippetProgram) { p.InitialVariants = make([]int, 513) },
		"equal": func(p *SnippetProgram) {
			p.Snippet.Text = "a a"
			p.Snippet.Fields[1].End = 3
			p.Snippet.Fields = p.Snippet.Fields[:2]
		},
		"undeclared":    func(p *SnippetProgram) { p.InitialVariants = nil },
		"UTF8":          func(p *SnippetProgram) { p.Snippet.Text = "a \xffXX" },
		"mirror parent": func(p *SnippetProgram) { p.Snippet.Fields = append(p.Snippet.Fields, Field{2, 2, 3, 2}) },
		"choice":        func(p *SnippetProgram) { p.Choices[0].Options[0] = "ABC" },
	} {
		t.Run(name, func(t *testing.T) {
			p := variantProgram()
			change(&p)
			if p.Validate() == nil {
				t.Fatal("accepted")
			}
			it := (CompletionItem{Label: "l"}).WithSnippetProgram(p)
			if len(it.fields) > 0 || len(it.choices) > 0 || len(it.initialVariants) > 0 {
				t.Fatal("partial metadata")
			}
		})
	}
}
func TestInitialVariantsCountsAndFrameBoundary(t *testing.T) {
	p := SnippetProgram{Snippet: PreparedSnippet{Text: "ab", Fields: []Field{{1, 0, 1, 0}}}}
	for i := 1; i < MaxSnippetFields; i++ {
		p.Snippet.Fields = append(p.Snippet.Fields, Field{1, 1, 1, 0})
		p.InitialVariants = append(p.InitialVariants, i+1)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p = variantProgram()
	p.Snippet.Text += strings.Repeat("z", MaxSnippetBody-200)
	b, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	p.Snippet.Text += strings.Repeat("z", MaxSnippetBody-len(b))
	b, err = p.Encode()
	if err != nil || len(b) != MaxSnippetBody {
		t.Fatalf("%d %v", len(b), err)
	}
	p.Snippet.Text += "z"
	if p.Validate() == nil {
		t.Fatal("oversized")
	}
}
func TestInitialVariantsCapabilityGuardIsUnsent(t *testing.T) {
	for _, features := range []map[string]bool{nil, {FeatureSnippets: true}, {FeatureSnippetVariants: true}} {
		wire := make(wireWriter, 4)
		e := testConnection(t, wire)
		e.agreed = features
		_, err := e.InsertSnippetProgramContext(context.Background(), 1, 0, Range{Pos{1, 1}, Pos{1, 1}}, variantProgram())
		if err == nil || len(wire) != 0 {
			t.Fatalf("%v queued=%d", err, len(wire))
		}
		if !reflect.DeepEqual(e.agreed, features) {
			t.Fatal("mutated negotiation")
		}
	}
}
