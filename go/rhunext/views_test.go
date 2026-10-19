package rhunext

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestPatchEncodesAsTheSpecSays(t *testing.T) {
	p := NewPatch().Title("TODO").Badge(3)
	p.Kids(nil, []*Node{NewNode("a", "Alpha").WithKids(KidsOpen).Badge(1)}, false)
	b, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "3\ntitle 4\nTODObadge 3 0\nkids 0 1 0 28\n1 5 10 open\naAlphabadge 1 0\n"
	if string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
	if _, err := NewPatch().Kids(nil, []*Node{NewNode("a\nb", "x")}, false).Bytes(); err == nil {
		t.Fatal("an id with a LF was accepted")
	}
}

type retained struct {
	opens  []ViewOpen
	events []RetainedEvent
}

func (r *retained) Ready(*Ext, string)    {}
func (r *retained) Run(*Ext, string, Doc) {}
func (r *retained) Event(*Ext, Event)     {}
func (r *retained) ViewOpen(_ *Ext, o ViewOpen) {
	r.opens = append(r.opens, o)
}
func (r *retained) ViewEvent(_ *Ext, _ View, ev RetainedEvent) {
	r.events = append(r.events, ev)
}

func TestRetainedEventsAreFramedAndParsed(t *testing.T) {
	in := "0 features view-tree\n0 ready /p\n0 view 3 open tree sidebar todo\n0 view 3 visible 1\n" +
		"0 view 3 select 2 1 4\nabc\n0 view 3 activate 2 2\nab0 view 3 focus 0\n0 bye\n"
	h := &retained{}
	var out bytes.Buffer
	if err := RunOnWith(strings.NewReader(in), &out, io.Discard, []string{FeatureViewTree}, h); err != nil {
		t.Fatal(err)
	}
	if len(h.opens) != 1 || h.opens[0].Name != "todo" || h.opens[0].View != 3 {
		t.Fatalf("opens %+v", h.opens)
	}
	if len(h.events) != 4 {
		t.Fatalf("events %+v", h.events)
	}
	if h.events[1].Kind != "select" || string(h.events[1].ID()) != "abc" || h.events[1].Rev != 2 {
		t.Fatalf("select %+v", h.events[1])
	}
	if h.events[2].Kind != "activate" || string(h.events[2].ID()) != "ab" {
		t.Fatalf("activate %+v", h.events[2])
	}
	if h.events[3].Kind != "focus" || h.events[3].On {
		t.Fatalf("focus %+v", h.events[3])
	}
}

type childrenProbe struct{ retained }

func (c *childrenProbe) ViewChildren(_ *Ext, req ChildrenRequest) (*Patch, error) {
	return NewPatch().Kids(req.Node, []*Node{NewNode("x1", "X one")}, false), nil
}
func (c *childrenProbe) ViewMore(_ *Ext, req ChildrenRequest) (*Patch, error) {
	return NewPatch().Add(req.Node, []*Node{NewNode(fmt.Sprintf("m%d", req.Have), "M")}, false), nil
}

func TestChildrenRequestsAreAnswered(t *testing.T) {
	in := "0 features view-tree\n0 ready /p\n5 vkids 3 2 1\nx6 vmore 3 3 4 0\n0 bye\n"
	var out bytes.Buffer
	if err := RunOnWith(strings.NewReader(in), &out, io.Discard, []string{FeatureViewTree}, &childrenProbe{}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "ok 5 35\n1\nkids 1 1 0 19\nx2 5 0 leaf\nx1X one") {
		t.Fatalf("vkids answer: %q", got)
	}
	if !strings.Contains(got, "ok 6 29\n1\nadd 0 1 0 14\n2 1 0 leaf\nm4M") {
		t.Fatalf("vmore answer: %q", got)
	}
	p := NewPatch().Action("pin", "pin", WhereNode, "Pin").SelectMode(true)
	p.Kids(nil, []*Node{NewNode("a", "A").Inline("pin").Menu("pin", "hide")}, false)
	b, _ := p.Bytes()
	if !strings.HasPrefix(string(b), "3\naction pin pin node 3\nPinselect-mode multi 0\n") ||
		!strings.Contains(string(b), "inline 3\npinmenu 8\npin hide") {
		t.Fatalf("actions: %q", b)
	}
}
