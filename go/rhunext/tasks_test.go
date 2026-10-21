package rhunext

import (
	"bufio"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func taskTestConnection(t *testing.T) (*Ext, wireWriter) {
	t.Helper()
	wire := make(wireWriter, 16)
	e := testConnection(t, wire)
	e.agreed = map[string]bool{FeatureTerminals: true, FeatureTasks: true}
	return e, wire
}

func taskTestReply(t *testing.T, e *Ext, s string) {
	t.Helper()
	if _, err := e.readMessage(bufio.NewReader(strings.NewReader(s))); err != nil {
		t.Fatal(err)
	}
}

// what rhun's print-pattern prints for a pattern and a line
func patternResult(p, line string) string {
	if err := CheckPattern(p); err != nil {
		return "error=" + err.Error()
	}
	caps, ok := MatchPattern(p, line)
	if !ok {
		return "match=0"
	}
	out := "match=1"
	for _, f := range PatternFields {
		if v, has := caps[f]; has {
			out += " " + f + "=" + v
		}
	}
	return out
}

func TestPatternVectors(t *testing.T) {
	data, err := os.ReadFile("../../v1.1/patterns.txt")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if strings.HasPrefix(row, "#") {
			continue
		}
		i := strings.LastIndexByte(row, '\t')
		j := strings.IndexByte(row, '\t')
		if got := patternResult(row[:j], row[j+1:i]); got != row[i+1:] {
			t.Errorf("%q on %q: %s, want %s", row[:j], row[j+1:i], got, row[i+1:])
		}
		n++
	}
	if n < 30 {
		t.Fatal("too few vectors", n)
	}
}

func TestPatternStepLimit(t *testing.T) {
	// many lazy fields over a long line that never matches: given up, not exponential
	p := "{file}:{code}:{message}:{_}:{_}:{_}:x"
	if _, ok := MatchPattern(p, strings.Repeat(":", 200)); ok {
		t.Fatal("matched")
	}
}

func TestMatcherChecks(t *testing.T) {
	for _, text := range BuiltinMatchers {
		if err := CheckMatcher(text); err != nil {
			t.Fatal(text, err)
		}
	}
	for text, want := range map[string]string{
		"2 {file}: {message}":               "steps go 1, 2, 3, 4 in order",
		"severity bad\n1 {file}: {message}": "a matcher line is",
		"loop":                              "a matcher needs a step 1 pattern",
		"1 {nope}":                          "unknown field",
		strings.Repeat("1 {file}: {message}\n", 9): "more than 8 patterns in a step",
	} {
		if err := CheckMatcher(text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %s", text, err, want)
		}
	}
}

