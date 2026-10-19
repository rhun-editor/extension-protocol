package rhunext

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type modelProbe struct {
	got chan string
}

func (m *modelProbe) Ready(ext *Ext, project string) {
	go m.run(ext)
}

func (m *modelProbe) run(ext *Ext) {
	code := func(err error) string {
		var re *Error
		if errors.As(err, &re) {
			return re.Code
		}
		if err != nil {
			return err.Error()
		}
		return "ok"
	}
	at := Pos{2, 3}
	m.got <- code(ext.SelectMany(4, []Selection{{Pos{1, 1}, Pos{1, 4}}, {at, at}}))
	m.got <- code(ext.SelectMany(4, nil))
	m.got <- code(ext.Reveal(4, at, RevealCenterIfOutside))
	m.got <- code(ext.ShowLocations("Callers", []Location{{Target: Target{Doc: 4}, Range: Range{at, at}}}))
	tab, err := ext.Diff(4, 5, "HEAD\nwork", DiffBackground)
	m.got <- fmt.Sprintf("diff %d %v", tab, err)
	m.got <- code(ext.DiffClose(tab))
	var w WorkspaceEdit
	w.Label = "Move"
	w.Rename("/p/a.go", "/p/b.go", RenameOptions{})
	w.Create("/p/c.go", CreateOptions{})
	w.AddFileHashed("/p/a.go", []byte("x"), TextEdit{Range{at, at}, "y"})
	err = ext.Apply(w)
	var ae *ApplyError
	if errors.As(err, &ae) {
		e, _ := w.Entry(ae.Entry)
		m.got <- fmt.Sprintf("%s %d %s %+v", ae.Code, ae.Entry, ae.Text, e)
	} else {
		m.got <- fmt.Sprint(err)
	}
}

func (m *modelProbe) Run(ext *Ext, command string, doc Doc) {}

func (m *modelProbe) Event(ext *Ext, ev Event) {
	if d, sels, ok := ParseSelections(ev); ok {
		m.got <- fmt.Sprintf("selections %d %v", d, sels)
	}
}

func (m *modelProbe) Linked(ext *Ext, req LinkedRequest) Linked {
	l, w := req.At.Line, "-"
	return Linked{Ranges: []Range{rg(l, 2, l, 5), rg(l+1, 3, l+1, 6)}, Word: &w}
}

func (m *modelProbe) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-m.got:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("nothing came")
		return ""
	}
}

