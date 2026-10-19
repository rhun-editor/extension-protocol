package rhunext

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

type commandProbe struct {
	name string
	err  chan error
}

func (p *commandProbe) Ready(e *Ext, project string)        { p.err <- e.Unregister(p.name) }
func (p *commandProbe) Event(e *Ext, ev Event)              {}
func (p *commandProbe) Run(e *Ext, command string, doc Doc) {}

func TestUnregisterDoesNotSendUnnegotiatedRequests(t *testing.T) {
	p := &commandProbe{name: "old", err: make(chan error, 1)}
	var out bytes.Buffer
	if err := RunOnWith(strings.NewReader("0 ready -\n0 bye\n"), &out, io.Discard, []string{FeatureCommands}, p); err != nil {
		t.Fatal(err)
	}
	err := <-p.err
	if e, ok := err.(*Error); !ok || e.Code != "unknown" {
		t.Fatalf("unnegotiated unregister: %v", err)
	}
	if out.String() != "hello 1 commands\n" {
		t.Fatalf("unexpected wire traffic: %q", out.String())
	}
}

func TestUnregisterNegotiatesAndPropagatesHostRefusal(t *testing.T) {
	input, hostW := io.Pipe()
	hostR, output := io.Pipe()
	defer input.Close()
	defer hostW.Close()
	defer hostR.Close()
	defer output.Close()
	p := &commandProbe{name: "manifest", err: make(chan error, 1)}
	done := make(chan error, 1)
	go func() { done <- RunOnWith(input, output, io.Discard, []string{FeatureCommands}, p) }()
	reader := bufio.NewReader(hostR)
	line, err := reader.ReadString('\n')
	if err != nil || line != "hello 1 commands\n" {
		t.Fatalf("hello %q, %v", line, err)
	}
	io.WriteString(hostW, "0 features commands\n0 ready -\n")
	line, err = reader.ReadString('\n')
	if err != nil || line != "1 unregister manifest\n" {
		t.Fatalf("unregister %q, %v", line, err)
	}
	io.WriteString(hostW, "error 1 refused not a runtime command\n")
	if err := <-p.err; err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("host refusal: %v", err)
	}
	io.WriteString(hostW, "0 bye\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUnregisterRejectsInvalidNameBeforeWriting(t *testing.T) {
	for _, name := range []string{"", "x\n2 unregister other", "UPPER", strings.Repeat("x", 41)} {
		p := &commandProbe{name: name, err: make(chan error, 1)}
		var out bytes.Buffer
		if err := RunOnWith(strings.NewReader("0 features commands\n0 ready -\n0 bye\n"), &out, io.Discard, []string{FeatureCommands}, p); err != nil {
			t.Fatal(err)
		}
		if e, ok := (<-p.err).(*Error); !ok || e.Code != "syntax" {
			t.Fatalf("invalid name %q: %v", name, e)
		}
		if out.String() != "hello 1 commands\n" {
			t.Fatalf("unexpected wire traffic for %q: %q", name, out.String())
		}
	}
}
