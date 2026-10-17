package main

// Sections 12d to 12h of the spec: definition, references, symbols, code actions, rename and
// format. The checker asks each agreed feature about a document its [provides] patterns match and
// reads the answer as rhun does: locations, symbols, actions and workspace edits, every range
// against the text it names (an open document at its version, or a file on disk). The workspace
// edits of a rename, a format and the chosen action are then made, as rhun makes them, and the
// chosen action's data goes back as the action event.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// providerFeatures are the features that name files in [provides] (all but views)
var providerFeatures = []string{"complete", "signature", "definition", "references", "symbols", "actions", "rename", "format"}

// symbolKinds are the kinds 12e lists; another is shown as given
var symbolKinds = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(`file module namespace package class method property field constructor enum
		interface function variable constant string number boolean array object key null enum_member struct event
		operator type_parameter`) {
		m[k] = true
	}
	return m
}()

// patterns are the file patterns [provides] gives a feature, "" for none ("no" is none)
func (h *host) patterns(f string) string {
	p := strings.TrimSpace(h.m.provides[f])
	if p == "no" {
		return ""
	}
	return p
}

// matches says whether a path's file name matches one of the patterns: * for all, *.go a suffix,
// Makefile an exact name (section 2)
func matches(patterns, path string) bool {
	base := filepath.Base(path)
	for _, p := range strings.Fields(patterns) {
		if p == "*" || (strings.HasPrefix(p, "*") && strings.HasSuffix(base, p[1:])) || p == base {
			return true
		}
	}
	return false
}

// provider says whether the extension provides an agreed feature for a document: it then hears
// the document's edits as if it subscribed to them
func (h *host) provider(d *doc) bool {
	for _, f := range providerFeatures {
		if h.features[f] && matches(h.patterns(f), d.path) {
			return true
		}
	}
	return false
}

// ---- reading a body as rhun does ----

// words splits a header line at single spaces; nil when a field is empty (two spaces, or one at
// an end), which the grammar does not allow
func words(l string) []string {
	w := strings.Split(l, " ")
	for _, x := range w {
		if x == "" {
			return nil
		}
	}
	return w
}

// count is a number of at most 10 digits, as rhun reads lengths and counts; -1 when it is not one
func count(s string) int {
	if s == "" || len(s) > 10 {
		return -1
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return -1
		}
	}
	n, _ := strconv.Atoi(s)
	return n
}

// cursor walks a body: header lines and the bytes that follow them
type cursor struct {
	b   []byte
	pos int
}

func (c *cursor) line() (string, bool) {
	e := bytes.IndexByte(c.b[c.pos:], '\n')
	if e < 0 {
		return "", false
	}
	l := string(c.b[c.pos : c.pos+e])
	c.pos += e + 1
	return l, true
}

func (c *cursor) take(n int) ([]byte, bool) {
	if n < 0 || n > len(c.b)-c.pos {
		return nil, false
	}
	x := c.b[c.pos : c.pos+n]
	c.pos += n
	return x, true
}

func (c *cursor) rest() int { return len(c.b) - c.pos }

// head reads the first line, N, at most max; why when it is not one
func (c *cursor) head(max int, what string) (int, string) {
	l, ok := c.line()
	if !ok {
		return 0, "the body has no first line N"
	}
	n := count(l)
	if n < 0 || n > max {
		return 0, fmt.Sprintf("the first line %q is not N, 0 to %d %s", l, max, what)
	}
	return n, ""
}

// span is the range L1 C1 L2 C2 (the first four words) in a text: byte offsets, or why not
func span(d *doc, w []string) (int, int, string) {
	for _, x := range w[:4] {
		if count(x) < 1 {
			return 0, 0, fmt.Sprintf("%q is not a line or column (1 or more)", x)
		}
	}
	a, why := d.position(w[0], w[1])
	if why != "" {
		return 0, 0, "the range starts badly: " + why
	}
	b, why := d.position(w[2], w[3])
	if why != "" {
		return 0, 0, "the range ends badly: " + why
	}
	if b < a {
		return 0, 0, "the range ends before it starts"
	}
	return a, b, ""
}

