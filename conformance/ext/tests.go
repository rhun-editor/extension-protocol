package main

// Protocol 1.1, feature tests (../../v1.1/v1.1.md "Feature: tests"): the checker resolves the
// roots and one lazy level, validating the items (kinds, flags, ranges, paths, ids unique within a
// body, caps); starts a run with each profile the manifest offers and checks that test-update
// records are well formed and name known items, test-coverage bodies are well formed, and test-end
// arrives within -test-timeout; cancels a run and expects its test-end within 5 s. test-exec is
// answered refused (the run must still end), or with a scripted program (-test-exec-script).

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var testTimeout = flag.Duration("test-timeout", 60*time.Second, "how long a test run may take before its test-end")
var testExecScript = flag.String("test-exec-script", "", "a file a test-exec program plays, a line at a time: out TEXT, err TEXT, sleep MS, exit CODE (default: test-exec is refused)")

type testItem struct {
	parent, kind string
	lazy         bool
}

type testRun struct {
	profile          string
	ended, cancelled bool
	records, unknown int
	coverage         int // files
	execs            []int64
	refusedExec      int
}

type testState struct {
	items   map[string]*testItem
	runs    map[int64]*testRun
	nextRun int64
	nextEx  int64
	stops   map[int64]chan struct{} // live scripted programs by EXEC
	script  []string
}

func (h *host) testsState() *testState {
	if h.tests == nil {
		h.tests = &testState{items: map[string]*testItem{}, runs: map[int64]*testRun{}, stops: map[int64]chan struct{}{}}
		if *testExecScript != "" {
			b, err := os.ReadFile(*testExecScript)
			if err != nil {
				fmt.Fprintln(os.Stderr, "-test-exec-script:", err)
				os.Exit(2)
			}
			sc := bufio.NewScanner(bytes.NewReader(b))
			for sc.Scan() {
				h.tests.script = append(h.tests.script, sc.Text())
			}
			if len(h.tests.script) == 0 {
				h.tests.script = []string{"exit 0"}
			}
		}
	}
	return h.tests
}

// ---- bodies, checked as rhun reads them

var testKinds = map[string]bool{"group": true, "file": true, "suite": true, "test": true}
var testStates = map[string]bool{"queued": true, "started": true, "passed": true, "failed": true, "errored": true, "skipped": true, "-": true}
var bareName = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// testID says why an item id is not one (1 to 512 bytes, no LF, CR or NUL), or ""
func testID(id []byte) string {
	if len(id) == 0 || len(id) > 512 {
		return fmt.Sprintf("the id %q is not 1 to 512 bytes", id)
	}
	if bytes.ContainsAny(id, "\n\r\x00") {
		return fmt.Sprintf("the id %q holds LF, CR or NUL", id)
	}
	return ""
}

// absPath: /… or C:/…
func absPath(p []byte) bool {
	return len(p) > 0 && (p[0] == '/' || (len(p) > 2 && p[1] == ':' && p[2] == '/' &&
		(p[0]|0x20) >= 'a' && (p[0]|0x20) <= 'z'))
}

type itemRec struct {
	id, kind string
	lazy     bool
}

// extraRec reads NAME ARGS… LEN LF and LEN bytes
func extraRec(c *cursor) (string, []string, []byte, string) {
	l, ok := c.line()
	if !ok {
		return "", nil, nil, "an extra record has no header line"
	}
	w := c.fields(l)
	if len(w) < 2 || count(w[len(w)-1]) < 0 {
		return "", nil, nil, fmt.Sprintf("the extra record %q is not NAME ARGS… LEN", l)
	}
	data, ok := c.take(count(w[len(w)-1]))
	if !ok {
		return "", nil, nil, fmt.Sprintf("the extra record %q is longer than its item", l)
	}
	return w[0], w[1 : len(w)-1], data, ""
}

