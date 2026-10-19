package rhunext

// Protocol 1.1's hover-query, selection-ranges, hierarchy, workspace-symbols and document-links
// (v1.1/v1.1.md, "Language features"): handlers answer rhun's requests; a handler implements only
// what it provides, and rhun's requests of a feature the handler lacks are answered "unknown".

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
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

// ---- selection-ranges

// SelectionRangesRequest is "selection-ranges DOC VERSION N LEN" with the N positions (each
// selection's cursor) as its body. Context is cancelled by rhun's cancel.
type SelectionRangesRequest struct {
	Doc       Doc
	Version   int64
	Positions []Pos
	Context   context.Context
}

// SelectionRanger answers selection-ranges requests (feature selection-ranges): for each position,
// in order, the ranges around it, innermost first, each containing the one before (Expand
// Selection grows to the next, Shrink Selection goes back).
type SelectionRanger interface {
	SelectionRanges(ext *Ext, req SelectionRangesRequest) [][]Range
}

// The caps of a selection-ranges answer: groups (one per position), ranges in a group, bytes.
const (
	MaxSelectionGroups = 64
	MaxSelectionRanges = 64
	MaxSelectionBody   = 64 << 10
)

// contains reports whether a contains b.
func contains(a, b Range) bool { return !before(b.Start, a.Start) && !before(a.End, b.End) }

// SelectionChain is ranges as rhun takes them: an equal range is skipped, and the first that is
// not well formed or does not contain the one before ends the chain; at most MaxSelectionRanges.
func SelectionChain(ranges []Range) []Range {
	var out []Range
	for _, r := range ranges {
		if len(out) == MaxSelectionRanges || !wellFormed(r) {
			break
		}
		if len(out) > 0 {
			last := out[len(out)-1]
			if r == last {
				continue
			}
			if !contains(r, last) {
				break
			}
		}
		out = append(out, r)
	}
	return out
}

// EncodeSelectionRanges is the selection-ranges answer for the request's positions (at) and a
// group of ranges per position, each made a SelectionChain. A group left empty becomes the empty
// range at its position, which expands nothing, unless no group after it has ranges: the answer
// then ends before it (rhun gives the selections without a group nothing). Groups past the
// positions or MaxSelectionGroups are left out, and the outermost ranges of the longest groups
// go until the body fits MaxSelectionBody.
func EncodeSelectionRanges(at []Pos, groups [][]Range) []byte {
	n := min(len(at), len(groups), MaxSelectionGroups)
	chains := make([][]Range, n)
	last := -1
	for i := 0; i < n; i++ {
		chains[i] = SelectionChain(groups[i])
		if len(chains[i]) > 0 {
			last = i
		}
	}
	chains = chains[:last+1]
	for i, c := range chains {
		if len(c) == 0 {
			chains[i] = []Range{{at[i], at[i]}}
		}
	}
	encode := func(m int) []byte {
		var b bytes.Buffer
		for _, c := range chains {
			c = c[:min(len(c), m)]
			fmt.Fprintf(&b, "%d\n", len(c))
			for _, r := range c {
				b.WriteString(rangeFields(r))
				b.WriteByte('\n')
			}
		}
		return b.Bytes()
	}
	// the most ranges per group that fit (one range per group always does)
	lo, hi := 1, MaxSelectionRanges
	for lo < hi {
		m := (lo + hi + 1) / 2
		if len(encode(m)) <= MaxSelectionBody {
			lo = m
		} else {
			hi = m - 1
		}
	}
	return encode(lo)
}

