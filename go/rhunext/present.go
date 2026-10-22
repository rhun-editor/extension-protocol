package rhunext

// Protocol 1.1's editor presentation (v1.1/v1.1.md, "Feature: decorations", "Feature:
// diagnostic-tags" and "Presentation providers"): an extension styles ranges of open documents
// (DecorationType, Decorate) and hears which lines are shown (ParseVisible); providers answer
// rhun's folding-ranges, semantic-tokens, inlay-hints and code-lens requests. rhun asks about the
// active document only, by itself, and withdraws a request when a newer one replaces it.

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// The presentation features.
const (
	FeatureDecorations    = "decorations"
	FeatureDiagnosticTags = "diagnostic-tags"
	FeatureFoldingRanges  = "folding-ranges"
	FeatureSemanticTokens = "semantic-tokens"
	FeatureInlayHints     = "inlay-hints"
	FeatureCodeLens       = "code-lens"
	FeatureHighlights     = "document-highlights"
	FeatureColors         = "document-colors"
	FeatureInlineComplete = "inline-complete"
)

// ---- decorations

// Decoration limits (v1.1.md, Feature decorations: Limits).
const (
	MaxDecorationTypeBody = 4 << 10
	MaxDecorations        = 20000
	MaxDecorationText     = 200
	MaxDecorationTip      = 1000
	MaxDecorationExtra    = 9000
)

// DecorationRange is one range of a decorate request: Before and After are virtual text (one line
// each, shown when the type has before or after), Tip shows while the pointer rests on the range
// and Command runs when its virtual text is clicked.
type DecorationRange struct {
	Range         Range
	Before, After string
	Tip           string
	Command       *CommandRef
}

