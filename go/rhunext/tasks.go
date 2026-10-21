package rhunext

// Terminals and tasks (protocol 1.1, features "terminals" and "tasks"): programs in terminal tabs of
// rhun's panel, run as the user after the one approval per extension and project, and published
// tasks rhun lists, runs with their dependencies, and turns into problems. The manifest needs
// `terminal = run` (input, output for TerminalInput and TerminalScreen and output events).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	FeatureTerminals = "terminals"
	FeatureTasks     = "tasks" // agreed only with FeatureTerminals
)

// Term names a terminal tab an extension opened, or a tab running one of its tasks (across the
// task's runs while the tab is open). It is never reused in a rhun session.
type Term uint64

// Program is what runs: a shell command line (Shell), or a program and its arguments (Argv). With
// both empty, a terminal runs the user's interactive shell.
type Program struct {
	Shell string   // run as /bin/sh -c (Linux, macOS) or powershell.exe -Command (Windows)
	Argv  []string // the program (looked up on PATH without a slash) and its arguments
	Cwd   string   // "" the project folder; relative: inside it
	Env   []EnvVar
	Unset []string
}

// EnvVar is one variable a program gets on top of the user's environment.
type EnvVar struct{ Name, Value string }

// Reveal is what rhun shows when a program starts: "" or "show" (the panel at its tab), "focus"
// (with the keyboard) or "silent" (only the tab).
type Reveal string

// Matcher turns a task's lines into problems: a built-in (go, gcc, rustc, tsc, msbuild) or the
// text of an inline matcher (see CheckMatcher).
type Matcher struct{ Builtin, Text string }

// Task is a task to publish (SetTasks) or run (RunTask).
type Task struct {
	ID       string // [a-z0-9_.-]{1,64}, from a letter or digit
	Label    string // the tab's title and the lists' label
	Detail   string // beside the label in lists ("" the command)
	Program  Program
	Group    string // "", "build" or "test"
	Default  bool   // its group's default
	Depends  []string
	Parallel bool // the dependencies run together, else one after another
	Matchers []Matcher
	Ready    []string // patterns: a background task, done for its dependents when a line matches
	Busy     []string // patterns: a new cycle began (with Ready)
	Reveal   Reveal
	Output   bool // terminal-output events for it (needs terminal = output)
}

// TerminalSpec is a terminal to open.
type TerminalSpec struct {
	Label, Detail string
	Program       Program
	Reveal        Reveal
	Output        bool
}

// Definition is a definition as rhun reads it from the wire (ParseDefinition): what the checker and
// tests compare.
type Definition struct {
	ID, Form, Label, Command, Cwd, Detail string
	Argv                                  []string
	Env                                   []EnvVar
	Unset                                 []string
	Group                                 string
	Default                               bool
	Order                                 string // sequence or parallel, with Depends
	Depends                               []string
	Matchers                              []Matcher
	Ready, Busy                           []string
	Reveal                                Reveal
	Output                                bool
}

func defErr(why string) error { return &Error{Code: "syntax", Text: "definition: " + why} }

// TaskIDOK says whether s is a task id: 1 to 64 bytes of a-z 0-9 _ . -, from a letter or digit.
func TaskIDOK(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '.' || c == '-') {
			continue
		}
		return false
	}
	return true
}

func envNameOK(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || c|0x20 >= 'a' && c|0x20 <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// plainText is v1's rule for term text: valid UTF-8, one line, no control, bidi-control or
// zero-width characters.
func plainText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r >= 0x7f && r < 0xa0 || r >= 0x200b && r <= 0x200f || r >= 0x202a && r <= 0x202e || r == 0x2060 || r >= 0x2066 && r <= 0x2069 || r == 0xfeff {
			return false
		}
	}
	return true
}

