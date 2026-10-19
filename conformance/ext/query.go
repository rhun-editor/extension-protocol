package main

// Protocol 1.1's language features (v1.1/v1.1.md, "Language features"): the [provides] keys,
// parts and progress the extension sends on rhun's pending requests (query-stream), narrowing
// [provides] while running (provider-scope), the complete-items records and their resolution, and
// signature overloads (signature-list). As in langfeat.go, what rhun drops fails the check and
// what it copes with is a warning.

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// providesKeys are [provides]'s keys, each with the feature that makes rhun ask: v1's features
// and triggers, 1.1's save_actions, and the language features' keys (v1.1 "Manifest keys")
var providesKeys = map[string]string{
	"complete": "complete", "complete.triggers": "complete", "signature": "signature", "signature.triggers": "signature",
	"definition": "definition", "references": "references", "symbols": "symbols", "actions": "actions",
	"rename": "rename", "format": "format", "save_actions": "save-actions",
	"hover": "hover-query", "declaration": "navigation", "type_definition": "navigation", "implementation": "navigation",
	"selection_ranges": "selection-ranges", "call_hierarchy": "hierarchy", "type_hierarchy": "hierarchy",
	"workspace_symbols": "workspace-symbols", "range_format": "partial-format", "type_format": "partial-format",
	"type_format.triggers": "partial-format", "document_links": "document-links",
}

// providesKeyProblem says why a [provides] entry is not one rhun reads, or ""
func providesKeyProblem(k, v string) string {
	if _, ok := providesKeys[k]; !ok {
		return fmt.Sprintf("[provides] %s is not a feature's patterns or triggers (section 12, v1.1 Manifest keys)", k)
	}
	if k == "type_format.triggers" {
		if strings.TrimSpace(v) == "" {
			return "[provides] type_format.triggers is empty"
		}
		for _, t := range strings.Fields(v) {
			if t != "newline" && utf8.RuneCountInString(t) != 1 {
				return fmt.Sprintf("[provides] type_format.triggers: %q is neither one character nor the word newline", t)
			}
		}
	}
	return ""
}

// partKind is how rhun takes the parts of a kind of request that takes them: the cap of one part
// (or the answer) and of the request with all its parts, in records and bytes
type partKind struct {
	answerItems, answerBytes, queryItems, queryBytes int
}

var partKinds = map[string]partKind{
	"complete":          {4096, 1 << 20, 8192, 2 << 20},
	"definition":        {10000, 1 << 20, 10000, 4 << 20},
	"declaration":       {10000, 1 << 20, 10000, 4 << 20},
	"type-definition":   {10000, 1 << 20, 10000, 4 << 20},
	"implementation":    {10000, 1 << 20, 20000, 8 << 20},
	"references":        {10000, 1 << 20, 100000, 16 << 20},
	"workspace-symbols": {10000, 1 << 20, 20000, 4 << 20},
	"calls-in":          {1000, 1 << 20, 10000, 4 << 20},
	"calls-out":         {1000, 1 << 20, 10000, 4 << 20},
	"supertypes":        {1000, 1 << 20, 10000, 4 << 20},
	"subtypes":          {1000, 1 << 20, 10000, 4 << 20},
}

// langCeilings are how far progress reports may move a request's hard limit (its base is
// langWaits); a kind without one is not extended
var langCeilings = map[string]time.Duration{
	"definition": 30 * time.Second, "references": 120 * time.Second, "rename": 120 * time.Second, "format": 30 * time.Second,
	"declaration": 30 * time.Second, "type-definition": 30 * time.Second, "implementation": 60 * time.Second,
	"range-format": 30 * time.Second, "code-action-resolve": 60 * time.Second,
}

// query is one of rhun's requests to the extension: its line, when its hard limit is, and what
// came for it before the answer
type query struct {
	line, verb               string
	base                     time.Duration
	started, due             time.Time
	parts, items, progresses int
	bytes                    int
	toldCancelled            bool // a part or report was answered cancelled
}

