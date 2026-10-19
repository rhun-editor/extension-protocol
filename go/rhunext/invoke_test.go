package rhunext

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type invokeProbe struct {
	ready chan *Ext
	invs  chan Invocation
	runs  chan string
	calls chan Call
	wait  bool
}

func (p *invokeProbe) Ready(e *Ext, _ string)            { p.ready <- e }
func (p *invokeProbe) Run(_ *Ext, command string, _ Doc) { p.runs <- command }
func (p *invokeProbe) Event(*Ext, Event)                 {}
func (p *invokeProbe) Invoke(_ *Ext, inv Invocation)     { p.invs <- inv }
func (p *invokeProbe) Call(_ *Ext, c Call) ([]byte, error) {
	p.calls <- c
	switch {
	case p.wait:
		<-c.Context.Done()
		return nil, errors.New("stopped")
	case string(c.Args) == "fail":
		return nil, errors.New("cannot")
	case string(c.Args) == "nodoc":
		return nil, &Error{Code: "nodoc", Text: "no document"}
	}
	return append([]byte("got "), c.Args...), nil
}

func newInvokeProbe() *invokeProbe {
	return &invokeProbe{ready: make(chan *Ext, 1), invs: make(chan Invocation, 8), runs: make(chan string, 8),
		calls: make(chan Call, 8)}
}

func TestRunBodiesWithFromAndTargets(t *testing.T) {
	p := newInvokeProbe()
	s := startHost(t, []string{FeatureCommandArgs}, p)
	s.expect("hello 1 command-args")
	s.send("0 features command-args\n0 ready /project\n")
	<-p.ready
	s.send("0 run demo.go 3 key 4\nfast0 run demo.go 0 palette 0\n")
	s.send("0 run demo.copy 2 menu 26\ntab doc 2 /p/a b.txt\nextra")
	s.send("0 run demo.old 5\n")
	inv := <-p.invs
	if inv.Command != "demo.go" || inv.Name() != "go" || inv.Doc != 3 || inv.From != FromKey || string(inv.Body) != "fast" {
		t.Fatalf("%+v", inv)
	}
	if inv = <-p.invs; inv.From != FromPalette || len(inv.Body) != 0 || inv.Doc != 0 {
		t.Fatalf("%+v", inv)
	}
	inv = <-p.invs
	target, args, ok := inv.Target()
	if !ok || target.Kind != "tab" || target.Path() != "/p/a b.txt" || string(args) != "extra" {
		t.Fatalf("%+v %q %v", target, args, ok)
	}
	if inv = <-p.invs; inv.Command != "demo.old" || inv.From != "" || inv.Body != nil {
		t.Fatalf("v1 run: %+v", inv)
	}
	s.send("0 bye\n")
	<-s.done
}

type runOnly struct {
	ready chan *Ext
	runs  chan string
}

func (p *runOnly) Ready(e *Ext, _ string)            { p.ready <- e }
func (p *runOnly) Run(_ *Ext, command string, _ Doc) { p.runs <- command }
func (p *runOnly) Event(*Ext, Event)                 {}

func TestRunWithoutInvokerSkipsTheBody(t *testing.T) {
	p := &runOnly{ready: make(chan *Ext, 1), runs: make(chan string, 4)}
	s := startHost(t, []string{FeatureCommandArgs}, p)
	s.expect("hello 1 command-args")
	s.send("0 features command-args\n0 ready -\n0 run demo.a 0 ref 5\n0 run0 run demo.b 0 palette 0\n")
	if a, b := <-p.runs, <-p.runs; a != "demo.a" || b != "demo.b" {
		t.Fatal(a, b)
	}
	s.send("0 bye\n")
	<-s.done
}

func TestMenuTargets(t *testing.T) {
	for _, c := range []struct{ line, kind, path string }{
		{"file /a b/c.txt", "file", "/a b/c.txt"}, {"root /p", "root", "/p"}, {"tab doc 3 -", "tab", ""},
		{"tab image /x.png", "tab", "/x.png"}, {"item alice/demo.files src/lib x", "item", ""},
		{"commit abc /p/f g", "commit", "/p/f g"}, {"editor 4 1 1 1 1", "editor", ""}, {"extension", "extension", ""},
	} {
		tg := ParseMenuTarget(c.line)
		if tg.Kind != c.kind || tg.Path() != c.path {
			t.Fatalf("%q: %+v %q", c.line, tg, tg.Path())
		}
	}
	if tg := ParseMenuTarget("item alice/demo.files src/lib x"); len(tg.Fields) != 2 || tg.Fields[1] != "src/lib x" {
		t.Fatalf("%+v", tg)
	}
}