func parseSelectionRanges(f []string, body []byte) (SelectionRangesRequest, bool) {
	if len(f) != 4 {
		return SelectionRangesRequest{}, false
	}
	doc, version, ok := parseDocVersion(f)
	n, ok1 := countField(f[2], MaxSelectionGroups)
	if !ok || !ok1 {
		return SelectionRangesRequest{}, false
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != n {
		return SelectionRangesRequest{}, false
	}
	req := SelectionRangesRequest{Doc: doc, Version: version}
	for _, l := range lines {
		lc := strings.Split(l, " ")
		if len(lc) != 2 {
			return SelectionRangesRequest{}, false
		}
		p, ok := parsePos(lc[0], lc[1])
		if !ok {
			return SelectionRangesRequest{}, false
		}
		req.Positions = append(req.Positions, p)
	}
	return req, true
}

// ---- hierarchy

// HierarchyItem is a symbol of a call or type hierarchy: Range is the whole symbol in Target (an
// absolute path, or an open document) and Selection its name inside it (Range when zero or not
// inside it); Name and Detail are one line each (cut at MaxText bytes); Data, at most
// MaxHierarchyData bytes, comes back verbatim when rhun asks about the item (CallsIn, CallsOut,
// Supertypes, Subtypes). Kind is SymbolFunction when "".
type HierarchyItem struct {
	Kind      SymbolKind
	Name      string
	Detail    string
	Target    Target
	Range     Range
	Selection Range
	Data      []byte
}

// Call is an item of a call hierarchy and its call sites (1 to MaxCallSites): for calls-in, the
// caller and where in its file it calls the item asked about; for calls-out, the callee and where
// in the file of the item asked about it is called.
type Call struct {
	Item  HierarchyItem
	Sites []Range
}

// HierarchyRequest is "call-hierarchy DOC VERSION LINE COL invoked -" or "type-hierarchy …": the
// roots at the cursor are asked for. Context is cancelled by rhun's cancel.
type HierarchyRequest struct {
	Doc     Doc
	Version int64
	At      Pos
	Context context.Context
}

// CallHierarchyProvider answers call-hierarchy (the roots, at most MaxHierarchyRoots), calls-in
// and calls-out (feature hierarchy). data is an item's Data; out streams parts (feature
// query-stream: PartCalls) and its Context is cancelled by rhun's cancel.
type CallHierarchyProvider interface {
	CallHierarchy(ext *Ext, req HierarchyRequest) []HierarchyItem
	CallsIn(ext *Ext, data []byte, out *Stream) []Call
	CallsOut(ext *Ext, data []byte, out *Stream) []Call
}

// TypeHierarchyProvider answers type-hierarchy (the roots), supertypes and subtypes (feature
// hierarchy), as CallHierarchyProvider (PartHierarchyItems).
type TypeHierarchyProvider interface {
	TypeHierarchy(ext *Ext, req HierarchyRequest) []HierarchyItem
	Supertypes(ext *Ext, data []byte, out *Stream) []HierarchyItem
	Subtypes(ext *Ext, data []byte, out *Stream) []HierarchyItem
}

// The caps of hierarchy answers: roots and their bytes; items or calls of one answer or part and
// their bytes; an item's data; a call's sites.
const (
	MaxHierarchyRoots     = 64
	MaxHierarchyRootsBody = 256 << 10
	MaxHierarchyRecords   = 1000
	MaxHierarchyBody      = 1 << 20
	MaxHierarchyData      = 16 << 10
	MaxCallSites          = 1000
)

// itemRecord is an item's header (without its line end) and bytes, or false when rhun would
// refuse it.
func itemRecord(it HierarchyItem) (string, []byte, bool) {
	name, detail, path := field(it.Name), field(it.Detail), it.Target.String()
	if name == "" || len(path) > MaxPath || len(it.Data) > MaxHierarchyData || !wellFormed(it.Range) {
		return "", nil, false
	}
	sel := it.Selection
	if !wellFormed(sel) || !contains(it.Range, sel) {
		sel = it.Range
	}
	head := fmt.Sprintf("%s %s %s %d %d %d %d", word(string(it.Kind), string(SymbolFunction)), rangeFields(it.Range),
		rangeFields(sel), len(path), len(name), len(detail), len(it.Data))
	b := make([]byte, 0, len(path)+len(name)+len(detail)+len(it.Data))
	b = append(append(append(append(b, path...), name...), detail...), it.Data...)
	return head, b, true
}

func encodeItems(items []HierarchyItem, maxN, maxBody int) []byte {
	var recs bytes.Buffer
	n := 0
	for _, it := range items {
		head, b, ok := itemRecord(it)
		if !ok {
			continue
		}
		if n == maxN || recs.Len()+len(head)+1+len(b)+16 > maxBody {
			break
		}
		recs.WriteString(head)
		recs.WriteByte('\n')
		recs.Write(b)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// EncodeHierarchyItems is the body of a supertypes or subtypes answer or part. Items without a
// name, with a path over MaxPath, data over MaxHierarchyData or a range whose end is before its
// start are left out, as those past MaxHierarchyRecords or MaxHierarchyBody.
func EncodeHierarchyItems(items []HierarchyItem) []byte {
	return encodeItems(items, MaxHierarchyRecords, MaxHierarchyBody)
}

// EncodeHierarchyRoots is the body of a call-hierarchy or type-hierarchy answer: as
// EncodeHierarchyItems, at most MaxHierarchyRoots items and MaxHierarchyRootsBody bytes.
func EncodeHierarchyRoots(items []HierarchyItem) []byte {
	return encodeItems(items, MaxHierarchyRoots, MaxHierarchyRootsBody)
}

// EncodeCalls is the body of a calls-in or calls-out answer or part: items as in
// EncodeHierarchyItems, each with its call sites (those whose end is before their start are left
// out, and those past MaxCallSites); a call without sites is left out.
func EncodeCalls(calls []Call) []byte {
	var recs bytes.Buffer
	n := 0
	for _, c := range calls {
		head, b, ok := itemRecord(c.Item)
		if !ok {
			continue
		}
		var sites bytes.Buffer
		r := 0
		for _, s := range c.Sites {
			if r == MaxCallSites {
				break
			}
			if wellFormed(s) {
				sites.WriteString(rangeFields(s))
				sites.WriteByte('\n')
				r++
			}
		}
		if r == 0 {
			continue
		}
		head += " " + strconv.Itoa(r) + "\n"
		if n == MaxHierarchyRecords || recs.Len()+len(head)+len(b)+sites.Len()+16 > MaxHierarchyBody {
			break
		}
		recs.WriteString(head)
		recs.Write(b)
		recs.Write(sites.Bytes())
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// PartCalls streams calls (calls-in, calls-out).
func (s *Stream) PartCalls(calls []Call) error { return s.Part(EncodeCalls(calls)) }

// PartHierarchyItems streams items (supertypes, subtypes).
func (s *Stream) PartHierarchyItems(items []HierarchyItem) error {
	return s.Part(EncodeHierarchyItems(items))
}

// itemData is the data of "calls-in LEN" + DATA (and calls-out, supertypes, subtypes).
func itemData(f []string, body []byte) ([]byte, bool) {
	if len(f) != 1 {
		return nil, false
	}
	n, err := strconv.Atoi(f[0])
	return body, err == nil && n == len(body)
}

// ---- workspace-symbols

// WorkspaceSymbolsRequest is "workspace-symbols MAX LEN" with the query (one line, at most 256
// bytes, maybe empty) as its body: Max is a hint of how many symbols are wanted.
type WorkspaceSymbolsRequest struct {
	Query string
	Max   int
}

// WorkspaceSymbol is a symbol anywhere in the project: Range in Target (an absolute path, or an
// open document); Kind is SymbolFunction when ""; Name and Detail (its container: a type, a
// module) one line each, cut at MaxText bytes.
type WorkspaceSymbol struct {
	Target Target
	Range  Range
	Kind   SymbolKind
	Name   string
	Detail string
}

// WorkspaceSymbolProvider answers workspace-symbols requests (feature workspace-symbols): out
// streams parts (feature query-stream: PartWorkspaceSymbols) and its Context is cancelled by
// rhun's cancel (Go to Symbol in Project withdraws a request as the query changes).
type WorkspaceSymbolProvider interface {
	WorkspaceSymbols(ext *Ext, req WorkspaceSymbolsRequest, out *Stream) []WorkspaceSymbol
}

// EncodeWorkspaceSymbols is the body of a workspace-symbols answer or part. Symbols without a name
// or with a path over MaxPath are left out, as those past MaxRecords or MaxRecordsBody.
func EncodeWorkspaceSymbols(syms []WorkspaceSymbol) []byte {
	var recs bytes.Buffer
	n := 0
	for _, s := range syms {
		name, detail, path := field(s.Name), field(s.Detail), s.Target.String()
		if name == "" || len(path) > MaxPath {
			continue
		}
		head := fmt.Sprintf("%s %s %d %d %d\n", rangeFields(s.Range), word(string(s.Kind), string(SymbolFunction)),
			len(path), len(name), len(detail))
		if n == MaxRecords || recs.Len()+len(head)+len(path)+len(name)+len(detail)+16 > MaxRecordsBody {
			break
		}
		recs.WriteString(head)
		recs.WriteString(path)
		recs.WriteString(name)
		recs.WriteString(detail)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// PartWorkspaceSymbols streams workspace symbols.
func (s *Stream) PartWorkspaceSymbols(syms []WorkspaceSymbol) error {
	return s.Part(EncodeWorkspaceSymbols(syms))
}

func parseWorkspaceSymbols(f []string, body []byte) (WorkspaceSymbolsRequest, bool) {
	if len(f) != 2 {
		return WorkspaceSymbolsRequest{}, false
	}
	m, err1 := strconv.Atoi(f[0])
	n, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || m < 0 || n != len(body) {
		return WorkspaceSymbolsRequest{}, false
	}
	return WorkspaceSymbolsRequest{Query: string(body), Max: m}, true
}

// ---- document-links

// DocRequest is a request about a whole document at a version ("document-links DOC VERSION").
// Context is cancelled by rhun's cancel.
type DocRequest struct {
	Doc     Doc
	Version int64
	Context context.Context
}

// DocumentLink is a link in a document: ctrl+click on Range opens Target (LinkURL, LinkPath,
// LinkDoc); Tip is one line shown beside it, cut at MaxText bytes, or "".
type DocumentLink struct {
	Range  Range
	Target string
	Tip    string
}

// DocumentLinker answers document-links requests (feature document-links).
type DocumentLinker interface {
	DocumentLinks(ext *Ext, req DocRequest) []DocumentLink
}

// LinkPath is the target of a link to a file by its absolute path, at a line (0 for none) and
// column (0 for none).
func LinkPath(path string, at Pos) string {
	switch {
	case at.Line > 0 && at.Col > 0:
		return path + ":" + strconv.Itoa(at.Line) + ":" + strconv.Itoa(at.Col)
	case at.Line > 0:
		return path + ":" + strconv.Itoa(at.Line)
	}
	return path
}

// LinkDoc is the target of a link to an open document.
func LinkDoc(doc Doc) string { return "#" + itoa(int64(doc)) }

// LinkURL is the target of a link to an http or https URL.
func LinkURL(url string) string { return url }

// ValidLinkTarget reports whether s is a link target rhun opens: an http or https URL, an absolute
// path (optionally followed by :LINE or :LINE:COL) or #DOC, of at most MaxPath bytes (a path's
// :LINE:COL besides) on one line.
func ValidLinkTarget(s string) bool {
	switch {
	case validURL(s, MaxPath):
		return true
	case strings.HasPrefix(s, "#"):
		d, err := strconv.ParseInt(s[1:], 10, 64)
		return err == nil && d > 0 && len(s) <= 11 && s[1] != '+'
	}
	return absPath(s) && len(s) <= MaxPath+22 && !strings.ContainsAny(s, "\n\r")
}

// EncodeDocumentLinks is the document-links answer. Links whose target is not ValidLinkTarget or
// whose range ends before it starts are left out, as those past MaxRecords or MaxRecordsBody.
func EncodeDocumentLinks(links []DocumentLink) []byte {
	var recs bytes.Buffer
	n := 0
	for _, l := range links {
		tip := field(l.Tip)
		if !ValidLinkTarget(l.Target) || !wellFormed(l.Range) {
			continue
		}
		head := fmt.Sprintf("%s %d %d\n", rangeFields(l.Range), len(l.Target), len(tip))
		if n == MaxRecords || recs.Len()+len(head)+len(l.Target)+len(tip)+16 > MaxRecordsBody {
			break
		}
		recs.WriteString(head)
		recs.WriteString(l.Target)
		recs.WriteString(tip)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
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
	case "selection-ranges":
		if r, is := h.(SelectionRanger); is && e.Has(FeatureSelectionRanges) {
			if req, ok := parseSelectionRanges(f, body); ok {
				req.Context = e.langStream(id).Context
				return EncodeSelectionRanges(req.Positions, r.SelectionRanges(e, req)), nil, true
			}
		}
	case "call-hierarchy", "type-hierarchy":
		req, ok := parsePosRequest(f)
		if !ok || !e.Has(FeatureHierarchy) {
			break
		}
		hreq := HierarchyRequest{req.Doc, req.Version, req.At, e.langStream(id).Context}
		if c, is := h.(CallHierarchyProvider); is && verb == "call-hierarchy" {
			return EncodeHierarchyRoots(c.CallHierarchy(e, hreq)), nil, true
		}
		if t, is := h.(TypeHierarchyProvider); is && verb == "type-hierarchy" {
			return EncodeHierarchyRoots(t.TypeHierarchy(e, hreq)), nil, true
		}
	case "calls-in", "calls-out":
		c, is := h.(CallHierarchyProvider)
		if data, ok := itemData(f, body); is && ok && e.Has(FeatureHierarchy) {
			if verb == "calls-in" {
				return EncodeCalls(c.CallsIn(e, data, e.langStream(id))), nil, true
			}
			return EncodeCalls(c.CallsOut(e, data, e.langStream(id))), nil, true
		}
	case "supertypes", "subtypes":
		t, is := h.(TypeHierarchyProvider)
		if data, ok := itemData(f, body); is && ok && e.Has(FeatureHierarchy) {
			if verb == "supertypes" {
				return EncodeHierarchyItems(t.Supertypes(e, data, e.langStream(id))), nil, true
			}
			return EncodeHierarchyItems(t.Subtypes(e, data, e.langStream(id))), nil, true
		}
	case "workspace-symbols":
		w, is := h.(WorkspaceSymbolProvider)
		if req, ok := parseWorkspaceSymbols(f, body); is && ok && e.Has(FeatureWorkspaceSymbols) {
			return EncodeWorkspaceSymbols(w.WorkspaceSymbols(e, req, e.langStream(id))), nil, true
		}
	case "document-links":
		l, is := h.(DocumentLinker)
		if doc, version, ok := parseDocVersion(f); is && ok && len(f) == 2 && e.Has(FeatureDocumentLinks) {
			return EncodeDocumentLinks(l.DocumentLinks(e, DocRequest{doc, version, e.langStream(id).Context})), nil, true
		}
	}
	return nil, nil, false
}

// countField is a count of 1 to max.
func countField(s string, max int) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1 && n <= max
}
