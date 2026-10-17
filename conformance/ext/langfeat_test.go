package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testHost has a.go (#1, version 0) and b.go (#2, version 3, a two-byte character) open, and
// disk.go on disk only
func testHost(t *testing.T) (*host, string) {
	dir := t.TempDir()
	h := &host{docs: map[int64]*doc{}, project: dir, m: &manifest{provides: map[string]string{}}}
	h.newDoc(filepath.Join(dir, "a.go"), "Go", []byte("package a\n\nfunc F() {}\n"))
	h.newDoc(filepath.Join(dir, "b.go"), "Go", []byte("héllo\n")).version = 3
	if err := os.WriteFile(filepath.Join(dir, "disk.go"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return h, dir
}

func grp(path, version string, edits ...string) string {
	return fmt.Sprintf("%d %s %d\n%s%s", len(path), version, len(edits), path, strings.Join(edits, ""))
}

func ed(l1, c1, l2, c2 int, text string) string {
	return fmt.Sprintf("%d %d %d %d %d\n%s", l1, c1, l2, c2, len(text), text)
}

func wsBody(groups ...string) string {
	return fmt.Sprintf("%d\n%s", len(groups), strings.Join(groups, ""))
}

func okMsg(body string) message {
	return message{line: fmt.Sprintf("ok 1 %d", len(body)), body: []byte(body)}
}

// want checks a validator's reason: "" for a body rhun takes, else a part of the reason
func want(t *testing.T, name, why, part string) {
	t.Helper()
	switch {
	case part == "" && why != "":
		t.Errorf("%s: refused: %s", name, why)
	case part != "" && !strings.Contains(why, part):
		t.Errorf("%s: want a reason with %q, got %q", name, part, why)
	}
}

func TestWorkspaceEdit(t *testing.T) {
	h, dir := testHost(t)
	a, b, disk := filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go"), filepath.Join(dir, "disk.go")
	cases := []struct{ name, body, code, want string }{
		{"empty", "0\n", "", ""},
		{"three files", wsBody(grp("#1", "0", ed(3, 6, 3, 7, "G")), grp(disk, "-", ed(2, 1, 2, 4, "2")),
			grp(b, "3", ed(1, 4, 1, 7, "LO"))), "", ""},
		{"insertions at one place", wsBody(grp(a, "0", ed(1, 1, 1, 1, "<"), ed(1, 1, 1, 1, ">"))), "", ""},
		{"out of order", wsBody(grp(a, "0", ed(3, 1, 3, 5, "fn"), ed(1, 1, 1, 8, "PACKAGE"))), "", ""},
		{"insertion before a replacement at its start", wsBody(grp(a, "0", ed(1, 1, 1, 1, "x"), ed(1, 1, 1, 8, "y"))), "", ""},
		{"overlap", wsBody(grp(a, "0", ed(1, 1, 1, 5, "x"), ed(1, 3, 1, 8, "y"))), "badpos", "overlap"},
		{"insertion after a replacement at its start", wsBody(grp(a, "0", ed(1, 1, 1, 8, "y"), ed(1, 1, 1, 1, "x"))), "badpos", "comes before it"},
		{"stale", wsBody(grp("#2", "2", ed(1, 1, 1, 1, "x"))), "stale", "which is at version 3"},
		{"dash for an open document", wsBody(grp(a, "-", ed(1, 1, 1, 1, "x"))), "stale", "which is open"},
		{"version for a file not open", wsBody(grp(disk, "0", ed(1, 1, 1, 1, "x"))), "nodoc", "which is not open"},
		{"version not a number", wsBody(grp(a, "v0", ed(1, 1, 1, 1, "x"))), "syntax", "neither a number nor -"},
		{"relative path", wsBody(grp("a.go", "0", ed(1, 1, 1, 1, "x"))), "nofile", "neither absolute nor #DOC"},
		{"unknown document", wsBody(grp("#9", "0", ed(1, 1, 1, 1, "x"))), "nodoc", "never opened"},
		{"missing file", wsBody(grp(filepath.Join(dir, "none.go"), "-", ed(1, 1, 1, 1, "x"))), "nofile", "neither open nor a file"},
		{"one file twice", wsBody(grp("#1", "0", ed(1, 1, 1, 1, "x")), grp(a, "0", ed(2, 1, 2, 1, "y"))), "syntax", "has a group already"},
		{"past a line's end", wsBody(grp(a, "0", ed(1, 1, 1, 20, "x"))), "badpos", "past the end of line 1"},
		{"inside a character", wsBody(grp(b, "3", ed(1, 3, 1, 4, "x"))), "badpos", "inside a multibyte character"},
		{"reversed", wsBody(grp(a, "0", ed(1, 5, 1, 1, "x"))), "badpos", "ends before it starts"},
		{"N says more", "2\n" + grp(a, "0", ed(1, 1, 1, 1, "x")), "syntax", "file 2 has no header line"},
		{"M says more", fmt.Sprintf("1\n%d 0 2\n%s", len(a), a) + ed(1, 1, 1, 1, "x"), "syntax", "edit 2 has no header line"},
		{"bytes after", wsBody(grp(a, "0", ed(1, 1, 1, 1, "x"))) + "junk", "syntax", "4 bytes after"},
		{"two spaces", fmt.Sprintf("1\n%d  0 0\n%s", len(a), a), "syntax", "is not PATH_LEN VERSION M"},
		{"text past the body", fmt.Sprintf("1\n%d 0 1\n%s1 1 1 1 9\nab", len(a), a), "syntax", "runs past the body"},
		{"over 1000 files", "1001\n", "toobig", "at most 1000"},
	}
	for _, c := range cases {
		_, code, why := h.checkWorkspaceEdit([]byte(c.body), nil)
		want(t, c.name, why, c.want)
		if code != c.code {
			t.Errorf("%s: the code is %q, want %q", c.name, code, c.code)
		}
	}
	_, _, why := h.checkWorkspaceEdit([]byte(wsBody(grp(b, "3", ed(1, 1, 1, 1, "x")))), h.docs[1])
	want(t, "format edits another document", why, "not the document asked about")
	_, _, why = h.checkWorkspaceEdit([]byte(wsBody(grp(a, "0", ed(1, 1, 1, 1, "x")))), h.docs[1])
	want(t, "format edits its document", why, "")
}

type sink struct{ bytes.Buffer }

func (sink) Close() error { return nil }

func TestApply(t *testing.T) {
	h, dir := testHost(t)
	out := &sink{}
	h.stdin = out
	h.m.provides["rename"] = "*.go"
	h.features = map[string]bool{"rename": true}
	disk := filepath.Join(dir, "disk.go")
	body := wsBody(grp("#1", "0", ed(3, 6, 3, 7, "G"), ed(1, 1, 1, 1, "<"), ed(1, 1, 1, 1, ">")), grp(disk, "-", ed(2, 1, 2, 4, "2")))
	gs, _, why := h.checkWorkspaceEdit([]byte(body), nil)
	if why != "" {
		t.Fatal(why)
	}
	h.apply(gs)
	if got := string(h.docs[1].text); got != "<>package a\n\nfunc G() {}\n" || h.docs[1].version != 4 {
		t.Errorf("a.go is %q at version %d", got, h.docs[1].version)
	}
	if d := h.docs[3]; d == nil || string(d.text) != "one\n2\n" || d.version != 2 {
		t.Errorf("disk.go did not open with its edit: %+v", d)
	}
	// the last edit first: G for F, then > and < at the start; disk.go opens at its version after
	wantOut := "0 edits 1 4 29\n16 1 0\n16 0 1\nG0 0 1\n>0 0 1\n<0 opened 3 2 Go " + disk + "\n"
	if out.String() != wantOut {
		t.Errorf("events:\n%q\nwant\n%q", out.String(), wantOut)
	}
}

func TestLocations(t *testing.T) {
	h, dir := testHost(t)
	loc := func(l1, c1, l2, c2 int, path, text string) string {
		return fmt.Sprintf("%d %d %d %d %d %d\n%s%s", l1, c1, l2, c2, len(path), len(text), path, text)
	}
	a, disk := filepath.Join(dir, "a.go"), filepath.Join(dir, "disk.go")
	cases := []struct{ name, body, want string }{
		{"none", "0\n", ""},
		{"three", "3\n" + loc(3, 6, 3, 7, a, "func F() {}") + loc(1, 1, 1, 6, "#2", "") + loc(2, 1, 2, 4, disk, "two"), ""},
		{"relative", "1\n" + loc(1, 1, 1, 2, "a.go", ""), "neither absolute"},
		{"N says fewer", "1\n" + loc(1, 1, 1, 2, a, "") + loc(1, 1, 1, 2, a, ""), "bytes after the 1 locations"},
		{"N says more", "2\n" + loc(1, 1, 1, 2, a, ""), "location 2 has no header line"},
		{"two-line preview", "1\n" + loc(1, 1, 1, 2, a, "a\nb"), "line break"},
		{"no path", "1\n" + loc(1, 1, 1, 2, "", ""), "PATH_LEN"},
		{"past the file's end", "1\n" + loc(5, 1, 5, 2, disk, ""), "past the last line"},
		{"over 10000", "10001\n", "0 to 10000"},
	}
	for _, c := range cases {
		_, why := h.checkLocations(okMsg(c.body))
		want(t, c.name, why, c.want)
	}
	_, why := h.checkLocations(message{line: "ok 1"})
	want(t, "no LEN", why, "not ok ID LEN")
}

func TestSymbols(t *testing.T) {
	h, _ := testHost(t)
	sym := func(l1, c1, l2, c2 int, kind, depth, name, detail string) string {
		return fmt.Sprintf("%d %d %d %d %s %s %d %d\n%s%s", l1, c1, l2, c2, kind, depth, len(name), len(detail), name, detail)
	}
	d := h.docs[1]
	cases := []struct{ name, body, want string }{
		{"two", "2\n" + sym(1, 1, 1, 10, "package", "0", "a", "") + sym(3, 1, 3, 12, "function", "0", "F", "func()"), ""},
		{"depth not a number", "1\n" + sym(1, 1, 1, 2, "function", "x", "F", ""), "DEPTH"},
		{"no name", "1\n" + sym(1, 1, 1, 2, "function", "0", "", ""), "NAME_LEN"},
		{"outside", "1\n" + sym(9, 1, 9, 2, "function", "0", "F", ""), "past the last line"},
		{"detail of two lines", "1\n" + sym(1, 1, 1, 2, "function", "0", "F", "a\nb"), "line break"},
		{"N says fewer", "1\n" + sym(1, 1, 1, 2, "function", "0", "F", "") + sym(1, 1, 1, 2, "function", "0", "F", ""), "bytes after"},
	}
	for _, c := range cases {
		_, _, why := checkSymbols(okMsg(c.body), d)
		want(t, c.name, why, c.want)
	}
	_, warns, why := checkSymbols(okMsg("2\n"+sym(3, 1, 3, 2, "gizmo", "0", "F", "")+sym(1, 1, 1, 2, "package", "0", "a", "")), d)
	if why != "" || len(warns) != 2 {
		t.Errorf("an unknown kind and a symbol out of order are warnings: %q, %q", warns, why)
	}
}

func TestActions(t *testing.T) {
	h, _ := testHost(t)
	act := func(kind, title, edit, data string) string {
		return fmt.Sprintf("%s %d %d %d\n%s%s%s", kind, len(title), len(edit), len(data), title, edit, data)
	}
	fix := wsBody(grp("#1", "0", ed(3, 6, 3, 7, "G")))
	cases := []struct{ name, body, want string }{
		{"two", "2\n" + act("quickfix", "Rename F", fix, "data") + act("source.organizeImports", "Organize", "", ""), ""},
		{"stale edit", "1\n" + act("quickfix", "Stale", wsBody(grp("#1", "1", ed(1, 1, 1, 1, "x"))), ""), "its edit: file 1: VERSION 1"},
		{"title of two lines", "1\n" + act("quickfix", "a\nb", "", ""), "line break"},
		{"no title", "1\n" + act("quickfix", "", "", ""), "TITLE_LEN"},
		{"N says more", "3\n" + act("quickfix", "One", "", ""), "action 2 has no header line"},
		{"over 1000", "1001\n", "0 to 1000"},
	}
	for _, c := range cases {
		_, why := h.checkActions(okMsg(c.body))
		want(t, c.name, why, c.want)
	}
	acts, _ := h.checkActions(okMsg("1\n" + act("quickfix", "Rename F", fix, "data")))
	if len(acts) != 1 || len(acts[0].edit) != 1 || string(acts[0].data) != "data" {
		t.Errorf("the action and its edit: %+v", acts)
	}
}