// oneLine says why a label rhun shows on one line is not one, or ""
func oneLine(what string, b []byte) string {
	if bytes.ContainsAny(b, "\r\n") {
		return what + " holds a line break: it is one line"
	}
	if !utf8.Valid(b) {
		return what + " is not UTF-8"
	}
	return ""
}

// answer is the body of an ok ID LEN answer, or why it is not one
func answer(m message) ([]byte, string) {
	f := strings.Fields(m.line)
	if len(f) != 3 || f[0] != "ok" {
		return nil, fmt.Sprintf("the answer is %q, not ok ID LEN", m.line)
	}
	if m.err != nil {
		return nil, m.err.Error()
	}
	return m.body, ""
}

// place is the text a PATH names: an open document, by its id or its path, or a file on disk
type place struct {
	d    *doc   // the open document, nil for a file that is not open
	path string // as the answer gave it
	text []byte
}

// at is the text as a document, for positions
func (t place) at() *doc {
	if t.d != nil {
		return t.d
	}
	return &doc{path: t.path, text: t.text}
}

// resolve finds what a PATH of 12d or 12g names: #DOC, an open document's path, or a readable
// file; or the code an apply request is refused with (section 8) and why
func (h *host) resolve(p []byte) (place, string, string) {
	s := string(p)
	if bytes.IndexByte(p, 0) >= 0 || bytes.ContainsAny(p, "\r\n") {
		return place{}, "nofile", fmt.Sprintf("the path %q holds a NUL or a line break", s)
	}
	if strings.HasPrefix(s, "#") {
		id := count(s[1:])
		if id < 1 {
			return place{}, "nofile", fmt.Sprintf("%q is not #DOC", s)
		}
		d := h.docs[int64(id)]
		if d == nil {
			return place{}, "nodoc", fmt.Sprintf("%s names document %d, which rhun never opened", s, id)
		}
		return place{d: d, path: s, text: d.text}, "", ""
	}
	if !filepath.IsAbs(s) {
		return place{}, "nofile", fmt.Sprintf("the path %q is neither absolute nor #DOC", s)
	}
	for _, id := range h.docIDs() {
		if d := h.docs[id]; d.path == s {
			return place{d: d, path: s, text: d.text}, "", ""
		}
	}
	data, err := os.ReadFile(s)
	if err != nil {
		return place{}, "nofile", fmt.Sprintf("%s is neither open nor a file rhun can read", s)
	}
	return place{path: s, text: data}, "", ""
}

// docIDs are the open documents' ids in order
func (h *host) docIDs() []int64 {
	ids := make([]int64, 0, len(h.docs))
	for id := range h.docs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// ---- the bodies ----

// checkLocations validates a locations answer (12d): the number of locations, or why rhun drops it
func (h *host) checkLocations(m message) (int, string) {
	b, why := answer(m)
	if why != "" {
		return 0, why
	}
	if len(b) > 1<<20 {
		return 0, "the locations are over 1 MB"
	}
	c := &cursor{b: b}
	n, why := c.head(10000, "locations")
	if why != "" {
		return 0, why
	}
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, fmt.Sprintf("location %d has no header line (N says %d)", i, n)
		}
		w := words(l)
		if len(w) != 6 {
			return 0, fmt.Sprintf("location %d's header %q is not L1 C1 L2 C2 PATH_LEN TEXT_LEN", i, l)
		}
		pl, tl := count(w[4]), count(w[5])
		if pl < 1 || pl > 4096 {
			return 0, fmt.Sprintf("location %d: PATH_LEN %q is not 1 to 4096", i, w[4])
		}
		if tl < 0 {
			return 0, fmt.Sprintf("location %d: TEXT_LEN %q is not a length", i, w[5])
		}
		path, ok1 := c.take(pl)
		text, ok2 := c.take(tl)
		if !ok1 || !ok2 {
			return 0, fmt.Sprintf("location %d's bytes run past the body", i)
		}
		t, _, why := h.resolve(path)
		if why != "" {
			return 0, fmt.Sprintf("location %d: %s", i, why)
		}
		if _, _, why := span(t.at(), w); why != "" {
			return 0, fmt.Sprintf("location %d in %s: %s", i, path, why)
		}
		if why := oneLine("the preview", text); why != "" {
			return 0, fmt.Sprintf("location %d: %s", i, why)
		}
	}
	if c.rest() > 0 {
		return 0, fmt.Sprintf("%d bytes after the %d locations N says", c.rest(), n)
	}
	return n, ""
}

