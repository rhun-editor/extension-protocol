package main

// Terminals and tasks (protocol 1.1): the checker agrees terminals and tasks (tasks only with
// terminals), checks every definition and task list exactly as rhun does (the Go SDK's parser is
// rhun's grammar, ids, depends, cycles and patterns), checks each verb against [needs] terminal,
// answers starts with made-up TERMs and plays the events a real run would send: terminal-started,
// terminal-output (with DROPPED) for a terminal opened with output, task-ready for a background
// task, terminal-ended and terminal-closed. A write the extension does not read fails the run.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

// terminalLevel is the [needs] terminal ladder: no, suggest (= yes), run, input, output
func terminalLevel(v string) int {
	switch v {
	case "yes", "suggest":
		return 1
	case "run":
		return 2
	case "input":
		return 3
	case "output":
		return 4
	}
	return 0
}

// publishedTasks are an extension's tasks by id, as its last tasks-set had them
type publishedTasks = map[string]rhunext.Definition

type checkerTerm struct {
	label   string
	task    string // "-" for a terminal
	running bool
}

// termRequest serves terminal-* and task* requests.
func (h *host) termRequest(id int64, verb string, args []string, msg message, reply func(string), rerr func(string, string)) {
	feature := "terminals"
	if strings.HasPrefix(verb, "task") {
		feature = "tasks"
	}
	if !h.features[feature] {
		h.fail(feature, verb+" without "+feature+" agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	level := terminalLevel(h.m.needs["terminal"])
	want := 2
	if verb == "terminal-input" {
		want = 3
	} else if verb == "terminal-screen" {
		want = 4
	}
	if level < want {
		name := []string{"", "", "run", "input", "output"}[want]
		h.fail("needs", fmt.Sprintf("%s needs terminal=%s but the manifest has %q", verb, name, h.m.needs["terminal"]))
		rerr("needs", "terminal="+name)
		return
	}
	if h.terms == nil {
		h.terms = map[uint64]*checkerTerm{}
		h.tasks = map[string]rhunext.Definition{}
	}
	bad := func(why string) {
		h.fail("request grammar", verb+": "+why)
		rerr("syntax", why)
	}
	body := func(lenField string) ([]byte, bool) {
		n, err := strconv.ParseInt(lenField, 10, 64)
		if err != nil || n < 0 || msg.err != nil || int64(len(msg.body)) != n {
			bad("LEN " + lenField + " does not match the body")
			return nil, false
		}
		return msg.body, true
	}
	term := func() (*checkerTerm, uint64, bool) {
		if len(args) < 1 {
			bad("TERM missing")
			return nil, 0, false
		}
		n, err := strconv.ParseUint(args[0], 10, 64)
		t := h.terms[n]
		if err != nil || t == nil {
			h.fail("terminals", fmt.Sprintf("%s names TERM %q, which is not one of its terminals", verb, args[0]))
			rerr("noterm", "no such terminal")
			return nil, 0, false
		}
		return t, n, true
	}
	switch verb {
	case "terminal-open", "task-run":
		if len(args) != 1 {
			bad(verb + " takes LEN")
			return
		}
		b, ok := body(args[0])
		if !ok {
			return
		}
		d, n, err := rhunext.ParseDefinition(b, verb == "task-run")
		if err == nil && n != len(b) {
			err = &rhunext.Error{Code: "syntax", Text: "definition: bytes after it"}
		}
		if err != nil {
			h.fail("definitions", verb+": "+err.(*rhunext.Error).Text)
			rerr("syntax", err.(*rhunext.Error).Text)
			return
		}
		for _, dep := range d.Depends {
			if _, ok := h.tasks[dep]; !ok {
				h.fail("definitions", fmt.Sprintf("task-run depends on %q, which it has not published", dep))
				rerr("syntax", "definition: depends: names a task that is not published")
				return
			}
		}
		if d.Output && level < 4 {
			h.fail("needs", verb+": an output record needs terminal=output")
			rerr("needs", "terminal=output")
			return
		}
		h.ok(verb + " definition as rhun reads it")
		h.run(id, d, reply)
	case "terminal-stop", "terminal-restart", "terminal-close":
		if len(args) != 1 {
			bad(verb + " takes TERM")
			return
		}
		t, n, ok := term()
		if !ok {
			return
		}
		reply(fmt.Sprintf("ok %d", id))
		switch verb {
		case "terminal-stop":
			if t.running {
				t.running = false
				h.write(fmt.Sprintf("0 terminal-ended %d stopped 130 0 0\n", n))
			}
		case "terminal-restart":
			if t.running {
				h.write(fmt.Sprintf("0 terminal-ended %d stopped 130 0 0\n", n))
			}
			h.write(fmt.Sprintf("0 terminal-started %d %d %s\n", n, 4000+n, t.task))
			h.write(fmt.Sprintf("0 terminal-ended %d exit 0 0 0\n", n))
			t.running = false
		case "terminal-close":
			if t.running {
				h.write(fmt.Sprintf("0 terminal-ended %d stopped 143 0 0\n", n))
			}
			h.write(fmt.Sprintf("0 terminal-closed %d\n", n))
			delete(h.terms, n)
		}
		h.ok(verb)
	case "terminal-show":
		if len(args) != 2 || args[1] != "show" && args[1] != "focus" {
			bad("terminal-show takes TERM show|focus")
			return
		}
		if _, _, ok := term(); ok {
			reply(fmt.Sprintf("ok %d", id))
			h.ok(verb)
		}
	case "terminal-input":
		if len(args) != 2 {
			bad("terminal-input takes TERM LEN")
			return
		}
		b, ok := body(args[1])
		if !ok {
			return
		}
		if len(b) > 65536 {
			h.fail("limits", "terminal-input over 64 KiB")
			rerr("toobig", "65536 bytes")
			return
		}
		if _, _, ok := term(); ok {
			reply(fmt.Sprintf("ok %d", id))
			h.ok(verb)
		}
	case "terminal-screen":
		if len(args) != 1 {
			bad("terminal-screen takes TERM")
			return
		}
		if t, _, ok := term(); ok {
			screen := fmt.Sprintf("2 80 1 0\n%s\n$\n", t.label)
			reply(fmt.Sprintf("ok %d %d\n%s", id, len(screen), screen))
			h.ok(verb)
		}
	case "tasks-set":
		if len(args) != 1 {
			bad("tasks-set takes LEN")
			return
		}
		b, ok := body(args[0])
		if !ok {
			return
		}
		list, err := rhunext.ParseTaskList(b)
		if err != nil {
			e := err.(*rhunext.Error)
			h.fail("task lists", "tasks-set: "+e.Code+" "+e.Text)
			rerr(e.Code, e.Text)
			return
		}
		h.tasks = map[string]rhunext.Definition{}
		for _, d := range list {
			if d.Output && level < 4 {
				h.fail("needs", "tasks-set: task "+d.ID+" has an output record without terminal=output")
				rerr("needs", "terminal=output")
				return
			}
			h.tasks[d.ID] = d
		}
		reply(fmt.Sprintf("ok %d", id))
		h.ok(fmt.Sprintf("tasks-set of %d tasks as rhun reads it", len(list)))
	case "task-start":
		if len(args) != 1 || !rhunext.TaskIDOK(args[0]) {
			bad("task-start takes TASK_ID")
			return
		}
		d, ok := h.tasks[args[0]]
		if !ok {
			h.fail("task lists", "task-start "+args[0]+": not a published task")
			rerr("notask", "no such published task")
			return
		}
		h.ok(verb)
		h.run(id, d, reply)
	}
}

// run answers a start with a new TERM and plays a run: its dependencies first (each its own TERM),
// then the program, which prints two lines and exits 0 (a background task becomes ready instead)
func (h *host) run(id int64, d rhunext.Definition, reply func(string)) {
	for _, dep := range d.Depends {
		h.nextTerm++
		n := h.nextTerm
		h.terms[n] = &checkerTerm{label: h.tasks[dep].Label, task: dep}
		h.write(fmt.Sprintf("0 terminal-started %d %d %s\n", n, 4000+n, dep))
		h.write(fmt.Sprintf("0 terminal-ended %d exit 0 0 0\n", n))
	}
	h.nextTerm++
	n := h.nextTerm
	task := d.ID
	t := &checkerTerm{label: d.Label, task: task, running: true}
	h.terms[n] = t
	reply(fmt.Sprintf("ok %d %d", id, n))
	h.write(fmt.Sprintf("0 terminal-started %d %d %s\n", n, 4000+n, task))
	if d.Output {
		lines := "checker line one\nchecker line two\n"
		h.write(fmt.Sprintf("0 terminal-output %d 1 %d\n%s", n, len(lines), lines))
	}
	if len(d.Ready) > 0 {
		h.write(fmt.Sprintf("0 task-ready %d 0 0\n", n))
		return
	}
	t.running = false
	h.write(fmt.Sprintf("0 terminal-ended %d exit 0 0 0\n", n))
}
