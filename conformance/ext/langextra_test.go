package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// itemRec is a hierarchy item (with sites, a call) whose symbol is 1 1 1 3 and name 1 1 1 2
func itemRec(kind, path, name, data string, sites ...string) string {
	head := fmt.Sprintf("%s 1 1 1 3 1 1 1 2 %d %d 3 %d", kind, len(path), len(name), len(data))
	if len(sites) > 0 {
		head += fmt.Sprintf(" %d", len(sites))
	}
	s := head + "\n" + path + name + "pkg" + data
	for _, r := range sites {
		s += r + "\n"
	}
	return s
}

func TestHoverAndSelectionAnswers(t *testing.T) {
	h, _ := testHost(t)
	d := h.docs[2] // "héllo\n"
	for _, body := range []string{"", "1 1 1 7\nfn héllo", "0 0 0 0\ntext"} {
		if warns, why := checkHover(okMsg(body), d); why != "" || len(warns) != 0 {
			t.Errorf("%q: %v %s", body, warns, why)
		}
	}
	if warns, _ := checkHover(okMsg("1 1 9 1\nx"), d); len(warns) != 1 {
		t.Errorf("a range past the text: %v", warns)
	}
	for _, c := range []struct{ name, body, part string }{
		{"no range", "fn\n", "not L1 C1 L2 C2"},
		{"backwards", "1 3 1 1\nx", "ends before it starts"},
		{"over 64 KiB", "0 0 0 0\n" + strings.Repeat("x", 64<<10), "over 64 KiB"},
	} {
		_, why := checkHover(okMsg(c.body), d)
		want(t, c.name, why, c.part)
	}
	// go/rhunext's answer for the cursors 1 1 and 1 2: a chain, then the empty range at its cursor
	d = h.docs[1] // "package a\n..."
	n, warns, why := checkSelectionRanges(okMsg("2\n1 2 1 3\n1 1 1 7\n1\n1 2 1 2\n"), d, 2)
	if n != 2 || len(warns) != 0 || why != "" {
		t.Fatal(n, warns, why)
	}
	if n, _, why := checkSelectionRanges(okMsg(""), d, 2); n != 0 || why != "" {
		t.Fatal("no groups gives the cursors nothing:", why)
	}
	if _, warns, _ := checkSelectionRanges(okMsg("2\n1 1 1 3\n1 1 1 3\n"), d, 1); len(warns) != 1 {
		t.Errorf("an equal range: %v", warns)
	}
	for _, c := range []struct{ name, body, part string }{
		{"not containing", "2\n1 1 1 3\n1 2 1 7\n", "does not contain the one before"},
		{"more groups", "1\n1 1 1 3\n1\n1 1 1 3\n1\n1 1 1 3\n", "more groups than the 2"},
		{"M 0", "0\n", "not M, 1 to 64"},
		{"M 65", "65\n", "not M, 1 to 64"},
		{"M says more", "2\n1 1 1 3\n", "no range 2"},
		{"a range of three", "1\n1 1 1\n", "not L1 C1 L2 C2"},
		{"backwards", "1\n1 3 1 1\n", "ends before it starts"},
		{"over 64 KiB", strings.Repeat("1\n1 1 1 3\n", 7000), "over 64 KiB"},
	} {
		_, _, why := checkSelectionRanges(okMsg(c.body), d, 2)
		want(t, c.name, why, c.part)
	}
}