// checkSymbols validates a symbols answer (12e) about d: the number of symbols and what is odd
// but allowed, or why rhun drops it
func checkSymbols(m message, d *doc) (int, []string, string) {
	b, why := answer(m)
	if why != "" {
		return 0, nil, why
	}
	if len(b) > 1<<20 {
		return 0, nil, "the symbols are over 1 MB"
	}
	c := &cursor{b: b}
	n, why := c.head(10000, "symbols")
	if why != "" {
		return 0, nil, why
	}
	var warns []string
	odd := map[string]bool{}
	prev := 0
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, nil, fmt.Sprintf("symbol %d has no header line (N says %d)", i, n)
		}
		w := words(l)
		if len(w) != 8 {
			return 0, nil, fmt.Sprintf("symbol %d's header %q is not L1 C1 L2 C2 KIND DEPTH NAME_LEN DETAIL_LEN", i, l)
		}
		a, _, why := span(d, w)
		if why != "" {
			return 0, nil, fmt.Sprintf("symbol %d: %s", i, why)
		}
		if len(w[4]) > 40 {
			return 0, nil, fmt.Sprintf("symbol %d: KIND %q is over 40 bytes", i, w[4])
		}
		if !symbolKinds[w[4]] && !odd[w[4]] {
			odd[w[4]] = true
			warns = append(warns, fmt.Sprintf("symbol %d: KIND %q is not one 12e lists (rhun shows it as given)", i, w[4]))
		}
		if count(w[5]) < 0 {
			return 0, nil, fmt.Sprintf("symbol %d: DEPTH %q is not a number", i, w[5])
		}
		nl, dl := count(w[6]), count(w[7])
		if nl < 1 || nl > 1000 {
			return 0, nil, fmt.Sprintf("symbol %d: NAME_LEN %q is not 1 to 1000", i, w[6])
		}
		if dl < 0 || dl > 1000 {
			return 0, nil, fmt.Sprintf("symbol %d: DETAIL_LEN %q is not 0 to 1000", i, w[7])
		}
		name, ok1 := c.take(nl)
		detail, ok2 := c.take(dl)
		if !ok1 || !ok2 {
			return 0, nil, fmt.Sprintf("symbol %d's bytes run past the body", i)
		}
		if why := oneLine("the name", name); why != "" {
			return 0, nil, fmt.Sprintf("symbol %d: %s", i, why)
		}
		if why := oneLine("the detail", detail); why != "" {
			return 0, nil, fmt.Sprintf("symbol %d: %s", i, why)
		}
		if a < prev && len(warns) < 20 {
			warns = append(warns, fmt.Sprintf("symbol %d starts before the one above it: symbols come in document order", i))
		}
		prev = a
	}
	if c.rest() > 0 {
		return 0, nil, fmt.Sprintf("%d bytes after the %d symbols N says", c.rest(), n)
	}
	return n, warns, ""
}

// codeAction is one action of a 12f answer, its edit checked
type codeAction struct {
	kind, title string
	edit        []wsGroup
	data        []byte
}

