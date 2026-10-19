package rhunext

// Protocol 1.1's hover-query, selection-ranges, hierarchy, workspace-symbols and document-links
// (v1.1/v1.1.md, "Language features"): handlers answer rhun's requests; a handler implements only
// what it provides, and rhun's requests of a feature the handler lacks are answered "unknown".

import (
	"context"
	"strconv"
)

// ---- hover-query

// HoverRequest is "hover-query DOC VERSION LINE COL KIND -": Kind is pointer (the pointer rested on
// text for half a second) or invoked (Show Hover at the cursor). Context is cancelled by rhun's
// cancel.
type HoverRequest struct {
	Doc     Doc
	Version int64
	At      Pos
	Kind    string
	Context context.Context
}

// Hover is a hover-query answer: the text (plain, or Markdown with the markdown feature agreed) and
// the range it describes, the zero Range for the position only. The card stays while the pointer is
// on the range or on the card.
type Hover struct {
	Range Range
	Text  string
}

// MaxHoverBody is the most bytes of a hover answer: Encode cuts the text at a character to fit.
const MaxHoverBody = 64 << 10

// before reports whether a comes before b.
func before(a, b Pos) bool { return a.Line < b.Line || a.Line == b.Line && a.Col < b.Col }

// wellFormed reports whether r's start is not after its end.
func wellFormed(r Range) bool { return !before(r.End, r.Start) }

// Encode is the answer's body: nil for nil or an empty text (nothing to say). A range whose end
// comes before its start is sent as the zero Range.
func (h *Hover) Encode() []byte {
	if h == nil || h.Text == "" {
		return nil
	}
	r := h.Range
	if !wellFormed(r) {
		r = Range{}
	}
	head := rangeFields(r) + "\n"
	return append([]byte(head), cut(h.Text, MaxHoverBody-len(head))...)
}

// HoverProvider answers hover-query requests (feature hover-query); nil says nothing. rhun then
// does not send the v1 hover event for that position.
type HoverProvider interface {
	Hover(ext *Ext, req HoverRequest) *Hover
}

// answerMore answers the requests of this file; known is false when the handler lacks the
// interface, the feature was not agreed or the request does not parse.
func (e *Ext) answerMore(h Handler, id int64, verb string, f []string, body []byte) (out []byte, rerr *Error, known bool) {
	switch verb {
	case "hover-query":
		if p, is := h.(HoverProvider); is && e.Has(FeatureHoverQuery) {
			if req, ok := parsePosRequest(f); ok && len(f) >= 5 && f[4] != "" {
				return p.Hover(e, HoverRequest{req.Doc, req.Version, req.At, f[4], e.langStream(id).Context}).Encode(), nil, true
			}
		}
	}
	return nil, nil, false
}

// countField is a count of 1 to max.
func countField(s string, max int) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1 && n <= max
}