// checkItems validates an items body: its parent and items, or why it is not one (rhun answers
// syntax, badpos or toobig and applies nothing)
func checkItems(b []byte) (string, []itemRec, []string, string) {
	if len(b) > 4<<20 {
		return "", nil, nil, fmt.Sprintf("the body is %d bytes: 4 MiB at most (toobig)", len(b))
	}
	c := &cursor{b: b}
	l, ok := c.line()
	if !ok {
		return "", nil, nil, "no first line PARENT_LEN N"
	}
	w := c.fields(l)
	if len(w) != 2 || numbers(w) != "" {
		return "", nil, nil, fmt.Sprintf("the first line %q is not PARENT_LEN N", l)
	}
	n := count(w[1])
	if n > 10000 {
		return "", nil, nil, fmt.Sprintf("%d items: 10,000 a body at most (toobig)", n)
	}
	parent, ok := c.take(count(w[0]))
	if !ok {
		return "", nil, nil, "the parent id is longer than the body"
	}
	if len(parent) > 0 {
		if why := testID(parent); why != "" {
			return "", nil, nil, "the parent: " + why
		}
	}
	var items []itemRec
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		l, ok := c.line()
		if !ok {
			return "", nil, nil, fmt.Sprintf("item %d of %d is missing", i+1, n)
		}
		w := c.fields(l)
		if len(w) != 10 {
			return "", nil, nil, fmt.Sprintf("the item line %q is not KIND L1 C1 L2 C2 FLAGS ID_LEN LABEL_LEN PATH_LEN EXTRA_LEN", l)
		}
		if !testKinds[w[0]] {
			return "", nil, nil, fmt.Sprintf("%q is not a kind (group, file, suite, test)", w[0])
		}
		if why := numbers(append(append([]string{}, w[1:5]...), w[6:]...)); why != "" {
			return "", nil, nil, fmt.Sprintf("the item line %q: %s", l, why)
		}
		lazy := false
		if w[5] != "-" {
			fl := map[string]bool{}
			for _, f := range strings.Split(w[5], ",") {
				if (f != "lazy" && f != "nodebug") || fl[f] {
					return "", nil, nil, fmt.Sprintf("the flags %q are not -, or a list of lazy and nodebug", w[5])
				}
				fl[f] = true
			}
			lazy = fl["lazy"]
		}
		id, ok1 := c.take(count(w[6]))
		label, ok2 := c.take(count(w[7]))
		path, ok3 := c.take(count(w[8]))
		extra, ok4 := c.take(count(w[9]))
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return "", nil, nil, fmt.Sprintf("the item line %q announces more bytes than the body has", l)
		}
		if why := testID(id); why != "" {
			return "", nil, nil, why
		}
		if seen[string(id)] {
			return "", nil, nil, fmt.Sprintf("the id %q twice in one body", id)
		}
		seen[string(id)] = true
		if len(label) > 200 {
			return "", nil, nil, fmt.Sprintf("the label of %q is %d bytes: 200 at most", id, len(label))
		}
		if why := oneLine("the label of "+strconv.Quote(string(id)), label); why != "" {
			return "", nil, nil, why
		}
		if len(path) > 0 && string(path) != "#DOC" && !absPath(path) {
			return "", nil, nil, fmt.Sprintf("the path %q of %q is not absolute, #DOC or empty", path, id)
		}
		r := [4]int{count(w[1]), count(w[2]), count(w[3]), count(w[4])}
		if r != [4]int{} {
			if r[0] < 1 || r[1] < 1 || r[3] < 1 || r[2] < r[0] || (r[2] == r[0] && r[3] < r[1]) {
				return "", nil, nil, fmt.Sprintf("the range %v of %q is not L1 C1 at or before L2 C2, one-based (badpos)", r, id)
			}
			if len(path) == 0 {
				return "", nil, nil, fmt.Sprintf("%q has a range but no path (badpos)", id)
			}
		}
		ec := &cursor{b: extra}
		for ec.rest() > 0 {
			name, _, data, why := extraRec(ec)
			if why != "" {
				return "", nil, nil, fmt.Sprintf("%q: %s", id, why)
			}
			switch name {
			case "desc":
				if len(data) > 200 || oneLine("desc", data) != "" {
					return "", nil, nil, fmt.Sprintf("the desc of %q is not one line of 200 bytes", id)
				}
			case "error":
				if len(data) > 1000 {
					return "", nil, nil, fmt.Sprintf("the error of %q is %d bytes: 1000 at most", id, len(data))
				}
			default:
				c.warn("%q: an extra record %q rhun skips", id, name)
			}
		}
		items = append(items, itemRec{string(id), w[0], lazy})
	}
	if c.rest() != 0 {
		return "", nil, nil, fmt.Sprintf("%d bytes after the last item", c.rest())
	}
	return string(parent), items, c.warns, ""
}

