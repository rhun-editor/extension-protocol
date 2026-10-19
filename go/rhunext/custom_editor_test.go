package rhunext

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

type toyEditor struct {
	data   []byte
	opened chan *EditorOpen
	paths  chan string
}

func (p *toyEditor) Ready(*Ext, string)    {}
func (p *toyEditor) Event(*Ext, Event)     {}
func (p *toyEditor) Run(*Ext, string, Doc) {}
func (p *toyEditor) EditorOpen(e *Ext, r *EditorOpen) error {
	p.opened <- r
	return nil
}
func (p *toyEditor) EditorSave(e *Ext, r *EditorSave) (bool, []byte, error) {
	more, part := EditorPartOf(p.data, r.Part, 3)
	return more, part, nil
}
func (p *toyEditor) EditorChanged(*Ext, Doc)               {}
func (p *toyEditor) EditorClosed(*Ext, Doc)                {}
func (p *toyEditor) EditorPath(e *Ext, _ Doc, path string) { p.paths <- path }

func TestEditorWire(t *testing.T) {
	if more, part := EditorPartOf([]byte("abcdefg"), 2, 3); more || string(part) != "g" {
		t.Fatal(more, part)
	}
	input, hostW := io.Pipe()
	hostR, output := io.Pipe()
	defer hostR.Close()
	p := &toyEditor{data: []byte("abcdefg"), opened: make(chan *EditorOpen, 1), paths: make(chan string, 1)}
	done := make(chan error, 1)
	go func() { done <- RunOnWith(input, output, io.Discard, []string{FeatureCustomEditors}, p) }()
	r := bufio.NewReader(hostR)
	if line, _ := r.ReadString('\n'); line != "hello 1 custom-editors\n" {
		t.Fatal(line)
	}
	io.WriteString(hostW, "0 features custom-editors\n0 ready -\n1 editor-open 5 2 owned file 6\n/a.bin")
	if line, _ := r.ReadString('\n'); line != "ok 1 0\n" {
		t.Fatal(line)
	}
	if o := <-p.opened; o.CDoc != 5 || o.View != 2 || o.Model != "owned" || o.Path != "/a.bin" {
		t.Fatal(o)
	}
	io.WriteString(hostW, "2 editor-save 5 save 0\n")
	line, _ := r.ReadString('\n')
	body := make([]byte, 5)
	io.ReadFull(r, body)
	if line != "ok 2 5\n" || string(body) != "1\nabc" {
		t.Fatal(line, body)
	}
	io.WriteString(hostW, "3 editor-undo 5\n0 editor-path 5 6\n/b.bin")
	if line, _ := r.ReadString('\n'); !strings.HasPrefix(line, "ok 3 0") {
		t.Fatal(line)
	}
	if path := <-p.paths; path != "/b.bin" {
		t.Fatal(path)
	}
	io.WriteString(hostW, "0 bye\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
