package rhunext

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestPreparedSnippetGoldenEncodingAndEmptyInsertion(t *testing.T) {
	s := PreparedSnippet{Text: "x x", Fields: []Field{{1, 0, 1, 0}, {1, 2, 3, 0}, {0, 3, 3, 0}}}
	body, err := s.Encode()
	want := "0 0 0 0 snippet 0 3 0 48\nx xfield 1 0 1 0 0\nfield 1 2 3 0 0\nfield 0 3 3 0 0\n"
	if err != nil || string(body) != want {
		t.Fatalf("%q %v", body, err)
	}
	for _, fields := range [][]Field{nil, {{1, 0, 0, 0}}, {{0, 0, 0, 0}}} {
		it := (CompletionItem{Label: "label"}).WithSnippet(PreparedSnippet{Fields: fields})
		out := string((CompletionList{Items: []CompletionItem{it}}).Encode())
		if strings.Contains(out, "labellabel") || !strings.Contains(out, "text 5 0 0 ") {
			t.Fatalf("empty prepared text fell back to its label: %q", out)
		}
	}
	ordinary := string((CompletionList{Items: []CompletionItem{{Label: "label"}}}).Encode())
	if !strings.HasSuffix(ordinary, "labellabel") {
		t.Fatal("ordinary label fallback changed", ordinary)
	}
}

func TestInsertSnippetFramesBodyAndValidatesCreditedVersion(t *testing.T) {
	for _, reply := range []string{"10", "8", "+10", "9223372036854775808", "10 extra"} {
		wire := make(wireWriter, 4)
		e := testConnection(t, wire)
		e.agreed = map[string]bool{FeatureSnippets: true}
		done := make(chan error, 1)
		go func() {
			version, err := e.InsertSnippet(7, 9, Range{Pos{1, 1}, Pos{1, 1}}, PreparedSnippet{})
			if err == nil && version != 10 {
				err = fmt.Errorf("unexpected version %d", version)
			}
			done <- err
		}()
		body := "0 0 0 0 snippet 0 0 0 0\n"
		if got := <-wire; got != fmt.Sprintf("1 snippet 7 9 1 1 1 1 %d\n", len(body)) {
			t.Fatal(got)
		}
		if got := <-wire; got != body {
			t.Fatal(got)
		}
		if _, err := e.readMessage(bufio.NewReader(strings.NewReader("ok 1 " + reply + "\n"))); err != nil {
			t.Fatal(err)
		}
		if err := <-done; (err == nil) != (reply == "10") {
			t.Fatalf("reply %q: %v", reply, err)
		}
	}
}

func TestPreparedSnippetNestedDefaultsAndIndependentFields(t *testing.T) {
	for _, s := range []PreparedSnippet{
		{},
		{Text: "x x", Fields: []Field{{1, 0, 1, 0}, {2, 0, 1, 1}, {2, 2, 3, 0}, {0, 3, 3, 0}}},
		{Text: "😀", Fields: []Field{{1, 0, 4, 0}, {2, 0, 4, 1}, {0, 4, 4, 2}}},
		{Fields: []Field{{1, 0, 0, 0}, {2, 0, 0, 1}, {3, 0, 0, 0}, {0, 0, 0, 1}}},
		{Text: "x x", Fields: []Field{{2, 3, 3, 0}, {1, 0, 1, 0}, {1, 2, 3, 0}}},
	} {
		if err := s.Validate(); err != nil {
			t.Fatalf("%+v: %v", s, err)
		}
	}
}

func TestPreparedSnippetRejectsInvalidGraphsAndCoordinates(t *testing.T) {
	cases := []PreparedSnippet{
		{Text: string([]byte{0xff})},
		{Text: "😀", Fields: []Field{{1, 1, 4, 0}}},
		{Text: "x", Fields: []Field{{1, -1, 1, 0}}},
		{Text: "x", Fields: []Field{{1, 0, 2, 0}}},
		{Text: "x", Fields: []Field{{1, 1, 0, 0}}},
		{Text: "x", Fields: []Field{{-1, 0, 1, 0}}},
		{Fields: []Field{{0, 0, 0, 0}, {0, 0, 0, 0}}},
		{Text: "x", Fields: []Field{{1, 0, 1, 1}}},
		{Text: "x", Fields: []Field{{1, 0, 1, -1}}},
		{Text: "x", Fields: []Field{{1, 0, 1, 2}, {2, 0, 1, 0}}},
		{Text: "xx", Fields: []Field{{1, 0, 1, 0}, {2, 1, 2, 1}}},
		{Text: "xx", Fields: []Field{{1, 0, 1, 0}, {1, 1, 2, 0}, {2, 1, 2, 2}}},
		{Text: "xy", Fields: []Field{{1, 0, 1, 0}, {1, 1, 2, 0}}},
		{Text: "xx", Fields: []Field{{1, 0, 2, 0}, {2, 1, 1, 0}}},
		{Text: "xx", Fields: []Field{{1, 0, 2, 0}, {2, 0, 2, 0}}},
		{Text: "xxx", Fields: []Field{{1, 0, 2, 0}, {2, 1, 3, 0}}},
		// Record parents are acyclic, but mirror propagation between groups is not.
		{Text: "x x", Fields: []Field{{1, 0, 1, 0}, {2, 2, 3, 0}, {2, 0, 1, 1}, {1, 2, 3, 2}}},
	}
	if strconv.IntSize == 64 {
		group := uint64(0x80000000)
		cases = append(cases, PreparedSnippet{Text: "x", Fields: []Field{{int(group), 0, 1, 0}}})
	}
	for _, s := range cases {
		if err := s.Validate(); err == nil {
			t.Fatalf("accepted %+v", s)
		}
		if _, err := s.Encode(); err == nil {
			t.Fatal("encoded invalid snippet")
		}
	}
}