// checkResults validates a test-update body: its records, those naming items known() says no to,
// what is odd, or why rhun refuses it
func checkResults(b []byte, known func(string) bool) (int, int, []string, string) {
	if len(b) > 1<<20 {
		return 0, 0, nil, fmt.Sprintf("the body is %d bytes: 1 MiB at most (toobig)", len(b))
	}
	c := &cursor{b: b}
	n, why := c.head(2000, "records (toobig past 2,000)")
	if why != "" {
		return 0, 0, nil, why
	}
	unknown := 0
	for i := 0; i < n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, 0, nil, fmt.Sprintf("record %d of %d is missing", i+1, n)
		}
		w := c.fields(l)
		if len(w) != 4 {
			return 0, 0, nil, fmt.Sprintf("the record line %q is not STATE MS ITEM_LEN EXTRA_LEN", l)
		}
		if !testStates[w[0]] {
			return 0, 0, nil, fmt.Sprintf("%q is not a state (queued, started, passed, failed, errored, skipped, -)", w[0])
		}
		if w[1] != "-" && count(w[1]) < 0 {
			return 0, 0, nil, fmt.Sprintf("the duration %q is not milliseconds or -", w[1])
		}
		if numbers(w[2:]) != "" {
			return 0, 0, nil, fmt.Sprintf("the record line %q: %s", l, numbers(w[2:]))
		}
		item, ok1 := c.take(count(w[2]))
		extra, ok2 := c.take(count(w[3]))
		if !ok1 || !ok2 {
			return 0, 0, nil, fmt.Sprintf("the record line %q announces more bytes than the body has", l)
		}
		if len(item) == 0 {
			if w[0] != "-" && w[0] != "errored" {
				return 0, 0, nil, fmt.Sprintf("a record for the run itself (empty ITEM) with state %s: only - and errored", w[0])
			}
		} else if why := testID(item); why != "" {
			return 0, 0, nil, why
		} else if !known(string(item)) {
			unknown++
		}
		ec := &cursor{b: extra}
		messages := 0
		for ec.rest() > 0 {
			name, args, data, why := extraRec(ec)
			if why != "" {
				return 0, 0, nil, fmt.Sprintf("the record of %q: %s", item, why)
			}
			switch name {
			case "message":
				messages++
				if len(data) > 8000 {
					c.warn("a message of %q is %d bytes: rhun keeps 8,000", item, len(data))
				}
			case "location":
				r := make([]int, len(args))
				for i, a := range args {
					r[i] = count(a)
				}
				if len(r) != 4 || r[0] < 1 || r[1] < 1 || r[2] < r[0] || r[3] < 1 || (r[2] == r[0] && r[3] < r[1]) {
					return 0, 0, nil, fmt.Sprintf("location %v of %q is not L1 C1 L2 C2 LEN, one-based", args, item)
				}
				if !absPath(data) {
					return 0, 0, nil, fmt.Sprintf("the location path %q of %q is not absolute", data, item)
				}
				if messages == 0 {
					c.warn("a location of %q before any message: rhun drops it", item)
				}
			case "expected", "actual":
				if messages == 0 {
					c.warn("%s of %q before any message: rhun drops it", name, item)
				}
				if len(data) > 64<<10 {
					c.warn("%s of %q is %d bytes: rhun keeps 64 KiB", name, item, len(data))
				}
			case "output":
			default:
				c.warn("the record of %q: an extra record %q rhun skips", item, name)
			}
		}
		if messages > 16 {
			c.warn("%d messages for %q: rhun keeps 16 per item per run", messages, item)
		}
	}
	if c.rest() != 0 {
		return 0, 0, nil, fmt.Sprintf("%d bytes after the last record", c.rest())
	}
	return n, unknown, c.warns, ""
}