func TestHierarchyAndSymbolAnswers(t *testing.T) {
	h, dir := testHost(t)
	a := filepath.Join(dir, "a.go")
	roots := "2\n" + itemRec("function", a, "main", "m1") + itemRec("class", "#1", "T", "")
	items, warns, why := h.checkHierarchy(okMsg(roots), "call-hierarchy", nil)
	if len(items) != 2 || len(warns) != 0 || why != "" || string(items[0].data) != "m1" || string(items[0].path) != a {
		t.Fatal(items, warns, why)
	}
	calls := "1\n" + itemRec("method", a, "caller", "c1", "1 1 1 2", "3 1 3 5")
	if items, warns, why := h.checkHierarchy(okMsg(calls), "calls-in", nil); len(items) != 1 || items[0].sites != 2 || len(warns) != 0 || why != "" {
		t.Fatal(items, warns, why)
	}
	// calls-out's sites are in the asked item's file: b.go has one line
	if _, warns, _ := h.checkHierarchy(okMsg(calls), "calls-out", []byte(filepath.Join(dir, "b.go"))); len(warns) != 1 {
		t.Errorf("a call site past the asked item's text: %v", warns)
	}
	odd := "1\n" + strings.Replace(itemRec("gizmo", a, "x", ""), " 1 1 1 2 ", " 2 1 2 2 ", 1)
	if _, warns, why := h.checkHierarchy(okMsg(odd), "supertypes", nil); why != "" || len(warns) != 2 {
		t.Errorf("an unknown kind, a name outside the symbol: %v %s", warns, why)
	}
	many := "65\n" + strings.Repeat(itemRec("function", a, "r", ""), 65)
	for _, c := range []struct{ name, verb, body, part string }{
		{"65 roots", "type-hierarchy", many, "not N, 0 to 64"},
		{"a header of 12", "subtypes", "1\nfunction 1 1 1 3 1 1 1 2 1 1 0\n", "is not KIND"},
		{"a relative path", "subtypes", "1\n" + itemRec("function", "a.go", "x", ""), "neither absolute nor #DOC"},
		{"no name", "subtypes", "1\nfunction 1 1 1 3 1 1 1 2 2 0 0 0\n#1", "NAME_LEN"},
		{"data over 16 KiB", "subtypes", "1\n" + itemRec("function", a, "x", strings.Repeat("d", 16<<10+1)), "over 16 KiB"},
		{"no sites", "calls-in", "1\n" + strings.Replace(itemRec("function", a, "x", "", "1 1 1 2"), " 1\n", " 0\n", 1), "not 1 to 1000"},
		{"R says more", "calls-in", "1\n" + strings.Replace(itemRec("function", a, "x", "", "1 1 1 2"), " 1\n", " 2\n", 1), "no call site 2"},
		{"a call without R", "calls-out", "1\n" + itemRec("function", a, "x", ""), "is not KIND"},
		{"bytes after", "subtypes", "0\nx", "1 bytes after"},
	} {
		_, _, why := h.checkHierarchy(okMsg(c.body), c.verb, nil)
		want(t, c.name, why, c.part)
	}
	sym := func(rng, kind, path, name, detail string) string {
		return fmt.Sprintf("%s %s %d %d %d\n%s%s%s", rng, kind, len(path), len(name), len(detail), path, name, detail)
	}
	if n, warns, why := h.checkWorkspaceSymbols(okMsg("2\n" + sym("3 6 3 7", "function", a, "F", "a") + sym("1 1 1 4", "variable", "#2", "hé", ""))); n != 2 || len(warns) != 0 || why != "" {
		t.Fatal(n, warns, why)
	}
	for _, c := range []struct{ name, body, part string }{
		{"no name", "1\n" + sym("1 1 1 2", "function", a, "", ""), "NAME_LEN"},
		{"a relative path", "1\n" + sym("1 1 1 2", "function", "a.go", "F", ""), "neither absolute"},
		{"a header of 7", "1\n1 1 1 2 function 1 1\n", "is not L1 C1 L2 C2 KIND"},
	} {
		_, _, why := h.checkWorkspaceSymbols(okMsg(c.body))
		want(t, c.name, why, c.part)
	}
	link := func(rng, target, tip string) string {
		return fmt.Sprintf("%s %d %d\n%s%s", rng, len(target), len(tip), target, tip)
	}
	d := h.docs[1]
	good := "3\n" + link("1 1 1 8", "https://example.test/a", "Open") + link("3 6 3 7", a+":3:6", "") + link("1 1 1 2", "#2", "")
	if n, warns, why := checkLinks(okMsg(good), d); n != 3 || len(warns) != 0 || why != "" {
		t.Fatal(n, warns, why)
	}
	for _, c := range []struct{ name, body, part string }{
		{"ftp", "1\n" + link("1 1 1 2", "ftp://example.test", ""), "neither an http(s) URL"},
		{"relative", "1\n" + link("1 1 1 2", "a.go:3", ""), "neither an http(s) URL"},
		{"a URL with a space", "1\n" + link("1 1 1 2", "http://a b", ""), "neither an http(s) URL"},
		{"no target", "1\n1 1 1 2 0 0\n", "TARGET_LEN"},
	} {
		_, _, why := checkLinks(okMsg(c.body), d)
		want(t, c.name, why, c.part)
	}
	if _, warns, _ := checkLinks(okMsg("1\n"+link("9 1 9 2", "#1", "a\nb")), d); len(warns) != 2 {
		t.Errorf("a tip of two lines, a range past the text: %v", warns)
	}
}

