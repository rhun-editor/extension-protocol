package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProvidesKeys(t *testing.T) {
	for _, k := range []string{"complete", "complete.triggers", "save_actions", "hover", "declaration", "type_definition",
		"implementation", "selection_ranges", "call_hierarchy", "type_hierarchy", "workspace_symbols", "range_format",
		"type_format", "document_links", "folding", "semantic_tokens", "inlay_hints", "code_lens", "highlights",
		"colors", "inline_complete"} {
		want(t, k, providesKeyProblem(k, "*.go"), "")
	}
	want(t, "triggers", providesKeyProblem("type_format.triggers", "; } newline"), "")
	want(t, "a word", providesKeyProblem("type_format.triggers", "; end"), "neither one character")
	want(t, "no triggers", providesKeyProblem("type_format.triggers", " "), "empty")
	want(t, "a feature's name", providesKeyProblem("save-actions", "*"), "not a feature's patterns")
	want(t, "unknown", providesKeyProblem("hover-query", "*"), "not a feature's patterns")
}

// item is a completion list of one item over 1 1 to 1 3 of a.go, with these EXTRA records
func item(extra string) message {
	return okMsg(fmt.Sprintf("1 0\n1 1 1 3 text 5 5 0 %d\nprintprint%s", len(extra), extra))
}

func TestCompleteItemsRecords(t *testing.T) {
	h, _ := testHost(t)
	d := h.docs[1]
	on := map[string]bool{"complete-items": true}
	all := "resolve 1 2\nr1replace 1 1 1 8 0\ncommit 2\n.(filter 3\nprtsuffix 3\n(s)preselect 0\n" +
		"deprecated 0\nadjust 0\nafter signature 0\nafter complete 0\n"
	info, why := checkCompletionList(item("edit 3 1 3 5 3\nfn "+all), d, on)
	want(t, "every record", why, "")
	if info.items != 1 || info.resolve == nil || string(info.resolve.data) != "r1" || !info.resolve.edits ||
		len(info.resolve.taken) != 2 {
		t.Fatalf("resolvable: %+v", info.resolve)
	}
	cases := []struct{ name, extra, part string }{
		{"edits 2", "resolve 2 0\n", "EDITS 0 or 1"},
		{"resolve over 16 KiB", fmt.Sprintf("resolve 0 %d\n%s", 16<<10+1, strings.Repeat("x", 16<<10+1)), "at most 16 KiB"},
		{"replace elsewhere", "replace 1 2 1 8 0\n", "rhun ignores it"},
		{"replace without its end", "replace 1 1 8 0\n", "not replace L1 C1 L2 C2 0"},
		{"replace past the line", "replace 1 1 1 30 0\n", "past the end of line 1"},
		{"no commit characters", "commit 0\n", "LEN 1 to 16"},
		{"17 commit characters", "commit 17\nabcdefghijklmnopq", "LEN 1 to 16"},
		{"a space as a commit character", "commit 2\n. ", "printable ASCII"},
		{"a filter of two lines", "filter 3\na\nb", "line break"},
		{"a suffix that is not UTF-8", "suffix 1\n\xff", "not UTF-8"},
		{"preselect 1", "preselect 1\nx", "not preselect 0"},
		{"after what", "after hover 0\n", "after signature 0 or after complete 0"},
	}
	for _, c := range cases {
		_, why := checkCompletionList(item(c.extra), d, on)
		want(t, c.name, why, c.part)
		// rhun skips them without complete-items agreed
		_, why = checkCompletionList(item(c.extra), d, nil)
		want(t, c.name+" without the feature", why, "")
	}
}

func TestResolveAnswers(t *testing.T) {
	h, _ := testHost(t)
	d := h.docs[1]
	info, why := checkCompletionList(item("edit 3 1 3 5 3\nfn resolve 0 2\nr1"), d, map[string]bool{"complete-items": true})
	if why != "" || info.resolve == nil {
		t.Fatal(why)
	}
	r := *info.resolve
	cases := []struct{ name, body, part string }{
		{"empty", "", ""},
		{"records", "detail 10\nfn print()doc 7\nDocs r1edit 2 1 2 1 7\nuse x;\nafter signature 0\n", ""},
		{"an unknown record is skipped", "later 1\nx", ""},
		{"a field record", "field 1 0 1 0 0\n", "no snippet or cursor records"},
		{"a cursor record", "cursor 1 0\n", "no snippet or cursor records"},
		{"an edit over the item's range", "edit 1 2 1 2 1\nx", "overlaps the item's range"},
		{"an edit over the item's edit", "edit 3 2 3 3 1\nx", "overlaps the item's range"},
		{"an edit past the text", "edit 9 1 9 1 1\nx", "past the last line"},
		{"a detail of two lines", "detail 3\na\nb", "line break"},
		{"after what", "after list 0\n", "after signature 0"},
		{"a length past the answer", "doc 9\nab", "not within the answer"},
		{"over 64 KiB", fmt.Sprintf("doc %d\n%s", 64<<10, strings.Repeat("d", 64<<10)), "over 64 KiB"},
	}
	for _, c := range cases {
		want(t, c.name, checkResolve(okMsg(c.body), d, r), c.part)
	}
	want(t, "not ok ID LEN", checkResolve(message{line: "ok 1"}, d, r), "not ok ID LEN")
}

