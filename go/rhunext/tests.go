package rhunext

// The tests feature (v1.1.md "Feature: tests"): a test controller publishes a tree of test items,
// answers tests-resolve for lazy ones, runs what rhun asks with test-run and reports results until
// EndTestRun; TestExec starts a program as the user (one of the manifest's [needs] tests) and
// streams its output back. A handler implements TestController to receive the requests.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// FeatureTests is the feature name for hello.
const FeatureTests = "tests"

// Limits of the tests feature.
const (
	MaxTestID        = 512
	MaxTestLabel     = 200
	MaxTestItems     = 10000 // per body
	MaxTestResults   = 2000  // records per test-update
	MaxTestResultsLn = 1 << 20
	MaxTestMessage   = 8000
	MaxTestValue     = 64 << 10
	maxTestOutQueue  = 1 << 20 // bytes of a process's output waiting to be read, per stream
)

// TestKind is what an item is.
type TestKind string

const (
	TestGroup TestKind = "group" // a module, package, crate or folder
	TestFile  TestKind = "file"
	TestSuite TestKind = "suite"
	TestCase  TestKind = "test"
)

// TestItem is one node of the tree. Range (one-based lines and byte columns) is in Path, an
// absolute file path (or "#DOC" for an open document's file).
type TestItem struct {
	ID, Label   string
	Kind        TestKind
	Path        string
	Range       *Range
	Lazy        bool // it has children rhun has not been given: rhun asks with tests-resolve
	NoDebug     bool
	Desc, Error string
}

// TestRun is a run rhun asks for: its number, profile ("run", "debug" or "coverage") and the items
// (none: everything).
type TestRun struct {
	Run     int64
	Profile string
	Items   []string
}

// TestState is a result's state; "" changes nothing (an output-only record).
type TestState string

const (
	TestQueued  TestState = "queued"
	TestStarted TestState = "started"
	TestPassed  TestState = "passed"
	TestFailed  TestState = "failed"
	TestErrored TestState = "errored"
	TestSkipped TestState = "skipped"
)

// NoDuration says a result carries no duration.
const NoDuration time.Duration = -1

// TestMessage is a failure's text, where it points, and the values compared.
type TestMessage struct {
	Text             string
	Path             string
	Range            *Range
	Expected, Actual *string
}

// TestResult is one record of a test-update: an item ("" for the run itself, which takes only
// TestErrored or ""), a state, a duration (NoDuration for none), messages and output.
type TestResult struct {
	Item     string
	State    TestState
	Duration time.Duration
	Messages []TestMessage
	Output   []byte
}

// CoverageRange is lines From to To (one-based, inclusive) and how often they ran.
type CoverageRange struct {
	From, To int
	Count    int64
}

// FileCoverage is a file's ranges.
type FileCoverage struct {
	Path   string
	Ranges []CoverageRange
}

// TestCommand is a program to start as the user: a bare name from [needs] tests, its folder
// ("" for the project), arguments and extra NAME=VALUE variables.
type TestCommand struct {
	Program string
	Dir     string
	Args    []string
	Env     []string
}

// TestController answers rhun's test requests (feature tests). RunTests must return at once:
// report on a goroutine of your own (TestReporter) and end the run with EndTestRun. CancelTests is
// the user's Cancel: stop and end the run.
type TestController interface {
	ResolveTests(ext *Ext, parent string) ([]TestItem, error)
	RunTests(ext *Ext, run TestRun) error
	CancelTests(ext *Ext, run int64)
}

func (e *Ext) testsAgreed() error {
	if !e.Has(FeatureTests) {
		return &Error{Code: "unknown", Text: "tests was not negotiated"}
	}
	return nil
}

func testText(s string, max int, allowEmpty bool) bool {
	if len(s) > max || (!allowEmpty && s == "") || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsAny(s, "\n\r\x00")
}

