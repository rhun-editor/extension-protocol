package rhunext

import (
	"context"
	"io"
	"testing"
)

func TestDebugConfigRoundTrip(t *testing.T) {
	var c DebugConfig
	c.Set("type", DebugStr("go"))
	c.Set("program", DebugStr("${project}"))
	c.SetList("args", []string{"-test.run", "^TestAdd$"})
	c.Set("env.LOG", DebugStr("debug"))
	c.Set("count", DebugInt(-7))
	c.Set("stopOnEntry", DebugBoolean(true))
	c.Set("extra", DebugNil())
	b, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, []byte("context 3 4 5 6\n/a/b.gounknown 1 2 3\nxyz")...)
	got, ctx, err := ParseDebugConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(c) {
		t.Fatalf("leaves %v", got)
	}
	for i := range c {
		if got[i] != c[i] {
			t.Fatalf("leaf %d: %v != %v", i, got[i], c[i])
		}
	}
	if l := got.List("args"); len(l) != 2 || l[1] != "^TestAdd$" {
		t.Fatalf("list %v", l)
	}
	if ctx.Doc != 3 || ctx.At != (Pos{4, 5}) || ctx.Path != "/a/b.g" {
		t.Fatalf("context %+v", ctx)
	}
	got.Remove("args")
	if len(got.List("args")) != 0 || got.String("type") != "go" {
		t.Fatal("remove")
	}
	for _, bad := range []string{"1\nx 1 1\nab", "1\nn 1 2\na1x", "1\nb 1 3\nayes", "1\nz 1 1\naa", "2\ns 1 1\nab", "x"} {
		if _, _, err := ParseDebugConfig([]byte(bad)); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

type debugProbe struct {
	ready chan *Ext
	ended chan DebugEnded
}

func (p *debugProbe) Ready(ext *Ext, project string)        { p.ready <- ext }
func (p *debugProbe) Run(ext *Ext, command string, doc Doc) {}
func (p *debugProbe) Event(ext *Ext, ev Event) {
	if d, ok := ParseDebugEnded(ev); ok {
		p.ended <- d
	}
}

func TestDebugStartAndEnded(t *testing.T) {
	p := &debugProbe{ready: make(chan *Ext, 1), ended: make(chan DebugEnded, 2)}
	s := startHost(t, []string{FeatureDebug}, p)
	s.expect("hello 1 debug")
	s.send("0 features debug\n0 ready /project\n")
	ext := <-p.ready
	got := make(chan int64, 1)
	errs := make(chan error, 1)
	go func() {
		var c DebugConfig
		c.Set("type", DebugStr("go"))
		id, err := ext.DebugStart(context.Background(), "Test", c)
		got <- id
		errs <- err
	}()
	s.expect("1 debug-start 4 18")
	body := make([]byte, 18)
	if _, err := io.ReadFull(s.br, body); err != nil || string(body) != "Test1\ns 4 2\ntypego" {
		t.Fatalf("body %q", body)
	}
	s.send("ok 1 7\n")
	if id := <-got; id != 7 || <-errs != nil {
		t.Fatalf("session %d", id)
	}
	s.send("0 debug-ended 7 3\n0 debug-ended 8 -\n")
	if d := <-p.ended; d != (DebugEnded{7, 3, true}) {
		t.Fatalf("ended %+v", d)
	}
	if d := <-p.ended; d != (DebugEnded{8, 0, false}) {
		t.Fatalf("ended %+v", d)
	}
}