// checkCoverage validates a coverage body: its files, or why rhun refuses it
func checkCoverage(b []byte) (int, string) {
	if len(b) > 4<<20 {
		return 0, fmt.Sprintf("the body is %d bytes: 4 MiB a message at most (toobig)", len(b))
	}
	c := &cursor{b: b}
	n, why := c.head(20000, "files (toobig past 20,000)")
	if why != "" {
		return 0, why
	}
	for i := 0; i < n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, fmt.Sprintf("file %d of %d is missing", i+1, n)
		}
		w := c.fields(l)
		if len(w) != 2 || numbers(w) != "" {
			return 0, fmt.Sprintf("the file line %q is not PATH_LEN M", l)
		}
		path, ok := c.take(count(w[0]))
		if !ok || !absPath(path) {
			return 0, fmt.Sprintf("the coverage path %q is not absolute", path)
		}
		for k := 0; k < count(w[1]); k++ {
			l, ok := c.line()
			if !ok {
				return 0, fmt.Sprintf("%s: range %d of %s is missing", path, k+1, w[1])
			}
			r := c.fields(l)
			if len(r) != 3 || numbers(r) != "" {
				return 0, fmt.Sprintf("%s: the range %q is not L1 L2 COUNT", path, l)
			}
			if a, z := count(r[0]), count(r[1]); a < 1 || z < a || z > 1048576 {
				return 0, fmt.Sprintf("%s: lines %s to %s are not one-based, in order, at most 1,048,576", path, r[0], r[1])
			}
		}
	}
	if c.rest() != 0 {
		return 0, fmt.Sprintf("%d bytes after the last file", c.rest())
	}
	return n, ""
}

// checkCommand validates a test-exec command body: the program and folder, or the code and why
// rhun refuses it
func checkCommand(b []byte) (string, string, string, string) {
	if len(b) > 256<<10 {
		return "", "", "toobig", "the command is over 256 KiB"
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", "", "syntax", "a NUL in the command"
	}
	c := &cursor{b: b}
	l, ok := c.line()
	w := c.fields(l)
	if !ok || len(w) != 4 || numbers(w) != "" {
		return "", "", "syntax", fmt.Sprintf("the first line %q is not PROGRAM_LEN CWD_LEN ARGC ENVC", l)
	}
	prog, ok1 := c.take(count(w[0]))
	cwd, ok2 := c.take(count(w[1]))
	if !ok1 || !ok2 {
		return "", "", "syntax", "the program and folder are longer than the body"
	}
	argc, envc := count(w[2]), count(w[3])
	if argc > 1000 || envc > 64 {
		return "", "", "toobig", fmt.Sprintf("%d arguments and %d variables: 1,000 and 64 at most", argc, envc)
	}
	for i := 0; i < argc+envc; i++ {
		l, ok := c.line()
		n := count(l)
		if !ok || n < 0 {
			return "", "", "syntax", fmt.Sprintf("argument or variable %d has no LEN line", i+1)
		}
		v, ok := c.take(n)
		if !ok {
			return "", "", "syntax", fmt.Sprintf("argument or variable %d is longer than the body", i+1)
		}
		if i < argc && n > 32<<10 {
			return "", "", "toobig", fmt.Sprintf("argument %d is over 32 KiB", i+1)
		}
		if i >= argc {
			name, _, eq := bytes.Cut(v, []byte("="))
			if !eq || !envName.Match(name) {
				return "", "", "syntax", fmt.Sprintf("the variable %q is not NAME=VALUE with NAME [A-Z_][A-Z0-9_]*", v)
			}
		}
	}
	if c.rest() != 0 {
		return "", "", "syntax", fmt.Sprintf("%d bytes after the last variable", c.rest())
	}
	if !bareName.Match(prog) {
		return "", "", "refused", fmt.Sprintf("the program %q must be a bare name", prog)
	}
	return string(prog), string(cwd), "", ""
}

// ---- the model

func (t *testState) remove(id string) {
	delete(t.items, id)
	for k, it := range t.items {
		if it.parent == id {
			t.remove(k)
		}
	}
}

// apply puts items under parent: replace drops parent's other children
func (t *testState) apply(parent string, items []itemRec, replace bool) {
	if replace {
		keep := map[string]bool{}
		for _, it := range items {
			keep[it.id] = true
		}
		for k, it := range t.items {
			if it.parent == parent && !keep[k] {
				t.remove(k)
			}
		}
	}
	for _, it := range items {
		t.items[it.id] = &testItem{parent: parent, kind: it.kind, lazy: it.lazy}
	}
}

// ---- the extension's requests