// checkActions validates a code actions answer (12f), each action's workspace edit included
func (h *host) checkActions(m message) ([]codeAction, string) {
	b, why := answer(m)
	if why != "" {
		return nil, why
	}
	if len(b) > 4<<20 {
		return nil, "the actions are over 4 MB"
	}
	c := &cursor{b: b}
	n, why := c.head(1000, "actions")
	if why != "" {
		return nil, why
	}
	var acts []codeAction
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return nil, fmt.Sprintf("action %d has no header line (N says %d)", i, n)
		}
		w := words(l)
		if len(w) != 4 {
			return nil, fmt.Sprintf("action %d's header %q is not KIND TITLE_LEN EDIT_LEN DATA_LEN", i, l)
		}
		if len(w[0]) > 40 {
			return nil, fmt.Sprintf("action %d: KIND %q is over 40 bytes", i, w[0])
		}
		tl, el, dl := count(w[1]), count(w[2]), count(w[3])
		if tl < 1 || tl > 1000 {
			return nil, fmt.Sprintf("action %d: TITLE_LEN %q is not 1 to 1000", i, w[1])
		}
		if el < 0 || dl < 0 {
			return nil, fmt.Sprintf("action %d: EDIT_LEN %q or DATA_LEN %q is not a length", i, w[2], w[3])
		}
		title, ok1 := c.take(tl)
		edit, ok2 := c.take(el)
		data, ok3 := c.take(dl)
		if !ok1 || !ok2 || !ok3 {
			return nil, fmt.Sprintf("action %d's bytes run past the body", i)
		}
		if why := oneLine("the title", title); why != "" {
			return nil, fmt.Sprintf("action %d: %s", i, why)
		}
		a := codeAction{kind: w[0], title: string(title), data: data}
		if len(edit) > 0 {
			gs, _, why := h.checkWorkspaceEdit(edit, nil)
			if why != "" {
				return nil, fmt.Sprintf("action %d (%q): its edit: %s", i, title, why)
			}
			a.edit = gs
		}
		acts = append(acts, a)
	}
	if c.rest() > 0 {
		return nil, fmt.Sprintf("%d bytes after the %d actions N says", c.rest(), n)
	}
	return acts, ""
}

// wsEdit is an edit of a workspace edit: bytes start to end of its file's text become text
type wsEdit struct {
	start, end int
	text       []byte
}

// wsGroup is a file of a workspace edit, its edits sorted as rhun sorts them
type wsGroup struct {
	t     place
	edits []wsEdit
}

