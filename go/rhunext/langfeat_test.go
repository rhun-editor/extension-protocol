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

func (m *moreProbe) SelectionRanges(ext *Ext, req SelectionRangesRequest) [][]Range {
	out := make([][]Range, 0, len(req.Positions))
	for _, p := range req.Positions {
		switch p.Line {
		case 1: // equal ranges skipped, the chain ends at one not containing the one before
			out = append(out, []Range{rg(1, 5, 1, 8), rg(1, 5, 1, 8), rg(1, 1, 1, 20), rg(1, 2, 2, 1), rg(1, 1, 9, 1)})
		case 2: // nothing for this one
			out = append(out, nil)
		default:
			out = append(out, []Range{rg(p.Line, 1, p.Line, 9), rg(p.Line, 9, p.Line, 1)})
		}
	}
	return out
}

func item(name string, data string) HierarchyItem {
	return HierarchyItem{Kind: SymbolFunction, Name: name, Detail: "pkg", Target: Target{Path: "/p/a.go"},
		Range: rg(3, 1, 9, 2), Selection: rg(3, 6, 3, 6+len(name)), Data: []byte(data)}
}

func itemRec(kind, rng, sel, path, name, detail, data string, sites ...string) string {
	head := fmt.Sprintf("%s %s %s %d %d %d %d", kind, rng, sel, len(path), len(name), len(detail), len(data))
	if len(sites) > 0 {
		head += fmt.Sprintf(" %d", len(sites))
	}
	s := head + "\n" + path + name + detail + data
	for _, r := range sites {
		s += r + "\n"
	}
	return s
}

func (m *moreProbe) CallHierarchy(ext *Ext, req HierarchyRequest) []HierarchyItem {
	bad := item("bad", "")
	bad.Range = rg(9, 1, 3, 1)
	sel := item("outside", "o")
	sel.Selection = rg(1, 1, 1, 2)
	return []HierarchyItem{item("main", "m1"), {Name: ""}, bad, sel}
}

func (m *moreProbe) CallsIn(ext *Ext, data []byte, out *Stream) []Call {
	if err := out.PartCalls([]Call{{Item: item("caller", "c1"), Sites: []Range{rg(4, 2, 4, 6)}}}); err != nil {
		return []Call{{Item: item("nostream", ""), Sites: []Range{rg(1, 1, 1, 2)}}}
	}
	return []Call{{Item: item("late"+string(data), ""), Sites: []Range{rg(5, 1, 5, 2), rg(6, 2, 6, 1), rg(7, 1, 7, 3)}},
		{Item: item("nosites", "")}}
}

func (m *moreProbe) CallsOut(ext *Ext, data []byte, out *Stream) []Call { return nil }

func (m *moreProbe) TypeHierarchy(ext *Ext, req HierarchyRequest) []HierarchyItem {
	it := item("Shape", "t")
	it.Kind = SymbolInterface
	return []HierarchyItem{it}
}

func (m *moreProbe) Supertypes(ext *Ext, data []byte, out *Stream) []HierarchyItem {
	return []HierarchyItem{item("Base"+string(data), "b")}
}

func (m *moreProbe) Subtypes(ext *Ext, data []byte, out *Stream) []HierarchyItem {
	huge := item("Huge", strings.Repeat("x", MaxHierarchyData+1))
	return []HierarchyItem{huge, item("Sub", "")}
}

