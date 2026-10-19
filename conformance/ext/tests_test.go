package main

import (
	"strings"
	"testing"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func itemsBody(t *testing.T, parent string, items ...rhunext.TestItem) []byte {
	t.Helper()
	b, err := rhunext.EncodeTestItems(parent, items)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheckItems(t *testing.T) {
	good := itemsBody(t, "p",
		rhunext.TestItem{ID: "a", Label: "A", Kind: rhunext.TestCase, Path: "/x/a_test.go",
			Range: &rhunext.Range{Start: rhunext.Pos{Line: 3, Col: 1}, End: rhunext.Pos{Line: 5, Col: 2}}, Desc: "d"},
		rhunext.TestItem{ID: "g", Label: "G", Kind: rhunext.TestGroup, Lazy: true, NoDebug: true, Error: "two\nlines"},
		rhunext.TestItem{ID: "w", Kind: rhunext.TestFile, Path: "C:/w/x.rs"},
		rhunext.TestItem{ID: "d", Kind: rhunext.TestSuite, Path: "#DOC"})
	parent, items, warns, why := checkItems(good)
	if why != "" || parent != "p" || len(items) != 4 || !items[1].lazy || items[0].lazy || len(warns) != 0 {
		t.Fatalf("good body: %q %q %v %v", why, parent, items, warns)
	}
	for _, c := range []struct{ name, body, part string }{
		{"kind", "0 1\nfunc 0 0 0 0 - 1 0 0 0\na", "not a kind"},
		{"flags", "0 1\ntest 0 0 0 0 fast 1 0 0 0\na", "flags"},
		{"twice", "0 2\ntest 0 0 0 0 - 1 0 0 0\natest 0 0 0 0 - 1 0 0 0\na", "twice"},
		{"range order", "0 1\ntest 3 5 3 4 - 1 0 2 0\na/x", "badpos"},
		{"range no path", "0 1\ntest 1 1 1 1 - 1 0 0 0\na", "no path"},
		{"relative path", "0 1\ntest 0 0 0 0 - 1 0 3 0\nax/y", "not absolute"},
		{"empty id", "0 1\ntest 0 0 0 0 - 0 0 0 0\n", "1 to 512"},
		{"label line", "0 1\ntest 0 0 0 0 - 1 3 0 0\naa\nb", "line break"},
		{"short", "0 1\ntest 0 0 0 0 - 5 0 0 0\na", "more bytes"},
		{"trailing", "0 0\nx", "after the last item"},
		{"desc", "0 1\ntest 0 0 0 0 - 1 0 0 9\nadesc 2\na\n", "desc"},
		{"header", "1 1 1\n", "PARENT_LEN N"},
	} {
		if _, _, _, why := checkItems([]byte(c.body)); !strings.Contains(why, c.part) {
			t.Errorf("%s: %q, want %q", c.name, why, c.part)
		}
	}
	if _, _, warns, _ := checkItems([]byte("0 1\ntest 0 0 0 0 - 1 0 0 8\naicon 1\nx")); len(warns) != 1 {
		t.Errorf("an unknown extra record is a warning: %v", warns)
	}
}

func TestCheckResults(t *testing.T) {
	exp, act := "1", "2"
	body, err := rhunext.EncodeTestResults([]rhunext.TestResult{
		{Item: "a", State: rhunext.TestFailed, Duration: 3, Messages: []rhunext.TestMessage{{Text: "boom", Path: "/x/a.go",
			Range: &rhunext.Range{Start: rhunext.Pos{Line: 4, Col: 2}, End: rhunext.Pos{Line: 4, Col: 2}}, Expected: &exp, Actual: &act}}},
		{Item: "", State: rhunext.TestErrored, Duration: rhunext.NoDuration},
		{Item: "ghost", State: rhunext.TestPassed, Duration: rhunext.NoDuration, Output: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, unknown, warns, why := checkResults(body, func(s string) bool { return s == "a" })
	if why != "" || n != 3 || unknown != 1 || len(warns) != 0 {
		t.Fatalf("%d %d %v %q", n, unknown, warns, why)
	}
	for _, c := range []struct{ name, body, part string }{
		{"state", "1\nbroken - 1 0\na", "not a state"},
		{"ms", "1\npassed 1.5 1 0\na", "milliseconds"},
		{"run state", "1\npassed - 0 0\n", "only - and errored"},
		{"location", "1\nfailed - 1 32\namessage 1\nxlocation 0 1 1 1 2\n/a", "one-based"},
		{"location path", "1\nfailed - 1 31\namessage 1\nxlocation 1 1 1 1 1\na", "absolute"},
		{"count", "2001\n", "records"},
	} {
		if _, _, _, why := checkResults([]byte(c.body), func(string) bool { return true }); !strings.Contains(why, c.part) {
			t.Errorf("%s: %q, want %q", c.name, why, c.part)
		}
	}
	if _, _, warns, _ := checkResults([]byte("1\nfailed - 1 12\naexpected 1\nx"), func(string) bool { return true }); len(warns) != 1 {
		t.Errorf("expected before a message is a warning: %v", warns)
	}
}

func TestCheckCoverageAndCommand(t *testing.T) {
	if n, why := checkCoverage([]byte("1\n5 2\n/a.go1 3 2\n5 5 0\n")); n != 1 || why != "" {
		t.Errorf("coverage: %d %q", n, why)
	}
	for body, part := range map[string]string{
		"1\n4 1\na.go1 2 3\n":  "absolute",
		"1\n5 1\n/a.go3 2 1\n": "in order",
		"1\n5 1\n/a.go1 2\n":   "L1 L2 COUNT",
		"1\n5 2\n/a.go1 2 1\n": "missing",
	} {
		if _, why := checkCoverage([]byte(body)); !strings.Contains(why, part) {
			t.Errorf("coverage %q: %q, want %q", body, why, part)
		}
	}
	prog, cwd, code, why := checkCommand([]byte("2 2 2 1\ngo/p4\ntest5\n-json3\nA=1"))
	if prog != "go" || cwd != "/p" || code != "" || why != "" {
		t.Errorf("command: %q %q %q %q", prog, cwd, code, why)
	}
	for body, want := range map[string]string{
		"4 0 0 0\n../x":       "refused",
		"2 0 0 1\ngo3\na=1":   "syntax",
		"2 0 1 0\ngo9\nshort": "syntax",
		"2 0 0 0\ngo\x00":     "syntax",
		"2 0 1001 0\ngo":      "toobig",
		"2 0 0 0\ngo extra":   "syntax",
	} {
		if _, _, code, _ := checkCommand([]byte(body)); code != want {
			t.Errorf("command %q: %q, want %q", body, code, want)
		}
	}
}

func TestTestRequests(t *testing.T) {
	h, _ := testHost(t)
	h.stdin = &sink{}
	h.features = map[string]bool{"tests": true}
	h.m.needs = map[string]string{"tests": "go"}
	var replies, codes []string
	do := func(verb string, args []string, body string) {
		replies, codes = nil, nil
		h.testRequest(7, verb, args, message{body: []byte(body)},
			func(s string) { replies = append(replies, s) },
			func(c, s string) { codes = append(codes, c) })
	}
	do("test-items", []string{"replace", "0"}, string(itemsBody(t, "", rhunext.TestItem{ID: "g", Kind: rhunext.TestGroup})))
	if len(replies) != 1 || h.tests.items["g"] == nil {
		t.Fatalf("test-items: %v %v", replies, codes)
	}
	do("test-items", []string{"add", "0"}, string(itemsBody(t, "nope", rhunext.TestItem{ID: "x", Kind: rhunext.TestCase})))
	if len(codes) != 1 || codes[0] != "refused" {
		t.Fatalf("unknown parent: %v", codes)
	}
	do("test-items", []string{"add", "0"}, string(itemsBody(t, "g", rhunext.TestItem{ID: "g/a", Kind: rhunext.TestCase})))
	do("test-remove", []string{"1", "2"}, "g\n")
	if h.tests.items["g/a"] != nil || h.tests.items["g"] != nil {
		t.Fatalf("test-remove keeps descendants: %v", h.tests.items)
	}
	// runs: an update for an unknown run, then one after test-end, are stale
	h.tests.runs[1] = &testRun{profile: "run"}
	do("test-update", []string{"2", "0"}, "0\n")
	if len(codes) != 1 || codes[0] != "stale" {
		t.Fatalf("unknown run: %v", codes)
	}
	do("test-exec", []string{"1", "0"}, "5 0 0 0\ncargo")
	if len(codes) != 1 || codes[0] != "needs" {
		t.Fatalf("a program [needs] does not name: %v", codes)
	}
	do("test-exec", []string{"1", "0"}, "2 0 0 0\ngo")
	if len(codes) != 1 || codes[0] != "refused" || h.tests.runs[1].refusedExec != 1 {
		t.Fatalf("test-exec without a script: %v", codes)
	}
	do("test-end", []string{"1"}, "")
	do("test-update", []string{"1", "0"}, "0\n")
	if len(codes) != 1 || codes[0] != "stale" {
		t.Fatalf("an update after test-end: %v", codes)
	}
	fails := len(h.fails)
	h.features = map[string]bool{}
	do("test-end", []string{"1"}, "")
	if len(codes) != 1 || codes[0] != "unknown" || len(h.fails) != fails+1 {
		t.Fatalf("without tests agreed: %v", codes)
	}
}
