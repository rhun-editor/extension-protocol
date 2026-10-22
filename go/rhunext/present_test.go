package rhunext

import (
	"fmt"
	"strings"
	"testing"
)

type presentProbe struct {
	events chan Event
}

func (p *presentProbe) Ready(ext *Ext, project string) {
	ext.DecorationType("blame", "after muted -\ngrow none\n")
	ext.Decorate(4, 7, "blame", 0, 0, []DecorationRange{
		{Range: rg(2, 1, 2, 1), After: "you,\n2h", Tip: "who", Command: &CommandRef{Name: "show", Args: []byte("x")}},
		{Range: rg(3, 5, 3, 1)}, // backwards: left out
	})
	ext.SemanticLegend("ra", []string{"function", "variable", "keyword=strike"}, []string{"mutable=underline"})
	ext.Diagnostics(4, 7, []Diagnostic{{Range: rg(1, 1, 1, 2), Severity: SevHint, Message: "unused",
		Tags: []DiagnosticTag{TagUnnecessary, TagUnnecessary, TagDeprecated}}})
}
func (p *presentProbe) Event(ext *Ext, ev Event)              { p.events <- ev }
func (p *presentProbe) Run(ext *Ext, command string, doc Doc) {}

func (p *presentProbe) FoldingRanges(ext *Ext, req DocRequest) []FoldingRange {
	return []FoldingRange{{Start: 1, End: 9}, {Start: 3, End: 5, Kind: "region", Text: "consts\nmore"},
		{Start: 4, End: 4}, {Start: 0, End: 2}}
}

func (p *presentProbe) SemanticTokens(ext *Ext, req SemanticTokensRequest) *SemanticTokens {
	if req.Prev == "r1" {
		return &SemanticTokens{Result: "r2", Legend: "ra", Delta: true, Edits: []SemanticEdit{{Start: 3, Del: 1, Data: []uint64{1}}}}
	}
	if req.Range {
		return &SemanticTokens{Legend: "ra", Data: []uint64{uint64(req.First - 1), 0, 2, 0, 0}}
	}
	return &SemanticTokens{Result: "r1", Legend: "ra", Data: []uint64{0, 3, 4, 0, 0, 1, 8, 1, 1, 1}}
}

func (p *presentProbe) InlayHints(ext *Ext, req LinesRequest) []InlayHint {
	return []InlayHint{
		{At: Pos{2, 10}, Parts: []InlayHintPart{{Label: ": i32", Tip: "the type"}}, Edits: []InlayEdit{{Range: rg(2, 10, 2, 10), Text: ": i32"}}},
		{At: Pos{4, 14}, Before: true, Parts: []InlayHintPart{{Label: "fmt:", Command: &CommandRef{Name: "a"}},
			{Label: "x", Goto: &InlayGoto{At: Pos{1, 4}}}}},
		{At: Pos{5, 1}, Parts: []InlayHintPart{{Label: ""}}}, // nothing to show
	}
}

func (p *presentProbe) CodeLenses(ext *Ext, req LinesRequest) []CodeLens {
	return []CodeLens{{Line: 1, Title: "Run", Command: &CommandRef{Name: "run"}}, {Line: 1, Title: "2 refs", Tip: "used"},
		{Line: 3}, {Line: 0, Title: "nowhere"}}
}

func TestPresentation(t *testing.T) {
	p := &presentProbe{events: make(chan Event, 8)}
	s := startHost(t, []string{FeatureDecorations, FeatureDiagnosticTags, FeatureFoldingRanges, FeatureSemanticTokens,
		FeatureInlayHints, FeatureCodeLens}, p)
	s.expect("hello 1 decorations diagnostic-tags folding-ranges semantic-tokens inlay-hints code-lens")
	s.send("0 features decorations diagnostic-tags folding-ranges semantic-tokens inlay-hints code-lens\n0 ready -\n")
	s.expect("1 decoration-type blame 24")
	if b := s.body(24); b != "after muted -\ngrow none\n" {
		t.Fatalf("type %q", b)
	}
	s.send("ok 1\n")
	rec := "2 1 2 1 0 7 3 16\nyou, 2hwhocommand show 1\nx"
	s.expect(fmt.Sprintf("2 decorate 4 7 blame 0 0 1 %d", len(rec)))
	if b := s.body(len(rec)); b != rec {
		t.Fatalf("decorate %q", b)
	}
	s.send("ok 2\n")
	legend := "function variable keyword=strike\nmutable=underline"
	s.expect(fmt.Sprintf("3 semantic-legend ra %d", len(legend)))
	if b := s.body(len(legend)); b != legend {
		t.Fatalf("legend %q", b)
	}
	s.send("ok 3\n")
	diag := "1 1 1 2 hint+unnecessary+deprecated unused\n"
	s.expect(fmt.Sprintf("4 diagnostics 4 7 1 %d", len(diag)))
	if b := s.body(len(diag)); b != diag {
		t.Fatalf("diagnostics %q", b)
	}
	s.send("ok 4\n")
	s.send("0 visible 4 1 40\n")
	ev := <-p.events
	for ev.Name != "visible" {
		ev = <-p.events
	}
	if v, ok := ParseVisible(ev); !ok || v != (Visible{4, 1, 40}) {
		t.Fatalf("visible %v %v", v, ok)
	}
	s.send("5 folding-ranges 4 7\n")
	s.answer(5, "2\n1 9 - 0\n3 5 region 11\nconsts more")
	s.send("6 semantic-tokens 4 7 -\n")
	s.answer(6, "r1 ra full 2\n0 3 4 0 0 1 8 1 1 1")
	s.send("7 semantic-tokens 4 8 r1\n")
	s.answer(7, "r2 ra delta 1\n3 1 1\n1\n")
	s.send("8 semantic-tokens-range 4 8 10 20\n")
	s.answer(8, "- ra range 1\n9 0 2 0 0")
	s.send("9 inlay-hints 4 8 1 40\n")
	s.answer(9, "2\n2 10 after 1 22\n5 8 0\n: i32the typeedit 2 10 2 10 5\n: i32"+
		"4 14 before 2 0\n4 0 12\nfmt:command a 0\n1 0 11\nxgoto 1 4 0\n")
	s.send("10 code-lens 4 8 1 40\n")
	s.answer(10, "3\n1 3 14\nRuncommand run 0\n1 6 10\n2 refstip 4\nused3 0 0\n")
	s.send("11 code-lens 4 8 9\n")
	s.expect("error 11 unknown no such request")
}

