package rhunext

import (
	"bufio"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeController struct {
	mu        sync.Mutex
	runs      []TestRun
	cancelled []int64
	exec      chan string
}

func (c *fakeController) Ready(*Ext, string)    {}
func (c *fakeController) Run(*Ext, string, Doc) {}
func (c *fakeController) Event(*Ext, Event)     {}
func (c *fakeController) CancelTests(e *Ext, run int64) {
	c.mu.Lock()
	c.cancelled = append(c.cancelled, run)
	c.mu.Unlock()
}
func (c *fakeController) ResolveTests(e *Ext, parent string) ([]TestItem, error) {
	if parent == "bad" {
		return nil, &Error{Code: "refused", Text: "cannot list"}
	}
	return []TestItem{{ID: "go:p.TestA", Label: "TestA", Kind: TestCase, Path: "/p/a_test.go",
		Range: &Range{Pos{3, 1}, Pos{5, 2}}, Desc: "fast"},
		{ID: "go:p", Label: "p", Kind: TestGroup, Lazy: true}}, nil
}
func (c *fakeController) RunTests(e *Ext, r TestRun) error {
	c.mu.Lock()
	c.runs = append(c.runs, r)
	c.mu.Unlock()
	go func() {
		p, err := e.TestExec(r.Run, TestCommand{Program: "go", Args: []string{"test", "-json"}, Env: []string{"A=1"}})
		if err != nil {
			c.exec <- "error " + err.Error()
			return
		}
		out, _ := io.ReadAll(p.Stdout)
		code := p.Wait()
		rep := NewTestReporter(e, r.Run)
		exp, act := "a\nb", "a"
		rep.Add(TestResult{Item: "go:p.TestA", State: TestFailed, Duration: 30 * time.Millisecond,
			Messages: []TestMessage{{Text: "mismatch", Path: "/p/a_test.go", Range: &Range{Pos{4, 2}, Pos{4, 2}}, Expected: &exp, Actual: &act}},
			Output:   out})
		if err := rep.Close(); err != nil {
			c.exec <- "report " + err.Error()
			return
		}
		e.EndTestRun(r.Run)
		c.exec <- "exit " + code
	}()
	return nil
}

// testHost drives an extension over pipes: send writes rhun's lines, next reads the extension's.
type testHost struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Reader
}

func (h *testHost) send(s string) {
	if _, err := io.WriteString(h.in, s); err != nil {
		h.t.Fatal(err)
	}
}
func (h *testHost) next() string {
	line, err := h.out.ReadString('\n')
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.TrimRight(line, "\n")
}
func (h *testHost) body(n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(h.out, b); err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func TestTestsController(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &fakeController{exec: make(chan string, 1)}
	go RunOnWith(inR, outW, io.Discard, []string{FeatureTests}, c)
	h := &testHost{t: t, in: inW, out: bufio.NewReader(outR)}
	if l := h.next(); l != "hello 1 tests" {
		t.Fatal(l)
	}
	h.send("0 features tests\n0 ready /p\n")
	// tests-resolve answers an items body for that parent
	h.send("5 tests-resolve 0\n")
	l := h.next()
	if !strings.HasPrefix(l, "ok 5 ") {
		t.Fatal(l)
	}
	var n int
	for _, f := range strings.Fields(l)[2:] {
		n = len(f)
		_ = n
	}
	size := testAtoi(strings.Fields(l)[2])
	parent, items, err := ParseTestItems([]byte(h.body(size)))
	if err != nil || parent != "" || len(items) != 2 || items[0].Desc != "fast" || items[0].Range.End != (Pos{5, 2}) || !items[1].Lazy {
		t.Fatal(parent, items, err)
	}
	h.send("6 tests-resolve 3\nbad")
	if l := h.next(); l != "error 6 refused cannot list" {
		t.Fatal(l)
	}
	// test-run: ok at once; the controller asks for a process, reads it, reports and ends
	h.send("7 test-run 3 run 1 11\ngo:p.TestA\n")
	if l := h.next(); l != "ok 7 0" {
		t.Fatal(l)
	}
	l = h.next()
	if !strings.HasPrefix(l, "1 test-exec 3 ") {
		t.Fatal(l)
	}
	cmd := h.body(testAtoi(strings.Fields(l)[3]))
	if cmd != "2 0 2 1\ngo4\ntest5\n-json3\nA=1" {
		t.Fatalf("%q", cmd)
	}
	h.send("ok 1 12\n0 test-out 12 out 6\nhello\n0 test-out 12 err 3\nerr0 test-exit 12 1\n")
	l = h.next()
	if !strings.HasPrefix(l, "2 test-update 3 ") {
		t.Fatal(l)
	}
	body := h.body(testAtoi(strings.Fields(l)[3]))
	if !strings.HasPrefix(body, "1\nfailed 30 10 ") || !strings.Contains(body, "message 8\nmismatch") ||
		!strings.Contains(body, "location 4 2 4 2 12\n/p/a_test.go") || !strings.Contains(body, "expected 3\na\nb") ||
		!strings.Contains(body, "actual 1\na") || !strings.HasSuffix(body, "output 6\nhello\n") {
		t.Fatalf("%q", body)
	}
	h.send("ok 2\n")
	if l := h.next(); l != "3 test-end 3" {
		t.Fatal(l)
	}
	h.send("ok 3\n")
	if got := <-c.exec; got != "exit 1" {
		t.Fatal(got)
	}
	h.send("0 test-cancel 3\n")
	h.send("8 tests-resolve 0\n")
	h.next()
	h.body(size)
	c.mu.Lock()
	if len(c.cancelled) != 1 || c.cancelled[0] != 3 || len(c.runs) != 1 || c.runs[0].Items[0] != "go:p.TestA" {
		t.Fatal(c.cancelled, c.runs)
	}
	c.mu.Unlock()
	h.send("0 bye\n")
}

func testAtoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestEncodeTestItemsChecks(t *testing.T) {
	if _, err := EncodeTestItems("", []TestItem{{ID: "a\nb", Label: "x", Kind: TestCase}}); err == nil {
		t.Fatal("an id with LF")
	}
	if _, err := EncodeTestItems("", []TestItem{{ID: "a", Label: "x", Kind: TestCase, Range: &Range{Pos{2, 1}, Pos{1, 1}}, Path: "/a"}}); err == nil {
		t.Fatal("a backwards range")
	}
	if _, err := EncodeTestResults([]TestResult{{State: TestPassed}}); err == nil {
		t.Fatal("the run itself passed")
	}
	b, err := EncodeTestResults([]TestResult{{Output: []byte("x")}})
	if err != nil || string(b) != "1\n- - 0 10\noutput 1\nx" {
		t.Fatalf("%q %v", b, err)
	}
}