// checkWorkspaceEdit validates a workspace edit (12g) as rhun does before it changes anything:
// each open document at its version, "-" only for a file that is not open, each range inside its
// text, no overlaps, one group per file; only, when not nil, is the one document it may change.
// A refused edit has the code rhun answers an apply request with (section 8) and the reason.
func (h *host) checkWorkspaceEdit(b []byte, only *doc) ([]wsGroup, string, string) {
	bad := func(code, format string, a ...any) ([]wsGroup, string, string) {
		return nil, code, fmt.Sprintf(format, a...)
	}
	if len(b) > 4<<20 {
		return bad("toobig", "the workspace edit is over 4 MB")
	}
	c := &cursor{b: b}
	if l, ok := c.line(); ok && count(l) > 1000 {
		return bad("toobig", "%s files: at most 1000", l)
	}
	c.pos = 0
	n, why := c.head(1000, "files")
	if why != "" {
		return bad("syntax", "%s", why)
	}
	var groups []wsGroup
	seen := map[string]bool{}
	total := 0
	for g := 1; g <= n; g++ {
		l, ok := c.line()
		if !ok {
			return bad("syntax", "file %d has no header line (N says %d)", g, n)
		}
		w := words(l)
		if len(w) != 3 {
			return bad("syntax", "file %d's header %q is not PATH_LEN VERSION M", g, l)
		}
		pl, m := count(w[0]), count(w[2])
		if pl < 1 || pl > 4096 {
			return bad("syntax", "file %d: PATH_LEN %q is not 1 to 4096", g, w[0])
		}
		if m < 0 {
			return bad("syntax", "file %d: M %q is not a count", g, w[2])
		}
		if total += m; total > 100000 {
			return bad("toobig", "over 100000 edits")
		}
		path, ok := c.take(pl)
		if !ok {
			return bad("syntax", "file %d's path runs past the body", g)
		}
		t, code, why := h.resolve(path)
		if why != "" {
			return bad(code, "file %d: %s", g, why)
		}
		key := t.path
		if t.d != nil {
			key = fmt.Sprintf("#%d", t.d.id)
		}
		if seen[key] {
			return bad("syntax", "file %d: %s has a group already (one group per file)", g, path)
		}
		seen[key] = true
		if only != nil && t.d != only {
			return bad("refused", "file %d: %s is not the document asked about (#%d, %s)", g, path, only.id, only.path)
		}
		v := w[1]
		switch {
		case v == "-" && t.d != nil:
			return bad("stale", "file %d: VERSION - for %s, which is open (at version %d): - is for a file that is not open", g, path, t.d.version)
		case v == "-":
		case len(v) > 18 || strings.Trim(v, "0123456789") != "":
			return bad("syntax", "file %d: VERSION %q is neither a number nor -", g, v)
		case t.d == nil:
			return bad("nodoc", "file %d: VERSION %s for %s, which is not open: - is for a file that is not open", g, v, path)
		default:
			if x, _ := strconv.ParseInt(v, 10, 64); x != t.d.version {
				return bad("stale", "file %d: VERSION %s for %s, which is at version %d", g, v, path, t.d.version)
			}
		}
		d := t.at()
		edits := make([]wsEdit, 0, m)
		for e := 1; e <= m; e++ {
			l, ok := c.line()
			if !ok {
				return bad("syntax", "file %d, edit %d has no header line (M says %d)", g, e, m)
			}
			w := words(l)
			if len(w) != 5 {
				return bad("syntax", "file %d, edit %d: %q is not L1 C1 L2 C2 LEN", g, e, l)
			}
			tl := count(w[4])
			if tl < 0 {
				return bad("syntax", "file %d, edit %d: LEN %q is not a length", g, e, w[4])
			}
			text, ok := c.take(tl)
			if !ok {
				return bad("syntax", "file %d, edit %d's text runs past the body", g, e)
			}
			a, z, why := span(d, w)
			if why != "" {
				return bad("badpos", "file %d, edit %d: %s", g, e, why)
			}
			edits = append(edits, wsEdit{a, z, text})
		}
		// rhun sorts a file's edits by where they start, an insertion keeping its place among those
		// that start with it (at most 5000 out of order), then refuses one that starts inside the
		// one before it
		byStart := func(x, y wsEdit) int { return x.start - y.start }
		if !slices.IsSortedFunc(edits, byStart) {
			if len(edits) > 5000 {
				return bad("toobig", "file %d: %d edits out of order (rhun sorts at most 5000: send them in order)", g, len(edits))
			}
			slices.SortStableFunc(edits, byStart)
		}
		for i := 1; i < len(edits); i++ {
			if p, e := edits[i-1], edits[i]; p.end > e.start {
				hint := ""
				if e.start == e.end && e.start == p.start {
					hint = ": an insertion where a replacement starts comes before it in the list"
				}
				return bad("badpos", "file %d: two edits overlap (bytes %d to %d and %d to %d of %s)%s", g,
					p.start, p.end, e.start, e.end, path, hint)
			}
		}
		groups = append(groups, wsGroup{t, edits})
	}
	if c.rest() > 0 {
		return bad("syntax", "%d bytes after the %d files N says", c.rest(), n)
	}
	return groups, "", ""
}

