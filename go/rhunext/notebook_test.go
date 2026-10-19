package rhunext

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func sampleNotebook() *Notebook {
	return &Notebook{Meta: []byte(`{"k":1}`), Cells: []Cell{
		{Kind: CellCode, Lang: "python", Count: 3, Source: []byte("print(1)\n"), Meta: []byte(`{"id":"a"}`),
			Outputs: []Output{{Kind: "display", ID: "d1", Keep: []byte("{}"), Items: []Item{
				{Type: "png", Width: 40, Height: 20, Data: []byte{1, 2, 3}}, {Type: "text", Data: []byte("<Figure>\n")}}}}},
		{Kind: CellMarkup, Lang: "-", Count: -1, Source: []byte("# T")},
	}}
}

func TestNotebookBodies(t *testing.T) {
	nb := sampleNotebook()
	b := nb.Encode()
	if !bytes.HasPrefix(b, []byte("2 7\n{\"k\":1}code python 3 9 10 1\nprint(1)\n{\"id\":\"a\"}display d1 2 2\n{}png 40 20 3\n")) {
		t.Fatalf("%q", b)
	}
	back, err := ParseNotebook(b)
	if err != nil {
		t.Fatal(err)
	}
	back.Cells[1].Outputs = nil
	nb.Cells[1].Outputs = nil
	if !reflect.DeepEqual(back, nb) {
		t.Fatalf("%#v", back)
	}
	for _, bad := range []string{"1 0\ncode python - 99 0 0\nx", "1 0\nlens - - 1 0 0\nx", "0 0\nextra"} {
		if _, err := ParseNotebook([]byte(bad)); err == nil {
			t.Fatal(bad)
		}
	}
	if _, err := ParseOutput([]byte("stdout BAD 0 0\n")); err == nil {
		t.Fatal("a bad id")
	}
}

type toyNotebooks struct {
	ran   chan *NbRun
	saved chan *NbSave
}

func (p *toyNotebooks) Ready(*Ext, string)    {}
func (p *toyNotebooks) Event(*Ext, Event)     {}
func (p *toyNotebooks) Run(*Ext, string, Doc) {}
func (p *toyNotebooks) NbOpen(e *Ext, req *NbOpen) (*Notebook, error) {
	if string(req.Data) == "bad" {
		return nil, errors.New("not a toy notebook")
	}
	return &Notebook{Cells: []Cell{{Kind: CellCode, Lang: "python", Count: -1, Source: req.Data}}}, nil
}
func (p *toyNotebooks) NbSave(e *Ext, req *NbSave) ([]byte, error) {
	p.saved <- req
	return req.Notebook.Cells[0].Source, nil
}
func (p *toyNotebooks) NbRun(e *Ext, r *NbRun) {
	p.ran <- r
	go func() {
		e.NbState(r.NB, r.Cell, r.Run, "done", 1)
	}()
}

func TestNotebookWire(t *testing.T) {
	input, hostW := io.Pipe()
	hostR, output := io.Pipe()
	defer input.Close()
	defer hostW.Close()
	defer hostR.Close()
	defer output.Close()
	p := &toyNotebooks{ran: make(chan *NbRun, 1), saved: make(chan *NbSave, 1)}
	done := make(chan error, 1)
	go func() { done <- RunOnWith(input, output, io.Discard, []string{FeatureNotebooks}, p) }()
	r := bufio.NewReader(hostR)
	line, _ := r.ReadString('\n')
	if line != "hello 1 notebooks\n" {
		t.Fatal(line)
	}
	open := "5 3\n/a.nbx=1"
	io.WriteString(hostW, "0 features notebooks\n0 ready -\n")
	io.WriteString(hostW, fmt.Sprintf("1 nb-open 4 %d\n%s", len(open), open))
	line, _ = r.ReadString('\n')
	want := (&Notebook{Cells: []Cell{{Kind: CellCode, Lang: "python", Count: -1, Source: []byte("x=1")}}}).Encode()
	if line != fmt.Sprintf("ok 1 %d\n", len(want)) {
		t.Fatal(line)
	}
	body := make([]byte, len(want))
	io.ReadFull(r, body)
	if !bytes.Equal(body, want) {
		t.Fatalf("%q", body)
	}
	bad := "5 3\n/a.nbbad"
	io.WriteString(hostW, fmt.Sprintf("2 nb-open 4 %d\n%s", len(bad), bad))
	line, _ = r.ReadString('\n')
	if line != "error 2 refused not a toy notebook\n" {
		t.Fatal(line)
	}
	save := append([]byte("5\n/a.nb"), want...)
	io.WriteString(hostW, fmt.Sprintf("3 nb-save 4 %d\n%s", len(save), save))
	line, _ = r.ReadString('\n')
	if line != "ok 3 3\n" {
		t.Fatal(line)
	}
	io.ReadFull(r, body[:3])
	if s := <-p.saved; s.Path != "/a.nb" || s.NB != 4 {
		t.Fatal(s)
	}
	io.WriteString(hostW, "0 nb-run 4 9 2 python 3\nx=2")
	if run := <-p.ran; run.Cell != 9 || run.Run != 2 || string(run.Source) != "x=2" {
		t.Fatal(run)
	}
	line, _ = r.ReadString('\n')
	if !strings.HasSuffix(line, " nb-state 4 9 2 done 1\n") {
		t.Fatal(line)
	}
	id := strings.Fields(line)[0]
	io.WriteString(hostW, "ok "+id+"\n0 bye\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type toyExecs struct{ exits chan [2]int64 }

func (p *toyExecs) Ready(*Ext, string)    {}
func (p *toyExecs) Event(*Ext, Event)     {}
func (p *toyExecs) Run(*Ext, string, Doc) {}
func (p *toyExecs) NbExecExit(e *Ext, exec int64, code int, killed bool) {
	if killed {
		code = -1
	}
	p.exits <- [2]int64{exec, int64(code)}
}

func TestNotebookExec(t *testing.T) {
	c := &Command{Program: "python3", Args: []string{"-m", "ipykernel_launcher"}, Env: []string{"A=b"}}
	b := c.Encode()
	if string(b) != "7 0 2 1\npython32\n-m18\nipykernel_launcher3\nA=b" {
		t.Fatalf("%q", b)
	}
	back, err := ParseCommand(b)
	if err != nil || !reflect.DeepEqual(back, c) {
		t.Fatal(back, err)
	}
	for _, bad := range []string{"0 0 0 0\n", "1 0 0 1\nx1\nA", "1 0 1 0\nx5\nab", "1 0 0 0\nxy"} {
		if _, err := ParseCommand([]byte(bad)); err == nil {
			t.Fatal(bad)
		}
	}
	input, hostW := io.Pipe()
	hostR, output := io.Pipe()
	defer hostR.Close()
	p := &toyExecs{exits: make(chan [2]int64, 2)}
	done := make(chan error, 1)
	go func() { done <- RunOnWith(input, output, io.Discard, []string{FeatureNotebooks}, p) }()
	r := bufio.NewReader(hostR)
	r.ReadString('\n')
	io.WriteString(hostW, "0 features notebooks\n0 ready -\n0 nb-exec-exit 4 3\n0 nb-exec-exit 5 killed\n0 bye\n")
	if x := <-p.exits; x != [2]int64{4, 3} {
		t.Fatal(x)
	}
	if x := <-p.exits; x != [2]int64{5, -1} {
		t.Fatal(x)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