func (s *host) body(n int) string {
	s.t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(s.br, b); err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

func TestWorkspaceEditTransactionsAndTheEditorModel(t *testing.T) {
	feats := []string{FeatureWorkspaceEdits, FeatureFileOperations, FeatureSelections, FeatureReveal, FeatureDiff, FeatureLinkedEditing}
	m := &modelProbe{got: make(chan string, 16)}
	s := startHost(t, feats, m)
	s.expect("hello 1 workspace-edits file-operations selections reveal diff linked-editing")
	s.send("0 features " + strings.Join(feats, " ") + "\n0 ready -\n")
	s.expect("1 select-many 4 2 16")
	if b := s.body(16); b != "1 1 1 4\n2 3 2 3\n" {
		t.Fatalf("select-many body %q", b)
	}
	s.send("ok 1\n")
	for _, want := range []string{"ok", "toobig"} {
		if got := m.next(t); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	s.expect("2 reveal 4 2 3 center-if-outside")
	s.send("ok 2\n")
	if got := m.next(t); got != "ok" {
		t.Fatal(got)
	}
	body := "Callers\n1\n2 3 2 3 2 0\n#4"
	s.expect(fmt.Sprintf("3 show-locations %d", len(body)))
	if b := s.body(len(body)); b != body {
		t.Fatalf("show-locations body %q", b)
	}
	s.send("error 3 busy the palette is open\n")
	if got := m.next(t); got != "busy" {
		t.Fatal(got)
	}
	s.expect("4 diff 4 5 background 9")
	if b := s.body(9); b != "HEAD work" {
		t.Fatalf("diff title %q", b)
	}
	s.send("ok 4 77\n")
	if got := m.next(t); got != "diff 77 <nil>" {
		t.Fatal(got)
	}
	s.expect("5 diff-close 77")
	s.send("ok 5\n")
	if got := m.next(t); got != "ok" {
		t.Fatal(got)
	}
	edit := fmt.Sprintf("3\ncreate 7 -\n/p/c.go7 fnv1a64:%016x 1\n/p/a.go2 3 2 3 1\nyrename 7 7 -\n/p/a.go/p/b.golabel 4\nMove", Fnv1a64([]byte("x")))
	s.expect(fmt.Sprintf("6 apply %d", len(edit)))
	if b := s.body(len(edit)); b != edit {
		t.Fatalf("apply body:\n%q\nwant\n%q", b, edit)
	}
	s.send("error 6 stale 2 /p/a.go changed on disk\n")
	if got := m.next(t); got != "stale 2 /p/a.go changed on disk {Op:false Index:0}" {
		t.Fatal(got)
	}
	s.send("0 selections 4 2 16\n3 1 3 5\n1 2 1 2\n9 linked 4 7 3 2\n")
	if got := m.next(t); got != "selections 4 [{{3 1} {3 5}} {{1 2} {1 2}}]" {
		t.Fatal(got)
	}
	s.answer(9, "2\n3 2 3 5\n4 3 4 6\nword 1\n-")
	s.send("0 bye\n")
	if err := <-s.done; err != nil {
		t.Fatal(err)
	}
}

func TestWithoutTheFeaturesTheEditorModelIsRefused(t *testing.T) {
	m := &modelProbe{got: make(chan string, 16)}
	s := startHost(t, nil, m)
	s.expect("hello 1")
	s.send("0 ready -\n")
	for _, want := range []string{"unknown", "unknown", "unknown", "unknown", "diff 0 rhun: unknown: diff was not agreed", "unknown",
		"rhun: unknown: file operations need the workspace-edits and file-operations features"} {
		if got := m.next(t); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	// a linked request without the feature is unknown; a selections event's body is still read
	s.send("0 selections 4 1 8\n1 1 1 1\n9 linked 4 7 3 2\n")
	if got := s.line(); !strings.HasPrefix(got, "error 9 unknown") {
		t.Fatal(got)
	}
	if got := m.next(t); got != "selections 4 [{{1 1} {1 1}}]" {
		t.Fatal(got)
	}
	s.send("0 bye\n")
	<-s.done
}

func TestEditorModelBodiesAndEvents(t *testing.T) {
	if _, err := EncodeSelections(make([]Selection, 513)); err == nil {
		t.Fatal("513 selections")
	}
	if b := EncodeShowLocations(strings.Repeat("é", 150), nil); len(b) != 203 {
		t.Fatalf("title cut: %d", len(b))
	}
	for _, bad := range []Event{
		{Name: "selections", Args: []string{"4", "3", "16"}, Body: []byte("3 1 3 5\n1 2 1 2\n")},
		{Name: "selections", Args: []string{"4", "1", "6"}, Body: []byte("3 1 3\n")},
		{Name: "selections", Args: []string{"4", "0", "0"}},
	} {
		if _, _, ok := ParseSelections(bad); ok {
			t.Fatalf("%+v parsed", bad)
		}
	}
	w := "-_\n-é:"
	if b := (Linked{Ranges: []Range{rg(1, 2, 1, 5), rg(3, 3, 3, 6)}, Word: &w}).Encode(); string(b) != "2\n1 2 1 5\n3 3 3 6\nword 3\n-_:" {
		t.Fatalf("%q", b)
	}
	for _, l := range []Linked{{}, {Ranges: []Range{rg(1, 2, 1, 5)}}, {Ranges: make([]Range, 65)}} {
		if b := l.Encode(); string(b) != "0\n" {
			t.Fatalf("%q", b)
		}
	}
	if _, ok := parseLinkedRequest([]string{"3", "9", "0", "4"}); ok {
		t.Fatal("line 0")
	}
	if r, ok := parseLinkedRequest([]string{"3", "9", "2", "4"}); !ok || r != (LinkedRequest{3, 9, Pos{2, 4}}) {
		t.Fatalf("%+v", r)
	}
}