// EncodeTestItems is the items body for parent.
func EncodeTestItems(parent string, items []TestItem) ([]byte, error) {
	if len(items) > MaxTestItems {
		return nil, &Error{Code: "toobig", Text: "10000 items per body"}
	}
	if parent != "" && !testText(parent, MaxTestID, false) {
		return nil, &Error{Code: "syntax", Text: "bad parent id"}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%d %d\n%s", len(parent), len(items), parent)
	for _, it := range items {
		if !testText(it.ID, MaxTestID, false) || !testText(it.Label, MaxTestLabel, true) ||
			!testText(it.Path, 4095, true) || !testText(it.Desc, 200, true) || len(it.Error) > 1000 {
			return nil, &Error{Code: "syntax", Text: "bad item " + strconv.Quote(it.ID)}
		}
		switch it.Kind {
		case TestGroup, TestFile, TestSuite, TestCase:
		default:
			return nil, &Error{Code: "syntax", Text: "bad kind " + string(it.Kind)}
		}
		var flags []string
		if it.Lazy {
			flags = append(flags, "lazy")
		}
		if it.NoDebug {
			flags = append(flags, "nodebug")
		}
		f := "-"
		if len(flags) > 0 {
			f = strings.Join(flags, ",")
		}
		var extra bytes.Buffer
		if it.Desc != "" {
			fmt.Fprintf(&extra, "desc %d\n%s", len(it.Desc), it.Desc)
		}
		if it.Error != "" {
			fmt.Fprintf(&extra, "error %d\n%s", len(it.Error), it.Error)
		}
		l1, c1, l2, c2 := 0, 0, 0, 0
		if it.Range != nil {
			l1, c1, l2, c2 = it.Range.Start.Line, it.Range.Start.Col, it.Range.End.Line, it.Range.End.Col
			if l1 < 1 || c1 < 1 || c2 < 1 || l2 < l1 || (l2 == l1 && c2 < c1) || it.Path == "" {
				return nil, &Error{Code: "badpos", Text: "bad range of " + strconv.Quote(it.ID)}
			}
		}
		fmt.Fprintf(&b, "%s %d %d %d %d %s %d %d %d %d\n%s%s%s", it.Kind, l1, c1, l2, c2, f,
			len(it.ID), len(it.Label), len(it.Path), extra.Len(), it.ID, it.Label, it.Path)
		b.Write(extra.Bytes())
	}
	if b.Len() > 4<<20 {
		return nil, &Error{Code: "toobig", Text: "4 MiB per body"}
	}
	return b.Bytes(), nil
}

// ParseTestItems decodes an items body (the checker; tests).
func ParseTestItems(body []byte) (string, []TestItem, error) {
	nl := bytes.IndexByte(body, '\n')
	if nl < 0 {
		return "", nil, errors.New("items: no header")
	}
	var plen, n int
	if _, err := fmt.Sscanf(string(body[:nl]), "%d %d", &plen, &n); err != nil || plen < 0 || n < 0 {
		return "", nil, errors.New("items: bad header")
	}
	body = body[nl+1:]
	if plen > len(body) {
		return "", nil, errors.New("items: short parent")
	}
	parent := string(body[:plen])
	body = body[plen:]
	var items []TestItem
	for i := 0; i < n; i++ {
		nl = bytes.IndexByte(body, '\n')
		if nl < 0 {
			return parent, items, errors.New("items: short item")
		}
		f := strings.Fields(string(body[:nl]))
		if len(f) != 10 {
			return parent, items, errors.New("items: an item header has not 10 fields")
		}
		body = body[nl+1:]
		num := func(s string) int { v, _ := strconv.Atoi(s); return v }
		l := []int{num(f[6]), num(f[7]), num(f[8]), num(f[9])}
		total := l[0] + l[1] + l[2] + l[3]
		if total > len(body) {
			return parent, items, errors.New("items: short item bytes")
		}
		it := TestItem{Kind: TestKind(f[0]), ID: string(body[:l[0]]), Label: string(body[l[0] : l[0]+l[1]]),
			Path: string(body[l[0]+l[1] : l[0]+l[1]+l[2]])}
		extra := body[l[0]+l[1]+l[2] : total]
		body = body[total:]
		if r := [4]int{num(f[1]), num(f[2]), num(f[3]), num(f[4])}; r != [4]int{} {
			it.Range = &Range{Pos{r[0], r[1]}, Pos{r[2], r[3]}}
		}
		for _, fl := range strings.Split(f[5], ",") {
			switch fl {
			case "lazy":
				it.Lazy = true
			case "nodebug":
				it.NoDebug = true
			}
		}
		for len(extra) > 0 {
			name, _, data, rest, ok := extraRecord(extra)
			if !ok {
				return parent, items, errors.New("items: bad extra record")
			}
			switch name {
			case "desc":
				it.Desc = string(data)
			case "error":
				it.Error = string(data)
			}
			extra = rest
		}
		items = append(items, it)
	}
	if len(body) != 0 {
		return parent, items, errors.New("items: trailing bytes")
	}
	return parent, items, nil
}

// extraRecord splits "NAME ARGS… LEN" LF and LEN bytes off the front.
func extraRecord(b []byte) (name string, args []string, data, rest []byte, ok bool) {
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return
	}
	f := strings.Fields(string(b[:nl]))
	if len(f) < 2 {
		return
	}
	n, err := strconv.Atoi(f[len(f)-1])
	if err != nil || n < 0 || n > len(b)-nl-1 {
		return
	}
	return f[0], f[1 : len(f)-1], b[nl+1 : nl+1+n], b[nl+1+n:], true
}

