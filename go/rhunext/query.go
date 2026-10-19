package rhunext

// Protocol 1.1's language features (v1.1/v1.1.md, "Language features"): streaming answers and
// progress (query-stream), narrowing [provides] at run time (provider-scope), completion items
// with resolve, ranges and commit characters (complete-items) and signature overloads
// (signature-list). Every language request a handler answers gets a context that rhun's
// "0 cancel ID" cancels, and a Stream for parts and progress.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// The language features of protocol 1.1.
const (
	FeatureQueryStream      = "query-stream"
	FeatureProviderScope    = "provider-scope"
	FeatureHoverQuery       = "hover-query"
	FeatureCompleteItems    = "complete-items"
	FeatureSignatureList    = "signature-list"
	FeatureNavigation       = "navigation"
	FeatureSelectionRanges  = "selection-ranges"
	FeatureHierarchy        = "hierarchy"
	FeatureWorkspaceSymbols = "workspace-symbols"
	FeaturePrepareRename    = "prepare-rename"
	FeaturePartialFormat    = "partial-format"
	FeatureCodeActions      = "code-actions"
	FeatureFileDiagnostics  = "file-diagnostics"
	FeatureDocumentLinks    = "document-links"
)

// bodiedRequests are rhun's requests whose last field is the length of a body that follows.
var bodiedRequests = map[string]bool{
	"complete-resolve": true, "code-action-resolve": true, "calls-in": true, "calls-out": true,
	"supertypes": true, "subtypes": true, "workspace-symbols": true, "selection-ranges": true,
}

// MaxRequestBody is the longest body one of rhun's requests carries (resolve data, hierarchy
// items' data, a workspace-symbols query, selection-ranges' positions).
const MaxRequestBody = 64 << 10

// ErrNoStream is what Stream methods return when rhun did not agree query-stream: the handler
// then returns everything in its answer.
var ErrNoStream = errors.New("rhun did not agree query-stream")

// Stream belongs to one of rhun's pending requests: Part sends part of the answer at once,
// Progress how far the work has come. Context is cancelled when rhun withdraws the request
// ("0 cancel ID"), when it answers cancelled or toobig to a part, or when the session ends.
type Stream struct {
	ext     *Ext
	id      int64
	Context context.Context
}

// ID is the request's id.
func (s *Stream) ID() int64 { return s.id }

// Part sends a part of the answer, in the grammar of the request's answer (Encode a list of the
// request's records). It waits for rhun's reply: that is the flow control. An *Error with Code
// cancelled or toobig means stop: rhun keeps what it has.
func (s *Stream) Part(body []byte) error {
	if s == nil || s.ext == nil || !s.ext.Has(FeatureQueryStream) {
		return ErrNoStream
	}
	if err := s.Context.Err(); err != nil {
		return err
	}
	_, err := s.ext.RequestContext(s.Context, "query-part", body, itoa(s.id), itoa(int64(len(body))))
	var re *Error
	if errors.As(err, &re) && (re.Code == "cancelled" || re.Code == "toobig") {
		s.ext.cancelLang(s.id)
	}
	return err
}

// Progress reports how far the work has come: percent 0 to 100, or -1 for none, and one line of
// text (at most 200 bytes; cut at a character). Each report extends the request's time limit.
func (s *Stream) Progress(percent int, text string) error {
	if s == nil || s.ext == nil || !s.ext.Has(FeatureQueryStream) {
		return ErrNoStream
	}
	p := "-"
	if percent >= 0 {
		if percent > 100 {
			percent = 100
		}
		p = strconv.Itoa(percent)
	}
	t := []byte(cut(oneLine(text), 200))
	_, err := s.ext.RequestContext(s.Context, "query-progress", t, itoa(s.id), p, itoa(int64(len(t))))
	var re *Error
	if errors.As(err, &re) && re.Code == "cancelled" {
		s.ext.cancelLang(s.id)
	}
	return err
}

// PartLocations streams locations (definition, declaration, type definition, implementation,
// references).
func (s *Stream) PartLocations(locs []Location) error { return s.Part(EncodeLocations(locs)) }

// PartItems streams completion items.
func (s *Stream) PartItems(items []CompletionItem) error {
	return s.Part(CompletionList{Items: items}.Encode())
}

// ---- request contexts

type langRequest struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type langRequests struct {
	mu sync.Mutex
	m  map[int64]*langRequest
}

var langReqs sync.Map // *Ext -> *langRequests

func (e *Ext) langTable() *langRequests {
	t, _ := langReqs.LoadOrStore(e, &langRequests{m: map[int64]*langRequest{}})
	return t.(*langRequests)
}

// beginLang registers a language request of rhun's (from the reader, before "0 cancel" can come).
func (e *Ext) beginLang(id int64) {
	t := e.langTable()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.m) >= 4096 || t.m[id] != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.m[id] = &langRequest{ctx: ctx, cancel: cancel}
}

func (e *Ext) cancelLang(id int64) {
	t := e.langTable()
	t.mu.Lock()
	r := t.m[id]
	t.mu.Unlock()
	if r != nil {
		r.cancel()
	}
}