// queryFor is the pending request REQ names, or nil
func (h *host) queryFor(s string) (int64, *query) {
	req, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, nil
	}
	if _, pending := h.hostReqs[req]; !pending {
		return req, nil
	}
	return req, h.queries[req]
}

// cancelledReply answers a part or report for a request that is not pending: cancelled, which a
// race allows once; a second one after it is a warning
func (h *host) cancelledReply(verb string, req int64, rerr func(string, string)) {
	if q := h.queries[req]; q != nil {
		if q.toldCancelled {
			h.warn("query-stream", fmt.Sprintf("a %s for request %d after rhun answered cancelled: stop working on it", verb, req))
		}
		q.toldCancelled = true
	} else if req < 1 || req > h.hostSeq {
		h.warn("query-stream", fmt.Sprintf("a %s for request %d, which rhun never sent", verb, req))
	}
	rerr("cancelled", "no such request")
}

// withdraw gives up on a request as rhun does (0 cancel ID): it finishes with what came, and a
// late answer is dropped
func (h *host) withdraw(req int64, empty string) {
	delete(h.hostReqs, req)
	if h.withdrawn == nil {
		h.withdrawn = map[int64]bool{}
	}
	h.withdrawn[req] = true
	h.hostAnswers[req] = message{line: fmt.Sprintf("ok %d %d", req, len(empty)), body: []byte(empty)}
	h.write(fmt.Sprintf("0 cancel %d\n", req))
}