func TestSignaturesAnswers(t *testing.T) {
	sigs := func(body string) ([]string, string) { return checkSignatures(okMsg(body)) }
	warns, why := sigs("3 1\n2 3 4 0\nf(a)5 6 7 3\nf(a, b)two-1 -1 3 0\nf()")
	if why != "" || len(warns) != 0 {
		t.Fatal(warns, why)
	}
	if _, why := sigs(""); why != "" {
		t.Fatal("ok ID 0 hides the help:", why)
	}
	if warns, why := sigs("1 5\n-1 -1 1 0\nf"); why != "" || len(warns) != 1 || !strings.Contains(warns[0], "shows the first") {
		t.Fatal(warns, why)
	}
	cases := []struct{ name, body, part string }{
		{"no overloads", "0 0\n", "not 1 to 32"},
		{"33 overloads", "33 0\n", "not 1 to 32"},
		{"no ACTIVE", "1\n-1 -1 1 0\nf", "not N ACTIVE"},
		{"ACTIVE -1", "1 -1\n-1 -1 1 0\nf", "not N ACTIVE"},
		{"N says more", "2 0\n-1 -1 1 0\nf", "overload 2 has no header line"},
		{"a header of three", "1 0\n-1 -1 1\nf", "not FROM TO SIG_LEN DOC_LEN"},
		{"FROM TO past the signature", "1 0\n0 2 1 0\nf", "neither -1 -1 nor within"},
		{"TO before FROM", "1 0\n2 1 3 0\nfoo", "neither -1 -1 nor within"},
		{"-1 and a number", "1 0\n-1 1 3 0\nfoo", "neither -1 -1 nor within"},
		{"a signature of two lines", "1 0\n-1 -1 3 0\nf\ng", "line break"},
		{"bytes past the body", "1 0\n-1 -1 5 0\nf", "run past the body"},
		{"bytes after", "1 0\n-1 -1 1 0\nfx", "1 bytes after"},
		{"over 64 KB", strings.Repeat("x", 65537), "over 64 KB"},
	}
	for _, c := range cases {
		_, why := sigs(c.body)
		want(t, c.name, why, c.part)
	}
}

// queryHost is testHost with query-stream agreed and rhun waiting on request 5, a references
// request about a.go
func queryHost(t *testing.T, verb string) (*host, *sink) {
	h, _ := testHost(t)
	out := &sink{}
	h.stdin, h.inflight = out, map[int64]bool{}
	h.features = map[string]bool{"query-stream": true, "references": true}
	h.hostReqs, h.hostAnswers = map[int64]string{}, map[int64]message{}
	line := verb + " 1 0 1 1 invoked -"
	now := time.Now()
	h.hostSeq = 5
	h.hostReqs[5] = line
	h.queries = map[int64]*query{5: {line: line, verb: verb, base: 10 * time.Second, started: now.Add(-time.Second), due: now}}
	return h, out
}

func send(h *host, line, body string) {
	h.handle(message{line: fmt.Sprintf("%s %d", line, len(body)), body: []byte(body)})
}

func TestQueryParts(t *testing.T) {
	h, out := queryHost(t, "references")
	part := "1\n1 1 1 3 2 1\n#1a"
	send(h, "1 query-part 5", part)
	send(h, "2 query-part 5", part)
	if q := h.queries[5]; out.String() != "ok 1\nok 2\n" || q.parts != 2 || q.items != 2 || len(h.fails) != 0 {
		t.Fatalf("%q %+v %v", out.String(), q, h.fails)
	}
	if said := h.partsSaid(5, "locations"); said != ", and 2 locations in 2 parts before it" {
		t.Fatal(said)
	}
	out.Reset()
	h.queries[4] = &query{verb: "references"} // answered
	send(h, "3 query-part 5", "2\n1 1 1 3 2 1\n#1a")
	send(h, "4 query-part 6", part) // never sent
	send(h, "5 query-part 4", part) // answered, a race: cancelled
	send(h, "6 query-part 4", part) // and again
	if out.String() != "error 3 syntax the part breaks the answer's rules\nerror 4 cancelled no such request\n"+
		"error 5 cancelled no such request\nerror 6 cancelled no such request\n" || len(h.fails) != 1 || len(h.warns) != 2 {
		t.Fatalf("%q fails %v warns %v", out.String(), h.fails, h.warns)
	}
	// past the request's cap rhun keeps what came and withdraws it; a late answer is dropped
	out.Reset()
	h.queries[5].items = 100000
	send(h, "7 query-part 5", part)
	if out.String() != "error 7 toobig the request's cap\n0 cancel 5\n" || h.hostReqs[5] != "" || string(h.hostAnswers[5].body) != "0\n" {
		t.Fatalf("%q %v", out.String(), h.hostAnswers)
	}
	if !h.hostReply([]string{"ok", "5", "2"}, message{line: "ok 5 2", body: []byte("0\n")}) {
		t.Fatal("a late answer after withdrawal is not dropped quietly")
	}
	// a kind that takes no parts; without the feature
	h, out = queryHost(t, "symbols")
	send(h, "1 query-part 5", "0\n")
	h.features["query-stream"] = false
	send(h, "2 query-part 5", "0\n")
	if out.String() != "error 1 refused this kind takes no parts\nerror 2 unknown no such request\n" || len(h.fails) != 2 {
		t.Fatalf("%q %v", out.String(), h.fails)
	}
}

