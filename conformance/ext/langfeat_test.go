package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testHost has a.go (#1, version 0) and b.go (#2, version 3, a two-byte character) open, without
// unsaved changes, and on disk only: disk.go, crlf.go (CRLF line ends), bom.go (a byte order mark,
// a CR alone), bin.go (a NUL), img.png (a PNG's signature, no NUL) and the folder dir.go
func testHost(t *testing.T) (*host, string) {
	dir := t.TempDir()
	h := &host{docs: map[int64]*doc{}, project: dir, m: &manifest{provides: map[string]string{}, subscribe: map[string]string{}}}
	h.newDoc(filepath.Join(dir, "a.go"), "Go", []byte("package a\n\nfunc F() {}\n"))
	h.newDoc(filepath.Join(dir, "b.go"), "Go", []byte("héllo\n")).version = 3
	files := map[string]string{"a.go": "package a\n\nfunc F() {}\n", "b.go": "héllo\n", "disk.go": "one\ntwo\n",
		"crlf.go": "one\r\ntwo\r\n", "bom.go": "\xef\xbb\xbfab\r\rc\n", "bin.go": "ab\x00cd\n", "img.png": "\x89PNG\r\n\x1a\nabc"}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.go"), 0o755); err != nil {
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
	crlf, bom := filepath.Join(dir, "crlf.go"), filepath.Join(dir, "bom.go")
	x := ed(1, 1, 1, 1, "x")
	unsorted := make([]string, 6000)
	for i := range unsorted {
		unsorted[i] = ed(1, 2-i%2, 1, 2-i%2, "x") // 6000 insertions, at 1 2 and 1 1 in turn
	}
	long := fmt.Sprintf("1\n%d 0 1\n%s", len(a), a)
	cases := []struct{ name, body, code, want string }{
		{"empty", "0\n", "", ""},
		{"three files", wsBody(grp("#1", "0", ed(3, 6, 3, 7, "G")), grp(disk, "-", ed(2, 1, 2, 4, "2")),
			grp(b, "3", ed(1, 4, 1, 7, "LO"))), "", ""},
		{"insertions at one place", wsBody(grp(a, "0", x, ed(1, 1, 1, 1, ">"))), "", ""},
		{"out of order", wsBody(grp(a, "0", ed(3, 1, 3, 5, "fn"), ed(1, 1, 1, 8, "PACKAGE"))), "", ""},
		{"insertion before a replacement at its start", wsBody(grp(a, "0", x, ed(1, 1, 1, 8, "y"))), "", ""},
		{"insertion after a replacement at its start", wsBody(grp(a, "0", ed(1, 1, 1, 8, "y"), x)), "", ""},
		{"edits that touch", wsBody(grp(a, "0", ed(1, 3, 1, 5, "y"), ed(1, 1, 1, 3, "x"))), "", ""},
		{"more than 5000 out of order", wsBody(grp(a, "0", unsorted...)), "", ""},
		{"overlap", wsBody(grp(a, "0", ed(1, 1, 1, 5, "x"), ed(1, 3, 1, 8, "y"))), "badpos", "overlap"},
		{"overlap once sorted", wsBody(grp(a, "0", ed(1, 5, 1, 8, "x"), ed(1, 1, 1, 6, "y"))), "badpos", "bytes 0 to 5 and 4 to 7"},
		{"two replacements at one start", wsBody(grp(a, "0", ed(1, 1, 1, 3, "x"), ed(1, 1, 1, 5, "y"))), "badpos", "overlap"},
		{"stale", wsBody(grp("#2", "2", x)), "stale", "which is at version 3"},
		{"dash for an open document without unsaved changes", wsBody(grp(a, "-", ed(3, 6, 3, 7, "G"))), "", ""},
		{"version for a file not open", wsBody(grp(disk, "0", x)), "nodoc", "which is not open"},
		{"version not a number", wsBody(grp(a, "v0", x)), "syntax", "neither - nor a number"},
		{"version of 19 digits", wsBody(grp(a, "1234567890123456789", x)), "syntax", "at most 18 digits"},
		{"version of 18 digits", wsBody(grp(a, "123456789012345678", x)), "stale", "which is at version 0"},
		{"relative path", wsBody(grp("a.go", "0", x)), "nofile", "neither absolute nor #DOC"},
		{"unknown document", wsBody(grp("#9", "0", x)), "nodoc", "never opened"},
		{"document 0", wsBody(grp("#0", "-", x)), "nodoc", "never opened"},
		{"an id of 18 digits", wsBody(grp("#123456789012345678", "-", x)), "nodoc", "never opened"},
		{"an id of 19 digits", wsBody(grp("#1234567890123456789", "-", x)), "nofile", "neither absolute nor #DOC"},
		{"a NUL in the path", wsBody(grp(a+"\x00", "-", x)), "nofile", "holds a NUL"},
		{"missing file", wsBody(grp(filepath.Join(dir, "none.go"), "-", x)), "nofile", "no such file"},
		{"binary file", wsBody(grp(filepath.Join(dir, "bin.go"), "-", x)), "nofile", "binary"},
		{"image", wsBody(grp(filepath.Join(dir, "img.png"), "-", x)), "nofile", "an image"},
		{"folder", wsBody(grp(filepath.Join(dir, "dir.go"), "-", x)), "nofile", "not a regular file"},
		{"CRLF made LF", wsBody(grp(crlf, "-", ed(1, 4, 2, 1, " "), ed(2, 1, 2, 4, "2"))), "", ""},
		{"no CR in a CRLF file's text", wsBody(grp(crlf, "-", ed(1, 5, 1, 5, "x"))), "badpos", "past the end of line 1"},
		{"a byte order mark is a character", wsBody(grp(bom, "-", ed(1, 2, 1, 2, "x"))), "badpos", "inside a multibyte"},
		{"a CR alone stays", wsBody(grp(bom, "-", ed(1, 4, 1, 8, "x"))), "", ""},
		{"one file twice", wsBody(grp("#1", "0", x), grp(a, "0", ed(2, 1, 2, 1, "y"))), "syntax", "has a group already"},
		{"one file on disk twice", wsBody(grp(disk, "-", x), grp(disk, "-", ed(2, 1, 2, 1, "y"))), "syntax", "has a group already"},
		{"past a line's end", wsBody(grp(a, "0", ed(1, 1, 1, 20, "x"))), "badpos", "past the end of line 1"},
		{"inside a character", wsBody(grp(b, "3", ed(1, 3, 1, 4, "x"))), "badpos", "inside a multibyte character"},
		{"reversed", wsBody(grp(a, "0", ed(1, 5, 1, 1, "x"))), "badpos", "ends before it starts"},
		{"line 0", wsBody(grp(a, "0", ed(0, 1, 1, 1, "x"))), "badpos", "positive"},
		{"a column that is not a number", fmt.Sprintf("1\n%d 0 1\n%s1 x 1 1 0\n", len(a), a), "syntax", "not a number"},
		{"a number of 11 digits", fmt.Sprintf("1\n%d 0 1\n%s1 1 1 10000000000 0\n", len(a), a), "syntax", "at most 10 digits"},
		{"N says more", "2\n" + grp(a, "0", x), "syntax", "file 2 has no header line"},
		{"M says more", long, "syntax", "edit 1 has no header line"},
		{"bytes after", wsBody(grp(a, "0", x)) + "junk", "syntax", "4 bytes after"},
		{"two spaces", fmt.Sprintf("1\n%d  0 0\n%s", len(a), a), "syntax", "is not PATH_LEN VERSION M"},
		{"a space after the last word", fmt.Sprintf("1 \n%d 0 1 \n%s1 1 1 1 1 \nx", len(a), a), "", ""},
		{"two spaces after the last word", fmt.Sprintf("1\n%d 0 0  \n%s", len(a), a), "syntax", "is not PATH_LEN VERSION M"},
		{"text past the body", fmt.Sprintf("1\n%d 0 1\n%s1 1 1 1 9\nab", len(a), a), "syntax", "runs past the body"},
		{"over 1000 files", "1001\n", "toobig", "at most 1000"},
		{"over 100000 edits", fmt.Sprintf("1\n%d 0 100001\n%s", len(a), a), "toobig", "over 100000 edits"},
		{"over 4 MB", strings.Repeat("x", 4<<20+1), "toobig", "over 4 MB"},
		// rhun's order: the whole grammar, then each file's document, then the files not open, then the ranges
		{"syntax before a path", wsBody(grp("a.go", "0", x)) + "junk", "syntax", "bytes after"},
		{"a document before a later path", wsBody(grp("#2", "2", x), grp("a.go", "0", x)), "stale", "version 3"},
		{"open documents before files on disk", wsBody(grp(filepath.Join(dir, "none.go"), "-", x), grp("#2", "2", x)), "stale", "version 3"},
		{"files on disk before ranges", wsBody(grp(a, "0", ed(9, 1, 9, 1, "x")), grp(filepath.Join(dir, "none.go"), "-", x)), "nofile", "no such file"},
	}
	for _, c := range cases {
		_, code, why := h.checkWorkspaceEdit([]byte(c.body), nil)
		want(t, c.name, why, c.want)
		if code != c.code {
			t.Errorf("%s: the code is %q, want %q (%s)", c.name, code, c.code, why)
		}
	}
	_, code, why := h.checkWorkspaceEdit([]byte(wsBody(grp(b, "3", x))), h.docs[1])
	want(t, "format edits another document", why, "not the document asked about")
	if code != "nofile" {
		t.Errorf("format edits another document: %q", code)
	}
	_, _, why = h.checkWorkspaceEdit([]byte(wsBody(grp(disk, "-", x))), h.docs[1])
	want(t, "format edits a file that is not open", why, "not the document asked about")
	_, _, why = h.checkWorkspaceEdit([]byte(wsBody(grp(a, "0", x))), h.docs[1])
	want(t, "format edits its document", why, "")

	// "-" for an open document is its text on disk only while it has no unsaved changes
	h.docs[2].dirty = true
	_, code, why = h.checkWorkspaceEdit([]byte(wsBody(grp(b, "-", x))), nil)
	want(t, "dash for a document with unsaved changes", why, "with unsaved changes")
	if code != "stale" {
		t.Errorf("dash for a document with unsaved changes: %q", code)
	}
	_, _, why = h.checkWorkspaceEdit([]byte(wsBody(grp(b, "3", x))), nil)
	want(t, "its version for a document with unsaved changes", why, "")
}

type sink struct{ bytes.Buffer }

func (sink) Close() error { return nil }

func TestApply(t *testing.T) {
	h, dir := testHost(t)
	out := &sink{}
	h.stdin = out
	h.m.provides["rename"] = "*.go"
	h.features = map[string]bool{"rename": true}
	disk, crlf := filepath.Join(dir, "disk.go"), filepath.Join(dir, "crlf.go")
	// edits in any order: by start, then by end (the insertion at 1 1 before the replacement there)
	body := wsBody(grp("#1", "0", ed(3, 6, 3, 7, "G"), ed(1, 1, 1, 8, "PACKAGE"), ed(1, 1, 1, 1, "<"), ed(1, 1, 1, 1, ">")),
		grp(disk, "-", ed(2, 1, 2, 4, "2")), grp(crlf, "-", ed(1, 4, 2, 1, " ")))
	gs, _, why := h.checkWorkspaceEdit([]byte(body), nil)
	if why != "" {
		t.Fatal(why)
	}
	h.apply(gs)
	if got := string(h.docs[1].text); got != "<>PACKAGE a\n\nfunc G() {}\n" || h.docs[1].version != 6 || !h.docs[1].dirty {
		t.Errorf("a.go is %q at version %d", got, h.docs[1].version)
	}
	if d := h.docs[3]; d == nil || string(d.text) != "one\n2\n" || d.version != 2 || !d.dirty {
		t.Errorf("disk.go did not open with its edit: %+v", d)
	}
	if d := h.docs[4]; d == nil || string(d.text) != "one two\n" || d.version != 2 {
		t.Errorf("crlf.go did not open as LF text with its edit: %+v", d)
	}
	if h.docs[2].dirty {
		t.Error("b.go, which the edit does not name, has unsaved changes")
	}
	// the last edit first: G for F, then PACKAGE for package, then > and < at the start; disk.go
	// and crlf.go open at their versions after
	wantOut := "0 edits 1 6 48\n16 1 0\n16 0 1\nG0 7 0\n0 0 7\nPACKAGE0 0 1\n>0 0 1\n<0 opened 3 2 Go " + disk + "\n" +
		"0 opened 4 2 Go " + crlf + "\n"
	if out.String() != wantOut {
		t.Errorf("events:\n%q\nwant\n%q", out.String(), wantOut)
	}
	// with unsaved changes "-" is stale; saved, it is the text on disk again
	gs, code, _ := h.checkWorkspaceEdit([]byte(wsBody(grp("#1", "-", ed(1, 1, 1, 1, "x")))), nil)
	if code != "stale" {
		t.Errorf("dash after the edit: %q", code)
	}
	out.Reset()
	h.save(h.docs[1])
	if out.Len() != 0 {
		t.Errorf("saved without the subscription: %q", out.String())
	}
	h.m.subscribe["saved"] = "*"
	h.save(h.docs[3])
	if out.String() != "0 saved 3 2\n" {
		t.Errorf("saved: %q", out.String())
	}
	if data, _ := os.ReadFile(disk); string(data) != "one\n2\n" {
		t.Errorf("disk.go on disk: %q", data)
	}
	gs, _, why = h.checkWorkspaceEdit([]byte(wsBody(grp("#1", "-", ed(1, 1, 1, 1, "x")))), nil)
	if why != "" || len(gs) != 1 || gs[0].t.d != h.docs[1] {
		t.Errorf("dash after saving: %s", why)
	}
}

func TestLocations(t *testing.T) {
	h, dir := testHost(t)
	loc := func(l1, c1, l2, c2 int, path, text string) string {
		return fmt.Sprintf("%d %d %d %d %d %d\n%s%s", l1, c1, l2, c2, len(path), len(text), path, text)
	}
	a, disk := filepath.Join(dir, "a.go"), filepath.Join(dir, "disk.go")
	cases := []struct {
		name, body, want string
		warns            int
		warn             string
	}{
		{"none", "0\n", "", 0, ""},
		{"three", "3\n" + loc(3, 6, 3, 7, a, "func F() {}") + loc(1, 1, 1, 6, "#2", "") + loc(2, 1, 2, 4, disk, "two"), "", 0, ""},
		{"relative", "1\n" + loc(1, 1, 1, 2, "a.go", ""), "neither absolute", 0, ""},
		{"N says fewer", "1\n" + loc(1, 1, 1, 2, a, "") + loc(1, 1, 1, 2, a, ""), "bytes after the 1 locations", 0, ""},
		{"N says more", "2\n" + loc(1, 1, 1, 2, a, ""), "location 2 has no header line", 0, ""},
		{"no path", "1\n" + loc(1, 1, 1, 2, "", ""), "PATH_LEN", 0, ""},
		{"a path over 4096 bytes", "1\n" + loc(1, 1, 1, 2, "/"+strings.Repeat("p", 4096), ""), "PATH_LEN", 0, ""},
		{"a number of 11 digits", "1\n" + strings.Replace(loc(1, 1, 1, 2, a, ""), " 2 ", " 10000000000 ", 1), "at most 10 digits", 0, ""},
		{"over 10000", "10001\n", "0 to 10000", 0, ""},
		// rhun lists these and copes when one is chosen
		{"two-line preview", "1\n" + loc(1, 1, 1, 2, a, "a\nb"), "", 1, "line break"},
		{"past the file's end", "1\n" + loc(5, 1, 5, 2, disk, ""), "", 1, "nearest place"},
		{"line 0", "1\n" + loc(0, 0, 1, 2, a, ""), "", 1, "nearest place"},
		{"a document never opened", "1\n" + loc(1, 1, 1, 2, "#9", ""), "", 1, "never opened"},
		{"a missing file", "1\n" + loc(1, 1, 1, 2, filepath.Join(dir, "none.go"), ""), "", 1, "no such file"},
		{"a space after the last word", "2 \n" + strings.Replace(loc(1, 1, 1, 2, a, ""), "\n", " \n", 1) +
			strings.Replace(loc(1, 1, 1, 2, a, ""), "\n", " \n", 1), "", 1, "ends in a space"},
	}
	for _, c := range cases {
		_, warns, why := h.checkLocations(okMsg(c.body))
		want(t, c.name, why, c.want)
		if len(warns) != c.warns || c.warn != "" && !strings.Contains(strings.Join(warns, "|"), c.warn) {
			t.Errorf("%s: warnings %q, want %d with %q", c.name, warns, c.warns, c.warn)
		}
	}
	_, _, why := h.checkLocations(message{line: "ok 1"})
	want(t, "no LEN", why, "not ok ID LEN")
}

func TestSymbols(t *testing.T) {
	h, _ := testHost(t)
	sym := func(l1, c1, l2, c2 int, kind, depth, name, detail string) string {
		return fmt.Sprintf("%d %d %d %d %s %s %d %d\n%s%s", l1, c1, l2, c2, kind, depth, len(name), len(detail), name, detail)
	}
	d := h.docs[1]
	cases := []struct {
		name, body, want string
		warns            int
	}{
		{"two", "2\n" + sym(1, 1, 1, 10, "package", "0", "a", "") + sym(3, 1, 3, 12, "function", "0", "F", "func()"), "", 0},
		{"a deep one", "1\n" + sym(1, 1, 1, 2, "function", "99", "F", ""), "", 0},
		{"depth not a number", "1\n" + sym(1, 1, 1, 2, "function", "x", "F", ""), "not a number", 0},
		{"no name", "1\n" + sym(1, 1, 1, 2, "function", "0", "", ""), "NAME_LEN", 0},
		{"a name over 1000 bytes", "1\n" + sym(1, 1, 1, 2, "function", "0", strings.Repeat("n", 1001), ""), "NAME_LEN", 0},
		{"a detail over 1000 bytes", "1\n" + sym(1, 1, 1, 2, "function", "0", "F", strings.Repeat("d", 1001)), "DETAIL_LEN", 0},
		{"a kind over 40 bytes", "1\n" + sym(1, 1, 1, 2, strings.Repeat("k", 41), "0", "F", ""), "over 40 bytes", 0},
		{"N says fewer", "1\n" + sym(1, 1, 1, 2, "function", "0", "F", "") + sym(1, 1, 1, 2, "function", "0", "F", ""), "bytes after", 0},
		// rhun shows these
		{"outside", "1\n" + sym(9, 1, 9, 2, "function", "0", "F", ""), "", 1},
		{"detail of two lines", "1\n" + sym(1, 1, 1, 2, "function", "0", "F", "a\nb"), "", 1},
		{"an unknown kind and a symbol out of order", "2\n" + sym(3, 1, 3, 2, "gizmo", "0", "F", "") + sym(1, 1, 1, 2, "package", "0", "a", ""), "", 2},
	}
	for _, c := range cases {
		_, warns, why := checkSymbols(okMsg(c.body), d)
		want(t, c.name, why, c.want)
		if len(warns) != c.warns {
			t.Errorf("%s: warnings %q, want %d", c.name, warns, c.warns)
		}
	}
}

func act(kind, title, edit, data string) string {
	return fmt.Sprintf("%s %d %d %d\n%s%s%s", kind, len(title), len(edit), len(data), title, edit, data)
}

func TestActions(t *testing.T) {
	h, _ := testHost(t)
	fix := wsBody(grp("#1", "0", ed(3, 6, 3, 7, "G")))
	stale := wsBody(grp("#1", "1", ed(1, 1, 1, 1, "x")))
	cases := []struct {
		name, body, want string
		warns            int
	}{
		{"two", "2\n" + act("quickfix", "Rename F", fix, "data") + act("source.organizeImports", "Organize", "", ""), "", 0},
		// made for another version: rhun lists it, and checks the edit when it is chosen
		{"stale edit", "1\n" + act("quickfix", "Stale", stale, ""), "", 0},
		{"an edit for a missing file", "1\n" + act("quickfix", "Gone", wsBody(grp("/no/such/file.go", "-", ed(1, 1, 1, 1, "x"))), ""), "", 0},
		// an edit that cannot be read is broken whatever the documents say
		{"an edit that cannot be read", "1\n" + act("quickfix", "Bad", "1\n2 0 1\n#11 1 1\n", ""), "its edit cannot be made", 0},
		{"an edit with a relative path", "1\n" + act("quickfix", "Rel", wsBody(grp("a.go", "0", ed(1, 1, 1, 1, "x"))), ""), "neither absolute", 0},
		{"title of two lines", "1\n" + act("quickfix", "a\nb", "", ""), "", 1},
		{"no title", "1\n" + act("quickfix", "", "", ""), "TITLE_LEN", 0},
		{"a title over 1000 bytes", "1\n" + act("quickfix", strings.Repeat("t", 1001), "", ""), "TITLE_LEN", 0},
		{"a kind over 40 bytes", "1\n" + act(strings.Repeat("k", 41), "T", "", ""), "over 40 bytes", 0},
		{"N says more", "3\n" + act("quickfix", "One", "", ""), "action 2 has no header line", 0},
		{"over 1000", "1001\n", "0 to 1000", 0},
	}
	for _, c := range cases {
		_, warns, why := h.checkActions(okMsg(c.body))
		want(t, c.name, why, c.want)
		if len(warns) != c.warns {
			t.Errorf("%s: warnings %q, want %d", c.name, warns, c.warns)
		}
	}
	acts, _, _ := h.checkActions(okMsg("1\n" + act("quickfix", "Rename F", fix, "data")))
	if len(acts) != 1 || string(acts[0].edit) != fix || string(acts[0].data) != "data" {
		t.Errorf("the action and its edit: %+v", acts)
	}
}

// TestChooseAction: the chosen action's edit is checked when it is chosen; one rhun refuses
// changes nothing and sends no action event
func TestChooseAction(t *testing.T) {
	h, _ := testHost(t)
	out := &sink{}
	h.stdin = out
	stale := codeAction{title: "Stale", edit: []byte(wsBody(grp("#1", "1", ed(1, 1, 1, 1, "x")))), data: []byte("d")}
	if why := h.chooseAction(stale); !strings.HasPrefix(why, "stale: ") || out.Len() != 0 || h.docs[1].version != 0 {
		t.Errorf("a stale action: %q, wrote %q", why, out.String())
	}
	fix := codeAction{title: "Fix", edit: []byte(wsBody(grp("#1", "0", ed(3, 6, 3, 7, "G")))), data: []byte("a b\nc")}
	if why := h.chooseAction(fix); why != "" || string(h.docs[1].text) != "package a\n\nfunc G() {}\n" {
		t.Errorf("an action with an edit: %q, %q", why, h.docs[1].text)
	}
	if got := out.String(); got != "0 action 5\na b\nc" {
		t.Errorf("the action event after the edit: %q", got)
	}
	out.Reset()
	if why := h.chooseAction(codeAction{title: "Run", data: []byte("go")}); why != "" || out.String() != "0 action 2\ngo" {
		t.Errorf("an action without an edit: %q, %q", why, out.String())
	}
}

// TestLoadFile: files read as rhun's doc_load reads them
func TestLoadFile(t *testing.T) {
	_, dir := testHost(t)
	for name, want := range map[string]string{"crlf.go": "one\ntwo\n", "bom.go": "\xef\xbb\xbfab\r\rc\n"} {
		if got, why, _ := loadFile(filepath.Join(dir, name)); why != "" || string(got) != want {
			t.Errorf("%s: %q %s", name, got, why)
		}
	}
	for name, part := range map[string]string{"bin.go": "binary", "img.png": "an image", "dir.go": "not a regular file", "none.go": "no such file"} {
		if _, why, image := loadFile(filepath.Join(dir, name)); !strings.Contains(why, part) || image != (name == "img.png") {
			t.Errorf("%s: %q, want %q", name, why, part)
		}
	}
	// a NUL past the first 8 KiB is text
	late := append(bytes.Repeat([]byte("a"), 8192), 0)
	os.WriteFile(filepath.Join(dir, "late.go"), late, 0o644)
	if _, why, _ := loadFile(filepath.Join(dir, "late.go")); why != "" {
		t.Errorf("a NUL past 8 KiB: %s", why)
	}
}