func (h *host) testRequest(id int64, verb string, args []string, msg message, reply func(string), rerr func(string, string)) {
	if !h.features["tests"] {
		h.fail("features", verb+" without tests agreed after hello (hello 1 tests)")
		rerr("unknown", "no such request")
		return
	}
	t := h.testsState()
	run := func() (int64, *testRun, bool) {
		n, err := strconv.ParseInt(args[0], 10, 64)
		r := t.runs[n]
		switch {
		case err != nil || r == nil:
			h.fail("tests", fmt.Sprintf("%s names run %s, which rhun never started", verb, args[0]))
			rerr("stale", "run over")
			return 0, nil, false
		case r.ended:
			h.fail("tests", fmt.Sprintf("%s for run %d after its test-end", verb, n))
			rerr("stale", "run over")
			return 0, nil, false
		}
		return n, r, true
	}
	if msg.err != nil {
		h.fail("bodies", fmt.Sprintf("%s: %v", verb, msg.err))
		rerr("syntax", "the body")
		return
	}
	switch verb {
	case "test-items":
		if len(args) != 2 || (args[0] != "replace" && args[0] != "add") {
			h.fail("tests", fmt.Sprintf("test-items takes MODE LEN with MODE replace or add, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		parent, items, warns, why := checkItems(msg.body)
		if why != "" {
			h.fail("tests", "test-items: "+why)
			rerr("syntax", why)
			return
		}
		for _, w := range warns {
			h.warn("tests", "test-items: "+w)
		}
		if parent != "" && t.items[parent] == nil {
			h.fail("tests", fmt.Sprintf("test-items under %q, an item rhun does not have", parent))
			rerr("refused", "unknown parent")
			return
		}
		t.apply(parent, items, args[0] == "replace")
		reply(fmt.Sprintf("ok %d", id))
	case "test-remove":
		if len(args) != 2 || count(args[0]) < 0 {
			h.fail("tests", "test-remove takes N LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		ids := strings.SplitAfter(string(msg.body), "\n")
		if len(ids) > 0 && ids[len(ids)-1] == "" {
			ids = ids[:len(ids)-1]
		}
		if len(ids) != count(args[0]) {
			h.fail("tests", fmt.Sprintf("test-remove says %s ids and sends %d (each ending in LF)", args[0], len(ids)))
			rerr("syntax", "the ids")
			return
		}
		for _, s := range ids {
			if !strings.HasSuffix(s, "\n") || testID([]byte(strings.TrimSuffix(s, "\n"))) != "" {
				h.fail("tests", fmt.Sprintf("test-remove: %q is not an id ending in LF", s))
				rerr("syntax", "the ids")
				return
			}
		}
		for _, s := range ids {
			t.remove(strings.TrimSuffix(s, "\n"))
		}
		reply(fmt.Sprintf("ok %d", id))
	case "test-update":
		if len(args) != 2 {
			h.fail("tests", "test-update takes RUN LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		_, r, ok := run()
		if !ok {
			return
		}
		n, unknown, warns, why := checkResults(msg.body, func(s string) bool { return t.items[s] != nil })
		if why != "" {
			h.fail("tests", "test-update: "+why)
			rerr("syntax", why)
			return
		}
		for _, w := range warns {
			h.warn("tests", "test-update: "+w)
		}
		r.records += n
		r.unknown += unknown
		reply(fmt.Sprintf("ok %d", id))
	case "test-coverage":
		if len(args) != 2 {
			h.fail("tests", "test-coverage takes RUN LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		_, r, ok := run()
		if !ok {
			return
		}
		n, why := checkCoverage(msg.body)
		if why != "" {
			h.fail("tests", "test-coverage: "+why)
			rerr("syntax", why)
			return
		}
		if r.profile != "coverage" {
			h.warn("tests", fmt.Sprintf("test-coverage in a %s run", r.profile))
		}
		r.coverage += n
		reply(fmt.Sprintf("ok %d", id))
	case "test-end":
		if len(args) != 1 {
			h.fail("tests", "test-end takes RUN")
			rerr("syntax", "cannot parse the line")
			return
		}
		_, r, ok := run()
		if !ok {
			return
		}
		r.ended = true
		h.stopPrograms(r)
		reply(fmt.Sprintf("ok %d", id))
	case "test-exec":
		if len(args) != 2 {
			h.fail("tests", "test-exec takes RUN LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		_, r, ok := run()
		if !ok {
			return
		}
		prog, cwd, code, why := checkCommand(msg.body)
		if code != "" {
			h.fail("tests", "test-exec: "+why)
			rerr(code, why)
			return
		}
		if !slices.Contains(strings.Fields(h.m.needs["tests"]), prog) {
			h.fail("needs", fmt.Sprintf("test-exec %s, which [needs] tests does not name", prog))
			rerr("needs", "tests="+prog)
			return
		}
		if cwd != "" && !absPath([]byte(cwd)) {
			h.fail("tests", fmt.Sprintf("test-exec in %q, not an absolute folder", cwd))
			rerr("refused", "the folder is not inside the project")
			return
		}
		if t.script == nil {
			r.refusedExec++
			rerr("refused", "conformance: test-exec is refused without -test-exec-script")
			return
		}
		t.nextEx++
		ex := t.nextEx
		stop := make(chan struct{})
		t.stops[ex] = stop
		r.execs = append(r.execs, ex)
		reply(fmt.Sprintf("ok %d %d", id, ex))
		go h.play(ex, t.script, stop)
	}
}

// play is a scripted program: its output and end as rhun sends them; stop kills it
func (h *host) play(ex int64, script []string, stop chan struct{}) {
	code := "0"
	for _, l := range script {
		select {
		case <-stop:
			h.write(fmt.Sprintf("0 test-exit %d killed\n", ex))
			return
		default:
		}
		verb, rest, _ := strings.Cut(l, " ")
		switch verb {
		case "out", "err":
			text := strings.ReplaceAll(rest, `\n`, "\n") + "\n"
			h.write(fmt.Sprintf("0 test-out %d %s %d\n%s", ex, verb, len(text), text))
		case "sleep":
			ms, _ := strconv.Atoi(rest)
			select {
			case <-stop:
				h.write(fmt.Sprintf("0 test-exit %d killed\n", ex))
				return
			case <-time.After(time.Duration(ms) * time.Millisecond):
			}
		case "exit":
			code = rest
		}
		if verb == "exit" {
			break
		}
	}
	h.write(fmt.Sprintf("0 test-exit %d %s\n", ex, code))
}

// stopPrograms kills a run's live programs, as rhun does when the run ends
func (h *host) stopPrograms(r *testRun) {
	t := h.testsState()
	for _, ex := range r.execs {
		if s := t.stops[ex]; s != nil {
			close(s)
			delete(t.stops, ex)
		}
	}
}

// ---- rhun's requests

// serveUntil serves the extension until done says so or the time is up
func (h *host) serveUntil(done func() bool, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for !done() && time.Now().Before(deadline) {
		msg, err := h.readMessage(100 * time.Millisecond)
		if err == nil && msg.line != "" {
			h.handle(msg)
		}
		if h.cmdExited() {
			break
		}
	}
	return done()
}

// resolveTests asks for parent's children and applies them: their records, or false
func (h *host) resolveTests(parent string) ([]itemRec, bool) {
	t := h.testsState()
	what := "the roots"
	if parent != "" {
		what = strconv.Quote(parent)
	}
	m, ok := h.askBody("tests-resolve", []byte(parent), 30*time.Second)
	f := strings.Fields(m.line)
	switch {
	case !ok:
		h.fail("tests", "no answer to tests-resolve for "+what+" within 30 s (rhun shows \"no answer in 30 s\")")
		return nil, false
	case len(f) > 0 && f[0] == "error":
		h.warn("tests", "tests-resolve for "+what+" answered "+m.line+" (rhun shows it on the row)")
		return nil, false
	}
	got, items, warns, why := checkItems(m.body)
	switch {
	case why != "":
		h.fail("tests", "tests-resolve for "+what+": "+why)
		return nil, false
	case got != parent:
		h.fail("tests", fmt.Sprintf("tests-resolve for %s answered items for %q: the body's PARENT is the item asked for", what, got))
		return nil, false
	}
	for _, w := range warns {
		h.warn("tests", "tests-resolve: "+w)
	}
	t.apply(parent, items, true)
	if it := t.items[parent]; it != nil {
		it.lazy = false
	}
	return items, true
}

// runTests starts a run with a profile and waits for its end; cancel cancels it first
func (h *host) runTests(profile string, cancel bool) {
	t := h.testsState()
	t.nextRun++
	n := t.nextRun
	r := &testRun{profile: profile}
	t.runs[n] = r
	what := profile + " run"
	if cancel {
		what = "cancelled run"
	}
	m, ok := h.askWithin(fmt.Sprintf("test-run %d %s 0 0", n, profile), 10*time.Second)
	f := strings.Fields(m.line)
	switch {
	case !ok:
		h.fail("tests", fmt.Sprintf("no answer to test-run (%s) within 10 s", profile))
		r.ended = true
		h.stopPrograms(r)
		return
	case len(f) >= 3 && f[0] == "error":
		if f[2] != "busy" && f[2] != "refused" {
			h.warn("tests", fmt.Sprintf("test-run (%s) answered %q: rhun expects busy or refused", profile, m.line))
		}
		r.ended = true
		h.stopPrograms(r)
		h.ok(fmt.Sprintf("refuses a %s with %s (the run ends errored)", what, f[2]))
		return
	case len(f) != 3 || f[0] != "ok" || f[2] != "0":
		h.fail("tests", fmt.Sprintf("test-run (%s) answered %q, not ok ID 0", profile, m.line))
	}
	if cancel {
		h.serveUntil(func() bool { return r.ended }, 300*time.Millisecond)
		if r.ended {
			h.ok("a run ended before its cancel")
			return
		}
		r.cancelled = true
		h.write(fmt.Sprintf("0 test-cancel %d\n", n))
		// rhun stops the run's programs after 2 s and ends the run itself after 5 s
		if !h.serveUntil(func() bool { return r.ended }, 2*time.Second) {
			h.stopPrograms(r)
			h.serveUntil(func() bool { return r.ended }, 3*time.Second)
		}
		if r.ended {
			h.ok("ends a cancelled run with test-end within 5 s")
		} else {
			h.fail("tests", "no test-end within 5 s of test-cancel (rhun ends the run itself, errored)")
			r.ended = true
		}
		return
	}
	if !h.serveUntil(func() bool { return r.ended }, *testTimeout) {
		h.fail("tests", fmt.Sprintf("no test-end for a %s within %v (-test-timeout)", what, *testTimeout))
		r.ended = true
		h.stopPrograms(r)
		return
	}
	if r.unknown > 0 {
		h.fail("tests", fmt.Sprintf("%d result records of a %s name items rhun does not have (it drops them)", r.unknown, what))
	}
	note := fmt.Sprintf("%d records", r.records)
	if r.coverage > 0 {
		note += fmt.Sprintf(", coverage of %d files", r.coverage)
	}
	if r.refusedExec > 0 {
		note += fmt.Sprintf(", after %d refused test-exec", r.refusedExec)
	}
	h.ok(fmt.Sprintf("a %s ends with test-end (%s)", what, note))
}

// checkTests is step 12 for tests: the tree, a run per profile, and a cancelled run
func (h *host) checkTests() {
	if !h.features["tests"] || h.patterns("tests") == "" {
		return
	}
	profiles := []string{"run"}
	for _, p := range strings.Fields(h.m.provides["tests.profiles"]) {
		switch p {
		case "run":
		case "debug", "coverage":
			profiles = append(profiles, p)
		default:
			h.fail("provides", "tests.profiles: "+p+" is not run, debug or coverage")
		}
	}
	h.sampleFor(h.patterns("tests"))
	h.serve(300 * time.Millisecond)
	roots, ok := h.resolveTests("")
	if ok {
		h.ok(fmt.Sprintf("answers tests-resolve for the roots with items rhun can read (%d)", len(roots)))
		asked := 0
		for _, it := range roots {
			if it.lazy && asked < 5 {
				asked++
				if kids, ok := h.resolveTests(it.id); ok {
					h.ok(fmt.Sprintf("answers tests-resolve for the lazy item %q (%d items)", it.id, len(kids)))
				}
			}
		}
	}
	if h.cmdExited() {
		h.fail("tests", "the program exited on tests-resolve")
		return
	}
	for _, p := range profiles {
		h.runTests(p, false)
		if h.cmdExited() {
			h.fail("tests", "the program exited during a "+p+" run")
			return
		}
	}
	h.runTests("run", true)
}