func (e *Ext) cancelAllLang() {
	t := e.langTable()
	t.mu.Lock()
	for _, r := range t.m {
		r.cancel()
	}
	t.mu.Unlock()
}

// langStream is the Stream of one of rhun's requests (a cancelled one when it is unknown).
func (e *Ext) langStream(id int64) *Stream {
	t := e.langTable()
	t.mu.Lock()
	r := t.m[id]
	t.mu.Unlock()
	if r == nil {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return &Stream{ext: e, id: id, Context: ctx}
	}
	return &Stream{ext: e, id: id, Context: r.ctx}
}

func (e *Ext) finishLang(id int64) {
	t := e.langTable()
	t.mu.Lock()
	r := t.m[id]
	delete(t.m, id)
	t.mu.Unlock()
	if r != nil {
		r.cancel()
	}
}

// ---- provider-scope

// Provides narrows a [provides] key until the process stops (feature provider-scope): values is
// a subset of the manifest's own values for key, each written as there. ProvidesManifest restores
// the manifest's values, Withdraw withdraws the kind.
func (e *Ext) Provides(key string, values ...string) error {
	if len(values) == 0 {
		return e.Withdraw(key)
	}
	return e.provides(key, strings.Join(values, " "))
}

// ProvidesManifest restores the manifest's values for key.
func (e *Ext) ProvidesManifest(key string) error { return e.provides(key, "*") }

// Withdraw stops rhun asking for key's kind until ProvidesManifest or Provides.
func (e *Ext) Withdraw(key string) error { return e.provides(key, "-") }

func (e *Ext) provides(key, values string) error {
	if !e.Has(FeatureProviderScope) {
		return &Error{Code: "unknown", Text: "provider-scope was not agreed"}
	}
	if key == "" || strings.ContainsAny(key, " \n\r") || strings.ContainsAny(values, "\n\r") {
		return &Error{Code: "syntax", Text: "a key is one word and values one line"}
	}
	_, err := e.Request("provides", nil, key, values)
	return err
}

// ---- complete-items

// ResolveRequest asks for more of a completion item (feature complete-items): Data is the item's
// Resolve bytes; Version the list's.
type ResolveRequest struct {
	ID      int64
	Doc     Doc
	Version int64
	Data    []byte
	Context context.Context
}

// Resolution is what resolving adds to an item: documentation, a detail that replaces the item's,
// extra edits (in the document at the list's version), and what comes after accepting.
type Resolution struct {
	Doc            string
	Detail         string
	HasDetail      bool
	Edits          []TextEdit
	AfterSignature bool
	AfterComplete  bool
}

// Encode is the resolve answer: EXTRA records only.
func (r Resolution) Encode() []byte {
	var b bytes.Buffer
	if r.HasDetail {
		d := oneLine(r.Detail)
		fmt.Fprintf(&b, "detail %d\n%s", len(d), d)
	}
	if r.Doc != "" {
		fmt.Fprintf(&b, "doc %d\n%s", len(r.Doc), r.Doc)
	}
	for _, ed := range r.Edits {
		fmt.Fprintf(&b, "edit %s %s %d\n%s", pos(ed.Range.Start), pos(ed.Range.End), len(ed.Text), ed.Text)
	}
	if r.AfterSignature {
		b.WriteString("after signature 0\n")
	}
	if r.AfterComplete {
		b.WriteString("after complete 0\n")
	}
	return b.Bytes()
}

// CompletionResolver resolves the selected item (feature complete-items).
type CompletionResolver interface {
	ResolveCompletion(ext *Ext, req ResolveRequest) Resolution
}

// StreamingCompleter is a Completer that may stream its items (feature query-stream); rhun
// prefers it over Complete when the handler has it.
type StreamingCompleter interface {
	CompleteStream(ext *Ext, req Request, out *Stream) CompletionList
}

// MaxResolveData is the most bytes of an item's Resolve; MaxCommit the most commit characters.
const (
	MaxResolveData = 16 << 10
	MaxCommit      = 16
)

// itemExtra is the complete-items records of an item (only those that are valid).
func itemExtra(it CompletionItem) []byte {
	var b bytes.Buffer
	if len(it.Resolve) > 0 && len(it.Resolve) <= MaxResolveData {
		edits := 0
		if it.ResolveEdits {
			edits = 1
		}
		fmt.Fprintf(&b, "resolve %d %d\n", edits, len(it.Resolve))
		b.Write(it.Resolve)
	}
	if it.Replace != nil && it.Replace.Start == it.Range.Start {
		fmt.Fprintf(&b, "replace %s %s 0\n", pos(it.Replace.Start), pos(it.Replace.End))
	}
	if c := commitChars(it.Commit); c != "" {
		fmt.Fprintf(&b, "commit %d\n%s", len(c), c)
	}
	if it.Filter != "" {
		f := oneLine(it.Filter)
		fmt.Fprintf(&b, "filter %d\n%s", len(f), f)
	}
	if it.Suffix != "" {
		s := oneLine(it.Suffix)
		fmt.Fprintf(&b, "suffix %d\n%s", len(s), s)
	}
	if it.Preselect {
		b.WriteString("preselect 0\n")
	}
	if it.Deprecated {
		b.WriteString("deprecated 0\n")
	}
	if it.Adjust {
		b.WriteString("adjust 0\n")
	}
	if it.AfterSignature {
		b.WriteString("after signature 0\n")
	}
	if it.AfterComplete {
		b.WriteString("after complete 0\n")
	}
	return b.Bytes()
}