// queryPart serves ID query-part REQ LEN: a part in the grammar of REQ's answer
func (h *host) queryPart(args []string, body []byte, reply func(string), rerr func(string, string), id int64) {
	if !h.features["query-stream"] {
		h.fail("features", "query-part without query-stream agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	if len(args) != 2 {
		h.fail("request grammar", "query-part takes REQ LEN")
		rerr("syntax", "cannot parse the line")
		return
	}
	req, q := h.queryFor(args[0])
	if q == nil {
		h.cancelledReply("part", req, rerr)
		return
	}
	kind, ok := partKinds[q.verb]
	if !ok {
		h.fail("query-stream", fmt.Sprintf("a part for request %d, a %s request, whose kind takes no parts", req, q.verb))
		rerr("refused", "this kind takes no parts")
		return
	}
	n, why := h.checkPart(q, body)
	if why != "" {
		h.fail("query-stream", fmt.Sprintf("a part of %s request %d: %s (rhun drops the part)", q.verb, req, why))
		rerr("syntax", "the part breaks the answer's rules")
		return
	}
	q.parts++
	q.items += n
	q.bytes += len(body)
	if q.items > kind.queryItems || q.bytes > kind.queryBytes {
		h.warn("query-stream", fmt.Sprintf("%s request %d crossed its cap of %d records and %d bytes with its parts: rhun keeps what came and withdraws it",
			q.verb, req, kind.queryItems, kind.queryBytes))
		rerr("toobig", "the request's cap")
		empty := "0\n"
		if q.verb == "complete" {
			empty = "0 0\n"
		}
		h.withdraw(req, empty)
		return
	}
	reply(fmt.Sprintf("ok %d", id))
}

// checkPart validates a part as rhun reads REQ's answer: its records, or why rhun drops it
func (h *host) checkPart(q *query, body []byte) (int, string) {
	m := message{line: fmt.Sprintf("ok 0 %d", len(body)), body: body}
	switch q.verb {
	case "complete":
		f := strings.Fields(q.line)
		d := h.docs[int64(count(f[1]))]
		if d == nil {
			return 0, "the document asked about is gone"
		}
		info, why := checkCompletionList(m, d, h.features)
		return info.items, why
	case "definition", "references", "declaration", "type-definition", "implementation":
		n, warns, why := h.checkLocations(m)
		for _, w := range warns {
			h.warn(q.verb, "a part: "+w)
		}
		return n, why
	case "workspace-symbols":
		n, warns, why := h.checkWorkspaceSymbols(m)
		for _, w := range warns {
			h.warn(q.verb, "a part: "+w)
		}
		return n, why
	case "calls-in", "calls-out", "supertypes", "subtypes":
		items, warns, why := h.checkHierarchy(m, q.verb, h.askedItem)
		for _, w := range warns {
			h.warn(q.verb, "a part: "+w)
		}
		return len(items), why
	}
	return 0, ""
}

// queryProgress serves ID query-progress REQ PERCENT LEN: how far the work on REQ has come
func (h *host) queryProgress(args []string, body []byte, reply func(string), rerr func(string, string), id int64) {
	if !h.features["query-stream"] {
		h.fail("features", "query-progress without query-stream agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	if len(args) != 3 {
		h.fail("request grammar", "query-progress takes REQ PERCENT LEN")
		rerr("syntax", "cannot parse the line")
		return
	}
	if p := count(args[1]); args[1] != "-" && (p < 0 || p > 100) {
		h.fail("query-stream", fmt.Sprintf("query-progress: PERCENT %q is neither 0 to 100 nor -", args[1]))
		rerr("syntax", "PERCENT")
		return
	}
	switch {
	case len(body) > 4096:
		h.fail("query-stream", fmt.Sprintf("a progress text of %d bytes: over 4096, rhun stops the extension", len(body)))
		rerr("syntax", "the text")
		return
	case len(body) > 200 || bytes.ContainsAny(body, "\r\n") || !utf8.Valid(body):
		h.fail("query-stream", fmt.Sprintf("the progress text %q is not one line of at most 200 bytes of UTF-8", body))
		rerr("syntax", "the text")
		return
	}
	req, q := h.queryFor(args[0])
	if q == nil {
		h.cancelledReply("progress report", req, rerr)
		return
	}
	q.progresses++
	if ceiling := langCeilings[q.verb]; ceiling > 0 {
		due := time.Now().Add(q.base)
		if limit := q.started.Add(ceiling); due.After(limit) {
			due = limit
		}
		if due.After(q.due) {
			q.due = due
		}
	}
	reply(fmt.Sprintf("ok %d", id))
}

// providesRequest serves ID provides KEY VALUES (provider-scope): the values rhun uses for KEY
// until the process stops
func (h *host) providesRequest(line string, reply func(string), rerr func(string, string), id int64) {
	if !h.features["provider-scope"] {
		h.fail("features", "provides without provider-scope agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	w := strings.SplitN(line, " ", 4)
	if len(w) < 4 || w[2] == "" || strings.TrimSpace(w[3]) == "" {
		h.fail("request grammar", "provides takes KEY VALUES")
		rerr("syntax", "cannot parse the line")
		return
	}
	key, values := w[2], w[3]
	if _, ok := providesKeys[key]; !ok {
		h.fail("provider-scope", fmt.Sprintf("provides %s: not a [provides] key", key))
		rerr("syntax", "not a [provides] key")
		return
	}
	if h.scope == nil {
		h.scope = map[string]string{}
	}
	switch values {
	case "*":
		delete(h.scope, key)
	case "-":
		h.scope[key] = "-"
	default:
		own := strings.Fields(h.m.provides[key])
		for _, v := range strings.Split(values, " ") {
			if !slices.Contains(own, v) {
				h.fail("provider-scope", fmt.Sprintf("provides %s %s: %q is not one of the manifest's values %q (rhun refuses it)", key, values, v, h.m.provides[key]))
				rerr("refused", "not a subset of the manifest's values")
				return
			}
		}
		h.scope[key] = values
	}
	if !h.scoped {
		h.scoped = true
		h.ok(fmt.Sprintf("narrows [provides] while running (provides %s %s)", key, values))
	}
	reply(fmt.Sprintf("ok %d", id))
}

// ---- complete-items ----

// extent is a range as byte offsets in a document
type extent struct{ a, b int }

// resolvable is the first item of a list with resolve data: what to send back, and its range and
// edits, which a resolution's edits must stay apart from
type resolvable struct {
	data  []byte
	edits bool
	taken []extent
}

// checkItemRecord validates a complete-items record of an item whose range starts at istart
// (only with complete-items agreed: rhun skips them otherwise); it notes resolve data in r
func checkItemRecord(w []string, data []byte, d *doc, istart int, r *resolvable) string {
	switch w[0] {
	case "resolve":
		if len(w) != 3 || (w[1] != "0" && w[1] != "1") {
			return fmt.Sprintf("%q is not resolve EDITS LEN (EDITS 0 or 1)", strings.Join(w, " "))
		}
		if len(data) > 16<<10 {
			return fmt.Sprintf("resolve data of %d bytes: at most 16 KiB", len(data))
		}
		if r.data == nil {
			r.data, r.edits = append([]byte{}, data...), w[1] == "1"
		}
	case "replace":
		if len(w) != 6 || w[5] != "0" {
			return fmt.Sprintf("%q is not replace L1 C1 L2 C2 0", strings.Join(w, " "))
		}
		a, _, why := span(d, w[1:5])
		if why != "" {
			return "the replace range: " + why
		}
		if a != istart {
			return fmt.Sprintf("the replace range starts at byte %d, the item's range at %d: rhun ignores it", a, istart)
		}
	case "commit":
		if len(w) != 2 || len(data) < 1 || len(data) > 16 {
			return fmt.Sprintf("%q is not commit LEN, LEN 1 to 16", strings.Join(w, " "))
		}
		for _, c := range data {
			if c < 0x21 || c > 0x7e {
				return fmt.Sprintf("the commit characters %q are not all printable ASCII", data)
			}
		}
	case "filter", "suffix":
		if len(w) != 2 {
			return fmt.Sprintf("%q is not %s LEN", strings.Join(w, " "), w[0])
		}
		if why := oneLine("the "+w[0], data); why != "" {
			return why
		}
	case "preselect", "deprecated", "adjust":
		if len(w) != 2 || w[1] != "0" {
			return fmt.Sprintf("%q is not %s 0", strings.Join(w, " "), w[0])
		}
	case "after":
		if len(w) != 3 || (w[1] != "signature" && w[1] != "complete") || w[2] != "0" {
			return fmt.Sprintf("%q is not after signature 0 or after complete 0", strings.Join(w, " "))
		}
	}
	return ""
}

// checkResolve validates a complete-resolve answer: EXTRA records only (detail, doc, edit, after),
// the edits in d apart from the item's range and its edits; why rhun drops it
func checkResolve(m message, d *doc, r resolvable) string {
	b, why := answer(m)
	if why != "" {
		return why
	}
	if len(b) > 64<<10 {
		return "the resolution is over 64 KiB"
	}
	taken := append([]extent{}, r.taken...)
	for len(b) > 0 {
		e := bytes.IndexByte(b, '\n')
		if e < 0 {
			return "a record without its header line"
		}
		w := strings.Fields(string(b[:e]))
		if len(w) < 2 {
			return fmt.Sprintf("the record %q has no length", b[:e])
		}
		n := count(w[len(w)-1])
		if n < 0 || n > len(b)-e-1 {
			return fmt.Sprintf("the record %q: its length is not within the answer", b[:e])
		}
		data := b[e+1 : e+1+n]
		switch w[0] {
		case "field", "choice", "cursor", "variant", "transforms":
			return fmt.Sprintf("a %s record: a resolution holds no snippet or cursor records", w[0])
		case "detail":
			if len(w) != 2 {
				return fmt.Sprintf("%q is not detail LEN", b[:e])
			}
			if why := oneLine("the detail", data); why != "" {
				return why
			}
		case "doc":
			if len(w) != 2 {
				return fmt.Sprintf("%q is not doc LEN", b[:e])
			}
		case "edit":
			if len(w) != 6 {
				return fmt.Sprintf("%q is not edit L1 C1 L2 C2 LEN", b[:e])
			}
			a, z, why := span(d, w[1:5])
			if why != "" {
				return "an edit: " + why
			}
			for _, t := range taken {
				if a < t.b && t.a < z {
					return "an edit overlaps the item's range or another of its edits"
				}
			}
			taken = append(taken, extent{a, z})
		case "after":
			if why := checkItemRecord(w, data, d, 0, &resolvable{}); why != "" {
				return why
			}
		}
		b = b[e+1+n:]
	}
	return ""
}

// checkResolveRequest sends complete-resolve with the resolve data of a list's item, as rhun does
// when the item is selected, and checks the answer
func (h *host) checkResolveRequest(d *doc, version int64, r resolvable) {
	m, ok := h.askBodyWithin(fmt.Sprintf("complete-resolve %d %d %d", d.id, version, len(r.data)), r.data, time.Second)
	f := strings.Fields(m.line)
	switch {
	case !ok:
		h.fail("complete-items", "no answer to a complete-resolve request within 1 s (rhun's limit)")
	case len(f) >= 3 && f[0] == "error":
		h.ok("answers complete-resolve " + m.line + " (rhun takes an error as nothing to add)")
	default:
		if why := checkResolve(m, d, r); why != "" {
			h.fail("complete-items", "a complete-resolve answer rhun drops: "+why)
		} else {
			h.ok("answers complete-resolve with records rhun can add to the item")
		}
	}
}

// ---- signature-list ----

// checkSignatures validates a signatures answer: what is odd but allowed, and why rhun hides the
// help instead
func checkSignatures(m message) ([]string, string) {
	b, why := answer(m)
	if why != "" {
		return nil, why
	}
	if len(b) == 0 {
		return nil, ""
	}
	if len(b) > 65536 {
		return nil, "over 64 KB"
	}
	c := &cursor{b: b}
	l, ok := c.line()
	w := c.fields(l)
	if !ok || len(w) != 2 || numbers(w) != "" {
		return nil, fmt.Sprintf("the first line %q is not N ACTIVE", l)
	}
	n, active := count(w[0]), count(w[1])
	if n < 1 || n > 32 {
		return nil, fmt.Sprintf("N %d is not 1 to 32", n)
	}
	if active >= n {
		c.warn("ACTIVE %d is not one of the %d overloads: rhun shows the first", active, n)
	}
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return nil, fmt.Sprintf("overload %d has no header line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 4 {
			return nil, fmt.Sprintf("overload %d's header %q is not FROM TO SIG_LEN DOC_LEN", i, l)
		}
		if why := numbers(w[2:]); why != "" {
			return nil, fmt.Sprintf("overload %d: %s", i, why)
		}
		sig, ok1 := c.take(count(w[2]))
		_, ok2 := c.take(count(w[3]))
		if !ok1 || !ok2 {
			return nil, fmt.Sprintf("overload %d's bytes run past the body", i)
		}
		if why := oneLine("the signature", sig); why != "" {
			return nil, fmt.Sprintf("overload %d: %s", i, why)
		}
		if !(w[0] == "-1" && w[1] == "-1") {
			from, to := count(w[0]), count(w[1])
			if from < 0 || to < from || to > len(sig) {
				return nil, fmt.Sprintf("overload %d: FROM TO %s %s is neither -1 -1 nor within its %d bytes", i, w[0], w[1], len(sig))
			}
		}
	}
	if c.rest() > 0 {
		return nil, fmt.Sprintf("%d bytes after the %d overloads N says", c.rest(), n)
	}
	return c.warns, ""
}