// SetTestItems publishes items under parent ("" for the roots): replace makes them parent's only
// children, in this order; otherwise they are added or updated.
func (e *Ext) SetTestItems(parent string, items []TestItem, replace bool) error {
	if err := e.testsAgreed(); err != nil {
		return err
	}
	body, err := EncodeTestItems(parent, items)
	if err != nil {
		return err
	}
	mode := "add"
	if replace {
		mode = "replace"
	}
	_, err = e.Request("test-items", body, mode, strconv.Itoa(len(body)))
	return err
}

// RemoveTestItems removes items and their descendants.
func (e *Ext) RemoveTestItems(ids ...string) error {
	if err := e.testsAgreed(); err != nil {
		return err
	}
	var b bytes.Buffer
	for _, id := range ids {
		if !testText(id, MaxTestID, false) {
			return &Error{Code: "syntax", Text: "bad id"}
		}
		b.WriteString(id)
		b.WriteByte('\n')
	}
	_, err := e.Request("test-remove", b.Bytes(), strconv.Itoa(len(ids)), strconv.Itoa(b.Len()))
	return err
}

// EncodeTestResults is a test-update body.
func EncodeTestResults(rs []TestResult) ([]byte, error) {
	if len(rs) > MaxTestResults {
		return nil, &Error{Code: "toobig", Text: "2000 records per body"}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%d\n", len(rs))
	for _, r := range rs {
		state := string(r.State)
		if state == "" {
			state = "-"
		}
		if r.Item == "" && r.State != "" && r.State != TestErrored {
			return nil, &Error{Code: "syntax", Text: "the run itself takes errored or no state"}
		}
		if len(r.Item) > MaxTestID || strings.ContainsAny(r.Item, "\n\r\x00") {
			return nil, &Error{Code: "syntax", Text: "bad item"}
		}
		ms := "-"
		if r.State != "" && r.Duration >= 0 {
			ms = strconv.FormatInt(r.Duration.Milliseconds(), 10)
		}
		var extra bytes.Buffer
		for _, m := range r.Messages {
			fmt.Fprintf(&extra, "message %d\n%s", len(m.Text), m.Text)
			if m.Range != nil && m.Path != "" {
				fmt.Fprintf(&extra, "location %d %d %d %d %d\n%s", m.Range.Start.Line, m.Range.Start.Col,
					m.Range.End.Line, m.Range.End.Col, len(m.Path), m.Path)
			}
			if m.Expected != nil {
				fmt.Fprintf(&extra, "expected %d\n%s", len(*m.Expected), *m.Expected)
			}
			if m.Actual != nil {
				fmt.Fprintf(&extra, "actual %d\n%s", len(*m.Actual), *m.Actual)
			}
		}
		if len(r.Output) > 0 {
			fmt.Fprintf(&extra, "output %d\n", len(r.Output))
			extra.Write(r.Output)
		}
		fmt.Fprintf(&b, "%s %s %d %d\n%s", state, ms, len(r.Item), extra.Len(), r.Item)
		b.Write(extra.Bytes())
	}
	if b.Len() > MaxTestResultsLn {
		return nil, &Error{Code: "toobig", Text: "1 MiB per body"}
	}
	return b.Bytes(), nil
}

// ReportTests sends one test-update of the run. Bodies past rhun's caps (2,000 records, 1 MiB)
// are split.
func (e *Ext) ReportTests(run int64, rs ...TestResult) error {
	if err := e.testsAgreed(); err != nil {
		return err
	}
	for len(rs) > 0 {
		n := len(rs)
		if n > MaxTestResults {
			n = MaxTestResults
		}
		body, err := EncodeTestResults(rs[:n])
		for err != nil && n > 1 {
			if ee, ok := err.(*Error); !ok || ee.Code != "toobig" {
				break
			}
			n /= 2
			body, err = EncodeTestResults(rs[:n])
		}
		if err != nil {
			return err
		}
		if _, err := e.Request("test-update", body, itoa(run), strconv.Itoa(len(body))); err != nil {
			return err
		}
		rs = rs[n:]
	}
	return nil
}

// EndTestRun says the run is over.
func (e *Ext) EndTestRun(run int64) error {
	if err := e.testsAgreed(); err != nil {
		return err
	}
	_, err := e.Request("test-end", nil, itoa(run))
	return err
}

// ReportCoverage sends line coverage found by a coverage run.
func (e *Ext) ReportCoverage(run int64, files []FileCoverage) error {
	if err := e.testsAgreed(); err != nil {
		return err
	}
	for len(files) > 0 {
		var b bytes.Buffer
		n := 0
		var rest bytes.Buffer
		for n < len(files) {
			rest.Reset()
			f := files[n]
			fmt.Fprintf(&rest, "%d %d\n%s", len(f.Path), len(f.Ranges), f.Path)
			for _, r := range f.Ranges {
				fmt.Fprintf(&rest, "%d %d %d\n", r.From, r.To, r.Count)
			}
			if n > 0 && b.Len()+rest.Len() > 4<<20-32 {
				break
			}
			b.Write(rest.Bytes())
			n++
		}
		body := append([]byte(strconv.Itoa(n)+"\n"), b.Bytes()...)
		if _, err := e.Request("test-coverage", body, itoa(run), strconv.Itoa(len(body))); err != nil {
			return err
		}
		files = files[n:]
	}
	return nil
}

// ---- test-exec

// TestProcess is a program rhun started for a run. Read Stdout and Stderr on goroutines that never
// wait for rhun (report through a TestReporter): rhun stops sending output while it is not read.
type TestProcess struct {
	Exec   int64
	Stdout io.Reader
	Stderr io.Reader
	p      *testProc
}

type testStream struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
}