// apply makes a checked workspace edit as rhun does: the files that are not open open, then each
// file's edits are made from the last to the first and reported at the next frame: edits for a
// document the extension hears, opened (at the version after them) for a file that just opened
func (h *host) apply(groups []wsGroup) {
	for _, g := range groups {
		d, fresh := g.t.d, g.t.d == nil
		if fresh {
			d = h.newDoc(g.t.path, langOf(g.t.path), g.t.text)
		}
		var recs []edit
		for i := len(g.edits) - 1; i >= 0; i-- {
			e := g.edits[i]
			d.text = append(append(append([]byte{}, d.text[:e.start]...), e.text...), d.text[e.end:]...)
			for _, r := range records(e.start, e.end-e.start, e.text) {
				d.cur, d.anchor = moved(d.cur, r), moved(d.anchor, r)
				recs = append(recs, r)
			}
		}
		d.version += int64(len(recs))
		switch {
		case fresh:
			h.write(fmt.Sprintf("0 opened %d %d %s %s\n", d.id, d.version, dash(d.lang), d.path))
		case len(recs) > 0:
			h.afterEdit(d, recs...)
		}
	}
}

// moved is where a position goes when an edit record is made: with the text around it
func moved(p int, r edit) int {
	switch {
	case r.del > 0 && p >= r.start+r.del:
		return p - r.del
	case r.del > 0 && p > r.start:
		return r.start
	case r.del == 0 && p > r.start:
		return p + len(r.ins)
	}
	return p
}

// ---- the requests ----

// emptyAnswer says whether an answer is ok ID 0: rhun takes it as nothing, though the empty
// answer the spec gives is the body "0" LF
func (h *host) emptyAnswer(f string, m message) bool {
	if w := strings.Fields(m.line); len(w) == 3 && w[0] == "ok" && w[2] == "0" && m.err == nil {
		h.warn(f, "the answer ok ID 0 has no body: rhun takes it as nothing, but the empty answer is the body \"0\" LF")
		return true
	}
	return false
}

// errorAnswer says whether an answer is error ID CODE TEXT, which counts as an empty answer (a
// rename's refused is shown to the user)
func (h *host) errorAnswer(f string, m message) bool {
	w := strings.SplitN(m.line, " ", 4)
	if w[0] != "error" {
		return false
	}
	text := ""
	if len(w) == 4 {
		text = strings.TrimSpace(w[3])
	}
	switch {
	case len(w) < 3:
		// hostReply failed it already
	case f == "rename" && w[2] == "refused":
		if text == "" {
			h.warn(f, "error ID refused without TEXT: rhun shows TEXT to say why nothing can be renamed there")
		} else if len(text) > 200 {
			h.warn(f, fmt.Sprintf("the refusal's TEXT is %d bytes: rhun shows 200", len(text)))
		}
		h.ok("refuses a rename with error ID refused TEXT (rhun shows the text)")
	case w[2] == "unknown":
		h.warn(f, fmt.Sprintf("it agreed %s after hello, yet answers %q: rhun takes that as nothing found", f, m.line))
	default:
		h.ok(fmt.Sprintf("answers a %s request %q (rhun takes an error as nothing found)", f, m.line))
	}
	return true
}

// langWaits are how long rhun waits for each answer (12d)
var langWaits = map[string]time.Duration{
	"definition": 5 * time.Second, "references": 10 * time.Second, "symbols": 5 * time.Second,
	"actions": 3 * time.Second, "rename": 15 * time.Second, "format": 10 * time.Second,
}

// langAsk sends one of the requests of 12d to 12h and returns its answer: false, after failing f,
// when none came, and when it was an error or ok ID 0 (rhun takes those as nothing)
func (h *host) langAsk(f, line string) (message, bool) {
	wait := langWaits[f]
	if wait == 0 {
		wait = 3 * time.Second
	}
	m, ok := h.askWithin(line, wait)
	if !ok {
		h.fail(f, fmt.Sprintf("no answer to a %s request within %v (rhun's wait)", f, wait))
		return m, false
	}
	if h.errorAnswer(f, m) || h.emptyAnswer(f, m) {
		return m, false
	}
	return m, true
}