func decimal(s string) (int, bool) {
	if len(s) == 0 || len(s) > 9 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// ParseDefinition reads one definition as rhun does: task false for a terminal-open (id "-", no
// task records). It returns the definition and the bytes it took, or a syntax *Error with rhun's
// reason.
func ParseDefinition(b []byte, task bool) (Definition, int, error) {
	var d Definition
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 || nl >= 512 {
		return d, 0, defErr("header: TASK_ID FORM LABEL_LEN CMD_LEN CWD_LEN EXTRA_LEN")
	}
	w := strings.Split(string(b[:nl]), " ")
	if len(w) != 6 {
		return d, 0, defErr("header: TASK_ID FORM LABEL_LEN CMD_LEN CWD_LEN EXTRA_LEN")
	}
	var n [4]int
	for i := range n {
		v, ok := decimal(w[2+i])
		if !ok {
			return d, 0, defErr("header: TASK_ID FORM LABEL_LEN CMD_LEN CWD_LEN EXTRA_LEN")
		}
		n[i] = v
	}
	total := nl + 1 + n[0] + n[1] + n[2] + n[3]
	if total > len(b) {
		return d, 0, defErr("header: the lengths go past the body")
	}
	d.ID, d.Form = w[0], w[1]
	if !task && d.ID != "-" {
		return d, 0, defErr("id: a terminal's is -")
	}
	if task && !TaskIDOK(d.ID) {
		return d, 0, defErr("id: 1 to 64 bytes of a-z 0-9 _ . -, from a letter or digit")
	}
	if d.Form != "shell" && d.Form != "argv" {
		return d, 0, defErr("form: shell or argv")
	}
	p := nl + 1
	d.Label = string(b[p : p+n[0]])
	if !plainText(d.Label, 128) {
		return d, 0, defErr("label: 1 to 128 bytes of plain text")
	}
	p += n[0]
	cmd := b[p : p+n[1]]
	if d.Form == "shell" {
		if len(cmd) == 0 && task || len(cmd) > 0 && !plainText(string(cmd), 4096) {
			return d, 0, defErr("command: 1 to 4096 bytes of plain text")
		}
		d.Command = string(cmd)
	} else {
		if len(cmd) == 0 || len(cmd) > 32768 || cmd[len(cmd)-1] != 0 || cmd[0] == 0 || bytes.Count(cmd, []byte{0}) > 64 {
			return d, 0, defErr("argv: 1 to 64 arguments, each ending with a NUL, the first not empty")
		}
		d.Argv = strings.Split(string(cmd[:len(cmd)-1]), "\x00")
	}
	p += n[1]
	d.Cwd = string(b[p : p+n[2]])
	if len(d.Cwd) > 4096 || strings.ContainsAny(d.Cwd, "\x00\n") {
		return d, 0, defErr("cwd: at most 4096 bytes, no NUL or LF")
	}
	p += n[2]
	end := p + n[3]
	envBytes := 0
	for p < end {
		e := bytes.IndexByte(b[p:end], '\n')
		if e < 0 {
			return d, 0, defErr("record: NAME ARGS... LEN")
		}
		rw := strings.Split(string(b[p:p+e]), " ")
		if len(rw) < 2 || len(rw) > 8 {
			return d, 0, defErr("record: NAME ARGS... LEN")
		}
		size, ok := decimal(rw[len(rw)-1])
		if !ok || p+e+1+size > end {
			return d, 0, defErr("record: NAME ARGS... LEN")
		}
		data := string(b[p+e+1 : p+e+1+size])
		p += e + 1 + size
		if err := d.record(rw, data, task, &envBytes); err != nil {
			return d, 0, err
		}
	}
	if len(d.Busy) > 0 && len(d.Ready) == 0 {
		return d, 0, defErr("busy needs ready")
	}
	return d, total, nil
}

func (d *Definition) record(w []string, data string, task bool, envBytes *int) error {
	bad := defErr("record: NAME ARGS... LEN")
	flag := func(n int) bool { return len(w) == n && data == "" }
	switch w[0] {
	case "detail":
		if len(w) != 2 {
			return bad
		}
		if !plainText(data, 200) {
			return defErr("detail: one line, at most 200 bytes")
		}
		d.Detail = data
	case "env":
		if len(w) != 3 {
			return bad
		}
		if !envNameOK(w[1]) || strings.ContainsAny(data, "\x00\n") {
			return defErr("env: NAME is [A-Za-z_][A-Za-z0-9_]*, the value one line")
		}
		*envBytes += len(w[1]) + len(data)
		if len(d.Env) >= 64 || *envBytes > 32768 {
			return defErr("too many: 64 env, 4 matchers, 8 patterns")
		}
		d.Env = append(d.Env, EnvVar{w[1], data})
	case "unset":
		if !flag(3) {
			return bad
		}
		if !envNameOK(w[1]) {
			return defErr("env: NAME is [A-Za-z_][A-Za-z0-9_]*, the value one line")
		}
		if len(d.Unset) >= 64 {
			return defErr("too many: 64 env, 4 matchers, 8 patterns")
		}
		d.Unset = append(d.Unset, w[1])
	case "reveal":
		if !flag(3) {
			return bad
		}
		if w[1] != "show" && w[1] != "focus" && w[1] != "silent" {
			return defErr("reveal: show, focus or silent")
		}
		d.Reveal = Reveal(w[1])
	case "output":
		if !flag(2) {
			return bad
		}
		d.Output = true
	case "location":
		if len(w) != 3 {
			return bad
		}
		if w[1] != "local" {
			return defErr("unsupported location")
		}
	case "group", "default", "depends", "matcher", "ready", "busy":
		if !task {
			return defErr("not in a terminal: group, default, depends, matcher, ready, busy")
		}
		return d.taskRecord(w, data)
	}
	return nil // an unknown record is skipped
}

func (d *Definition) taskRecord(w []string, data string) error {
	bad := defErr("record: NAME ARGS... LEN")
	switch w[0] {
	case "group":
		if len(w) != 3 || data != "" {
			return bad
		}
		if w[1] != "build" && w[1] != "test" {
			return defErr("group: build or test")
		}
		d.Group = w[1]
	case "default":
		if len(w) != 2 || data != "" {
			return bad
		}
		d.Default = true
	case "depends":
		if len(w) != 3 {
			return bad
		}
		if w[1] != "sequence" && w[1] != "parallel" {
			return defErr("depends: sequence or parallel")
		}
		d.Order = w[1]
		d.Depends = nil
		if data == "" {
			return defErr("depends: 1 to 16 task ids")
		}
		for _, id := range strings.Split(data, " ") {
			if !TaskIDOK(id) || len(d.Depends) >= 16 {
				return defErr("depends: 1 to 16 task ids")
			}
			d.Depends = append(d.Depends, id)
		}
	case "matcher":
		if len(w) != 3 {
			return bad
		}
		if len(d.Matchers) >= 4 {
			return defErr("too many: 64 env, 4 matchers, 8 patterns")
		}
		if w[1] != "-" {
			if data != "" {
				return bad
			}
			if _, ok := BuiltinMatchers[w[1]]; !ok {
				return defErr("matcher: go, gcc, rustc, tsc or msbuild")
			}
			d.Matchers = append(d.Matchers, Matcher{Builtin: w[1]})
			return nil
		}
		if len(data) > 4096 {
			return defErr("too many: 64 env, 4 matchers, 8 patterns")
		}
		if err := CheckMatcher(data); err != nil {
			return defErr(err.Error())
		}
		d.Matchers = append(d.Matchers, Matcher{Text: data})
	case "ready", "busy":
		if len(w) != 2 {
			return bad
		}
		list := &d.Ready
		if w[0] == "busy" {
			list = &d.Busy
		}
		for rest := data; rest != ""; {
			line := rest
			if i := strings.IndexByte(rest, '\n'); i >= 0 {
				line, rest = rest[:i], rest[i+1:]
			} else {
				rest = ""
			}
			if len(*list) >= 8 {
				return defErr("too many: 64 env, 4 matchers, 8 patterns")
			}
			if err := CheckPattern(line); err != nil {
				return defErr(err.Error())
			}
			*list = append(*list, line)
		}
		if len(*list) == 0 {
			return defErr("ready, busy: 1 to 8 patterns")
		}
	}
	return nil
}

// ParseTaskList reads a tasks-set body as rhun does: the count, the definitions, ids unique,
// depends naming ids of the list, no cycle. The error is a syntax or toobig *Error.
func ParseTaskList(body []byte) ([]Definition, error) {
	nl := bytes.IndexByte(body, '\n')
	if nl <= 0 || nl > 8 {
		return nil, &Error{Code: "syntax", Text: "the count of tasks"}
	}
	count, ok := decimal(string(body[:nl]))
	if !ok {
		return nil, &Error{Code: "syntax", Text: "the count of tasks"}
	}
	if count > 1000 || len(body) > 1<<20 {
		return nil, &Error{Code: "toobig", Text: "1000 tasks, 4 MB of definitions in all"}
	}
	rest := body[nl+1:]
	list := make([]Definition, 0, count)
	for i := 0; i < count; i++ {
		d, n, err := ParseDefinition(rest, true)
		if err != nil {
			return nil, &Error{Code: "syntax", Text: fmt.Sprintf("task %d: %s", i+1, strings.TrimPrefix(err.(*Error).Text, "definition: "))}
		}
		list = append(list, d)
		rest = rest[n:]
	}
	if len(rest) != 0 {
		return nil, &Error{Code: "syntax", Text: "bytes after the last definition"}
	}
	index := map[string]int{}
	for i, d := range list {
		if first, dup := index[d.ID]; dup { // rhun names the first of the two
			return nil, &Error{Code: "syntax", Text: fmt.Sprintf("task %d: id listed twice", first+1)}
		}
		index[d.ID] = i
	}
	for i, d := range list {
		for _, dep := range d.Depends {
			if _, ok := index[dep]; !ok {
				return nil, &Error{Code: "syntax", Text: fmt.Sprintf("task %d: depends: no task %q", i+1, dep)}
			}
		}
	}
	state := make([]int, len(list)) // 0 not seen, 1 on the path, 2 done
	var visit func(i int) bool
	visit = func(i int) bool {
		if state[i] == 1 {
			return false
		}
		if state[i] == 2 {
			return true
		}
		state[i] = 1
		for _, dep := range list[i].Depends {
			if !visit(index[dep]) {
				return false
			}
		}
		state[i] = 2
		return true
	}
	for i := range list {
		if !visit(i) {
			return nil, &Error{Code: "syntax", Text: fmt.Sprintf("task %d: depends: a cycle", i+1)}
		}
	}
	return list, nil
}

func record(name, args string, data []byte) []byte {
	head := name
	if args != "" {
		head += " " + args
	}
	return append([]byte(head+" "+strconv.Itoa(len(data))+"\n"), data...)
}

func encodeDefinition(id, label, detail string, p Program, reveal Reveal, output bool, more []byte, task bool) ([]byte, error) {
	form, cmd := "shell", []byte(p.Shell)
	if p.Shell == "" && len(p.Argv) > 0 {
		form, cmd = "argv", nil
		for _, a := range p.Argv {
			cmd = append(append(cmd, a...), 0)
		}
	}
	var extra []byte
	if detail != "" {
		extra = append(extra, record("detail", "", []byte(detail))...)
	}
	for _, v := range p.Env {
		extra = append(extra, record("env", v.Name, []byte(v.Value))...)
	}
	for _, n := range p.Unset {
		extra = append(extra, record("unset", n, nil)...)
	}
	if reveal != "" {
		extra = append(extra, record("reveal", string(reveal), nil)...)
	}
	if output {
		extra = append(extra, record("output", "", nil)...)
	}
	extra = append(extra, more...)
	b := fmt.Appendf(nil, "%s %s %d %d %d %d\n", id, form, len(label), len(cmd), len(p.Cwd), len(extra))
	b = append(append(append(append(b, label...), cmd...), p.Cwd...), extra...)
	// checked as rhun will check it, so a mistake is an error here and not a refusal later
	if _, _, err := ParseDefinition(b, task); err != nil {
		return nil, err
	}
	return b, nil
}

// Encode is the task's definition on the wire, checked as rhun checks it.
func (t Task) Encode() ([]byte, error) {
	var more []byte
	if t.Group != "" {
		more = append(more, record("group", t.Group, nil)...)
	}
	if t.Default {
		more = append(more, record("default", "", nil)...)
	}
	if len(t.Depends) > 0 {
		order := "sequence"
		if t.Parallel {
			order = "parallel"
		}
		more = append(more, record("depends", order, []byte(strings.Join(t.Depends, " ")))...)
	}
	for _, m := range t.Matchers {
		if m.Builtin != "" {
			more = append(more, record("matcher", m.Builtin, nil)...)
		} else {
			more = append(more, record("matcher", "-", []byte(m.Text))...)
		}
	}
	if len(t.Ready) > 0 {
		more = append(more, record("ready", "", []byte(strings.Join(t.Ready, "\n")))...)
	}
	if len(t.Busy) > 0 {
		more = append(more, record("busy", "", []byte(strings.Join(t.Busy, "\n")))...)
	}
	return encodeDefinition(t.ID, t.Label, t.Detail, t.Program, t.Reveal, t.Output, more, true)
}

// Encode is the terminal's definition on the wire, checked as rhun checks it.
func (s TerminalSpec) Encode() ([]byte, error) {
	return encodeDefinition("-", s.Label, s.Detail, s.Program, s.Reveal, s.Output, nil, false)
}

// EncodeTaskList is a tasks-set body, checked whole as rhun checks it.
func EncodeTaskList(tasks []Task) ([]byte, error) {
	body := []byte(strconv.Itoa(len(tasks)) + "\n")
	for _, t := range tasks {
		b, err := t.Encode()
		if err != nil {
			return nil, err
		}
		body = append(body, b...)
	}
	if _, err := ParseTaskList(body); err != nil {
		return nil, err
	}
	return body, nil
}

func (e *Ext) termAgreed(feature string) error {
	if !e.Has(feature) {
		return &Error{Code: "unknown", Text: feature + " was not agreed"}
	}
	return nil
}

func termReply(r Reply, err error) (Term, error) {
	if err != nil {
		return 0, err
	}
	if len(r.Args) != 1 {
		return 0, fmt.Errorf("rhun: a TERM reply with %d fields", len(r.Args))
	}
	n, perr := strconv.ParseUint(r.Args[0], 10, 64)
	if perr != nil || n == 0 {
		return 0, fmt.Errorf("rhun: a TERM reply %q", r.Args[0])
	}
	return Term(n), nil
}

// start sends a request that may wait for the approval dialog. When ctx ends first it sends
// `cancel ID` (the dialog closes) and stops a terminal whose ok TERM was already on its way.
func (e *Ext) start(ctx context.Context, verb string, body []byte, args ...string) (Term, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ids := make(chan int64, 1)
	type answer struct {
		r   Reply
		err error
	}
	done := make(chan answer, 1)
	go func() {
		id, ch, err := e.startRequest(context.Background(), verb, body, nil, args...)
		if err != nil {
			done <- answer{Reply{}, err}
			return
		}
		ids <- id
		r, err := e.waitReply(context.Background(), id, ch)
		done <- answer{r, err}
	}()
	select {
	case a := <-done:
		return termReply(a.r, a.err)
	case <-ctx.Done():
	}
	select {
	case id := <-ids:
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		e.RequestContext(cleanup, "cancel", nil, itoa(id))
		cancel()
	case a := <-done:
		if t, err := termReply(a.r, a.err); err == nil {
			e.StopTerminal(t)
		}
		return 0, ctx.Err()
	}
	if t, err := termReply((<-done).r, nil); err == nil {
		e.StopTerminal(t)
	}
	return 0, ctx.Err()
}

// SetTasks replaces the extension's published tasks (all of them, checked whole first).
func (e *Ext) SetTasks(tasks []Task) error { return e.SetTasksContext(context.Background(), tasks) }
func (e *Ext) SetTasksContext(ctx context.Context, tasks []Task) error {
	if err := e.termAgreed(FeatureTasks); err != nil {
		return err
	}
	body, err := EncodeTaskList(tasks)
	if err != nil {
		return err
	}
	_, err = e.RequestContext(ctx, "tasks-set", body, strconv.Itoa(len(body)))
	return err
}

// StartTask runs a published task after its dependencies (asking the approval first when needed);
// a running task stays running and its Term comes back.
func (e *Ext) StartTask(id string) (Term, error) { return e.StartTaskContext(context.Background(), id) }
func (e *Ext) StartTaskContext(ctx context.Context, id string) (Term, error) {
	if err := e.termAgreed(FeatureTasks); err != nil {
		return 0, err
	}
	if !TaskIDOK(id) {
		return 0, &Error{Code: "syntax", Text: "a task id"}
	}
	return e.start(ctx, "task-start", nil, id)
}

// RunTask runs a task that is not published (its Depends may name published ones).
func (e *Ext) RunTask(t Task) (Term, error) { return e.RunTaskContext(context.Background(), t) }
func (e *Ext) RunTaskContext(ctx context.Context, t Task) (Term, error) {
	if err := e.termAgreed(FeatureTasks); err != nil {
		return 0, err
	}
	b, err := t.Encode()
	if err != nil {
		return 0, err
	}
	return e.start(ctx, "task-run", b, strconv.Itoa(len(b)))
}

// OpenTerminal starts a program in a new terminal tab (asking the approval first when needed).
func (e *Ext) OpenTerminal(s TerminalSpec) (Term, error) {
	return e.OpenTerminalContext(context.Background(), s)
}
func (e *Ext) OpenTerminalContext(ctx context.Context, s TerminalSpec) (Term, error) {
	if err := e.termAgreed(FeatureTerminals); err != nil {
		return 0, err
	}
	b, err := s.Encode()
	if err != nil {
		return 0, err
	}
	return e.start(ctx, "terminal-open", b, strconv.Itoa(len(b)))
}

func (e *Ext) termVerb(verb string, t Term, more ...string) error {
	if err := e.termAgreed(FeatureTerminals); err != nil {
		return err
	}
	if t == 0 {
		return &Error{Code: "noterm", Text: "no such terminal"}
	}
	_, err := e.Request(verb, nil, append([]string{strconv.FormatUint(uint64(t), 10)}, more...)...)
	return err
}

// StopTerminal types Ctrl+C into the program and kills what is left of it 2 s later.
func (e *Ext) StopTerminal(t Term) error { return e.termVerb("terminal-stop", t) }

// RestartTerminal stops the program, waits for its end and runs it again in the same tab.
func (e *Ext) RestartTerminal(t Term) error { return e.termVerb("terminal-restart", t) }

// CloseTerminal kills the program and closes its tab.
func (e *Ext) CloseTerminal(t Term) error { return e.termVerb("terminal-close", t) }

// ShowTerminal opens the panel at the tab; focus gives it the keyboard.
func (e *Ext) ShowTerminal(t Term, focus bool) error {
	how := "show"
	if focus {
		how = "focus"
	}
	return e.termVerb("terminal-show", t, how)
}

// TerminalInput types the bytes into the program (LF is Enter); needs terminal = input.
func (e *Ext) TerminalInput(t Term, b []byte) error {
	if err := e.termAgreed(FeatureTerminals); err != nil {
		return err
	}
	if len(b) > 65536 {
		return &Error{Code: "toobig", Text: "65536 bytes"}
	}
	_, err := e.Request("terminal-input", b, strconv.FormatUint(uint64(t), 10), strconv.Itoa(len(b)))
	return err
}

// Screen is what a terminal shows: its size, the cursor (0-based) and its rows, trailing blanks
// dropped.
type Screen struct {
	Rows, Cols, CursorRow, CursorCol int
	Lines                            []string
}

// TerminalScreen reads the visible screen; needs terminal = output.
func (e *Ext) TerminalScreen(t Term) (Screen, error) {
	if err := e.termAgreed(FeatureTerminals); err != nil {
		return Screen{}, err
	}
	r, err := e.Request("terminal-screen", nil, strconv.FormatUint(uint64(t), 10))
	if err != nil {
		return Screen{}, err
	}
	return ParseScreen(r.Body)
}

// ParseScreen decodes a terminal-screen body.
func ParseScreen(body []byte) (Screen, error) {
	var s Screen
	text := string(body)
	head, rest, _ := strings.Cut(text, "\n")
	if _, err := fmt.Sscanf(head, "%d %d %d %d", &s.Rows, &s.Cols, &s.CursorRow, &s.CursorCol); err != nil {
		return s, errors.New("rhun: a terminal-screen head " + strconv.Quote(head))
	}
	rest = strings.TrimSuffix(rest, "\n")
	if s.Rows > 0 {
		s.Lines = strings.Split(rest, "\n")
	}
	if len(s.Lines) != s.Rows {
		return s, fmt.Errorf("rhun: a terminal-screen of %d rows with %d lines", s.Rows, len(s.Lines))
	}
	return s, nil
}

// TerminalEvent is one of the terminals and tasks events, decoded.
type TerminalEvent struct {
	Kind     string // started, ended, output, closed, ready, busy
	Term     Term
	PID      int
	TaskID   string // started: "" for a terminal
	Reason   string // ended: exit, stopped or failed
	Code     int64  // ended: the exit code; -1 when it could not start
	Errors   int    // ended, ready: the problems the run made
	Warnings int
	Dropped  int      // output: lines left out since the last event
	Lines    []string // output: finished lines
}

// ParseTerminalEvent decodes terminal-started, terminal-ended, terminal-output, terminal-closed,
// task-ready and task-busy; false for any other event or a malformed one.
func ParseTerminalEvent(ev Event) (TerminalEvent, bool) {
	var te TerminalEvent
	a := ev.Args
	num := func(s string) (int64, bool) {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil && n >= 0
	}
	term := func() bool {
		if len(a) == 0 {
			return false
		}
		n, err := strconv.ParseUint(a[0], 10, 64)
		te.Term = Term(n)
		return err == nil && n > 0
	}
	switch ev.Name {
	case "terminal-started":
		if len(a) != 3 || !term() {
			return te, false
		}
		pid, ok := num(a[1])
		te.Kind, te.PID = "started", int(pid)
		if a[2] != "-" {
			te.TaskID = a[2]
		}
		return te, ok
	case "terminal-ended":
		if len(a) != 5 || !term() {
			return te, false
		}
		te.Kind, te.Reason, te.Code = "ended", a[1], -1
		if a[2] != "-" {
			c, err := strconv.ParseInt(a[2], 10, 64)
			if err != nil {
				return te, false
			}
			te.Code = c
		}
		errs, ok1 := num(a[3])
		warns, ok2 := num(a[4])
		te.Errors, te.Warnings = int(errs), int(warns)
		return te, ok1 && ok2
	case "terminal-output":
		if len(a) != 3 || !term() {
			return te, false
		}
		dropped, ok := num(a[1])
		te.Kind, te.Dropped = "output", int(dropped)
		for _, l := range strings.SplitAfter(string(ev.Body), "\n") {
			if l != "" {
				te.Lines = append(te.Lines, strings.TrimSuffix(l, "\n"))
			}
		}
		return te, ok
	case "terminal-closed":
		te.Kind = "closed"
		return te, len(a) == 1 && term()
	case "task-ready":
		if len(a) != 3 || !term() {
			return te, false
		}
		errs, ok1 := num(a[1])
		warns, ok2 := num(a[2])
		te.Kind, te.Errors, te.Warnings = "ready", int(errs), int(warns)
		return te, ok1 && ok2
	case "task-busy":
		te.Kind = "busy"
		return te, len(a) == 1 && term()
	}
	return te, false
}