// diag is a file-diagnostics record
func diag(rng, sev, msg, extra string) string {
	return fmt.Sprintf("%s %s %d %d\n%s%s", rng, sev, len(msg), len(extra), msg, extra)
}

func xr(name, bytes string) string { return fmt.Sprintf("%s %d\n%s", name, len(bytes), bytes) }

func TestFileDiagnosticsRequests(t *testing.T) {
	h, dir := testHost(t)
	out := &sink{}
	h.stdin, h.inflight = out, map[int64]bool{}
	h.features = map[string]bool{"file-diagnostics": true}
	h.m.needs = map[string]string{"documents": "read"}
	h.state = filepath.Join(dir, "state")
	disk := filepath.Join(dir, "disk.go")
	fd := func(id int, version, target string, recs ...string) {
		body := target + strings.Join(recs, "")
		h.handle(message{line: fmt.Sprintf("%d file-diagnostics %s %d %d %d", id, version, len(recs), len(target), len(body)), body: []byte(body)})
	}
	// go/rhunext/langfeat_test.go's records: ids, codes, sources, a link, tags, related places
	extra := xr("id", "d1") + xr("code", "W1") + xr("source", "lint") + xr("href", "https://example.test/W1") +
		"tag unnecessary 0\ntag deprecated 0\n" + "related 1 1 1 4 7 13\n/p/a.godeclared here" + "related 1 1 1 1 2 0\n#3"
	fd(1, "0", "#1", diag("1 1 1 8", "warning", "unused\ntotal", extra), diag("1 1 1 1", "error", "zero", ""))
	fd(2, "-", disk, diag("1 1 1 4", "error", "bad", xr("id", "e1")))
	fd(3, "0", "#1")
	h.handle(message{line: "4 file-diagnostics-reset"})
	if out.String() != "ok 1\nok 2\nok 3\nok 4\n" || len(h.fails) != 0 || len(h.warns) != 0 {
		t.Fatalf("%q fails %v warns %v", out.String(), h.fails, h.warns)
	}
	if !slices.Contains(h.oks, "sends file-diagnostics rhun keeps (2 for #1, 1 with ids)") {
		t.Errorf("oks %v", h.oks)
	}
	cases := []struct {
		version, target, rec, reply, fail string
	}{
		{"3", "#1", diag("1 1 1 2", "error", "x", ""), "stale 0", "versions"},
		{"-", "#1", diag("1 1 1 2", "error", "x", ""), "syntax VERSION", "file-diagnostics"},
		{"0", "#9", diag("1 1 1 2", "error", "x", ""), "nodoc no such document", "document ids"},
		{"0", disk, diag("1 1 1 2", "error", "x", ""), "syntax VERSION", "file-diagnostics"},
		{"-", "disk.go", diag("1 1 1 2", "error", "x", ""), "syntax the target", "file-diagnostics"},
		{"0", "#1", diag("0 1 1 2", "error", "x", ""), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 3 1 2", "error", "x", ""), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "fatal", "x", ""), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "error", "", ""), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "error", strings.Repeat("m", 4001), ""), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "error", "x", xr("id", "a b")), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "error", "x", xr("id", "d1")) + diag("1 1 1 2", "error", "y", xr("id", "d1")),
			"syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "error", "x", xr("code", strings.Repeat("c", 101))), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "error", "x", xr("href", "ftp://x")), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "error", "x", "related 1 1 1 2 3 0\nabc"), "syntax a record breaks the grammar", "file-diagnostics"},
		{"0", "#1", diag("1 1 1 2", "error", "x", "related 1 1 1 2 2 9\n#1"), "syntax a record breaks the grammar", "file-diagnostics"},
	}
	for i, c := range cases {
		out.Reset()
		h.fails = nil
		fd(10+i, c.version, c.target, c.rec)
		if got := out.String(); got != fmt.Sprintf("error %d %s\n", 10+i, c.reply) || !slices.Equal(h.fails, []string{c.fail}) {
			t.Errorf("case %d: %q fails %v", i, got, h.fails)
		}
	}
	// a file open with unsaved changes is stale; a path outside the project needs home read access
	h.fails, h.warns = nil, nil
	h.docs[1].dirty = true
	out.Reset()
	fd(40, "-", h.docs[1].path, diag("1 1 1 2", "error", "x", ""))
	fd(41, "-", "/elsewhere/x.go", diag("1 1 1 2", "error", "x", ""))
	if out.String() != "error 40 stale 0\nerror 41 needs a path it could not open\n" || len(h.fails) != 0 || len(h.warns) != 2 {
		t.Fatalf("%q fails %v warns %v", out.String(), h.fails, h.warns)
	}
	// unknown records are skipped (a warning); more than 2000 is toobig; without documents = read or the feature
	h.warns = nil
	out.Reset()
	fd(42, "-", disk, diag("1 1 1 2", "error", "x", xr("fix", "z")))
	if out.String() != "ok 42\n" || len(h.warns) != 1 {
		t.Fatalf("%q warns %v", out.String(), h.warns)
	}
	out.Reset()
	h.handle(message{line: "43 file-diagnostics - 2001 1 1", body: []byte("/")})
	h.m.needs["documents"] = "none"
	fd(44, "0", "#2")
	h.features["file-diagnostics"] = false
	fd(45, "0", "#2")
	h.handle(message{line: "46 file-diagnostics-reset"})
	if out.String() != "error 43 toobig 2000\nerror 44 needs documents=read\nerror 45 unknown no such request\nerror 46 unknown no such request\n" {
		t.Fatalf("%q", out.String())
	}
}

// moreExt answers this file's requests as go/rhunext's langfeat_test.go probe does, about path (the
// sample), streaming a part of calls-in and workspace-symbols first
func moreExt(h *host, path string) func(int64, string, []string, []byte) *message {
	return func(id int64, verb string, f []string, body []byte) *message {
		part := func(b string) {
			h.msgs <- message{line: fmt.Sprintf("900 query-part %d %d", id, len(b)), body: []byte(b)}
		}
		switch verb {
		case "hover-query":
			return okAnswer(id, "1 1 1 3\nfn "+f[4])
		case "selection-ranges":
			return okAnswer(id, "2\n1 1 1 2\n1 1 1 3\n1\n1 2 1 2\n")
		case "call-hierarchy", "type-hierarchy":
			return okAnswer(id, "1\n"+itemRec("function", path, "main", "m1"))
		case "calls-in":
			part("1\n" + itemRec("function", path, "caller", "c1", "1 1 1 2"))
			return okAnswer(id, "1\n"+itemRec("function", path, "late"+string(body), "", "1 2 1 3"))
		case "calls-out", "subtypes":
			return okAnswer(id, "0\n")
		case "supertypes":
			return okAnswer(id, "1\n"+itemRec("interface", path, "Base", "b"))
		case "workspace-symbols":
			sym := fmt.Sprintf("1 1 1 3 function %d 4 4\n%sMainmain", len(path), path)
			part("1\n" + sym)
			return okAnswer(id, "1\n"+sym)
		case "document-links":
			t := path + ":1:2"
			return okAnswer(id, fmt.Sprintf("2\n1 1 1 3 22 4\nhttps://example.test/aOpen1 1 1 2 %d 0\n%s", len(t), t))
		}
		return &message{line: fmt.Sprintf("error %d unknown no such request", id)}
	}
}

func TestLanguageRequestsMore(t *testing.T) {
	h, x := langHost(t, nil)
	x.answer = moreExt(h, filepath.Join(h.project, "sample.go"))
	h.features = map[string]bool{"hover-query": true, "selection-ranges": true, "hierarchy": true, "workspace-symbols": true,
		"document-links": true, "query-stream": true}
	h.m.provides = map[string]string{}
	for _, k := range []string{"hover", "selection_ranges", "call_hierarchy", "type_hierarchy", "workspace_symbols", "document_links"} {
		h.m.provides[k] = "*.go"
	}
	h.checkKeyFeatures()
	h.checkLangFeatures()
	if len(h.fails) != 0 || len(h.warns) != 0 {
		t.Fatalf("fails %v warns %v\noks %v", h.fails, h.warns, h.oks)
	}
	for _, ok := range []string{
		"answers hover-query with a range and a text rhun can show",
		"answers selection-ranges with chains rhun can expand along (2 of 2 cursors)",
		"answers call-hierarchy requests with roots rhun can show (1)",
		`answers calls-in about "main" with calls rhun can list (1, and 1 calls in 1 parts before it)`,
		`answers calls-out about "main" with calls rhun can list (0)`,
		"answers type-hierarchy requests with roots rhun can show (1)",
		`answers supertypes about "main" with items rhun can list (1)`,
		`answers subtypes about "main" with items rhun can list (0)`,
		"answers workspace-symbols with symbols rhun can list (1, and 1 symbols in 1 parts before it)",
		"answers document-links with links rhun can open (2)",
	} {
		if !slices.Contains(h.oks, ok) {
			t.Errorf("no %q in %q", ok, h.oks)
		}
	}
	heard := strings.Join(x.heard, "\n")
	for _, req := range []string{" hover-query 3 0 1 1 pointer -", " selection-ranges 3 0 2 8", " call-hierarchy 3 0 1 1 invoked -",
		" calls-in 2", " calls-out 2", " supertypes 2", " workspace-symbols 500 2", " document-links 3 0"} {
		if !strings.Contains(heard, req) {
			t.Errorf("no %q in what it heard:\n%s", req, heard)
		}
	}
	// parts of a kind that takes none, and a broken chain, fail
	h, x = langHost(t, nil)
	x.answer = func(id int64, verb string, f []string, body []byte) *message {
		if verb == "hover-query" {
			h.msgs <- message{line: fmt.Sprintf("900 query-part %d 2", id), body: []byte("0\n")}
			return okAnswer(id, "")
		}
		return okAnswer(id, "2\n1 1 1 3\n1 2 1 3\n")
	}
	h.features = map[string]bool{"hover-query": true, "selection-ranges": true, "query-stream": true}
	h.m.provides = map[string]string{"hover": "*.go", "selection_ranges": "*.go"}
	h.checkLangFeatures()
	if !slices.Equal(h.fails, []string{"query-stream", "selection-ranges"}) {
		t.Errorf("fails %v", h.fails)
	}
	// agreed without a key, file-diagnostics without documents = read
	h, _ = langHost(t, nil)
	h.features = map[string]bool{"hierarchy": true, "file-diagnostics": true}
	h.m.provides = map[string]string{}
	h.m.needs = map[string]string{}
	h.checkKeyFeatures()
	if !slices.Equal(h.fails, []string{"provides"}) || !slices.Equal(h.warns, []string{"needs"}) {
		t.Errorf("fails %v warns %v", h.fails, h.warns)
	}
}
