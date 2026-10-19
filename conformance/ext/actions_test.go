package main

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// goActions is go/rhunext/actions_test.go's answer for any kind, goQuickFixes for quickfix
const (
	goActions = "3\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1refactor.extract 7 0 21\nExtractcommand extract 3\n" +
		"a bquickfix 6 0 23\nCannotdisabled 11\nNo fix here"
	goQuickFixes = "2\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1quickfix 6 0 23\nCannotdisabled 11\nNo fix here"
)

// actionsHost is testHost for an extension x with the commands extract and fix
func actionsHost(t *testing.T) *host {
	h, _ := testHost(t)
	h.m.name, h.m.commands = "x", []string{"extract", "fix"}
	return h
}

func act2(kind, title, edit, extra string) string {
	return fmt.Sprintf("%s %d %d %d\n%s%s%s", kind, len(title), len(edit), len(extra), title, edit, extra)
}

func TestHierarchicalKinds(t *testing.T) {
	for _, k := range []string{"quickfix", "refactor.extract", "source.organizeImports", "source.fixAll.es-lint_2", strings.Repeat("k", 128)} {
		if !hierarchicalKind(k) {
			t.Errorf("%q is a kind", k)
		}
	}
	for _, k := range []string{"", "quick fix", "refactor.", ".refactor", "refactor..extract", "quick/fix", strings.Repeat("k", 129)} {
		if hierarchicalKind(k) {
			t.Errorf("%q is no kind", k)
		}
	}
	if !kindWanted(nil, "x") || !kindWanted([]string{"source.organizeImports"}, "source.organizeImports.go") ||
		kindWanted([]string{"source.organizeImports"}, "source.organizeImportsX") || kindWanted([]string{"quickfix"}, "refactor") {
		t.Error("kinds match by hierarchy")
	}
}

func TestPrepareRenameAnswers(t *testing.T) {
	h, _ := testHost(t)
	d := h.docs[2] // "héllo\n"
	ph, warns, why := checkPrepareRename(okMsg("1 1 1 7 5\ntotal"), d)
	if why != "" || ph != "total" || len(warns) != 0 {
		t.Fatal(ph, warns, why)
	}
	if ph, _, why := checkPrepareRename(okMsg(""), d); why != "" || ph != "" {
		t.Fatal("ok ID 0 is the word at the cursor:", why)
	}
	if _, _, why := checkPrepareRename(okMsg("1 1 1 1 0\n"), d); why != "" {
		t.Fatal("an empty placeholder:", why)
	}
	cases := []struct{ name, body, part string }{
		{"no line", "1 1 1 7 5", "no line"},
		{"four fields", "1 1 1 7\n", "not L1 C1 L2 C2 PLEN"},
		{"a word", "1 1 1 x 1\na", "not a number"},
		{"PLEN over 200", fmt.Sprintf("1 1 1 7 201\n%s", strings.Repeat("p", 201)), "at most 200"},
		{"PLEN says less", "1 1 1 7 2\nabc", "PLEN says 2 bytes, but 3 come"},
		{"PLEN says more", "1 1 1 7 4\nabc", "PLEN says 4 bytes, but 3 come"},
		{"two lines", "1 1 1 7 3\na\nb", "line break"},
		{"not UTF-8", "1 1 1 7 1\n\xff", "not UTF-8"},
		{"past the line", "1 1 1 9 1\na", "past the end of line 1"},
		{"inside a character", "1 3 1 7 1\na", "inside a multibyte character"},
		{"backwards", "1 4 1 2 1\na", "ends before it starts"},
		{"over 4 KiB", fmt.Sprintf("1 1 1 7 0\n%s", strings.Repeat("x", 4096)), "over 4 KiB"},
	}
	for _, c := range cases {
		_, _, why := checkPrepareRename(okMsg(c.body), d)
		want(t, c.name, why, c.part)
	}
}