func TestTaskEncodesAsRhunReadsIt(t *testing.T) {
	task := Task{ID: "serve", Label: "Dev server", Detail: "on 8765",
		Program: Program{Argv: []string{"python3", "-m", "http.server", "8765"}, Cwd: "web", Env: []EnvVar{{"PORT", "8765"}}, Unset: []string{"HOME"}},
		Group:   "build", Default: true, Depends: []string{"build", "lint"}, Parallel: true,
		Matchers: []Matcher{{Builtin: "go"}, {Text: "1 {file}:{line}: {message}"}},
		Ready:    []string{"Serving HTTP on {_}"}, Busy: []string{"restarting"}, Reveal: "silent", Output: true}
	b, err := task.Encode()
	if err != nil {
		t.Fatal(err)
	}
	d, n, err := ParseDefinition(b, true)
	if err != nil || n != len(b) {
		t.Fatal(err, n, len(b))
	}
	want := Definition{ID: "serve", Form: "argv", Label: "Dev server", Cwd: "web", Detail: "on 8765",
		Argv: []string{"python3", "-m", "http.server", "8765"}, Env: []EnvVar{{"PORT", "8765"}}, Unset: []string{"HOME"},
		Group: "build", Default: true, Order: "parallel", Depends: []string{"build", "lint"},
		Matchers: []Matcher{{Builtin: "go"}, {Text: "1 {file}:{line}: {message}"}},
		Ready:    []string{"Serving HTTP on {_}"}, Busy: []string{"restarting"}, Reveal: "silent", Output: true}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("%+v", d)
	}
	// the design's example, byte for byte
	b, err = Task{ID: "build", Label: "Go build", Program: Program{Shell: "go build ./..."}, Group: "build", Default: true,
		Matchers: []Matcher{{Builtin: "go"}}}.Encode()
	if err != nil || string(b) != "build shell 8 14 0 37\nGo buildgo build ./...group build 0\ndefault 0\nmatcher go 0\n" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestDefinitionRefusals(t *testing.T) {
	for _, c := range []struct {
		task Task
		want string
	}{
		{Task{ID: "Bad", Label: "l", Program: Program{Shell: "x"}}, "id: 1 to 64 bytes"},
		{Task{ID: "a", Label: "", Program: Program{Shell: "x"}}, "label: 1 to 128 bytes"},
		{Task{ID: "a", Label: "l"}, "command: 1 to 4096 bytes"},
		{Task{ID: "a", Label: "l", Program: Program{Shell: "x", Env: []EnvVar{{"1X", "v"}}}}, "env: NAME"},
		{Task{ID: "a", Label: "l", Program: Program{Shell: "x"}, Busy: []string{"b"}}, "busy needs ready"},
		{Task{ID: "a", Label: "l", Program: Program{Shell: "x"}, Ready: []string{"{x}"}}, "unknown field"},
		{Task{ID: "a", Label: "l", Program: Program{Shell: "x"}, Matchers: []Matcher{{Builtin: "javac"}}}, "matcher: go, gcc"},
		{Task{ID: "a", Label: "l", Program: Program{Shell: "x"}, Reveal: "loud"}, "reveal: show, focus or silent"},
	} {
		_, err := c.task.Encode()
		var re *Error
		if !errors.As(err, &re) || re.Code != "syntax" || !strings.Contains(re.Text, c.want) {
			t.Errorf("%+v: %v, want %s", c.task, err, c.want)
		}
	}
	// a terminal takes no task records, and runs the user's shell without a command
	b := []byte("- shell 1 1 0 14\nlxgroup build 0\n")
	if _, _, err := ParseDefinition(b, false); err == nil || !strings.Contains(err.Error(), "not in a terminal") {
		t.Fatal(err)
	}
	if b, err := (TerminalSpec{Label: "Shell"}).Encode(); err != nil || string(b) != "- shell 5 0 0 0\nShell" {
		t.Fatalf("%q %v", b, err)
	}
	if _, _, err := ParseDefinition([]byte("- shell 1 1 0 18\nlxlocation remote 0\n"), false); err == nil || !strings.Contains(err.Error(), "unsupported location") {
		t.Fatal(err)
	}
	// an unknown record is skipped
	if _, _, err := ParseDefinition([]byte("- shell 1 1 0 13\nlxfuture a b 0\n"), false); err != nil {
		t.Fatal(err)
	}
}

func TestTaskListChecks(t *testing.T) {
	shell := Program{Shell: "true"}
	for _, c := range []struct {
		tasks []Task
		want  string
	}{
		{[]Task{{ID: "a", Label: "A", Program: shell}, {ID: "a", Label: "B", Program: shell}}, "task 2: id listed twice"},
		{[]Task{{ID: "a", Label: "A", Program: shell, Depends: []string{"lint"}}}, `task 1: depends: no task "lint"`},
		{[]Task{{ID: "a", Label: "A", Program: shell, Depends: []string{"b"}}, {ID: "b", Label: "B", Program: shell, Depends: []string{"a"}}}, "depends: a cycle"},
	} {
		if _, err := EncodeTaskList(c.tasks); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %s", err, c.want)
		}
	}
	if _, err := ParseTaskList([]byte("1\nbad")); err == nil || !strings.Contains(err.Error(), "task 1: header") {
		t.Fatal(err)
	}
	if _, err := ParseTaskList([]byte("0\nx")); err == nil || !strings.Contains(err.Error(), "bytes after the last definition") {
		t.Fatal(err)
	}
}