func TestPreparedSnippetBoundedFieldsGroupsDepthAndBody(t *testing.T) {
	s := PreparedSnippet{}
	for i := 0; i < MaxSnippetFields; i++ {
		s.Fields = append(s.Fields, Field{Group: 1})
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Fields = append(s.Fields, Field{Group: 1})
	if s.Validate() == nil {
		t.Fatal("accepted too many fields")
	}
	s.Fields = nil
	for i := 0; i < MaxSnippetGroups; i++ {
		s.Fields = append(s.Fields, Field{Group: i + 1})
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Fields = append(s.Fields, Field{Group: MaxSnippetGroups + 1})
	if s.Validate() == nil {
		t.Fatal("accepted too many groups")
	}
	s.Fields = nil
	for i := 0; i < MaxSnippetDepth; i++ {
		s.Fields = append(s.Fields, Field{Group: i + 1, Parent: i})
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Fields = append(s.Fields, Field{Group: MaxSnippetDepth + 1, Parent: MaxSnippetDepth})
	if s.Validate() == nil {
		t.Fatal("accepted excessive nesting")
	}
	s = PreparedSnippet{Text: strings.Repeat("x", MaxSnippetBody)}
	if s.Validate() == nil {
		t.Fatal("ignored framing overhead")
	}
}

func TestCompletionSnippetCopiesFieldsAndInvalidMetadataFallsBack(t *testing.T) {
	s := PreparedSnippet{Text: "x", Fields: []Field{{1, 0, 1, 0}}}
	it := (CompletionItem{Label: "label"}).WithSnippet(s)
	s.Fields[0].End = 100
	out := string((CompletionList{Items: []CompletionItem{it}}).Encode())
	if !strings.HasSuffix(out, "labelxfield 1 0 1 0 0\n") {
		t.Fatal("prepared fields alias caller storage", out)
	}
	it = (CompletionItem{Label: "label"}).WithSnippet(s)
	out = string((CompletionList{Items: []CompletionItem{it}}).Encode())
	if !strings.HasSuffix(out, "labelx") || strings.Contains(out, "field ") {
		t.Fatal("invalid metadata was partially emitted", out)
	}
}

func TestInsertSnippetGuardsDoNotSendInvalidRequests(t *testing.T) {
	e := testConnection(t, io.Discard)
	span := Range{Start: Pos{1, 1}, End: Pos{1, 1}}
	if _, err := e.InsertSnippet(1, 0, span, PreparedSnippet{}); err == nil {
		t.Fatal("unnegotiated request")
	}
	e.agreed = map[string]bool{FeatureSnippets: true}
	for _, target := range []struct {
		doc     Doc
		version int64
		span    Range
	}{{0, 0, span}, {1, -1, span}, {1, 0, Range{}}, {1, 0, Range{Pos{2, 1}, Pos{1, 1}}}} {
		if _, err := e.InsertSnippet(target.doc, target.version, target.span, PreparedSnippet{}); err == nil {
			t.Fatal("invalid target sent")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.InsertSnippetContext(ctx, 1, 0, span, PreparedSnippet{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if pendingCount(e) != 0 || e.nextID.Load() != 0 {
		t.Fatal("guarded requests allocated wire IDs")
	}
}

func TestSnippetFinalSelectionIsNonOwningAndBounded(t *testing.T) {
	for _, s := range []PreparedSnippet{
		{Text: "result", Fields: []Field{{0, 0, 6, 0}}},
		{Text: "xxy", Fields: []Field{{1, 0, 1, 0}, {1, 1, 2, 0}, {0, 1, 3, 0}}},
		{Text: "abc", Fields: []Field{{1, 0, 3, 0}, {0, 1, 2, 1}}},
		{Text: "ab", Fields: []Field{{0, 0, 2, 0}, {1, 0, 1, 0}, {2, 1, 2, 0}}},
	} {
		if err := s.Validate(); err != nil {
			t.Fatal(s, err)
		}
	}
	for _, s := range []PreparedSnippet{
		{Text: "ab", Fields: []Field{{0, 0, 2, 0}, {1, 0, 1, 1}}},
		{Text: "ab", Fields: []Field{{0, 0, 1, 0}, {0, 1, 2, 0}}},
		{Text: "é", Fields: []Field{{0, 1, 2, 0}}},
		{Text: "ab", Fields: []Field{{0, 2, 1, 0}}},
		{Text: "ab", Fields: []Field{{0, 0, 3, 0}}},
		{Text: "ab", Fields: []Field{{1, 0, 1, 0}, {0, 0, 2, 1}}},
	} {
		if s.Validate() == nil {
			t.Fatal("invalid final selection", s)
		}
	}
	body, err := (PreparedSnippet{Text: "result", Fields: []Field{{0, 0, 6, 0}}}).Encode()
	if err != nil || string(body) != "0 0 0 0 snippet 0 6 0 16\nresultfield 0 0 6 0 0\n" {
		t.Fatal(string(body), err)
	}
}
