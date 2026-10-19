package rhunext

import (
	"bufio"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func choiceProgram() SnippetProgram {
	return SnippetProgram{PreparedSnippet{"x x", []Field{{1, 0, 1, 0}, {1, 2, 3, 0}, {0, 3, 3, 0}}}, []SnippetChoice{{1, []string{"x", "", "a\n😀"}}}, nil, nil}
}

func TestSnippetProgramGoldenAndSnapshot(t *testing.T) {
	p := choiceProgram()
	want := "0 0 0 0 snippet 0 3 0 75\nx xfield 1 0 1 0 0\nfield 1 2 3 0 0\nfield 0 3 3 0 0\nchoice 1 3 13\n1\nx0\n6\na\n😀"
	body, err := p.Encode()
	if err != nil || string(body) != want {
		t.Fatalf("%q %v", body, err)
	}
	it := (CompletionItem{Label: "label"}).WithSnippetProgram(p)
	original := (CompletionList{Items: []CompletionItem{it}}).Encode()
	p.Snippet.Fields[0].End = 99
	p.Choices[0].Options[0] = "changed"
	p.Choices[0].Group = 2
	if got := (CompletionList{Items: []CompletionItem{it}}).Encode(); !reflect.DeepEqual(got, original) {
		t.Fatal("choice builder aliases caller storage")
	}
	// Altering the public insertion text must revalidate the snapshot before encoding.
	it.Insert = "different"
	if got := string((CompletionList{Items: []CompletionItem{it}}).Encode()); strings.Contains(got, "choice ") || strings.Contains(got, "field ") {
		t.Fatal(got)
	}
	// Switching to plain prepared text must discard earlier choices too.
	it = it.WithSnippet(PreparedSnippet{Text: "plain"})
	if got := string((CompletionList{Items: []CompletionItem{it}}).Encode()); strings.Contains(got, "choice ") {
		t.Fatal(got)
	}
}

func TestSnippetProgramRejectsWholeMetadata(t *testing.T) {
	for name, change := range map[string]func(*SnippetProgram){
		"zero group":       func(p *SnippetProgram) { p.Choices[0].Group = 0 },
		"missing group":    func(p *SnippetProgram) { p.Choices[0].Group = 2 },
		"duplicate":        func(p *SnippetProgram) { p.Choices = append(p.Choices, p.Choices[0]) },
		"empty options":    func(p *SnippetProgram) { p.Choices[0].Options = nil },
		"too many options": func(p *SnippetProgram) { p.Choices[0].Options = make([]string, 257); p.Choices[0].Options[0] = "x" },
		"first mismatch":   func(p *SnippetProgram) { p.Choices[0].Options[0] = "bad" },
		"bad utf8":         func(p *SnippetProgram) { p.Choices[0].Options[1] = string([]byte{255}) },
		"oversize option":  func(p *SnippetProgram) { p.Choices[0].Options[1] = strings.Repeat("x", MaxSnippetOptionBytes+1) },
		"total body": func(p *SnippetProgram) {
			p.Choices[0].Options = []string{"x", strings.Repeat("y", MaxSnippetOptionBytes), strings.Repeat("y", MaxSnippetOptionBytes), strings.Repeat("y", MaxSnippetOptionBytes), strings.Repeat("y", MaxSnippetOptionBytes)}
		},
		"too many groups": func(p *SnippetProgram) { p.Choices = make([]SnippetChoice, 257) },
	} {
		t.Run(name, func(t *testing.T) {
			p := choiceProgram()
			change(&p)
			if p.Validate() == nil {
				t.Fatal("invalid program validated")
			}
			if _, err := p.Encode(); err == nil {
				t.Fatal("invalid program encoded")
			}
			it := (CompletionItem{Label: "label"}).WithSnippetProgram(p)
			got := string((CompletionList{Items: []CompletionItem{it}}).Encode())
			if strings.Contains(got, "field ") || strings.Contains(got, "choice ") || !strings.HasSuffix(got, "label"+p.Snippet.Text) {
				t.Fatal("partial metadata fallback", got)
			}
		})
	}
}

func TestSnippetProgramMaximumChoicesAndBody(t *testing.T) {
	p := choiceProgram()
	p.Choices[0].Options = make([]string, MaxSnippetChoiceOptions)
	p.Choices[0].Options[0] = "x"
	p.Choices[0].Options[255] = "last\x00😀\n"
	if p.Validate() != nil {
		t.Fatal("256 literal options rejected")
	}
	p = SnippetProgram{Snippet: PreparedSnippet{Fields: []Field{{1, 0, 0, 0}}}, Choices: []SnippetChoice{{1, []string{"", strings.Repeat("a", MaxSnippetOptionBytes), strings.Repeat("b", MaxSnippetOptionBytes), strings.Repeat("c", MaxSnippetOptionBytes), strings.Repeat("d", MaxSnippetOptionBytes-200)}}}}
	body, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	p.Choices[0].Options[4] += strings.Repeat("d", MaxSnippetBody-len(body))
	body, err = p.Encode()
	if err != nil || len(body) != MaxSnippetBody {
		t.Fatal(len(body), err)
	}
	p.Choices[0].Options[4] += "d"
	if p.Validate() == nil {
		t.Fatal("framing overflow accepted")
	}
}

func TestInsertSnippetProgramWireAndInvalidGuard(t *testing.T) {
	wire := make(wireWriter, 4)
	e := testConnection(t, wire)
	e.agreed = map[string]bool{FeatureSnippets: true}
	p := choiceProgram()
	body, _ := p.Encode()
	done := make(chan error, 1)
	go func() {
		v, err := e.InsertSnippetProgram(7, 9, Range{Pos{1, 1}, Pos{1, 1}}, p)
		if err == nil && v != 10 {
			err = fmt.Errorf("version %d", v)
		}
		done <- err
	}()
	if got := <-wire; got != fmt.Sprintf("1 snippet 7 9 1 1 1 1 %d\n", len(body)) {
		t.Fatal(got)
	}
	if got := <-wire; got != string(body) {
		t.Fatal(got)
	}
	if _, err := e.readMessage(bufio.NewReader(strings.NewReader("ok 1 10\n"))); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	p.Choices[0].Group = 0
	if _, err := e.InsertSnippetProgram(7, 10, Range{Pos{1, 1}, Pos{1, 1}}, p); err == nil {
		t.Fatal("invalid program sent")
	}
	if len(wire) != 0 || e.nextID.Load() != 1 || pendingCount(e) != 0 {
		t.Fatal("invalid program changed transport state")
	}
}
