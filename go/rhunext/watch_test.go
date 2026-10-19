package rhunext

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestParseWorkspaceWatch(t *testing.T) {
	for _, kind := range []string{"changed", "rescan"} {
		watch, event, ok := ParseWorkspaceWatch(Event{Name: "watch", Args: []string{"7", kind}})
		if !ok || watch != 7 || event != kind {
			t.Fatal(watch, event, ok)
		}
	}
	for _, ev := range []Event{{Name: "watch"}, {Name: "watch", Args: []string{"0", "changed"}}, {Name: "watch", Args: []string{"-1", "changed"}}, {Name: "watch", Args: []string{"+1", "changed"}}, {Name: "watch", Args: []string{"2147483648", "changed"}}, {Name: "watch", Args: []string{"1", "unknown"}}, {Name: "watch", Args: []string{"1", "rescan", "extra"}}, {Name: "other", Args: []string{"1", "changed"}}} {
		if _, _, ok := ParseWorkspaceWatch(ev); ok {
			t.Fatal(ev)
		}
	}
}

type workspaceWatchProbe struct {
	watch WorkspaceWatch
	err   chan error
}

func (p *workspaceWatchProbe) Ready(e *Ext, project string) {
	var err error
	p.watch, err = e.WatchWorkspace()
	if err == nil {
		err = e.UnwatchWorkspace(p.watch)
	}
	p.err <- err
}
func (p *workspaceWatchProbe) Event(*Ext, Event)     {}
func (p *workspaceWatchProbe) Run(*Ext, string, Doc) {}
func TestWorkspaceWatchNegotiationAndWireLifecycle(t *testing.T) {
	input, hostW := io.Pipe()
	hostR, output := io.Pipe()
	defer input.Close()
	defer hostW.Close()
	defer hostR.Close()
	defer output.Close()
	probe := &workspaceWatchProbe{err: make(chan error, 1)}
	done := make(chan error, 1)
	go func() { done <- RunOnWith(input, output, io.Discard, []string{FeatureWorkspaceWatch}, probe) }()
	reader := bufio.NewReader(hostR)
	line, err := reader.ReadString('\n')
	if err != nil || line != "hello 1 workspace_watch\n" {
		t.Fatal(line, err)
	}
	io.WriteString(hostW, "0 features workspace_watch\n0 ready /project\n")
	line, err = reader.ReadString('\n')
	if err != nil || line != "1 watch-workspace\n" {
		t.Fatal(line, err)
	}
	io.WriteString(hostW, "ok 1 7\n")
	line, err = reader.ReadString('\n')
	if err != nil || line != "2 unwatch-workspace 7\n" {
		t.Fatal(line, err)
	}
	io.WriteString(hostW, "ok 2\n")
	if err := <-probe.err; err != nil || probe.watch != 7 {
		t.Fatal(probe.watch, err)
	}
	io.WriteString(hostW, "0 bye\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceWatchWithoutNegotiationDoesNotWrite(t *testing.T) {
	probe := &workspaceWatchProbe{err: make(chan error, 1)}
	var out bytes.Buffer
	if err := RunOnWith(strings.NewReader("0 ready /project\n0 bye\n"), &out, io.Discard, []string{FeatureWorkspaceWatch}, probe); err != nil {
		t.Fatal(err)
	}
	if err, ok := (<-probe.err).(*Error); !ok || err.Code != "unknown" {
		t.Fatal(err)
	}
	if out.String() != "hello 1 workspace_watch\n" {
		t.Fatal(out.String())
	}
}