// commitChars keeps the printable ASCII characters, at most MaxCommit, each once.
func commitChars(s string) string {
	var out []byte
	for i := 0; i < len(s) && len(out) < MaxCommit; i++ {
		c := s[i]
		if c >= 0x21 && c <= 0x7e && bytes.IndexByte(out, c) < 0 {
			out = append(out, c)
		}
	}
	return string(out)
}

// ---- signature-list

// SignatureInfo is one overload: its label, the active parameter's bytes in it (ActiveFrom -1 for
// none), its documentation.
type SignatureInfo struct {
	Label                string
	ActiveFrom, ActiveTo int
	Doc                  string
}

// SignatureList answers a signatures request (feature signature-list): at most 32 overloads and
// the active one (zero-based).
type SignatureList struct {
	Signatures []SignatureInfo
	Active     int
}

// Encode is the body rhun reads; an empty list hides the help (nil).
func (l SignatureList) Encode() []byte {
	sigs := l.Signatures
	if len(sigs) > 32 {
		sigs = sigs[:32]
	}
	if len(sigs) == 0 {
		return nil
	}
	active := l.Active
	if active < 0 || active >= len(sigs) {
		active = 0
	}
	var recs bytes.Buffer
	for _, s := range sigs {
		label := oneLine(s.Label)
		a, b := s.ActiveFrom, s.ActiveTo
		if a < 0 || b < a || b > len(label) {
			a, b = -1, -1
		}
		fmt.Fprintf(&recs, "%d %d %d %d\n%s%s", a, b, len(label), len(s.Doc), label, s.Doc)
	}
	out := append([]byte(fmt.Sprintf("%d %d\n", len(sigs), active)), recs.Bytes()...)
	if len(out) > 65536 {
		// documentation goes first, then overloads, until it fits
		for i := range sigs {
			sigs[i].Doc = ""
		}
		return SignatureList{Signatures: sigs[:max(1, len(sigs)/2)], Active: 0}.Encode()
	}
	return out
}

// SignatureLister answers signatures requests (feature signature-list) instead of Signature.
type SignatureLister interface {
	Signatures(ext *Ext, req Request) *SignatureList
}

// ---- answering

// answerQuery answers the 1.1 requests and the streaming variants; known is false when the
// handler does not answer the verb.
func (e *Ext) answerQuery(h Handler, id int64, verb string, f []string, body []byte) (out []byte, rerr *Error, known bool) {
	switch verb {
	case "complete":
		sc, is := h.(StreamingCompleter)
		if req, ok := parseRequest(f); is && ok {
			return sc.CompleteStream(e, req, e.langStream(id)).Encode(), nil, true
		}
	case "complete-resolve":
		r, is := h.(CompletionResolver)
		if req, ok := parseResolve(f, body); is && ok && e.Has(FeatureCompleteItems) {
			req.ID = id
			req.Context = e.langStream(id).Context
			return r.ResolveCompletion(e, req).Encode(), nil, true
		}
	case "signatures":
		s, is := h.(SignatureLister)
		if req, ok := parseRequest(f); is && ok && e.Has(FeatureSignatureList) {
			if l := s.Signatures(e, req); l != nil {
				return l.Encode(), nil, true
			}
			return nil, nil, true
		}
	case "definition", "references", "declaration", "type-definition", "implementation":
		if sl, is := h.(StreamingLocator); is {
			if req, ok := parsePosRequest(f); ok {
				return EncodeLocations(sl.LocationsStream(e, verb, req, e.langStream(id))), nil, true
			}
		}
	}
	return nil, nil, false
}

// StreamingLocator answers definition, references, declaration, type-definition and
// implementation requests with a Stream for parts (feature query-stream); rhun prefers it over
// Definer and ReferenceFinder when the handler has it. Kind is the request's verb.
type StreamingLocator interface {
	LocationsStream(ext *Ext, kind string, req PosRequest, out *Stream) []Location
}

func parseResolve(f []string, body []byte) (ResolveRequest, bool) {
	if len(f) != 3 {
		return ResolveRequest{}, false
	}
	doc, version, ok := parseDocVersion(f)
	n, e := strconv.Atoi(f[2])
	if !ok || e != nil || n != len(body) {
		return ResolveRequest{}, false
	}
	return ResolveRequest{Doc: doc, Version: version, Data: body}, true
}

// validUTF8Line reports whether s is one line of valid UTF-8.
func validUTF8Line(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsAny(s, "\n\r")
}