func TestSetTasksAndStartTask(t *testing.T) {
	e, wire := taskTestConnection(t)
	done := make(chan error, 1)
	go func() {
		done <- e.SetTasks([]Task{{ID: "build", Label: "Go build", Program: Program{Shell: "go build ./..."}, Group: "build", Default: true, Matchers: []Matcher{{Builtin: "go"}}}})
	}()
	body := "1\nbuild shell 8 14 0 37\nGo buildgo build ./...group build 0\ndefault 0\nmatcher go 0\n"
	if got := <-wire; got != "1 tasks-set 83\n" {
		t.Fatal(got)
	}
	if got := <-wire; got != body {
		t.Fatalf("%q", got)
	}
	taskTestReply(t, e, "ok 1\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	terms := make(chan Term, 1)
	go func() { term, err := e.StartTask("build"); done <- err; terms <- term }()
	if got := <-wire; got != "2 task-start build\n" {
		t.Fatal(got)
	}
	taskTestReply(t, e, "ok 2 4\n")
	if err := <-done; err != nil || <-terms != 4 {
		t.Fatal(err)
	}
}

func TestAStartCancelledWhileTheDialogIsOpen(t *testing.T) {
	e, wire := taskTestConnection(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.OpenTerminalContext(ctx, TerminalSpec{Label: "repl", Program: Program{Argv: []string{"python3"}}})
		done <- err
	}()
	if got := <-wire; got != "1 terminal-open 27\n" {
		t.Fatal(got)
	}
	<-wire
	cancel()
	if got := <-wire; got != "2 cancel 1\n" {
		t.Fatal(got)
	}
	taskTestReply(t, e, "ok 2\n")
	taskTestReply(t, e, "error 1 cancelled\n")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAStartThatWonTheRaceIsStopped(t *testing.T) {
	e, wire := taskTestConnection(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.StartTaskContext(ctx, "serve"); done <- err }()
	if got := <-wire; got != "1 task-start serve\n" {
		t.Fatal(got)
	}
	cancel()
	if got := <-wire; got != "2 cancel 1\n" {
		t.Fatal(got)
	}
	taskTestReply(t, e, "ok 1 9\n") // allowed just before the cancel arrived
	taskTestReply(t, e, "ok 2\n")
	if got := <-wire; got != "3 terminal-stop 9\n" {
		t.Fatal(got)
	}
	taskTestReply(t, e, "ok 3\n")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTerminalEventsAndScreen(t *testing.T) {
	e, wire := taskTestConnection(t)
	br := bufio.NewReader(strings.NewReader("0 terminal-output 3 2 6\nab\ncd\n0 terminal-ended 3 exit 2 3 1\n"))
	ev, err := e.readMessage(br)
	if err != nil {
		t.Fatal(err)
	}
	te, ok := ParseTerminalEvent(*ev)
	if !ok || te.Kind != "output" || te.Term != 3 || te.Dropped != 2 || !reflect.DeepEqual(te.Lines, []string{"ab", "cd"}) {
		t.Fatalf("%+v", te)
	}
	ev, _ = e.readMessage(br)
	if te, ok = ParseTerminalEvent(*ev); !ok || te.Reason != "exit" || te.Code != 2 || te.Errors != 3 || te.Warnings != 1 {
		t.Fatalf("%+v", te)
	}
	for line, want := range map[string]TerminalEvent{
		"terminal-started 4 81234 build": {Kind: "started", Term: 4, PID: 81234, TaskID: "build"},
		"terminal-started 5 1 -":         {Kind: "started", Term: 5, PID: 1},
		"terminal-ended 4 failed - 0 0":  {Kind: "ended", Term: 4, Reason: "failed", Code: -1},
		"terminal-closed 4":              {Kind: "closed", Term: 4},
		"task-ready 4 1 0":               {Kind: "ready", Term: 4, Errors: 1},
		"task-busy 4":                    {Kind: "busy", Term: 4},
	} {
		w := strings.Split(line, " ")
		if got, ok := ParseTerminalEvent(Event{Name: w[0], Args: w[1:]}); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v %v", line, got, ok)
		}
	}
	if _, ok := ParseTerminalEvent(Event{Name: "terminal-started", Args: []string{"0", "1", "-"}}); ok {
		t.Fatal("TERM 0 accepted")
	}
	screens := make(chan Screen, 1)
	go func() { s, err := e.TerminalScreen(3); screens <- s; _ = err }()
	if got := <-wire; got != "1 terminal-screen 3\n" {
		t.Fatal(got)
	}
	taskTestReply(t, e, "ok 1 17\n2 4 1 0\n$ ls\na b\n")
	if s := <-screens; s.Rows != 2 || s.Cols != 4 || s.CursorRow != 1 || !reflect.DeepEqual(s.Lines, []string{"$ ls", "a b"}) {
		t.Fatalf("%+v", s)
	}
}

func TestReadyKeepsTheProjectsSpaces(t *testing.T) {
	e, _ := taskTestConnection(t)
	const workspace = "/workspace  日本語 😀 "
	ev, err := e.readMessage(bufio.NewReader(strings.NewReader("0 ready " + workspace + "\n")))
	if err != nil || ev == nil || ev.Name != "ready" || len(ev.Args) != 1 || ev.Args[0] != workspace {
		t.Fatal(ev, err)
	}
}

func TestTerminalsNeedTheFeature(t *testing.T) {
	e, _ := taskTestConnection(t)
	e.agreed = map[string]bool{}
	if _, err := e.StartTask("x"); err == nil || !strings.Contains(err.Error(), "tasks was not agreed") {
		t.Fatal(err)
	}
	if err := e.StopTerminal(1); err == nil || !strings.Contains(err.Error(), "terminals was not agreed") {
		t.Fatal(err)
	}
}