func newTestStream() *testStream {
	s := &testStream{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *testStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.buf.Len() == 0 && !s.closed {
		s.cond.Wait()
	}
	if s.buf.Len() == 0 {
		return 0, io.EOF
	}
	n, _ := s.buf.Read(p)
	s.cond.Broadcast()
	return n, nil
}

// put adds bytes, waiting while the stream holds maxTestOutQueue (rhun's back-pressure reaches the
// program through the pipe); a closed stream drops them
func (s *testStream) put(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.buf.Len() >= maxTestOutQueue && !s.closed {
		s.cond.Wait()
	}
	if !s.closed {
		s.buf.Write(b)
	}
	s.cond.Broadcast()
}

func (s *testStream) close() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

type testProc struct {
	out, err *testStream
	code     string
	done     chan struct{}
	ended    bool // test-exit came
	claimed  bool // TestExec has it
}

func (e *Ext) testProcFor(ex int64) *testProc {
	e.tmu.Lock()
	defer e.tmu.Unlock()
	if e.tprocs == nil {
		e.tprocs = map[int64]*testProc{}
	}
	p := e.tprocs[ex]
	if p == nil {
		p = &testProc{out: newTestStream(), err: newTestStream(), done: make(chan struct{})}
		e.tprocs[ex] = p
	}
	return p
}

// testOut and testExit run on the reader goroutine: output into the process's streams
func (e *Ext) testOut(ex int64, stream string, body []byte) {
	p := e.testProcFor(ex)
	if stream == "err" {
		p.err.put(body)
	} else {
		p.out.put(body)
	}
}

func (e *Ext) testExit(ex int64, code string) {
	p := e.testProcFor(ex)
	p.out.close()
	p.err.close()
	e.tmu.Lock()
	p.code = code
	p.ended = true
	if p.claimed {
		delete(e.tprocs, ex) // an exit before TestExec read its reply keeps the entry for it
	}
	e.tmu.Unlock()
	close(p.done)
}

// testsGone: rhun went away: every process's streams end, as killed
func (e *Ext) testsGone() {
	e.tmu.Lock()
	ps := e.tprocs
	e.tprocs = nil
	e.tmu.Unlock()
	for _, p := range ps {
		if p.ended {
			continue
		}
		p.out.close()
		p.err.close()
		p.code = "killed"
		p.ended = true
		close(p.done)
	}
}

// TestExec asks rhun to start a program for the run.
func (e *Ext) TestExec(run int64, c TestCommand) (*TestProcess, error) {
	if err := e.testsAgreed(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%d %d %d %d\n%s%s", len(c.Program), len(c.Dir), len(c.Args), len(c.Env), c.Program, c.Dir)
	for _, a := range c.Args {
		fmt.Fprintf(&b, "%d\n%s", len(a), a)
	}
	for _, v := range c.Env {
		fmt.Fprintf(&b, "%d\n%s", len(v), v)
	}
	if bytes.IndexByte(b.Bytes(), 0) >= 0 {
		return nil, &Error{Code: "syntax", Text: "a NUL in the command"}
	}
	r, err := e.Request("test-exec", b.Bytes(), itoa(run), strconv.Itoa(b.Len()))
	if err != nil {
		return nil, err
	}
	if len(r.Args) != 1 {
		return nil, errors.New("test-exec: bad reply")
	}
	ex, err := strconv.ParseInt(r.Args[0], 10, 64)
	if err != nil || ex <= 0 {
		return nil, errors.New("test-exec: bad reply")
	}
	p := e.testProcFor(ex)
	e.tmu.Lock()
	p.claimed = true
	if p.ended {
		delete(e.tprocs, ex)
	}
	e.tmu.Unlock()
	return &TestProcess{Exec: ex, Stdout: p.out, Stderr: p.err, p: p}, nil
}

// Wait waits for the program's end: its code is the exit status ("0", "1"), "signal N", or
// "killed" (rhun stopped it); ExitCode is the status as a number (-1 for the others).
func (p *TestProcess) Wait() (code string) {
	<-p.p.done
	return p.p.code
}

// ExitCode is Wait's code as a number, -1 when it is not an exit status.
func (p *TestProcess) ExitCode() int {
	c := p.Wait()
	n, err := strconv.Atoi(c)
	if err != nil {
		return -1
	}
	return n
}

// ---- reporting

// TestReporter batches results of one run and sends them from a goroutine of its own every 100
// ms, so the goroutine that reads a test program's output never waits for rhun.
type TestReporter struct {
	ext   *Ext
	run   int64
	mu    sync.Mutex
	queue []TestResult
	kick  chan struct{}
	done  chan struct{}
	err   error
}

// NewTestReporter starts a reporter for the run.
func NewTestReporter(ext *Ext, run int64) *TestReporter {
	r := &TestReporter{ext: ext, run: run, kick: make(chan struct{}, 1), done: make(chan struct{})}
	go r.loop()
	return r
}

func (r *TestReporter) loop() {
	defer close(r.done)
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case _, ok := <-r.kick:
			if !ok {
				r.flush()
				return
			}
		case <-t.C:
		}
		r.flush()
	}
}