// ValidDecorationName reports whether s names a decoration type: 1 to 40 of [a-z0-9_-].
func ValidDecorationName(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// DecorationType registers this extension's type name, or replaces its style. Body is "KEY ARGS"
// lines (color, background, line-background, border, underline, strike, faded, before, after,
// width, gutter, scrollbar, grow, priority); see the feature's Type body.
func (e *Ext) DecorationType(name, body string) error {
	if !ValidDecorationName(name) {
		return fmt.Errorf("decoration-type: bad name %q", name)
	}
	_, err := e.Request("decoration-type", []byte(body), name, itoa(int64(len(body))))
	return err
}

// EncodeDecorations is a decorate request's body and record count. Texts are made one line and
// cut to their limits; ranges whose end comes before their start are left out, as those past
// MaxDecorations.
func EncodeDecorations(ranges []DecorationRange) ([]byte, int) {
	var b bytes.Buffer
	n := 0
	for _, r := range ranges {
		if !wellFormed(r.Range) || n == MaxDecorations {
			continue
		}
		before := cut(oneLine(r.Before), MaxDecorationText)
		after := cut(oneLine(r.After), MaxDecorationText)
		tip := cut(r.Tip, MaxDecorationTip)
		extra := r.Command.Encode()
		if len(extra) > MaxDecorationExtra {
			extra = nil
		}
		fmt.Fprintf(&b, "%s %d %d %d %d\n", rangeFields(r.Range), len(before), len(after), len(tip), len(extra))
		b.WriteString(before)
		b.WriteString(after)
		b.WriteString(tip)
		b.Write(extra)
		n++
	}
	return b.Bytes(), n
}

// Decorate replaces this extension's ranges of the type name on doc whose first line is in
// from..to (1-based, inclusive; 0 0: all of them). Version may be the current one or one rhun
// still keeps the edits since.
func (e *Ext) Decorate(doc Doc, version int64, name string, from, to int, ranges []DecorationRange) error {
	body, n := EncodeDecorations(ranges)
	_, err := e.Request("decorate", body, itoa(int64(doc)), itoa(version), name, num(from), num(to),
		itoa(int64(n)), itoa(int64(len(body))))
	return err
}

// Visible is the visible event: the 1-based first and last lines the editor shows of the active
// document (Doc 0: none).
type Visible struct {
	Doc         Doc
	First, Last int
}

// ParseVisible reads a visible event ("0 visible DOC L1 L2").
func ParseVisible(ev Event) (Visible, bool) {
	if ev.Name != "visible" || len(ev.Args) != 3 {
		return Visible{}, false
	}
	d, e1 := strconv.ParseInt(ev.Args[0], 10, 64)
	a, e2 := strconv.Atoi(ev.Args[1])
	b, e3 := strconv.Atoi(ev.Args[2])
	if e1 != nil || e2 != nil || e3 != nil || d < 0 || a < 0 || b < 0 {
		return Visible{}, false
	}
	return Visible{Doc(d), a, b}, true
}

// diagnosticSeverity is a diagnostics line's SEVERITY: with diagnostic-tags agreed, followed by
// its tags (warning+deprecated).
func (e *Ext) diagnosticSeverity(d Diagnostic) string {
	s := string(d.Severity)
	if !e.Has(FeatureDiagnosticTags) {
		return s
	}
	seen := map[DiagnosticTag]bool{}
	for _, t := range d.Tags {
		if (t == TagUnnecessary || t == TagDeprecated) && !seen[t] {
			s += "+" + string(t)
			seen[t] = true
		}
	}
	return s
}

// ---- folding ranges

// FoldingRange lets lines Start+1..End hide behind line Start, which then shows Text (one line,
// at most 80 bytes; "" for rhun's own). Kind is "region", "comment", "imports" or "".
type FoldingRange struct {
	Start, End int
	Kind       string
	Text       string
}

// FoldingRangeProvider answers folding-ranges requests (feature folding-ranges, [provides]
// folding): its ranges replace rhun's indentation ranges in the document.
type FoldingRangeProvider interface {
	FoldingRanges(ext *Ext, req DocRequest) []FoldingRange
}

// Folding range limits.
const (
	MaxFoldingRanges    = 50000
	MaxFoldingRangeText = 80
)

// EncodeFoldingRanges is the folding-ranges answer. Ranges with Start not before End, or Start
// below 1, are left out, as those past MaxFoldingRanges.
func EncodeFoldingRanges(ranges []FoldingRange) []byte {
	var recs bytes.Buffer
	n := 0
	for _, r := range ranges {
		if r.Start < 1 || r.End <= r.Start || n == MaxFoldingRanges {
			continue
		}
		kind := r.Kind
		if kind != "region" && kind != "comment" && kind != "imports" {
			kind = "-"
		}
		text := cut(oneLine(r.Text), MaxFoldingRangeText)
		fmt.Fprintf(&recs, "%d %d %s %d\n%s", r.Start, r.End, kind, len(text), text)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// ---- semantic tokens

// Semantic token limits.
const (
	MaxSemanticLegends = 8
	MaxSemanticTypes   = 256
	MaxSemanticMods    = 64
	MaxSemanticTokens  = 200000
	MaxSemanticEdits   = 10000
)

// SemanticLegend registers a legend: the token types and modifiers its tokens index, each name
// optionally followed by "=EFFECT" (a class name, strike, underline, faded or -).
func (e *Ext) SemanticLegend(name string, types, modifiers []string) error {
	body := strings.Join(types, " ") + "\n" + strings.Join(modifiers, " ")
	_, err := e.Request("semantic-legend", []byte(body), name, itoa(int64(len(body))))
	return err
}

// SemanticTokensRequest is "semantic-tokens DOC VERSION PREV" (Prev the last result's id, "" for
// none) or, for a document over 200,000 lines or 8 MB, "semantic-tokens-range DOC VERSION L1 L2"
// (Range true, lines First..Last).
type SemanticTokensRequest struct {
	Doc         Doc
	Version     int64
	Prev        string
	Range       bool
	First, Last int
	Context     context.Context
}

// SemanticEdit replaces Del ints at Start of the previous result's array with Data.
type SemanticEdit struct {
	Start, Del int
	Data       []uint64
}

// SemanticTokens is an answer: Result its id ("" for none), Legend the legend its types index.
// Data is LSP's five ints per token (DLINE DCOL LEN TYPE MODS, byte columns, the first DLINE from
// line 1; MODS 64 bits, bit i the legend's modifier i); with Edits set instead the answer is a
// delta against Prev.
type SemanticTokens struct {
	Result, Legend string
	Data           []uint64
	Edits          []SemanticEdit
	Delta          bool
}

// SemanticTokensProvider answers semantic-tokens requests (feature semantic-tokens, [provides]
// semantic_tokens); nil says nothing.
type SemanticTokensProvider interface {
	SemanticTokens(ext *Ext, req SemanticTokensRequest) *SemanticTokens
}

func appendInts(b *bytes.Buffer, data []uint64) {
	for i, v := range data {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.FormatUint(v, 10))
	}
}

// Encode is the answer's body: nil for nil, or for data that is not whole tokens or over the
// limits.
func (t *SemanticTokens) Encode(rangeMode bool) []byte {
	if t == nil || !validSemanticName(t.Legend) || t.Result != "" && !validSemanticName(t.Result) {
		return nil
	}
	result := t.Result
	if result == "" {
		result = "-"
	}
	var b bytes.Buffer
	if t.Delta && !rangeMode {
		if len(t.Edits) > MaxSemanticEdits {
			return nil
		}
		fmt.Fprintf(&b, "%s %s delta %d\n", result, t.Legend, len(t.Edits))
		for _, ed := range t.Edits {
			fmt.Fprintf(&b, "%d %d %d\n", ed.Start, ed.Del, len(ed.Data))
			appendInts(&b, ed.Data)
			if len(ed.Data) > 0 {
				b.WriteByte('\n')
			}
		}
		return b.Bytes()
	}
	if len(t.Data)%5 != 0 || len(t.Data)/5 > MaxSemanticTokens {
		return nil
	}
	mode := "full"
	if rangeMode {
		mode = "range"
	}
	fmt.Fprintf(&b, "%s %s %s %d\n", result, t.Legend, mode, len(t.Data)/5)
	appendInts(&b, t.Data)
	return b.Bytes()
}

// validSemanticName is a legend name or result id: 1 to 64 of [A-Za-z0-9_.-].
func validSemanticName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// ---- inlay hints

// InlayHintPart is part of a hint's label: Tip shows while the pointer rests on it; a click runs
// Command, or opens Goto (its Path, "" for this document, at At).
type InlayHintPart struct {
	Label   string
	Tip     string
	Command *CommandRef
	Goto    *InlayGoto
}

// InlayGoto is the place a hint part opens.
type InlayGoto struct {
	Path string
	At   Pos
}

// InlayEdit replaces Range with Text when the user double-clicks the hint.
type InlayEdit struct {
	Range Range
	Text  string
}

// InlayHint sits at At: Before keeps it before the text after it (a parameter name), otherwise it
// follows the text before it (a type).
type InlayHint struct {
	At     Pos
	Before bool
	Parts  []InlayHintPart
	Edits  []InlayEdit
}

// LinesRequest is a request about the lines First..Last of a document ("inlay-hints DOC VERSION
// L1 L2", "code-lens DOC VERSION L1 L2").
type LinesRequest struct {
	Doc         Doc
	Version     int64
	First, Last int
	Context     context.Context
}

// InlayHintProvider answers inlay-hints requests (feature inlay-hints, [provides] inlay_hints):
// the hints of lines First..Last replace this extension's there.
type InlayHintProvider interface {
	InlayHints(ext *Ext, req LinesRequest) []InlayHint
}

// Inlay hint limits.
const (
	MaxInlayHints = 5000
	MaxInlayLabel = 120
	MaxInlayParts = 8
	MaxInlayTip   = 4000
	MaxInlayEdits = 16
)

// EncodeInlayHints is the inlay-hints answer. Labels are made one line and cut so a hint's parts
// hold at most MaxInlayLabel bytes; hints with nothing to show, at a position below 1 1, are left
// out, as those past MaxInlayHints.
func EncodeInlayHints(hints []InlayHint) []byte {
	var recs bytes.Buffer
	n := 0
	for _, h := range hints {
		if h.At.Line < 1 || h.At.Col < 1 || n == MaxInlayHints {
			continue
		}
		var parts bytes.Buffer
		total, np := 0, 0
		for _, p := range h.Parts {
			if np == MaxInlayParts {
				break
			}
			label := cut(oneLine(p.Label), MaxInlayLabel-total)
			if label == "" {
				continue
			}
			total += len(label)
			tip := cut(p.Tip, MaxInlayTip)
			var extra []byte
			extra = append(extra, p.Command.Encode()...)
			if p.Goto != nil {
				path := oneLine(p.Goto.Path)
				extra = append(extra, fmt.Sprintf("goto %s %d\n%s", pos(p.Goto.At), len(path), path)...)
			}
			fmt.Fprintf(&parts, "%d %d %d\n%s%s", len(label), len(tip), len(extra), label, tip)
			parts.Write(extra)
			np++
		}
		if np == 0 {
			continue
		}
		var extra bytes.Buffer
		for i, ed := range h.Edits {
			if i == MaxInlayEdits || !wellFormed(ed.Range) {
				break
			}
			fmt.Fprintf(&extra, "edit %s %d\n%s", rangeFields(ed.Range), len(ed.Text), ed.Text)
		}
		side := "after"
		if h.Before {
			side = "before"
		}
		fmt.Fprintf(&recs, "%s %s %d %d\n", pos(h.At), side, np, extra.Len())
		recs.Write(parts.Bytes())
		recs.Write(extra.Bytes())
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// ---- code lens

// CodeLens is a title over Line ("" while not resolved: drawn "…", asked again when shown); a
// click runs Command, Tip shows while the pointer rests on it.
type CodeLens struct {
	Line    int
	Title   string
	Tip     string
	Command *CommandRef
}

// CodeLensProvider answers code-lens requests (feature code-lens, [provides] code_lens): every
// lens of the document, titles at least for lines First..Last.
type CodeLensProvider interface {
	CodeLenses(ext *Ext, req LinesRequest) []CodeLens
}

// Code lens limits.
const (
	MaxCodeLenses    = 10000
	MaxCodeLensTitle = 200
)

// EncodeCodeLenses is the code-lens answer. Titles are made one line and cut to MaxCodeLensTitle;
// lenses on a line below 1 are left out, as those past MaxCodeLenses.
func EncodeCodeLenses(lenses []CodeLens) []byte {
	var recs bytes.Buffer
	n := 0
	for _, l := range lenses {
		if l.Line < 1 || n == MaxCodeLenses {
			continue
		}
		title := cut(oneLine(l.Title), MaxCodeLensTitle)
		var extra []byte
		extra = append(extra, l.Command.Encode()...)
		if l.Tip != "" {
			tip := cut(l.Tip, 1000)
			extra = append(extra, fmt.Sprintf("tip %d\n%s", len(tip), tip)...)
		}
		fmt.Fprintf(&recs, "%d %d %d\n%s", l.Line, len(title), len(extra), title)
		recs.Write(extra)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// ---- document highlights

// Highlight is a place of the symbol under the caret: Write for a place that writes it.
type Highlight struct {
	Range Range
	Write bool
	Read  bool
}

// HighlightProvider answers highlights requests (feature document-highlights, [provides]
// highlights): the places of what is at req.At (a PosRequest's position).
type HighlightProvider interface {
	Highlights(ext *Ext, req PosRequest) []Highlight
}

// MaxHighlights is the most ranges a highlights answer holds.
const MaxHighlights = 5000

// EncodeHighlights is the highlights answer. Ranges that run backwards are left out, as those past
// MaxHighlights.
func EncodeHighlights(hs []Highlight) []byte {
	var recs bytes.Buffer
	n := 0
	for _, h := range hs {
		if !wellFormed(h.Range) || h.Range.Start.Line < 1 || h.Range.Start.Col < 1 || n == MaxHighlights {
			continue
		}
		kind := "text"
		switch {
		case h.Write:
			kind = "write"
		case h.Read:
			kind = "read"
		}
		fmt.Fprintf(&recs, "%s %s\n", rangeFields(h.Range), kind)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// ---- document colors

// DocumentColor is a color in a document: R, G, B, A 0 to 255.
type DocumentColor struct {
	Range      Range
	R, G, B, A uint8
}

// ColorEditRequest is "color-edit DOC VERSION L1 C1 L2 C2 RRGGBBAA": the user chose a new color
// for the color at Range.
type ColorEditRequest struct {
	Doc        Doc
	Version    int64
	Range      Range
	R, G, B, A uint8
	Context    context.Context
}

// ColorProvider answers colors requests (feature document-colors, [provides] colors) for lines
// First..Last, and color-edit with the edit that writes the new color in the code's notation.
type ColorProvider interface {
	Colors(ext *Ext, req LinesRequest) []DocumentColor
	ColorEdit(ext *Ext, req ColorEditRequest) (WorkspaceEdit, error)
}

// MaxColors is the most colors an answer holds.
const MaxColors = 10000

// EncodeColors is the colors answer. Ranges that run backwards are left out, as those past
// MaxColors.
func EncodeColors(cs []DocumentColor) []byte {
	var recs bytes.Buffer
	n := 0
	for _, c := range cs {
		if !wellFormed(c.Range) || c.Range.Start.Line < 1 || c.Range.Start.Col < 1 || n == MaxColors {
			continue
		}
		fmt.Fprintf(&recs, "%s %02x%02x%02x%02x\n", rangeFields(c.Range), c.R, c.G, c.B, c.A)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

func parseColorEdit(f []string) (ColorEditRequest, bool) {
	d, v, ok := parseDocVersion(f)
	if !ok || len(f) != 7 || len(f[6]) != 8 {
		return ColorEditRequest{}, false
	}
	var n [4]int
	for i := range n {
		x, err := strconv.Atoi(f[2+i])
		if err != nil || x < 1 {
			return ColorEditRequest{}, false
		}
		n[i] = x
	}
	rgba, err := strconv.ParseUint(f[6], 16, 32)
	if err != nil {
		return ColorEditRequest{}, false
	}
	return ColorEditRequest{Doc: d, Version: v, Range: Range{Pos{n[0], n[1]}, Pos{n[2], n[3]}},
		R: uint8(rgba >> 24), G: uint8(rgba >> 16), B: uint8(rgba >> 8), A: uint8(rgba)}, true
}

// ---- inline completion

// InlineRequest is "inline-complete DOC VERSION LINE COL KIND": Kind typed (a typing pause) or
// invoked (alt+\).
type InlineRequest struct {
	Doc     Doc
	Version int64
	At      Pos
	Kind    string
	Context context.Context
}

// InlineItem replaces Range with Text when taken; it shows when Range holds the caret and its
// text agrees with Text's start and end around the caret. Command runs after it is taken; Edits
// are made with it.
type InlineItem struct {
	Range   Range
	Text    string
	Command *CommandRef
	Edits   []InlayEdit
}

// InlineCompleter answers inline-complete requests (feature inline-complete, [provides]
// inline_complete).
type InlineCompleter interface {
	InlineComplete(ext *Ext, req InlineRequest) []InlineItem
}

// Inline completion limits.
const (
	MaxInlineItems = 8
	MaxInlineText  = 64 << 10
)

// EncodeInlineItems is the inline-complete answer: items whose range runs backwards, or whose
// text is empty or over MaxInlineText, are left out, as those past MaxInlineItems.
func EncodeInlineItems(items []InlineItem) []byte {
	var recs bytes.Buffer
	n := 0
	for _, it := range items {
		if !wellFormed(it.Range) || it.Range.Start.Line < 1 || it.Text == "" || len(it.Text) > MaxInlineText || n == MaxInlineItems {
			continue
		}
		var extra []byte
		extra = append(extra, it.Command.Encode()...)
		for i, ed := range it.Edits {
			if i == MaxInlayEdits || !wellFormed(ed.Range) {
				break
			}
			extra = append(extra, fmt.Sprintf("edit %s %d\n%s", rangeFields(ed.Range), len(ed.Text), ed.Text)...)
		}
		fmt.Fprintf(&recs, "%s %d %d\n%s", rangeFields(it.Range), len(it.Text), len(extra), it.Text)
		recs.Write(extra)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// ---- answering

func parseLinesRequest(f []string) (LinesRequest, bool) {
	d, v, ok := parseDocVersion(f)
	if !ok || len(f) != 4 {
		return LinesRequest{}, false
	}
	a, e1 := strconv.Atoi(f[2])
	b, e2 := strconv.Atoi(f[3])
	if e1 != nil || e2 != nil || a < 1 || b < a {
		return LinesRequest{}, false
	}
	return LinesRequest{Doc: d, Version: v, First: a, Last: b}, true
}

// answerPresent answers the presentation requests; known is false when the handler lacks the
// interface, the feature was not agreed or the request does not parse.
func (e *Ext) answerPresent(h Handler, id int64, verb string, f []string) (out []byte, rerr *Error, known bool) {
	switch verb {
	case "folding-ranges":
		p, is := h.(FoldingRangeProvider)
		if doc, version, ok := parseDocVersion(f); is && ok && len(f) == 2 && e.Has(FeatureFoldingRanges) {
			return EncodeFoldingRanges(p.FoldingRanges(e, DocRequest{doc, version, e.langStream(id).Context})), nil, true
		}
	case "semantic-tokens", "semantic-tokens-range":
		p, is := h.(SemanticTokensProvider)
		doc, version, ok := parseDocVersion(f)
		if !is || !ok || !e.Has(FeatureSemanticTokens) {
			break
		}
		req := SemanticTokensRequest{Doc: doc, Version: version, Context: e.langStream(id).Context}
		if verb == "semantic-tokens" {
			if len(f) != 3 {
				break
			}
			if f[2] != "-" {
				req.Prev = f[2]
			}
		} else {
			lr, ok := parseLinesRequest(f)
			if !ok {
				break
			}
			req.Range, req.First, req.Last = true, lr.First, lr.Last
		}
		return p.SemanticTokens(e, req).Encode(req.Range), nil, true
	case "inlay-hints":
		p, is := h.(InlayHintProvider)
		if req, ok := parseLinesRequest(f); is && ok && e.Has(FeatureInlayHints) {
			req.Context = e.langStream(id).Context
			return EncodeInlayHints(p.InlayHints(e, req)), nil, true
		}
	case "code-lens":
		p, is := h.(CodeLensProvider)
		if req, ok := parseLinesRequest(f); is && ok && e.Has(FeatureCodeLens) {
			req.Context = e.langStream(id).Context
			return EncodeCodeLenses(p.CodeLenses(e, req)), nil, true
		}
	case "highlights":
		p, is := h.(HighlightProvider)
		if req, ok := parsePosRequest(f); is && ok && len(f) == 4 && e.Has(FeatureHighlights) {
			return EncodeHighlights(p.Highlights(e, req)), nil, true
		}
	case "colors":
		p, is := h.(ColorProvider)
		if req, ok := parseLinesRequest(f); is && ok && e.Has(FeatureColors) {
			req.Context = e.langStream(id).Context
			return EncodeColors(p.Colors(e, req)), nil, true
		}
	case "color-edit":
		p, is := h.(ColorProvider)
		if req, ok := parseColorEdit(f); is && ok && e.Has(FeatureColors) {
			req.Context = e.langStream(id).Context
			w, err := p.ColorEdit(e, req)
			if err != nil {
				return nil, &Error{Code: "refused", Text: err.Error()}, true
			}
			return e.encodeEdit(w)
		}
	case "inline-complete":
		p, is := h.(InlineCompleter)
		if pr, ok := parsePosRequest(f); is && ok && len(f) == 5 && e.Has(FeatureInlineComplete) {
			req := InlineRequest{Doc: pr.Doc, Version: pr.Version, At: pr.At, Kind: f[4], Context: e.langStream(id).Context}
			return EncodeInlineItems(p.InlineComplete(e, req)), nil, true
		}
	}
	return nil, nil, false
}