func TestSelectionRanges(t *testing.T) {
	s := startHost(t, []string{FeatureSelectionRanges}, &moreProbe{})
	s.expect("hello 1 selection-ranges")
	s.send("0 features selection-ranges\n0 ready -\n")
	s.send("3 selection-ranges 4 7 3 12\n1 6\n2 3\n5 4\n")
	s.answer(3, "2\n1 5 1 8\n1 1 1 20\n1\n2 3 2 3\n1\n5 1 5 9\n")
	s.send("4 selection-ranges 4 7 2 8\n5 4\n2 3\n") // the empty group last: the answer ends before it
	s.answer(4, "1\n5 1 5 9\n")
	s.send("5 selection-ranges 4 7 2 4\n5 4\n") // N does not match the positions
	s.expect("error 5 unknown no such request")
	// the outermost ranges go until the body fits 64 KiB
	at := make([]Pos, 64)
	groups := make([][]Range, 64)
	for i := range groups {
		at[i] = Pos{1000000, 1000}
		for j := 0; j < 64; j++ {
			groups[i] = append(groups[i], rg(1000000-j, 1000-j, 1000000+j, 1000+j))
		}
	}
	b := EncodeSelectionRanges(at, groups)
	if len(b) > MaxSelectionBody || !strings.HasPrefix(string(b), "42\n1000000 1000 1000000 1000\n") {
		t.Fatalf("%d bytes: %q", len(b), b[:40])
	}
}

func TestHierarchy(t *testing.T) {
	s := startHost(t, []string{FeatureHierarchy, FeatureQueryStream}, &moreProbe{})
	s.expect("hello 1 hierarchy query-stream")
	s.send("0 features hierarchy query-stream\n0 ready -\n")
	s.send("3 call-hierarchy 4 7 3 7 invoked -\n")
	s.answer(3, "2\n"+itemRec("function", "3 1 9 2", "3 6 3 10", "/p/a.go", "main", "pkg", "m1")+
		itemRec("function", "3 1 9 2", "3 1 9 2", "/p/a.go", "outside", "pkg", "o"))
	s.send("4 calls-in 2\nm1")
	part := "1\n" + itemRec("function", "3 1 9 2", "3 6 3 12", "/p/a.go", "caller", "pkg", "c1", "4 2 4 6")
	s.expect(fmt.Sprintf("1 query-part 4 %d", len(part)))
	if b := s.body(len(part)); b != part {
		t.Fatalf("part %q", b)
	}
	s.send("ok 1\n")
	s.answer(4, "1\n"+itemRec("function", "3 1 9 2", "3 6 3 12", "/p/a.go", "latem1", "pkg", "", "5 1 5 2", "7 1 7 3"))
	s.send("5 calls-out 1\nx")
	s.answer(5, "0\n")
	s.send("6 type-hierarchy 4 7 3 7 invoked -\n")
	s.answer(6, "1\n"+itemRec("interface", "3 1 9 2", "3 6 3 11", "/p/a.go", "Shape", "pkg", "t"))
	s.send("7 supertypes 1\nt")
	s.answer(7, "1\n"+itemRec("function", "3 1 9 2", "3 6 3 11", "/p/a.go", "Baset", "pkg", "b"))
	s.send("8 subtypes 1\nt")
	s.answer(8, "1\n"+itemRec("function", "3 1 9 2", "3 6 3 9", "/p/a.go", "Sub", "pkg", ""))
	// a part answered cancelled: the handler returns what it has, rhun drops it
	s.send("9 calls-in 2\nm1")
	s.line()
	s.body(len(part))
	s.send("error 2 cancelled\n")
	s.answer(9, "1\n"+itemRec("function", "3 1 9 2", "3 6 3 14", "/p/a.go", "nostream", "pkg", "", "1 1 1 2"))
	// roots: at most 64
	roots := make([]HierarchyItem, 70)
	for i := range roots {
		roots[i] = item("r", "")
	}
	if b := EncodeHierarchyRoots(roots); !strings.HasPrefix(string(b), "64\n") {
		t.Fatalf("roots %q", b[:8])
	}
	sites := make([]Range, MaxCallSites+5)
	for i := range sites {
		sites[i] = rg(1, 1, 1, 2)
	}
	if b := EncodeCalls([]Call{{Item: item("c", ""), Sites: sites}}); !strings.Contains(string(b), " 1000\n") {
		t.Fatalf("sites %q", b[:80])
	}
}
