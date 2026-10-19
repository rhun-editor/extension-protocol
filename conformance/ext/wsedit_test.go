package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func op(kind string, flags string, paths ...string) string {
	lens := make([]string, len(paths))
	for i, p := range paths {
		lens[i] = fmt.Sprint(len(p))
	}
	return fmt.Sprintf("%s %s %s\n%s", kind, strings.Join(lens, " "), flags, strings.Join(paths, ""))
}

func fnv(t *testing.T, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("fnv1a64:%016x", rhunext.Fnv1a64(data))
}

func txHost(t *testing.T) (*host, string) {
	h, dir := testHost(t)
	h.features = map[string]bool{"workspace-edits": true, "file-operations": true}
	h.stdin = &sink{}
	h.inflight = map[int64]bool{}
	return h, dir
}

func TestTransactions(t *testing.T) {
	h, dir := txHost(t)
	p := func(name string) string { return filepath.Join(dir, name) }
	os.MkdirAll(p("full"), 0o755)
	os.WriteFile(p("full/x"), []byte("x"), 0o644)
	os.MkdirAll(p(".git"), 0o755)
	x := ed(1, 1, 1, 1, "x")
	disk := p("disk.go")
	cases := []struct {
		name, body, code string
		entry            int
		want             string
	}{
		{"a hashed file on disk", wsBody(grp(disk, fnv(t, disk), x)), "", 0, ""},
		{"a hash that does not match", wsBody(grp(disk, "fnv1a64:0000000000000001", x)), "stale", 1, "does not hash"},
		{"a hash in capitals", wsBody(grp(disk, strings.ToUpper(fnv(t, disk)[8:]), x)), "syntax", 1, "PRE"},
		{"a short hash", wsBody(grp(disk, "fnv1a64:abc", x)), "syntax", 1, "16 lowercase"},
		{"a hash for an open document without unsaved changes", wsBody(grp(p("a.go"), fnv(t, p("a.go")), x)), "", 0, ""},
		{"records", wsBody(grp(disk, "-", x)) + "label 4\nMovefrob 1 2 3\nabcconfirm 0\n", "", 0, ""},
		{"a label of two lines", wsBody(grp(disk, "-", x)) + "label 3\na\nb", "syntax", 0, "line break"},
		{"confirm with bytes", wsBody(grp(disk, "-", x)) + "confirm 1\nx", "syntax", 0, "confirm 0"},
		{"a record past the body", wsBody(grp(disk, "-", x)) + "label 9\nab", "syntax", 0, "runs past"},
		{"a record without a length", wsBody(grp(disk, "-", x)) + "confirm\n", "syntax", 0, "NAME ARGS"},
		{"create, then its text", wsBody(op("create", "-", p("new.go")), grp(p("new.go"), "-", ed(1, 1, 1, 1, "package x\n"))), "", 0, ""},
		{"a created file with a version", wsBody(op("create", "-", p("new.go")), grp(p("new.go"), "0", x)), "stale", 2, "its PRE is -"},
		{"a folder and a file in it", wsBody(op("create", "-", p("newdir")+"/"), op("create", "-", p("newdir/f"))), "", 0, ""},
		{"two operations on one path", wsBody(op("delete", "-", disk), op("rename", "-", p("bom.go"), disk)), "refused", 2, "also the path"},
		{"a text group for a deleted path", wsBody(grp(disk, "-", x), op("delete", "-", disk)), "refused", 1, "deletes"},
		{"a text group for a rename target", wsBody(grp(disk, "-", x), op("rename", "overwrite", p("bom.go"), disk)), "refused", 1, "renames another file onto"},
		{"an entry inside a folder another renames", wsBody(op("rename", "-", p("full"), p("full2")), op("delete", "-", p("full/x"))), "refused", 2, "inside"},
		{"inside .git", wsBody(op("create", "-", p(".git/hooks"))), "refused", 1, ".git"},
		{"a text group inside .git", wsBody(grp(p(".git/config"), "-", x)), "refused", 1, ".git"},
		{"the project folder", wsBody(op("delete", "recursive", dir)), "refused", 1, "project folder"},
		{"outside the project", wsBody(op("create", "-", filepath.Join(filepath.Dir(dir), "elsewhere"))), "needs", 1, "outside the project"},
		{"a create of a file that exists", wsBody(op("create", "-", disk)), "refused", 1, "neither overwrite nor ignore-existing"},
		{"ignore-existing", wsBody(op("create", "ignore-existing", disk)), "", 0, ""},
		{"overwrite", wsBody(op("create", "overwrite", p("bom.go"))), "", 0, ""},
		{"a rename of a missing file", wsBody(op("rename", "-", p("none.go"), p("n2.go"))), "nofile", 1, "does not exist"},
		{"a rename onto a file", wsBody(op("rename", "-", p("bom.go"), disk)), "refused", 1, "neither overwrite"},
		{"a rename onto a folder with overwrite", wsBody(op("rename", "overwrite", p("bom.go"), p("full"))), "refused", 1, "never replaced"},
		{"a delete of a missing file", wsBody(op("delete", "-", p("none.go"))), "nofile", 1, "ignore-missing"},
		{"ignore-missing", wsBody(op("delete", "ignore-missing", p("none.go"))), "", 0, ""},
		{"a folder with files, not recursive", wsBody(op("delete", "-", p("full"))), "refused", 1, "not recursive"},
		{"recursive", wsBody(op("delete", "recursive", p("full"))), "", 0, ""},
		{"an unknown flag", wsBody(op("delete", "force", disk)), "syntax", 1, "not a flag"},
		{"a flag of another kind", wsBody(op("create", "recursive", disk)), "syntax", 1, "not a flag"},
		{"a relative operation path", wsBody(op("delete", "-", "disk.go")), "nofile", 1, "not an absolute path"},
		{"an operation header with a field missing", wsBody("rename 3 -\nabc"), "syntax", 1, "rename FROM_LEN TO_LEN FLAGS"},
		{"entries are counted with the operations", "2\n" + op("delete", "ignore-missing", p("n1")) + grp(disk, "-") + op("delete", "ignore-missing", p("n2")), "syntax", 0, "record"},
	}
	for _, c := range cases {
		_, code, entry, why := h.checkTx([]byte(c.body))
		want(t, c.name, why, c.want)
		if code != c.code || entry != c.entry {
			t.Errorf("%s: %s entry %d, want %s entry %d (%s)", c.name, code, entry, c.code, c.entry, why)
		}
	}
	// an open document with unsaved changes is neither deleted nor overwritten
	h.docs[2].dirty = true
	if _, code, _, why := h.checkTx([]byte(wsBody(op("delete", "-", p("b.go"))))); code != "refused" || !strings.Contains(why, "unsaved") {
		t.Errorf("delete a dirty document: %s %s", code, why)
	}
	h.docs[2].dirty = false
	// without file-operations, an operation is syntax; without workspace-edits, a hash and records are too
	if _, code, _, why := parseTx([]byte(wsBody(op("delete", "-", disk))), txCaps{ws: true}); code != "syntax" || !strings.Contains(why, "file-operations") {
		t.Errorf("operation without the feature: %s %s", code, why)
	}
	if _, code, _, _ := parseTx([]byte(wsBody(grp(disk, fnv(t, disk), x))), txCaps{}); code != "syntax" {
		t.Errorf("hash without the feature: %s", code)
	}
	if _, code, _, _ := parseTx([]byte(wsBody(grp(disk, "-", x))+"confirm 0\n"), txCaps{}); code != "syntax" {
		t.Errorf("record without the feature: %s", code)
	}
}