func TestCommandRefs(t *testing.T) {
	b, err := CommandRef("run_task", []byte("all\nx"))
	if err != nil || string(b) != "command run_task 5\nall\nx" {
		t.Fatalf("%q %v", b, err)
	}
	for _, name := range []string{"", "Run", "a.b", "x y", strings.Repeat("a", 41)} {
		if _, err := CommandRef(name, nil); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
	if _, err := CommandRef("a", make([]byte, MaxRefArgs+1)); err == nil {
		t.Fatal("8193 bytes of args accepted")
	}
}

func TestRunCommandAndCall(t *testing.T) {
	p := newInvokeProbe()
	s := startHost(t, []string{FeatureCommandArgs}, p)
	s.expect("hello 1 command-args")
	s.send("0 features command-args\n0 ready -\n")
	ext := <-p.ready
	done := make(chan error, 1)
	go func() {
		res, err := ext.RunCommand("bob/tools.format", []byte("x y"))
		if err == nil && string(res) != "done" {
			err = errors.New("result " + string(res))
		}
		if err == nil {
			_, err = ext.RunCommand("goto_line", []byte("12"))
		}
		if err == nil {
			_, err = ext.RunCommand("demo.self", nil)
			if e, ok := err.(*Error); ok && e.Code == "refused" {
				err = nil
			} else {
				err = errors.New("not refused")
			}
		}
		done <- err
	}()
	s.expect("1 run-command bob/tools.format 3")
	readBody(t, s, "x y")
	s.send("ok 1 4\ndone")
	s.expect("2 run-command goto_line 2")
	readBody(t, s, "12")
	s.send("ok 2 0\n")
	s.expect("3 run-command demo.self 0")
	s.send("error 3 refused its own command\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// calls from other extensions
	s.send("7 call demo.fmt 4 bob/tools 3\nabc")
	s.answer(7, "got abc")
	if c := <-p.calls; c.Caller != "bob/tools" || c.Doc != 4 || c.Name() != "fmt" {
		t.Fatalf("%+v", c)
	}
	s.send("8 call demo.fmt 0 bob/tools 4\nfail")
	s.expect("error 8 refused cannot")
	s.send("9 call demo.fmt 0 bob/tools 5\nnodoc")
	s.expect("error 9 nodoc no document")
	<-p.calls
	<-p.calls
	p.wait = true
	s.send("10 call demo.fmt 0 bob/tools 0\n")
	select {
	case c := <-p.calls:
		if c.ID != 10 {
			t.Fatalf("%+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("no call")
	}
	s.send("0 cancel 10\n")
	s.expect("error 10 cancelled the call was cancelled")
	s.send("0 bye\n")
	<-s.done
}

func TestCallWithoutCalleeOrFeature(t *testing.T) {
	p := &runOnly{ready: make(chan *Ext, 1), runs: make(chan string, 4)}
	s := startHost(t, []string{FeatureCommandArgs}, p)
	s.expect("hello 1 command-args")
	s.send("0 features command-args\n0 ready -\n4 call demo.x 0 bob/t 2\nhi")
	s.expect("error 4 unknown no such request")
	s.send("0 bye\n")
	<-s.done
	ext := &Ext{agreed: map[string]bool{}}
	if _, err := ext.RunCommand("find", nil); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatal(err)
	}
}

func TestContextKeys(t *testing.T) {
	p := newInvokeProbe()
	s := startHost(t, []string{FeatureContextKeys}, p)
	s.expect("hello 1 context-keys")
	s.send("0 features context-keys\n0 ready -\n")
	ext := <-p.ready
	done := make(chan error, 1)
	go func() {
		err := ext.SetContext("mode", "insert here")
		if err == nil {
			err = ext.RemoveContext("mode")
		}
		done <- err
	}()
	s.expect("1 set-context mode insert here")
	s.send("ok 1\n")
	s.expect("2 set-context mode -")
	s.send("ok 2\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"Mode", "x"}, {"1a", "x"}, {"a.b", "x"}, {strings.Repeat("a", 41), "x"},
		{"a", ""}, {"a", "x\ny"}, {"a", "‮x"}, {"a", strings.Repeat("v", 257)}, {"a", "-"}} {
		if err := ext.SetContext(kv[0], kv[1]); err == nil {
			t.Fatalf("%q = %q accepted", kv[0], kv[1])
		}
	}
	s.send("0 bye\n")
	<-s.done
}

func TestStatusItems(t *testing.T) {
	it := StatusItem{Name: "build", Align: "left", Priority: 30, Text: "$(gear) Build", Tooltip: "Builds it",
		Command: "run_task", Args: []byte("all"), Color: "accent", Background: "error", When: "projectOpen"}
	b, err := it.Encode()
	want := "left 30 13 9 75\n$(gear) BuildBuilds itcommand run_task 3\nallcolor accent 0\nbackground error 0\nwhen 11\nprojectOpen"
	if err != nil || string(b) != want {
		t.Fatalf("%q %v", b, err)
	}
	if b, _ := (StatusItem{Name: "x", Text: "x"}).Encode(); string(b) != "right 0 1 0 0\nx" {
		t.Fatalf("%q", b)
	}
	for _, bad := range []StatusItem{{Name: "X"}, {Name: "a", Align: "up"}, {Name: "a", Priority: 1001},
		{Name: "a", Text: strings.Repeat("x", 121)}, {Name: "a", Text: "a\tb"}, {Name: "a", Color: "pink"},
		{Name: "a", Background: "accent"}, {Name: "a", Command: "Bad"}, {Name: "a", Tooltip: strings.Repeat("t", 501)}} {
		if _, err := bad.Encode(); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	p := newInvokeProbe()
	s := startHost(t, []string{FeatureStatusItems}, p)
	s.expect("hello 1 status-items")
	s.send("0 features status-items\n0 ready -\n")
	ext := <-p.ready
	done := make(chan error, 1)
	go func() {
		err := ext.SetStatusItem(StatusItem{Name: "go", Text: "Go"})
		if err == nil {
			err = ext.RemoveStatusItem("go")
		}
		done <- err
	}()
	s.expect("1 status-item go 16")
	readBody(t, s, "right 0 2 0 0\nGo")
	s.send("ok 1\n")
	s.expect("2 status-remove go")
	s.send("ok 2\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.send("0 bye\n")
	<-s.done
}
