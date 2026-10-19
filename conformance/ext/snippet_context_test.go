package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnippetContextPreservesOpenedCRLFModeAndJSONComment(t *testing.T) {
	h, dir := testHost(t)
	h.stdin = &sink{}
	h.inflight = map[int64]bool{}
	h.features = map[string]bool{"snippets": true}
	h.handle(message{line: "1 open 0 0 " + filepath.Join(dir, "crlf.go")})
	b, _, code, why := h.snippetContext([]string{"3", "0", "1", "1", "1", "1"})
	if code != "" || !strings.HasPrefix(string(b), "context 1 1 1 4 1 1 ") {
		t.Errorf("CRLF: %q %s %s", b, code, why)
	}
	if string(h.docs[3].text) != "one\ntwo\n" {
		t.Fatal("CRLF document text must remain normalized")
	}
	h.docs[3].text = []byte("changed\n")
	h.save(h.docs[3])
	disk, err := os.ReadFile(filepath.Join(dir, "crlf.go"))
	if err != nil || string(disk) != "changed\r\n" {
		t.Fatal("CRLF save", string(disk), err)
	}
	h.docs[1].lang = "JSON"
	b, _, code, why = h.snippetContext([]string{"1", "0", "1", "1", "1", "1"})
	if code != "" || !strings.HasSuffix(string(b), "//") {
		t.Fatalf("JSON: %q %s %s", b, code, why)
	}
}

func TestSnippetContextSnapshotAndGuards(t *testing.T) {
	h, _ := testHost(t)
	h.features = map[string]bool{"snippets": true}
	h.project = "/p"
	d := h.docs[1]
	d.text = []byte("  😀\nnext\x00line")
	d.path = "/p/a.go"
	d.lang = "Go"
	d.cur = 2
	d.anchor = 0
	h.options = []string{"1 tab_width 8", "1 insert_spaces no", "2 tab_width 16"}
	b, v, code, why := h.snippetContext([]string{"1", "0", "1", "3", "2", "6"})
	want := "context 1 1 3 8 0 0 10 6 7 2 2 2\n😀\nnext\x00  😀/p/a.go/p  //"
	if code != "" || v != 0 || string(b) != want {
		t.Fatalf("%q %d %s %s", b, v, code, why)
	}
	if string(d.text) != "  😀\nnext\x00line" || d.cur != 2 || d.anchor != 0 || d.version != 0 {
		t.Fatal("snapshot mutated document")
	}
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"1", "1", "1", "1", "1", "1"}, "stale"},
		{[]string{"99", "0", "1", "1", "1", "1"}, "nodoc"},
		{[]string{"1", "0", "1", "4", "1", "4"}, "badpos"},
		{[]string{"1", "0", "2", "1", "1", "1"}, "badpos"},
		{[]string{"1", "+0", "1", "1", "1", "1"}, "syntax"},
		{[]string{"1", "0", "1", "1", "1", "1", "extra"}, "syntax"},
	} {
		if _, _, code, _ := h.snippetContext(tc.args); code != tc.code {
			t.Fatal(tc, code)
		}
	}
	d.text = []byte(strings.Repeat("x", 4<<20))
	if _, _, code, _ := h.snippetContext([]string{"1", "0", "1", "1", "1", "1"}); code != "toobig" {
		t.Fatal(code)
	}
	d.text = []byte{255}
	if _, _, code, _ := h.snippetContext([]string{"1", "0", "1", "1", "1", "1"}); code != "refused" {
		t.Fatal(code)
	}
}

func TestSnippetContextWorkspaceOpenRetainsCRLF(t *testing.T) {
	h, dir := testHost(t)
	h.stdin = &sink{}
	h.features = map[string]bool{"snippets": true}
	groups, code, why := h.checkWorkspaceEdit([]byte(wsBody(grp(filepath.Join(dir, "crlf.go"), "-", ed(1, 1, 1, 1, "x")))), nil)
	if code != "" {
		t.Fatal(code, why)
	}
	h.apply(groups)
	b, _, code, why := h.snippetContext([]string{"3", "1", "1", "1", "1", "1"})
	if code != "" || !strings.HasPrefix(string(b), "context 1 1 1 4 1 1 ") {
		t.Fatalf("%q %s %s", b, code, why)
	}
	h.save(h.docs[3])
	data, err := os.ReadFile(filepath.Join(dir, "crlf.go"))
	if err != nil || string(data) != "xone\r\ntwo\r\n" {
		t.Fatal(string(data), err)
	}
}

func TestSnippetContextOptionsUseNumericDocumentIdentity(t *testing.T) {
	for _, ids := range [][2]string{{"01", "1"}, {"1", "01"}} {
		h, _ := testHost(t)
		h.features = map[string]bool{"snippets": true}
		h.options = []string{ids[0] + " tab_width 8", ids[0] + " insert_spaces no"}
		b, _, code, why := h.snippetContext([]string{ids[1], "0", "1", "1", "1", "1"})
		if code != "" || !strings.HasPrefix(string(b), "context 1 1 1 8 0 0 ") {
			t.Errorf("%v %q %s %s", ids, b, code, why)
		}
	}
}