func TestCodeActionsAnswers(t *testing.T) {
	h := actionsHost(t)
	acts, warns, why := h.checkCodeActions(okMsg(goActions), nil)
	if why != "" || len(warns) != 0 || len(acts) != 3 {
		t.Fatal(acts, warns, why)
	}
	if a := acts[0]; a.kind != "quickfix" || !a.preferred || string(a.resolve) != "R" || a.fixes != "d1" || a.off {
		t.Errorf("Fix: %+v", a)
	}
	if a := acts[1]; a.command != "extract" || string(a.args) != "a b" {
		t.Errorf("Extract: %+v", a)
	}
	if a := acts[2]; !a.off || a.disabled != "No fix here" {
		t.Errorf("Cannot: %+v", a)
	}
	if _, _, why := h.checkCodeActions(okMsg(goQuickFixes), []string{"quickfix"}); why != "" {
		t.Fatal(why)
	}
	// a runtime command counts; the edit of an action is read whatever the documents say
	h.rtcmds = []string{"x.later"}
	fix := wsBody(grp("#1", "0", ed(3, 6, 3, 7, "G")))
	if _, _, why := h.checkCodeActions(okMsg("2\n"+act2("source.organizeImports.go", "Later", "", "command later 0\n")+
		act2("quickfix", "Edit", fix, "")), []string{"source.organizeImports", "quickfix"}); why != "" {
		t.Fatal(why)
	}
	one := func(extra string) string { return "1\n" + act2("quickfix", "T", "", extra) }
	cases := []struct{ name, body, part string }{
		{"the kind asked", goActions, "none of the kinds asked"},
		{"a kind with a space", "1\nquick fix 1 0 0\nT", "not KIND TITLE_LEN"},
		{"not hierarchical", "1\n" + act2("quick/fix", "T", "", "preferred 0\n"), "not a hierarchical kind"},
		{"a kind over 128 bytes", "1\n" + act2(strings.Repeat("k", 129), "T", "", "preferred 0\n"), "not a hierarchical kind"},
		{"no title", "1\n" + act2("quickfix", "", "", "disabled 1\nx"), "TITLE_LEN"},
		{"does nothing", one("preferred 0\nfixes 2\nd1"), "does nothing"},
		{"preferred 1", one("preferred 1\nx"), "not preferred 0"},
		{"a reason over 1000 bytes", one(fmt.Sprintf("disabled 1001\n%s", strings.Repeat("r", 1001))), "at most 1000"},
		{"a reason of two lines", one("disabled 3\na\nb"), "line break"},
		{"resolve data over 16 KiB", one(fmt.Sprintf("resolve %d\n%s", 16<<10+1, strings.Repeat("r", 16<<10+1))), "at most 16 KiB"},
		{"a command name in capitals", one("command Fix 0\n"), "not [a-z0-9_]{1,40}"},
		{"a command without args' length", one("command fix\n"), "its length is not within"},
		{"args over 8192 bytes", one(fmt.Sprintf("command fix 8193\n%s", strings.Repeat("a", 8193))), "at most 8192"},
		{"a command it does not have", one("command other 0\n"), "not one of its commands"},
		{"fixes of two lines", one("disabled 1\nxfixes 3\na\nb"), "line break"},
		{"a record past the extra records", one("resolve 5\nab"), "not within the extra records"},
		{"an edit that cannot be read", "1\n" + act2("quickfix", "Bad", "1\n2 0 1\n#11 1 1\n", ""), "its edit cannot be made"},
		{"N says more", "2\n" + act2("quickfix", "T", "", "disabled 1\nx"), "action 2 has no header line"},
		{"bytes after", one("disabled 1\nx") + "z", "1 bytes after"},
		{"over 1000", "1001\n", "0 to 1000"},
	}
	for _, c := range cases {
		_, _, why := h.checkCodeActions(okMsg(c.body), []string{"quickfix"})
		want(t, c.name, why, c.part)
	}
	// odd but allowed: a title of two lines, a disabled action without a reason, a record rhun does
	// not know
	_, warns, why = h.checkCodeActions(okMsg("2\n"+act2("quickfix", "a\nb", "", "disabled 0\n")+act2("quickfix", "T", "", "later 1\nxresolve 1\nR")), nil)
	if why != "" || len(warns) != 2 {
		t.Fatal(warns, why)
	}
}