func TestTransactionsAreMadeAsRhunMakesThem(t *testing.T) {
	h, dir := txHost(t)
	h.m.subscribe["edits"] = "*"
	p := func(name string) string { return filepath.Join(dir, name) }
	out := h.stdin.(*sink)
	body := wsBody(
		op("create", "-", p("pkg/new.go")),
		grp(p("pkg/new.go"), "-", ed(1, 1, 1, 1, "package pkg\n")),
		grp(p("crlf.go"), fnv(t, p("crlf.go")), ed(2, 1, 2, 4, "TWO")),
		grp("#1", "0", ed(1, 9, 1, 10, "A")),
		op("rename", "-", p("b.go"), p("c.go")),
		op("delete", "-", p("bom.go")),
	) + "label 4\nMove"
	tx, code, _, why := h.checkTx([]byte(body))
	if why != "" {
		t.Fatal(code, why)
	}
	h.makeTx(tx)
	read := func(name string) string {
		b, err := os.ReadFile(p(name))
		if err != nil {
			return "missing"
		}
		return string(b)
	}
	for name, want := range map[string]string{
		"pkg/new.go": "package pkg\n",
		"crlf.go":    "one\r\nTWO\r\n",             // edited on disk, its line ends kept, no tab opened
		"a.go":       "package A\n\nfunc F() {}\n", // open without unsaved changes: written, as the edit touches disk
		"c.go":       "héllo\n",
		"b.go":       "missing",
		"bom.go":     "missing",
	} {
		if got := read(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if d := h.docs[1]; d.dirty || string(d.text) != "package A\n\nfunc F() {}\n" || d.version != 2 {
		t.Errorf("a.go: %+v", d)
	}
	if h.docs[2] != nil || h.docs[3] == nil || h.docs[3].path != p("c.go") || h.docs[3].version != 3 {
		t.Errorf("b.go did not follow its rename under a new id: %v", h.docIDs())
	}
	if len(h.docs) != 2 {
		t.Errorf("crlf.go opened a tab: %v", h.docIDs())
	}
	want := "0 edits 1 2 13\n8 1 0\n8 0 1\nA0 closed 2\n0 opened 3 3 Go " + p("c.go") + "\n"
	if out.String() != want {
		t.Errorf("events:\n%q\nwant\n%q", out.String(), want)
	}
}

func TestApplyAnswersTheEntry(t *testing.T) {
	h, dir := txHost(t)
	h.m.needs = map[string]string{"documents": "write"}
	out := h.stdin.(*sink)
	body := wsBody(grp(filepath.Join(dir, "disk.go"), "-", ed(1, 1, 1, 1, "x")), op("delete", "-", filepath.Join(dir, "none.go")))
	h.handle(message{line: fmt.Sprintf("5 apply %d", len(body)), body: []byte(body)})
	if got := out.String(); !strings.HasPrefix(got, "error 5 nofile 2 "+filepath.Join(dir, "none.go")+" does not exist") {
		t.Fatalf("%q", got)
	}
	if len(h.fails) != 1 {
		t.Fatalf("fails %v", h.fails)
	}
}

func TestEditorModelRequests(t *testing.T) {
	h, _ := txHost(t)
	h.features = map[string]bool{"selections": true, "reveal": true, "diff": true}
	out := h.stdin.(*sink)
	try := func(line, body string) string {
		out.Reset()
		h.handle(message{line: line, body: []byte(body)})
		return strings.TrimSuffix(out.String(), "\n")
	}
	loc := "1\n1 1 1 3 2 0\n#1"
	cases := []struct{ line, body, want string }{
		{"1 select-many 1 2 16", "1 1 1 4\n3 1 3 2\n", "ok 1"},
		{"2 select-many 1 0 0", "", "error 2 toobig 1 to 512 selections"},
		{"3 select-many 1 1 8", "9 1 9 1\n", "error 3 badpos line 9 is past the last line"},
		{"4 select-many 1 2 8", "1 1 1 1\n", "error 4 syntax the body does not have N lines"},
		{"5 reveal 1 3 1 center-if-outside", "", "ok 5"},
		{"6 reveal 1 3 1 middle", "", "error 6 syntax cannot parse the line"},
		{"7 reveal 9 1 1 top", "", "error 7 nodoc no such document"},
		{"8 show-locations " + fmt.Sprint(len("Callers\n"+loc)), "Callers\n" + loc, "ok 8"},
		{"9 show-locations 5", "Calls", "error 9 syntax no title line"},
		{"10 diff 1 2 show 4", "HEAD", "ok 10 3"},
		{"11 diff 1 2 background 4", "HEAD", "ok 11 3"},
		{"12 diff 1 2 sideways 4", "HEAD", "error 12 syntax cannot parse the line"},
		{"13 diff 1 7 show 4", "HEAD", "error 13 nodoc no such document"},
		{"14 diff-close 3", "", "ok 14"},
		{"15 diff-close 3", "", "error 15 nodoc not one of its diff tabs"},
	}
	for _, c := range cases {
		if got := try(c.line, c.body); got != c.want {
			t.Errorf("%s: %q, want %q", c.line, got, c.want)
		}
	}
	if d := h.docs[1]; d.anchor != 0 || d.cur != 3 {
		t.Errorf("select-many: the primary is %d..%d", d.anchor, d.cur)
	}
	h.features = map[string]bool{}
	if got := try("16 reveal 1 1 1 top", ""); got != "error 16 unknown no such request" {
		t.Errorf("without the feature: %q", got)
	}
}

func TestLinkedAnswers(t *testing.T) {
	d := &doc{text: []byte("<div>\n</div>\n")}
	ans := func(body string) message { return okMsg(body) }
	cases := []struct{ body, want string }{
		{"0\n", ""},
		{"2\n1 2 1 5\n2 3 2 6\n", ""},
		{"2\n1 2 1 5\n2 3 2 6\nword 2\n-_frob 1 3\nabc", ""},
		{"1\n1 2 1 5\n", "one range"},
		{"65\n", "0 to 64"},
		{"2\n1 2 1 5\n2 3 2 5\n", "not the first's"},
		{"2\n1 2 1 5\n1 2 1 5\n", "overlaps"},
		{"2\n2 3 2 6\n1 3 1 5\n", "not the first's"},
		{"2\n1 2 1 9\n2 3 2 6\n", "past the end"},
		{"2\n1 2 1 5\n2 3 2 6\nword 2\n-\n", "printable ASCII"},
		{"2\n1 2 1 5\n2 3 2 6\nword 9\n-", "runs past"},
	}
	for _, c := range cases {
		_, why := checkLinkedAnswer(ans(c.body), d, 1, 2)
		want(t, c.body, why, c.want)
	}
	if _, why := checkLinkedAnswer(ans("2\n1 2 1 5\n2 3 2 6\n"), d, 1, 1); !strings.Contains(why, "holds the cursor") {
		t.Errorf("cursor outside: %q", why)
	}
}