func (p *presentProbe) Highlights(ext *Ext, req PosRequest) []Highlight {
	return []Highlight{{Range: rg(2, 9, 2, 10), Write: true}, {Range: rg(3, 13, 3, 14), Read: true}, {Range: rg(4, 2, 4, 1)}}
}

func (p *presentProbe) Colors(ext *Ext, req LinesRequest) []DocumentColor {
	return []DocumentColor{{Range: rg(1, 9, 1, 17), R: 255, A: 255}}
}

func (p *presentProbe) ColorEdit(ext *Ext, req ColorEditRequest) (WorkspaceEdit, error) {
	return WorkspaceEdit{Files: []FileEdit{{Target: Target{Doc: req.Doc}, Version: req.Version,
		Edits: []TextEdit{{Range: req.Range, Text: fmt.Sprintf("0x%02x%02x%02x", req.R, req.G, req.B)}}}}}, nil
}

func (p *presentProbe) InlineComplete(ext *Ext, req InlineRequest) []InlineItem {
	return []InlineItem{{Range: Range{req.At, req.At}, Text: " // one", Command: &CommandRef{Name: "a"}}, {Range: rg(1, 1, 1, 1)}}
}

func TestPresentationMore(t *testing.T) {
	p := &presentProbe{events: make(chan Event, 8)}
	s := startHost(t, []string{FeatureHighlights, FeatureColors, FeatureInlineComplete}, &morePresent{p})
	s.expect("hello 1 document-highlights document-colors inline-complete")
	s.send("0 features document-highlights document-colors inline-complete\n0 ready -\n")
	s.send("5 highlights 4 7 2 9\n")
	s.answer(5, "2\n2 9 2 10 write\n3 13 3 14 read\n")
	s.send("6 colors 4 7 1 40\n")
	s.answer(6, "1\n1 9 1 17 ff0000ff\n")
	s.send("7 color-edit 4 7 1 9 1 17 0000ffff\n")
	s.answer(7, "1\n2 7 1\n#41 9 1 17 8\n0x0000ff")
	s.send("8 inline-complete 4 7 2 15 invoked\n")
	s.answer(8, "1\n2 15 2 15 7 12\n // onecommand a 0\n")
}

// morePresent is presentProbe without its Ready requests
type morePresent struct{ *presentProbe }

func (m *morePresent) Ready(ext *Ext, project string) {}

func TestPresentationEncoders(t *testing.T) {
	if b := (&SemanticTokens{Legend: "bad name"}).Encode(false); b != nil {
		t.Fatalf("bad legend %q", b)
	}
	if b := (&SemanticTokens{Legend: "ra", Data: []uint64{1, 2, 3}}).Encode(false); b != nil {
		t.Fatalf("partial token %q", b)
	}
	// a modifier past bit 31 (the legend takes 64), in a full answer and in a delta's edit
	high := uint64(1)<<40 | 1<<63
	if b := string((&SemanticTokens{Legend: "ra", Data: []uint64{0, 3, 4, 0, high}}).Encode(false)); b != "- ra full 1\n0 3 4 0 9223373136366403584" {
		t.Fatalf("64-bit MODS %q", b)
	}
	d := &SemanticTokens{Result: "r2", Legend: "ra", Delta: true, Edits: []SemanticEdit{{Start: 4, Del: 1, Data: []uint64{1 << 32}}}}
	if b := string(d.Encode(false)); b != "r2 ra delta 1\n4 1 1\n4294967296\n" {
		t.Fatalf("64-bit edit %q", b)
	}
	long := strings.Repeat("x", 200)
	b := EncodeInlayHints([]InlayHint{{At: Pos{1, 1}, Parts: []InlayHintPart{{Label: long}, {Label: "y"}}}})
	if !strings.Contains(string(b), "120 0 0\n"+strings.Repeat("x", 120)) || strings.Contains(string(b), "1 0 0\ny") {
		t.Fatalf("label cut %q", b)
	}
	if b := EncodeCodeLenses([]CodeLens{{Line: 2, Title: strings.Repeat("é", 150)}}); !strings.Contains(string(b), "2 200 0\n") {
		t.Fatalf("title cut %q", b[:20])
	}
	if !ValidDecorationName("git-blame_2") || ValidDecorationName("Blame") || ValidDecorationName("") {
		t.Fatal("decoration names")
	}
}