func TestActionResolveAnswers(t *testing.T) {
	h := actionsHost(t)
	edit := wsBody(grp("#1", "0", ed(1, 1, 1, 1, "R")))
	got, why := h.checkActionResolve(okMsg(fmt.Sprintf("%d 15\n%scommand fix 1\nz", len(edit), edit)))
	if why != "" || string(got) != edit {
		t.Fatal(why)
	}
	for _, body := range []string{"0 0\n", fmt.Sprintf("%d 0\n%s", len(edit), edit), "0 14\ncommand fix 0\n"} {
		if _, why := h.checkActionResolve(okMsg(body)); why != "" {
			t.Errorf("%q: %s", body, why)
		}
	}
	cases := []struct{ name, body, part string }{
		{"no lengths", "0\n", "not EDIT_LEN EXTRA_LEN"},
		{"no line", "", "not EDIT_LEN EXTRA_LEN"},
		{"past the body", "5 0\nab", "run past the body"},
		{"bytes after", "0 0\nx", "1 bytes after"},
		{"another record", "0 12\npreferred 0\n", "command records only"},
		{"two commands", "0 28\ncommand fix 0\ncommand fix 0\n", "2 command records"},
		{"a command it does not have", "0 16\ncommand other 0\n", "not one of its commands"},
		{"an edit that cannot be read", "11 0\n1\n2 0 1\n#11", "its edit cannot be made"},
	}
	for _, c := range cases {
		_, why := h.checkActionResolve(okMsg(c.body))
		want(t, c.name, why, c.part)
	}
}

func TestKeyFeatures(t *testing.T) {
	h := actionsHost(t)
	h.features = map[string]bool{"navigation": true, "partial-format": true, "code-actions": true, "prepare-rename": true}
	h.checkKeyFeatures()
	if !slices.Equal(h.fails, []string{"provides", "provides", "provides"}) || len(h.warns) != 1 {
		t.Fatalf("fails %v warns %v", h.fails, h.warns)
	}
	h.fails, h.warns = nil, nil
	h.features["rename"] = true
	for k, v := range map[string]string{"implementation": "*.go", "type_format": "*.go", "actions": "*.go", "rename": "*.go"} {
		h.m.provides[k] = v
	}
	h.checkKeyFeatures()
	if len(h.fails) != 0 || len(h.warns) != 1 { // type_format without triggers
		t.Fatalf("fails %v warns %v", h.fails, h.warns)
	}
	h.warns = nil
	h.m.provides["type_format.triggers"] = "; newline"
	h.checkKeyFeatures()
	if len(h.fails) != 0 || len(h.warns) != 0 {
		t.Fatalf("fails %v warns %v", h.fails, h.warns)
	}
	// the 1.1 keys make rhun send edits; actions also with code-actions alone
	for _, f := range []string{"code-actions", "navigation", "partial-format"} {
		h.features = map[string]bool{f: true}
		if !h.provider(h.docs[1]) {
			t.Errorf("a %s provider hears the edits of its documents", f)
		}
	}
	h.features = map[string]bool{"prepare-rename": true, "hover-query": true}
	if h.provider(h.docs[1]) {
		t.Error("a provider of no agreed feature hears no edits")
	}
}

// fakeExt is an extension in the test's process: each request the checker writes is answered by
// answer (nil for none), and the requests and events it hears are kept
type fakeExt struct {
	h      *host
	answer func(id int64, verb string, f []string, body []byte) *message
	heard  []string
}

func (x *fakeExt) Write(p []byte) (int, error) {
	line, body, _ := bytes.Cut(p, []byte("\n"))
	x.heard = append(x.heard, string(line))
	f := strings.Split(string(line), " ")
	if id, err := strconv.ParseInt(f[0], 10, 64); err == nil && id > 0 && len(f) >= 2 {
		if m := x.answer(id, f[1], f[2:], body); m != nil {
			x.h.msgs <- *m
		}
	}
	return len(p), nil
}

func (x *fakeExt) Close() error { return nil }

func okAnswer(id int64, body string) *message {
	return &message{line: fmt.Sprintf("ok %d %d", id, len(body)), body: []byte(body)}
}

// goExt answers as an extension made with the Go SDK and actions_test.go's probe
func goExt(id int64, verb string, f []string, body []byte) *message {
	doc := "#" + f[0]
	switch verb {
	case "declaration", "implementation":
		return okAnswer(id, fmt.Sprintf("1\n1 1 1 2 %d 1\n%sd", len(doc), doc))
	case "type-definition":
		return okAnswer(id, "0\n")
	case "prepare-rename":
		return okAnswer(id, "1 1 1 3 2\npr")
	case "rename":
		return &message{line: fmt.Sprintf("error %d refused not here", id)}
	case "range-format":
		return okAnswer(id, wsBody(grp(doc, f[1], ed(1, 1, 1, 1, "x"))))
	case "type-format":
		l, _ := strconv.Atoi(f[2])
		c, _ := strconv.Atoi(f[3])
		return okAnswer(id, wsBody(grp(doc, f[1], ed(l, c, l, c, " "))))
	case "code-actions":
		if f[7] == "quickfix" {
			return okAnswer(id, goQuickFixes)
		}
		return okAnswer(id, goActions)
	case "code-action-resolve":
		edit := wsBody(grp(doc, f[1], ed(1, 1, 1, 1, string(body))))
		return okAnswer(id, fmt.Sprintf("%d 15\n%scommand fix 1\nz", len(edit), edit))
	}
	return &message{line: fmt.Sprintf("error %d unknown no such request", id)}
}