func (r *TestReporter) flush() {
	r.mu.Lock()
	q := r.queue
	r.queue = nil
	r.mu.Unlock()
	if len(q) > 0 {
		if err := r.ext.ReportTests(r.run, q...); err != nil {
			r.mu.Lock()
			if r.err == nil {
				r.err = err
			}
			r.mu.Unlock()
		}
	}
}

// Add queues results.
func (r *TestReporter) Add(rs ...TestResult) {
	r.mu.Lock()
	r.queue = append(r.queue, rs...)
	r.mu.Unlock()
}

// Close sends what is queued and stops the reporter; it returns the first error a send had.
func (r *TestReporter) Close() error {
	close(r.kick)
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// ---- dispatch

// answerTests answers tests-resolve and test-run on the handler's goroutine.
func (e *Ext) answerTests(h Handler, f []string, body []byte) {
	id := f[0]
	c, is := h.(TestController)
	if !is || !e.Has(FeatureTests) {
		e.write(fmt.Sprintf("error %s unknown no such request\n", id), nil)
		return
	}
	switch f[1] {
	case "tests-resolve":
		items, err := c.ResolveTests(e, string(body))
		if err != nil {
			e.write(errorLine(id, asError(err)), nil)
			return
		}
		out, err := EncodeTestItems(string(body), items)
		if err != nil {
			e.write(errorLine(id, asError(err)), nil)
			return
		}
		e.write(fmt.Sprintf("ok %s %d\n", id, len(out)), out)
	case "test-run":
		if len(f) < 6 {
			e.write(fmt.Sprintf("error %s syntax bad test-run\n", id), nil)
			return
		}
		run, _ := strconv.ParseInt(f[2], 10, 64)
		tr := TestRun{Run: run, Profile: f[3]}
		for _, s := range strings.Split(string(body), "\n") {
			if s != "" {
				tr.Items = append(tr.Items, s)
			}
		}
		if err := c.RunTests(e, tr); err != nil {
			e.write(errorLine(id, asError(err)), nil)
			return
		}
		e.write(fmt.Sprintf("ok %s 0\n", id), nil)
	}
}

func asError(err error) *Error {
	var ee *Error
	if errors.As(err, &ee) {
		return ee
	}
	return &Error{Code: "refused", Text: err.Error()}
}

// ParseTestCancel is a test-cancel event's run.
func ParseTestCancel(ev Event) (int64, bool) {
	if ev.Name != "test-cancel" || len(ev.Args) != 1 {
		return 0, false
	}
	n, err := strconv.ParseInt(ev.Args[0], 10, 64)
	return n, err == nil && n > 0
}