// checkLangFeatures asks each agreed feature of 12d to 12h on a document its patterns match
func (h *host) checkLangFeatures() {
	has := func(f string) bool { return h.features[f] && h.patterns(f) != "" }
	sample := func(f string) *doc {
		d := h.sampleFor(h.patterns(f))
		h.serve(300 * time.Millisecond)
		return d
	}
	gone := func(f, after string) bool {
		h.serve(700 * time.Millisecond)
		select {
		case err := <-h.exited:
			h.exited <- err
			h.fail(f, "the program exited on "+after)
			return true
		case <-time.After(100 * time.Millisecond):
			return false
		}
	}
	for _, f := range []string{"definition", "references"} {
		if !has(f) {
			continue
		}
		d := sample(f)
		m, ok := h.langAsk(f, fmt.Sprintf("%s %d %d 1 1 invoked -", f, d.id, d.version))
		if !ok {
			continue
		}
		if n, why := h.checkLocations(m); why != "" {
			h.fail(f, why)
		} else {
			h.ok(fmt.Sprintf("answers %s requests with locations rhun can open (%d)", f, n))
		}
	}
	if has("symbols") {
		d := sample("symbols")
		if m, ok := h.langAsk("symbols", fmt.Sprintf("symbols %d %d 1 1 invoked -", d.id, d.version)); ok {
			n, warns, why := checkSymbols(m, d)
			for _, w := range warns {
				h.warn("symbols", w)
			}
			if why != "" {
				h.fail("symbols", why)
			} else {
				h.ok(fmt.Sprintf("answers symbols requests with an outline rhun can show (%d)", n))
			}
		}
	}
	if has("actions") {
		d := sample("actions")
		if m, ok := h.langAsk("actions", fmt.Sprintf("actions %d %d 1 1 1 3", d.id, d.version)); ok {
			acts, why := h.checkActions(m)
			if why != "" {
				h.fail("actions", why)
			} else {
				h.ok(fmt.Sprintf("answers code action requests with actions rhun can list (%d)", len(acts)))
			}
			// the user chooses the first action that sends data back: its edit is made, then the
			// event carries the data
			for _, a := range acts {
				if len(a.data) == 0 {
					continue
				}
				h.apply(a.edit)
				h.write(fmt.Sprintf("0 action %d\n%s", len(a.data), a.data))
				if gone("actions", fmt.Sprintf("the action event of its action %q", a.title)) {
					return
				}
				h.ok(fmt.Sprintf("takes the action event of its chosen action %q", a.title))
				break
			}
		}
	}
	for _, f := range []string{"rename", "format"} {
		if !has(f) {
			continue
		}
		d := sample(f)
		line, only := fmt.Sprintf("rename %d %d 1 1 invoked renamed", d.id, d.version), (*doc)(nil)
		if f == "format" {
			line, only = fmt.Sprintf("format %d %d 4 yes", d.id, d.version), d
		}
		m, ok := h.langAsk(f, line)
		if !ok {
			continue
		}
		b, why := answer(m)
		var gs []wsGroup
		if why == "" {
			gs, _, why = h.checkWorkspaceEdit(b, only)
		}
		if why != "" {
			h.fail(f, why)
			continue
		}
		files := "1 file"
		if len(gs) != 1 {
			files = fmt.Sprintf("%d files", len(gs))
		}
		h.ok(fmt.Sprintf("answers %s requests with a workspace edit rhun can make (%s)", f, files))
		h.apply(gs)
		if gone(f, "the edits of its "+f) {
			return
		}
	}
}

// applyRequest serves the apply request (section 8): a workspace edit checked whole, then made
func (h *host) applyRequest(b []byte, reply func(string), rerr func(string, string), id int64) {
	gs, code, why := h.checkWorkspaceEdit(b, nil)
	if why != "" {
		h.fail("apply", why)
		rerr(code, why)
		return
	}
	reply(fmt.Sprintf("ok %d", id))
	h.apply(gs)
}