// langHost is actionsHost with the extension x answering through answer, the 1.1 features agreed
// and *.go files provided for each
func langHost(t *testing.T, answer func(int64, string, []string, []byte) *message) (*host, *fakeExt) {
	h := actionsHost(t)
	x := &fakeExt{h: h, answer: answer}
	h.stdin, h.msgs, h.inflight = x, make(chan message, 64), map[int64]bool{}
	h.hostReqs, h.hostAnswers = map[int64]string{}, map[int64]message{}
	h.features = map[string]bool{"navigation": true, "prepare-rename": true, "rename": true, "partial-format": true, "code-actions": true}
	for _, k := range []string{"declaration", "type_definition", "implementation", "rename", "range_format", "type_format", "actions"} {
		h.m.provides[k] = "*.go"
	}
	h.m.provides["type_format.triggers"] = "; newline"
	return h, x
}

func TestLanguageRequests11(t *testing.T) {
	h, x := langHost(t, goExt)
	h.checkLangFeatures()
	if len(h.fails) != 0 || len(h.warns) != 0 {
		t.Fatalf("fails %v warns %v\noks %v", h.fails, h.warns, h.oks)
	}
	for _, ok := range []string{
		"answers declaration requests with locations rhun can list (1)",
		"answers type-definition requests with locations rhun can list (0)",
		"answers implementation requests with locations rhun can list (1)",
		"answers code-actions for kinds - with actions rhun can list (3)",
		"answers code-actions for kinds quickfix with actions rhun can list (2)",
		`answers code-action-resolve for "Fix" with an edit and command rhun can use`,
		`answers prepare-rename with a range and the placeholder "pr"`,
		"answers range-format requests with a workspace edit rhun can make (an edit of its document)",
		`answers type-format requests after ";" with a workspace edit rhun can make (an edit of its document)`,
	} {
		if !slices.Contains(h.oks, ok) {
			t.Errorf("no %q in %q", ok, h.oks)
		}
	}
	var sample *doc
	for _, d := range h.docs {
		if strings.HasSuffix(d.path, "sample.go") {
			sample = d
		}
	}
	// the resolved edit (R), the range format (x), the typed trigger and its format
	if sample == nil || string(sample.text) != "xRpr; \n" {
		t.Fatalf("the sample document: %+v", sample)
	}
	heard := strings.Join(x.heard, "\n")
	for _, req := range []string{" code-actions 3 0 1 1 1 3 invoked - -", " code-actions 3 0 1 1 1 3 invoked quickfix -",
		" code-action-resolve 3 0 1", " prepare-rename 3 1 1 1 invoked -", " range-format 3 1 1 1 1 4 4 yes yes yes",
		"0 edits 3 3 7", " type-format 3 3 1 6 ; 4 yes yes yes"} {
		if !strings.Contains(heard, req) {
			t.Errorf("no %q in what it heard:\n%s", req, heard)
		}
	}
}

func TestLanguageRequests11Broken(t *testing.T) {
	h, _ := langHost(t, func(id int64, verb string, f []string, body []byte) *message {
		switch verb {
		case "prepare-rename":
			return okAnswer(id, "1 1 1 3 2\np")
		case "code-actions":
			return okAnswer(id, goActions) // the refactor too when quickfix was asked
		case "code-action-resolve":
			return okAnswer(id, "0 12\npreferred 0\n")
		case "type-format":
			return okAnswer(id, wsBody(grp("#1", "0", ed(1, 1, 1, 1, " ")))) // another document
		}
		return goExt(id, verb, f, body)
	})
	h.features["navigation"] = false
	h.checkLangFeatures()
	for _, f := range []string{"code-actions", "prepare-rename", "type-format"} {
		if !slices.Contains(h.fails, f) {
			t.Errorf("no %s failure in %v", f, h.fails)
		}
	}
	if n := len(h.fails); n != 4 { // code-actions twice: the kinds, the resolve answer
		t.Errorf("fails %v", h.fails)
	}
}