func TestQueryCompletionParts(t *testing.T) {
	h, out := queryHost(t, "complete")
	h.features["complete"] = true
	send(h, "1 query-part 5", "2 0\n1 1 1 3 text 1 1 0 0\nxx1 1 1 3 text 1 1 0 0\nyy")
	send(h, "2 query-part 5", "1 0\n1 1 1 30 text 1 1 0 0\nxx")
	if out.String() != "ok 1\nerror 2 syntax the part breaks the answer's rules\n" || h.queries[5].items != 2 {
		t.Fatalf("%q %+v", out.String(), h.queries[5])
	}
}

func TestQueryProgress(t *testing.T) {
	h, out := queryHost(t, "references")
	q := h.queries[5]
	send(h, "1 query-progress 5 40", "Indexing")
	if out.String() != "ok 1\n" || q.progresses != 1 || time.Until(q.due) < 8*time.Second {
		t.Fatalf("%q, due in %v", out.String(), time.Until(q.due))
	}
	send(h, "2 query-progress 5 -", "")
	q.started, q.due = time.Now().Add(-119*time.Second), time.Now() // the ceiling, 120 s after it was sent, is near
	send(h, "3 query-progress 5 100", "x")
	if time.Until(q.due) > 2*time.Second {
		t.Fatalf("due in %v, past the ceiling", time.Until(q.due))
	}
	out.Reset()
	send(h, "4 query-progress 5 101", "")
	send(h, "5 query-progress 5 4x", "")
	send(h, "6 query-progress 5 1", "two\nlines")
	send(h, "7 query-progress 5 1", strings.Repeat("x", 201))
	send(h, "8 query-progress 5 1", strings.Repeat("x", 4097))
	send(h, "9 query-progress 5", "")
	send(h, "10 query-progress 4 1", "")
	if out.String() != "error 4 syntax PERCENT\nerror 5 syntax PERCENT\nerror 6 syntax the text\nerror 7 syntax the text\n"+
		"error 8 syntax the text\nerror 9 syntax cannot parse the line\nerror 10 cancelled no such request\n" || len(h.fails) != 6 {
		t.Fatalf("%q %v", out.String(), h.fails)
	}
}

func TestProvidesRequests(t *testing.T) {
	h, _ := testHost(t)
	out := &sink{}
	h.stdin, h.inflight = out, map[int64]bool{}
	h.features = map[string]bool{"provider-scope": true, "definition": true}
	h.m.provides["definition"] = "*.go *.mod"
	h.handle(message{line: "1 provides definition *.go"})
	if out.String() != "ok 1\n" || h.patterns("definition") != "*.go" || h.manifestPatterns("definition") != "*.go *.mod" || len(h.oks) != 1 {
		t.Fatalf("%q %q %v", out.String(), h.patterns("definition"), h.oks)
	}
	h.handle(message{line: "2 provides definition -"})
	if h.patterns("definition") != "" {
		t.Fatal("withdrawn, yet asked")
	}
	h.handle(message{line: "3 provides definition *"})
	if h.patterns("definition") != "*.go *.mod" {
		t.Fatal("the manifest's values are not back")
	}
	out.Reset()
	h.handle(message{line: "4 provides definition *.rs"})
	h.handle(message{line: "5 provides definition *.go  *.mod"})
	h.handle(message{line: "6 provides hover-query *"})
	h.handle(message{line: "7 provides definition"})
	h.features["provider-scope"] = false
	h.handle(message{line: "8 provides definition *"})
	if out.String() != "error 4 refused not a subset of the manifest's values\nerror 5 refused not a subset of the manifest's values\n"+
		"error 6 syntax not a [provides] key\nerror 7 syntax cannot parse the line\nerror 8 unknown no such request\n" || len(h.fails) != 5 {
		t.Fatalf("%q %v", out.String(), h.fails)
	}
	if len(h.oks) != 1 {
		t.Fatal("provides said more than once:", h.oks)
	}
}
