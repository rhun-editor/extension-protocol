package rhunext

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
)

type probe struct {
	got  chan string
	view chan ViewEvent
}

func (p *probe) Ready(ext *Ext, project string) {
	if ext.Has(FeatureComplete) && !ext.Has(FeatureViews) {
		p.got <- "ready complete"
	}
}
func (p *probe) Run(ext *Ext, command string, doc Doc) {}
func (p *probe) Event(ext *Ext, ev Event)              {}
func (p *probe) View(ext *Ext, v View, ev ViewEvent)   { p.view <- ev }
func (p *probe) Complete(ext *Ext, req Request) CompletionList {
	r := Range{Start: Pos{req.At.Line, 1}, End: req.At}
	return CompletionList{Items: []CompletionItem{
		{Range: r, Kind: KindKeyword, Label: "fn"},
		{Range: r, Kind: KindModule, Label: "fmt", Doc: "Formatting.", Edits: []TextEdit{{Range{Pos{1, 1}, Pos{1, 1}}, "import fmt\n"}}},
	}}
}
func (p *probe) Signature(ext *Ext, req Request) *SignatureHelp {
	if req.Kind == "update" {
		return nil
	}
	return &SignatureHelp{Signature: "fn add(a: i32, b: i32)", ActiveFrom: 7, ActiveTo: 13, Doc: "Adds."}
}

func TestASessionWithFeatures(t *testing.T) {
	toExt, hostW := io.Pipe()
	hostR, fromExt := io.Pipe()
	p := &probe{got: make(chan string, 4), view: make(chan ViewEvent, 4)}
	done := make(chan error, 1)
	go func() { done <- RunOnWith(toExt, fromExt, io.Discard, []string{FeatureComplete, FeatureSignature}, p) }()
	br := bufio.NewReader(hostR)
	line := func() string {
		s, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(s, "\n")
	}
	body := func(n int) string {
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	send := func(s string) { io.WriteString(hostW, s) }

	if got := line(); got != "hello 1 complete signature" {
		t.Fatalf("hello: %q", got)
	}
	send("0 features complete signature\n0 ready /p\n")
	select {
	case g := <-p.got:
		if g != "ready complete" {
			t.Fatal(g)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ready")
	}
	send("4 complete 1 2 3 3 typed -\n")
	want := "2 0\n3 1 3 3 keyword 2 2 0 0\nfnfn3 1 3 3 module 3 3 0 45\nfmtfmtedit 1 1 1 1 11\nimport fmt\ndoc 11\nFormatting."
	if got := line(); got != "ok 4 "+itoa(int64(len(want))) {
		t.Fatalf("reply: %q", got)
	}
	if got := body(len(want)); got != want {
		t.Fatalf("body: %q", got)
	}
	send("5 signature 1 2 3 9 trigger (\n")
	sig := "7 13\nfn add(a: i32, b: i32)\nAdds."
	if got := line(); got != "ok 5 "+itoa(int64(len(sig))) {
		t.Fatalf("signature: %q", got)
	}
	if got := body(len(sig)); got != sig {
		t.Fatalf("signature body: %q", got)
	}
	send("6 signature 1 3 3 10 update -\n")
	if got := line(); got != "ok 6 0" {
		t.Fatalf("hidden: %q", got)
	}
	send("7 frobnicate 1 2 3 4 typed -\n")
	if got := line(); got != "error 7 unknown no such request" {
		t.Fatalf("unknown: %q", got)
	}
	send("0 view 3 click 10.5 -2\n0 bye\n")
	select {
	case ev := <-p.view:
		if ev.Kind != "click" || ev.X != 10.5 || ev.Y != -2 {
			t.Fatalf("view event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no view event")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPicturesAndNumbers(t *testing.T) {
	var p Picture
	p.Rect(1, 2, 3, 4, RGB(255, 0, 0)).Poly(2, Series(3), [][2]float64{{0, 0}, {5, 5.5}}).
		Text(0, 0, Small, Right, ColorMuted, "a\nb").Line(0, 0, 1, 1, -1, ColorAccent)
	want := "rect 1 2 3 4 #ff0000\npoly 2 c3 0 0 5 5.5\ntext 0 0 small right muted a b\nline 0 0 1 1 0 accent\n"
	if string(p.Bytes()) != want || p.Len() != 4 {
		t.Fatalf("%q", p.Bytes())
	}
	for in, out := range map[float64]string{7.257: "7.26", 1e12: "9999999", -0.001: "0", 12.5: "12.5"} {
		if Num(in) != out {
			t.Fatalf("Num(%v) = %s", in, Num(in))
		}
	}
	if Series(9) != "c1" {
		t.Fatal(Series(9))
	}
}

func TestWordStart(t *testing.T) {
	text := []byte("package main\n\tfmt.Prin\n")
	if got := WordStart(text, Pos{2, 10}); got != (Pos{2, 6}) {
		t.Fatalf("%+v", got)
	}
	if got := WordStart(text, Pos{2, 6}); got != (Pos{2, 6}) {
		t.Fatalf("%+v", got)
	}
}

func TestALongListIsCutAndMarkedIncomplete(t *testing.T) {
	var l CompletionList
	for i := 0; i < 5000; i++ {
		l.Items = append(l.Items, CompletionItem{Label: "w"})
	}
	if !strings.HasPrefix(string(l.Encode()), "4096 1\n") {
		t.Fatal(string(l.Encode()[:10]))
	}
}
