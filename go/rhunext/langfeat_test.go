package rhunext

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// body reads n bytes rhun's side was sent after a line.
func (s *host) body(n int) string {
	s.t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(s.br, b); err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

func diagRec(rng, sev, msg, extra string) string {
	return fmt.Sprintf("%s %s %d %d\n%s%s", rng, sev, len(msg), len(extra), msg, extra)
}

func xrec(name, bytes string) string { return fmt.Sprintf("%s %d\n%s", name, len(bytes), bytes) }

func TestFileDiagnosticsRecords(t *testing.T) {
	ds := []Diagnostic{
		{Range: rg(2, 9, 2, 14), Severity: SevWarning, Message: "unused\ntotal", ID: "d1", Code: "W1", Source: "lint",
			Href: "https://example.test/W1", Tags: []DiagnosticTag{TagUnnecessary, TagUnnecessary, "bogus", TagDeprecated},
			Related: []RelatedInfo{{Target: Target{Path: "/p/a.go"}, Range: rg(1, 1, 1, 4), Message: "declared\nhere"},
				{Target: Target{Path: "relative.go"}, Message: "left out"}, {Target: Target{Doc: 3}, Range: rg(0, 0, 0, 0)}}},
		{Message: "zero", ID: "d1"},
		{Severity: SevError},
		{Severity: "fatal", Message: "x"},
		{Range: rg(3, 5, 3, 1), Severity: SevHint, Message: "back", ID: "bad id", Href: "ftp://x", Code: strings.Repeat("c", 150),
			Source: "a\nb"},
	}
	got, n := EncodeFileDiagnostics(ds)
	extra := xrec("id", "d1") + xrec("code", "W1") + xrec("source", "lint") + xrec("href", "https://example.test/W1") +
		"tag unnecessary 0\ntag deprecated 0\n" +
		"related 1 1 1 4 7 13\n/p/a.godeclared here" + "related 1 1 1 1 2 0\n#3"
	want := diagRec("2 9 2 14", "warning", "unused\ntotal", extra) +
		diagRec("1 1 1 1", "error", "zero", "") +
		diagRec("3 5 3 5", "hint", "back", xrec("code", strings.Repeat("c", 100))+xrec("source", "a b"))
	if n != 3 || string(got) != want {
		t.Fatalf("%d records:\ngot  %q\nwant %q", n, got, want)
	}
	// caps: 2000 records, the body within 4 MiB (the target included), messages of 4000 bytes
	many := make([]Diagnostic, 2001)
	for i := range many {
		many[i] = Diagnostic{Range: rg(1, 1, 1, 2), Message: "m"}
	}
	if _, n := EncodeFileDiagnostics(many); n != MaxFileDiagnostics {
		t.Fatalf("%d records", n)
	}
	for i := range many {
		many[i].Message = strings.Repeat("é", 2500)
	}
	b, n := EncodeFileDiagnostics(many)
	if n >= MaxFileDiagnostics || len(b)+MaxPath > MaxFileDiagnosticsBody || !strings.HasPrefix(string(b), "1 1 1 2 error 4000 0\n") {
		t.Fatalf("%d records in %d bytes", n, len(b))
	}
}

func TestFileDiagnosticsRequests(t *testing.T) {
	got := make(chan error, 8)
	h := &readyHook{fn: func(ext *Ext) {
		ds := []Diagnostic{{Range: rg(1, 4, 1, 9), Severity: SevError, Message: "bad\nname", ID: "e1"}}
		got <- ext.FileDiagnostics(Target{Doc: 1}, 0, ds)
		got <- ext.FileDiagnostics(Target{Path: "/p/other.rs"}, OnDisk, ds)
		got <- ext.FileDiagnostics(Target{Path: "/p/other.rs"}, 3, nil)
		got <- ext.FileDiagnostics(Target{Path: "other.rs"}, OnDisk, ds)
		got <- ext.FileDiagnostics(Target{Doc: 1}, OnDisk, ds)
		got <- ext.ResetDiagnostics()
	}}
	s := startHost(t, []string{FeatureFileDiagnostics}, h)
	s.expect("hello 1 file-diagnostics")
	s.send("0 features file-diagnostics\n0 ready -\n")
	rec := diagRec("1 4 1 9", "error", "bad\nname", xrec("id", "e1"))
	s.expect(fmt.Sprintf("1 file-diagnostics 0 1 2 %d", 2+len(rec)))
	if b := s.body(2 + len(rec)); b != "#1"+rec {
		t.Fatalf("body %q", b)
	}
	s.send("error 1 stale 4\n")
	var re *Error
	if err := <-got; !errors.As(err, &re) || re.Code != "stale" || re.Text != "4" {
		t.Fatalf("stale: %v", err)
	}
	s.expect(fmt.Sprintf("2 file-diagnostics - 1 11 %d", 11+len(rec)))
	if b := s.body(11 + len(rec)); b != "/p/other.rs"+rec {
		t.Fatalf("body %q", b)
	}
	s.send("ok 2\n")
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	s.expect("3 file-diagnostics - 0 11 11") // a path's version is not sent; no records clear the set
	s.body(11)
	s.send("ok 3\n")
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	for _, what := range []string{"relative path", "document without a version"} {
		if err := <-got; !errors.As(err, &re) || re.Code != "syntax" {
			t.Fatalf("%s: %v", what, err)
		}
	}
	s.expect("4 file-diagnostics-reset")
	s.send("ok 4\n")
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	s2 := startHost(t, nil, &readyHook{fn: func(ext *Ext) {
		got <- ext.FileDiagnostics(Target{Doc: 1}, 0, nil)
		got <- ext.ResetDiagnostics()
	}})
	s2.expect("hello 1")
	s2.send("0 ready -\n")
	for i := 0; i < 2; i++ {
		if err := <-got; !errors.As(err, &re) || re.Code != "unknown" {
			t.Fatalf("without the feature: %v", err)
		}
	}
}

type moreProbe struct{}

func (m *moreProbe) Ready(ext *Ext, project string)        {}
func (m *moreProbe) Event(ext *Ext, ev Event)              {}
func (m *moreProbe) Run(ext *Ext, command string, doc Doc) {}

func (m *moreProbe) Hover(ext *Ext, req HoverRequest) *Hover {
	switch {
	case req.At.Line == 9:
		return nil
	case req.At.Line == 8:
		return &Hover{Range: rg(8, 5, 8, 1), Text: strings.Repeat("é", 40000)}
	case req.Context == nil:
		return &Hover{Text: "no context"}
	}
	return &Hover{Range: rg(2, 1, 2, 9), Text: "fn " + req.Kind}
}

func TestHoverQuery(t *testing.T) {
	s := startHost(t, []string{FeatureHoverQuery}, &moreProbe{})
	s.expect("hello 1 hover-query")
	s.send("0 features hover-query\n0 ready -\n")
	s.send("3 hover-query 4 7 2 5 pointer -\n")
	s.answer(3, "2 1 2 9\nfn pointer")
	s.send("4 hover-query 4 7 9 1 invoked -\n")
	s.answer(4, "")
	s.send("5 hover-query 4 7 8 1 invoked -\n")
	head := "0 0 0 0\n"
	n := (MaxHoverBody - len(head)) / 2 * 2 // cut at a character
	s.answer(5, head+strings.Repeat("é", n/2))
	s2 := startHost(t, nil, &moreProbe{})
	s2.expect("hello 1")
	s2.send("0 ready -\n6 hover-query 4 7 2 5 pointer -\n")
	s2.expect("error 6 unknown no such request")
}
