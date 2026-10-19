package rhunext

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestScmPanelEncode(t *testing.T) {
	p := ScmPanel{
		Status:      "default",
		Placeholder: "Message",
		Actions:     []ScmAction{{Icon: "check", Command: "commit", Label: "Commit"}},
		Groups: []ScmGroup{{ID: "changes", Label: "Changes", Actions: []ScmAction{{Command: "revert_all"}},
			Resources: []ScmResource{
				{Path: "a.txt", Code: 'M', Color: "modified", Click: ScmAction{Command: "open_change", Args: []byte("a.txt")},
					Actions: []ScmAction{{Command: "revert", Args: []byte("a.txt"), Label: "Revert"}}},
				{Path: "gone.txt", Code: 'R', Tip: "renamed"},
			}}},
	}
	b, err := p.Encode()
	want := "7 7 1 1\ndefaultMessage" +
		"action check commit 0 6\nCommit" +
		"group changes 7 1 2\nChanges" + "action - revert_all 0 0\n" +
		"res M modified 5 2\na.txt" + "command open_change 5\na.txt" + "action - revert 5 6\na.txtRevert" +
		"res R - 8 1\ngone.txt" + "tip 7\nrenamed"
	if err != nil || string(b) != want {
		t.Fatalf("%q %v\nwant %q", b, err, want)
	}
	bad := []ScmPanel{
		{Status: "two\nlines"},
		{Actions: make([]ScmAction, 5)},
		{Actions: []ScmAction{{Command: "Bad"}}},
		{Groups: []ScmGroup{{ID: "g", Resources: []ScmResource{{Path: "a", Code: 'M', Color: "pink"}}}}},
		{Groups: []ScmGroup{{ID: "has space"}}},
		{Groups: []ScmGroup{{ID: "g", Resources: []ScmResource{{Path: "", Code: 'M'}}}}},
		{Actions: []ScmAction{{Command: "x", Label: strings.Repeat("l", 201)}}},
		{Groups: []ScmGroup{{ID: "g", Resources: []ScmResource{{Path: "a", Code: 'M', Tip: strings.Repeat("t", 1001)}}}}},
	}
	for i, p := range bad {
		if _, err := p.Encode(); err == nil {
			t.Errorf("bad panel %d accepted", i)
		}
	}
}

type scmProbe struct {
	ready chan *Ext
	runs  chan string
}

func (p *scmProbe) Ready(e *Ext, _ string)          { p.ready <- e }
func (p *scmProbe) Run(_ *Ext, cmd string, doc Doc) { p.runs <- "run " + cmd }
func (p *scmProbe) Event(*Ext, Event)               {}
func (p *scmProbe) Invoke(_ *Ext, inv Invocation) {
	if inv.From == "" {
		p.runs <- "run " + inv.Command
		return
	}
	p.runs <- "args " + inv.Command + " " + inv.From + " " + string(inv.Body)
}

func TestScmRequestsAndCommandArgs(t *testing.T) {
	p := &scmProbe{ready: make(chan *Ext, 1), runs: make(chan string, 4)}
	s := startHost(t, []string{FeatureScm, FeatureCommandArgs}, p)
	s.expect("hello 1 scm command-args")
	s.send("0 features scm command-args\n0 ready /project\n")
	ext := <-p.ready
	done := make(chan error, 1)
	go func() {
		h, err := ext.ScmRegister(context.Background(), "/r", "Hg")
		if err == nil && h != 3 {
			err = errors.New("handle")
		}
		if err == nil {
			err = ext.ScmUpdate(context.Background(), h, ScmPanel{Status: "s"})
		}
		if err == nil {
			var in string
			in, err = ext.ScmInput(context.Background(), h)
			if err == nil && in != "msg" {
				err = errors.New("input " + in)
			}
		}
		if err == nil {
			err = ext.ScmInputSet(context.Background(), h, "")
		}
		done <- err
	}()
	s.expect("1 scm-register 8")
	readBody(t, s, "2 2\n/rHg")
	s.send("ok 1 3\n")
	s.expect("2 scm-update 3 9")
	readBody(t, s, "1 0 0 0\ns")
	s.send("ok 2\n")
	s.expect("3 scm-input 3")
	s.send("ok 3 3\nmsg")
	s.expect("4 scm-input-set 3 0")
	s.send("ok 4\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.send("0 run hg.open_change 0 ref 5\na.txt0 run hg.refresh 0\n")
	if r := <-p.runs; r != "args hg.open_change ref a.txt" {
		t.Fatal(r)
	}
	if r := <-p.runs; r != "run hg.refresh" {
		t.Fatal(r)
	}
	s.send("0 bye\n")
	<-s.done
}
