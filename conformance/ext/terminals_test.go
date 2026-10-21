package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func termHost(t *testing.T, level string) (*host, *sink) {
	h, _ := testHost(t)
	out := &sink{}
	h.stdin = out
	h.m.needs = map[string]string{"terminal": level}
	h.features = map[string]bool{"terminals": true, "tasks": true}
	return h, out
}

func TestCheckerRunsPublishedTasksAndPlaysTheirEvents(t *testing.T) {
	h, out := termHost(t, "run")
	list, err := rhunext.EncodeTaskList([]rhunext.Task{
		{ID: "build", Label: "Build", Program: rhunext.Program{Shell: "go build ./..."}, Group: "build", Default: true, Matchers: []rhunext.Matcher{{Builtin: "go"}}},
		{ID: "serve", Label: "Serve", Program: rhunext.Program{Argv: []string{"python3", "-m", "http.server"}}, Ready: []string{"Serving HTTP on {_}"}, Depends: []string{"build"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var replies []string
	reply := func(s string) { replies = append(replies, s) }
	rerr := func(code, text string) { replies = append(replies, "error "+code+" "+text) }
	h.termRequest(1, "tasks-set", []string{"0"}, message{body: list}, reply, rerr) // LEN wrong
	if len(h.fails) != 1 {
		t.Fatal(h.fails)
	}
	h.fails = nil
	h.termRequest(2, "tasks-set", []string{itoaLen(list)}, message{body: list}, reply, rerr)
	h.termRequest(3, "task-start", []string{"serve"}, message{}, reply, rerr)
	if len(h.fails) != 0 || replies[len(replies)-2] != "ok 2" || replies[len(replies)-1] != "ok 3 2" {
		t.Fatal(h.fails, replies)
	}
	events := out.String()
	for _, want := range []string{"0 terminal-started 1 4001 build\n", "0 terminal-ended 1 exit 0 0 0\n", "0 terminal-started 2 4002 serve\n", "0 task-ready 2 0 0\n"} {
		if !strings.Contains(events, want) {
			t.Fatalf("%q has no %q", events, want)
		}
	}
	h.termRequest(4, "terminal-stop", []string{"2"}, message{}, reply, rerr)
	if !strings.Contains(out.String(), "0 terminal-ended 2 stopped 130 0 0\n") {
		t.Fatal(out.String())
	}
	h.termRequest(5, "task-start", []string{"nothing"}, message{}, reply, rerr)
	if replies[len(replies)-1] != "error notask no such published task" || len(h.fails) != 1 {
		t.Fatal(replies, h.fails)
	}
}

func TestCheckerRefusesWhatRhunRefuses(t *testing.T) {
	h, _ := termHost(t, "suggest")
	var last string
	reply := func(s string) { last = s }
	rerr := func(code, text string) { last = "error " + code + " " + text }
	def := []byte("- shell 2 4 0 0\nhiecho")
	h.termRequest(1, "terminal-open", []string{"22"}, message{body: def}, reply, rerr)
	if last != "error needs terminal=run" {
		t.Fatal(last)
	}
	h, _ = termHost(t, "run")
	h.termRequest(2, "terminal-open", []string{"22"}, message{body: def}, reply, rerr)
	if last != "ok 2 1" {
		t.Fatal(last, h.fails)
	}
	bad := []byte("- shell 2 4 0 14\nhiechogroup build 0\n")
	h.termRequest(3, "terminal-open", []string{itoaLen(bad)}, message{body: bad}, reply, rerr)
	if !strings.Contains(last, "not in a terminal") {
		t.Fatal(last)
	}
	h.termRequest(4, "terminal-input", []string{"1", "3"}, message{body: []byte("ls\n")}, reply, rerr)
	if last != "error needs terminal=input" {
		t.Fatal(last)
	}
	h.termRequest(5, "terminal-stop", []string{"99"}, message{}, reply, rerr)
	if last != "error noterm no such terminal" {
		t.Fatal(last)
	}
	h.features = map[string]bool{"terminals": true}
	h.termRequest(6, "tasks-set", []string{"2"}, message{body: []byte("0\n")}, reply, rerr)
	if last != "error unknown no such request" {
		t.Fatal(last)
	}
}

func TestCheckerAgreesTasksOnlyWithTerminals(t *testing.T) {
	h, out := termHost(t, "run")
	h.agree("hello 1 tasks")
	if h.features["tasks"] || out.Len() != 0 {
		t.Fatal(h.features, out.String())
	}
	h.agree("hello 1 terminals tasks")
	if !h.features["tasks"] || !strings.Contains(out.String(), "0 features terminals tasks\n") {
		t.Fatal(h.features, out.String())
	}
}

func itoaLen(b []byte) string { return strconv.Itoa(len(b)) }
