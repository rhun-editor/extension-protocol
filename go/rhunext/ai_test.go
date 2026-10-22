package rhunext

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestSessionsAndTranscriptsAreFramedAsRhunReadsThem(t *testing.T) {
	s := NewSession("s1", "Fix it")
	s.Updated, s.State, s.Model, s.Cwd = 12, SessionWorking, "m", "/p"
	b, err := EncodeSessions([]Session{s})
	if err != nil || string(b) != "1\n12 working 2 6 1 2\ns1Fix itm/p" {
		t.Fatalf("%q %v", b, err)
	}
	for _, bad := range [][]Session{{NewSession("a b", "")}, {{ID: "a", Cwd: "rel"}}, {NewSession("a", ""), NewSession("a", "")}} {
		if _, err := EncodeSessions(bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	all := []Message{{Role: RoleUser, Text: "hi"}, {Role: RoleTool, Name: "read_file", Text: "src/main.rs"}}
	tr := TranscriptPage(all, 1)
	if got := string(tr.Encode()); got != "2 1\ntool 9 11 0\nread_filesrc/main.rs" {
		t.Fatalf("%q", got)
	}
	back, err := ParseTranscript(tr.Encode())
	if err != nil || back.Total != 2 || back.Messages[0] != all[1] {
		t.Fatalf("%v %v", back, err)
	}
}

func TestProposalEventsAndTools(t *testing.T) {
	ev := Event{Name: "proposal", Args: strings.Fields("3 1 partial 1 2")}
	if p, ok := ParseProposal(ev); !ok || p.State != "partial" || p.Accepted != 1 || p.Hunks != 2 {
		t.Fatalf("%v %v", p, ok)
	}
	ev = Event{Name: "proposal", Args: strings.Fields("3 done 1 1")}
	if p, ok := ParseProposal(ev); !ok || p.State != "done" || p.Accepted != 1 || p.Rejected != 1 {
		t.Fatalf("%v %v", p, ok)
	}
	ts, err := ParseTools([]byte("1\nrhun.read ro 4 1 9\nReaddpath PATH"))
	if err != nil || len(ts) != 1 || !ts[0].Readonly || ts[0].Input != "path PATH" {
		t.Fatalf("%v %v", ts, err)
	}
	in := (&ToolInput{}).Set("path", "a b").Bytes("text", []byte("x\ny")).Encode()
	if string(in) != "path a b\ntext 3\nx\ny" {
		t.Fatalf("%q", in)
	}
}

type aiProbe struct {
	out chan []byte
}

func (p *aiProbe) Ready(e *Ext, project string) {
	go func() {
		out, err := e.CallTool("rhun.read", []byte("path a.txt\n"))
		if err != nil {
			out = []byte(err.Error())
		}
		p.out <- out
	}()
}
func (p *aiProbe) Event(e *Ext, ev Event)              {}
func (p *aiProbe) Run(e *Ext, command string, doc Doc) {}
func (p *aiProbe) AgentTranscript(e *Ext, sid string, from int64) (Transcript, error) {
	return TranscriptPage([]Message{{Role: RoleUser, Text: sid}}, from), nil
}
func (p *aiProbe) Tool(e *Ext, c ToolCall) ([]byte, error) {
	if string(c.Input) == "fail" {
		return nil, &Error{Code: "busy", Text: "later"}
	}
	return []byte(c.Name + " for " + c.Caller + ": " + string(c.Input)), nil
}

func TestToolsAndTranscriptsOverTheWire(t *testing.T) {
	toExt, hostW := io.Pipe()
	hostR, fromExt := io.Pipe()
	p := &aiProbe{out: make(chan []byte, 1)}
	done := make(chan error, 1)
	go func() { done <- RunOnWith(toExt, fromExt, io.Discard, []string{FeatureAgents, FeatureTools}, p) }()
	r := bufio.NewReader(hostR)
	line := func() string {
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := line(); l != "hello 1 agents tools\n" {
		t.Fatalf("%q", l)
	}
	io.WriteString(hostW, "0 features agents tools\n0 ready -\n")
	if l := line(); l != "1 tool-call rhun.read 11\n" {
		t.Fatalf("%q", l)
	}
	body := make([]byte, 11)
	io.ReadFull(r, body)
	io.WriteString(hostW, "ok 1 5\nhello")
	if got := <-p.out; string(got) != "hello" {
		t.Fatalf("%q", got)
	}
	io.WriteString(hostW, "7 tool search acme/agent 4\nfold")
	if l := line(); l != "ok 7 27\n" {
		t.Fatalf("%q", l)
	}
	ans := make([]byte, 27)
	io.ReadFull(r, ans)
	if string(ans) != "search for acme/agent: fold" {
		t.Fatalf("%q", ans)
	}
	io.WriteString(hostW, "8 tool search acme/agent 4\nfail")
	if l := line(); l != "error 8 busy later\n" {
		t.Fatalf("%q", l)
	}
	io.WriteString(hostW, "9 agent-transcript s1 0\n")
	if l := line(); l != "ok 9 17\n" {
		t.Fatalf("%q", l)
	}
	tr := make([]byte, 17)
	io.ReadFull(r, tr)
	if string(tr) != "1 1\nuser 0 2 0\ns1" {
		t.Fatalf("%q", tr)
	}
	io.WriteString(hostW, "0 bye\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProposalsAre12gWhateverWasAgreed(t *testing.T) {
	var w WorkspaceEdit
	at := Pos{Line: 1, Col: 1}
	w.AddFileHashed("/p/a.txt", []byte("x\r\n"), TextEdit{Range: Range{Start: at, End: at}, Text: "y"})
	w.Label, w.Confirm = "Fix", true
	b, bad := proposalBody("L", "s1", w)
	if bad != nil || string(b) != "1 2\nLs1"+"1\n8 - 1\n/p/a.txt1 1 1 1 1\ny" {
		t.Fatalf("%q %v", b, bad)
	}
	var ops WorkspaceEdit
	ops.Create("/p/b.txt", CreateOptions{})
	if _, bad := proposalBody("L", "", ops); bad == nil || bad.Code != "unknown" {
		t.Fatalf("file operations: %v", bad)
	}
}

type askProbe struct {
	got chan string
}

func (p *askProbe) Ready(e *Ext, project string) {
	go func() {
		i, err := e.AgentAsk("s1", "Run the tests?", []string{"Yes", "No"})
		p.got <- fmt.Sprint(i, err)
		_, err = e.AgentAsk("s1", "Again?", []string{"Yes", "No"})
		p.got <- fmt.Sprint(err)
	}()
}
func (p *askProbe) Event(e *Ext, ev Event)                               {}
func (p *askProbe) Run(e *Ext, command string, doc Doc)                  {}
func (p *askProbe) AgentPrompt(e *Ext, sid, text string) (string, error) { return sid, nil }
func (p *askProbe) AgentStop(e *Ext, sid string)                         {}

// The wire's answer counts options from 1; AgentAsk returns an index into options, from 0.
func TestAgentAskReturnsAnIndexFromZero(t *testing.T) {
	toExt, hostW := io.Pipe()
	hostR, fromExt := io.Pipe()
	p := &askProbe{got: make(chan string, 2)}
	done := make(chan error, 1)
	go func() { done <- RunOnWith(toExt, fromExt, io.Discard, []string{FeatureAgentPrompts}, p) }()
	r := bufio.NewReader(hostR)
	line := func() string {
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := line(); l != "hello 1 agent-prompts\n" {
		t.Fatalf("%q", l)
	}
	io.WriteString(hostW, "0 features agent-prompts\n0 ready -\n")
	if l := line(); l != "1 agent-ask s1 21\n" {
		t.Fatalf("%q", l)
	}
	body := make([]byte, 21)
	io.ReadFull(r, body)
	if string(body) != "Run the tests?\nYes\nNo" {
		t.Fatalf("%q", body)
	}
	io.WriteString(hostW, "ok 1 2\n") // the user picked No
	if got := <-p.got; got != "1 <nil>" {
		t.Fatalf("%q", got)
	}
	if l := line(); l != "2 agent-ask s1 13\n" {
		t.Fatalf("%q", l)
	}
	io.ReadFull(r, make([]byte, 13))
	io.WriteString(hostW, "error 2 cancelled closed\n")
	if got := <-p.got; !strings.Contains(got, "cancelled") {
		t.Fatalf("%q", got)
	}
	io.WriteString(hostW, "0 bye\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
