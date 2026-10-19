package main

// Sections 12d to 12h of the spec: definition, references, symbols, code actions, rename and
// format. The checker asks each agreed feature about a document its [provides] patterns match and
// reads the answer as rhun does (src/app/langfeat.s): a record that breaks one of rhun's rules
// makes it drop the whole answer, which fails the check; what the spec asks but rhun copes with (a
// range past the text, a preview with a line break) is a warning. The workspace edits of a rename,
// a format and the chosen code action are checked against the texts they name (an open document
// at its version, or a file on disk as rhun would open it) and made as rhun makes them; the chosen
// action's data then goes back as the action event.

import (
	"bytes"
	"cmp"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// providerFeatures are the features that name files in [provides] (all but views)
var providerFeatures = []string{"complete", "signature", "definition", "references", "symbols", "actions", "rename", "format", "save-actions", "linked-editing"}

// provideKey is a provider feature's key in [provides]
func provideKey(f string) string {
	switch f {
	case "save-actions":
		return "save_actions"
	case "linked-editing":
		return "linked"
	}
	return f
}

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
	p := strings.TrimSpace(h.m.provides[provideKey(f)])
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

// maxWarns is how many warnings one answer reports
const maxWarns = 20

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

// cursor walks a body: header lines and the bytes that follow them; what is odd but allowed
// gathers in warns
type cursor struct {
	b      []byte
	pos    int
	warns  []string
	spaced bool // a header line ended in a space (said once)
}

func (c *cursor) warn(format string, a ...any) {
	if len(c.warns) < maxWarns {
		c.warns = append(c.warns, fmt.Sprintf(format, a...))
	}
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

// fields splits a header line as rhun reads it (ps_word): words between single spaces; nil when
// one is empty (two spaces, or one at the start). rhun also takes one space after the last word,
// which the grammar does not have (a warning).
func (c *cursor) fields(l string) []string {
	t, trailing := strings.CutSuffix(l, " ")
	w := strings.Split(t, " ")
	for _, x := range w {
		if x == "" {
			return nil
		}
	}
	if trailing && !c.spaced {
		c.spaced = true
		c.warn("the header line %q ends in a space: rhun reads it, but the grammar has none", l)
	}
	return w
}

// head reads the first line, N, at most max; why when it is not one
func (c *cursor) head(max int, what string) (int, string) {
	l, ok := c.line()
	if !ok {
		return 0, "the body has no first line N"
	}
	n := -1
	if w := c.fields(l); len(w) == 1 {
		n = count(w[0])
	}
	if n < 0 || n > max {
		return 0, fmt.Sprintf("the first line %q is not N, 0 to %d %s", l, max, what)
	}
	return n, ""
}

// numbers says which of the words is not a number of at most 10 digits (rhun's ps_num), or ""
func numbers(w []string) string {
	for _, x := range w {
		if count(x) < 0 {
			return fmt.Sprintf("%q is not a number of at most 10 digits", x)
		}
	}
	return ""
}

// span is the range L1 C1 L2 C2 (the first four words) in a text: byte offsets, or why not
func span(d *doc, w []string) (int, int, string) {
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

// pathOK says whether rhun reads p as a PATH (lf_path_ok): "#" and 1 to 18 digits, or an absolute
// path ("/..."; on Windows also "\...", "X:/..." and "X:\..."), without a NUL, CR or LF
func pathOK(p []byte) bool {
	if len(p) == 0 || bytes.ContainsAny(p, "\x00\r\n") {
		return false
	}
	if p[0] == '#' {
		if len(p) < 2 || len(p) > 19 {
			return false
		}
		for _, c := range p[1:] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	if p[0] == '/' {
		return true
	}
	return runtime.GOOS == "windows" && (p[0] == '\\' || len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\'))
}

// badPath is why rhun does not read a PATH
func badPath(p []byte) string {
	return fmt.Sprintf("the path %q is neither absolute nor #DOC (or holds a NUL or a line break)", p)
}

// place is the text a PATH names: an open document, or a file on disk as rhun would open it
type place struct {
	d    *doc   // the open document, nil for a file that is not open
	path string // as the answer gave it
	text []byte
	crlf bool
}

// at is the text as a document, for positions
func (t place) at() *doc {
	if t.d != nil {
		return t.d
	}
	return &doc{path: t.path, text: t.text, crlf: t.crlf}
}

// openDoc is the document a PATH rhun reads names when it is open (#DOC, or an open file's path),
// or nil
func (h *host) openDoc(p []byte) *doc {
	s := string(p)
	if s[0] == '#' {
		id, _ := strconv.ParseInt(s[1:], 10, 64)
		return h.docs[id]
	}
	for _, id := range h.docIDs() {
		if d := h.docs[id]; d.path == s {
			return d
		}
	}
	return nil
}

// lookup finds what a PATH rhun reads names: an open document, or a file rhun would open as text;
// why not
func (h *host) lookup(p []byte) (place, string) {
	if d := h.openDoc(p); d != nil {
		return place{d: d, path: string(p), text: d.text, crlf: d.crlf}, ""
	}
	if p[0] == '#' {
		return place{}, fmt.Sprintf("%s names a document rhun never opened", p)
	}
	text, why, _, crlf := loadFileContext(string(p))
	if why != "" {
		return place{}, fmt.Sprintf("%s is not open, and %s", p, why)
	}
	return place{path: string(p), text: text, crlf: crlf}, ""
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

// checkLocations validates a locations answer (12d): the number of locations and what is odd but
// allowed, or why rhun drops it. rhun reads the numbers and the path's form; where a location
// leads is found when it is chosen (a range past the text goes to the nearest place).
func (h *host) checkLocations(m message) (int, []string, string) {
	b, why := answer(m)
	if why != "" {
		return 0, nil, why
	}
	if len(b) > 1<<20 {
		return 0, nil, "the locations are over 1 MB"
	}
	c := &cursor{b: b}
	n, why := c.head(10000, "locations")
	if why != "" {
		return 0, nil, why
	}
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, nil, fmt.Sprintf("location %d has no header line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 6 {
			return 0, nil, fmt.Sprintf("location %d's header %q is not L1 C1 L2 C2 PATH_LEN TEXT_LEN", i, l)
		}
		if why := numbers(w); why != "" {
			return 0, nil, fmt.Sprintf("location %d: %s", i, why)
		}
		pl, tl := count(w[4]), count(w[5])
		if pl < 1 || pl > 4096 {
			return 0, nil, fmt.Sprintf("location %d: PATH_LEN %q is not 1 to 4096", i, w[4])
		}
		path, ok1 := c.take(pl)
		text, ok2 := c.take(tl)
		if !ok1 || !ok2 {
			return 0, nil, fmt.Sprintf("location %d's bytes run past the body", i)
		}
		if !pathOK(path) {
			return 0, nil, fmt.Sprintf("location %d: %s", i, badPath(path))
		}
		if t, why := h.lookup(path); why != "" {
			c.warn("location %d: %s: rhun lists it, but cannot go there", i, why)
		} else if _, _, why := span(t.at(), w); why != "" {
			c.warn("location %d in %s: %s (rhun goes to the nearest place)", i, path, why)
		}
		if why := oneLine("the preview", text); why != "" {
			c.warn("location %d: %s", i, why)
		}
	}
	if c.rest() > 0 {
		return 0, nil, fmt.Sprintf("%d bytes after the %d locations N says", c.rest(), n)
	}
	return n, c.warns, ""
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
	odd := map[string]bool{}
	prev := 0
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, nil, fmt.Sprintf("symbol %d has no header line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 8 {
			return 0, nil, fmt.Sprintf("symbol %d's header %q is not L1 C1 L2 C2 KIND DEPTH NAME_LEN DETAIL_LEN", i, l)
		}
		if why := numbers(append(w[:4:4], w[5:]...)); why != "" {
			return 0, nil, fmt.Sprintf("symbol %d: %s", i, why)
		}
		if len(w[4]) > 40 {
			return 0, nil, fmt.Sprintf("symbol %d: KIND %q is over 40 bytes", i, w[4])
		}
		nl, dl := count(w[6]), count(w[7])
		if nl < 1 || nl > 1000 {
			return 0, nil, fmt.Sprintf("symbol %d: NAME_LEN %q is not 1 to 1000", i, w[6])
		}
		if dl > 1000 {
			return 0, nil, fmt.Sprintf("symbol %d: DETAIL_LEN %q is not 0 to 1000", i, w[7])
		}
		name, ok1 := c.take(nl)
		detail, ok2 := c.take(dl)
		if !ok1 || !ok2 {
			return 0, nil, fmt.Sprintf("symbol %d's bytes run past the body", i)
		}
		if !symbolKinds[w[4]] && !odd[w[4]] {
			odd[w[4]] = true
			c.warn("symbol %d: KIND %q is not one 12e lists (rhun shows it as given)", i, w[4])
		}
		if why := oneLine("the name", name); why != "" {
			c.warn("symbol %d: %s", i, why)
		}
		if why := oneLine("the detail", detail); why != "" {
			c.warn("symbol %d: %s", i, why)
		}
		a, _, why := span(d, w)
		switch {
		case why != "":
			c.warn("symbol %d: %s (rhun goes to the nearest place)", i, why)
		case a < prev:
			c.warn("symbol %d starts before the one above it: symbols come in document order", i)
		}
		if why == "" {
			prev = a
		}
	}
	if c.rest() > 0 {
		return 0, nil, fmt.Sprintf("%d bytes after the %d symbols N says", c.rest(), n)
	}
	return n, c.warns, ""
}

// codeAction is one action of a 12f answer: its edit is read when the list comes and checked
// against the documents when it is chosen
type codeAction struct {
	kind, title string
	edit, data  []byte
}

// checkActions validates a code actions answer (12f): the actions and what is odd but allowed, or
// why the answer is broken. rhun keeps each action's edit whole and checks it when the action is
// chosen; an edit that cannot be read is broken whatever the documents say, so it fails here, while
// one made for another version of a document may still be right when chosen.
func (h *host) checkActions(m message) ([]codeAction, []string, string) {
	b, why := answer(m)
	if why != "" {
		return nil, nil, why
	}
	if len(b) > 4<<20 {
		return nil, nil, "the actions are over 4 MB"
	}
	c := &cursor{b: b}
	n, why := c.head(1000, "actions")
	if why != "" {
		return nil, nil, why
	}
	var acts []codeAction
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return nil, nil, fmt.Sprintf("action %d has no header line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 4 {
			return nil, nil, fmt.Sprintf("action %d's header %q is not KIND TITLE_LEN EDIT_LEN DATA_LEN", i, l)
		}
		if len(w[0]) > 40 {
			return nil, nil, fmt.Sprintf("action %d: KIND %q is over 40 bytes", i, w[0])
		}
		if why := numbers(w[1:]); why != "" {
			return nil, nil, fmt.Sprintf("action %d: %s", i, why)
		}
		tl, el, dl := count(w[1]), count(w[2]), count(w[3])
		if tl < 1 || tl > 1000 {
			return nil, nil, fmt.Sprintf("action %d: TITLE_LEN %q is not 1 to 1000", i, w[1])
		}
		title, ok1 := c.take(tl)
		edit, ok2 := c.take(el)
		data, ok3 := c.take(dl)
		if !ok1 || !ok2 || !ok3 {
			return nil, nil, fmt.Sprintf("action %d's bytes run past the body", i)
		}
		if why := oneLine("the title", title); why != "" {
			c.warn("action %d: %s", i, why)
		}
		if len(edit) > 0 {
			if code, why := h.editGrammarFor(edit); why != "" {
				return nil, nil, fmt.Sprintf("action %d (%q): its edit cannot be made whatever the documents say (rhun would answer an apply %s): %s",
					i, title, code, why)
			}
		}
		acts = append(acts, codeAction{kind: w[0], title: string(title), edit: edit, data: data})
	}
	if c.rest() > 0 {
		return nil, nil, fmt.Sprintf("%d bytes after the %d actions N says", c.rest(), n)
	}
	return acts, c.warns, ""
}

// wsEdit is an edit of a workspace edit: its range as written, bytes start to end of its file's
// text once checked, and the new text
type wsEdit struct {
	at         []string
	start, end int
	text       []byte
}

// wsGroup is a file of a workspace edit: its PATH, VERSION (-1 for "-") and edits; once checked,
// the text it names and its edits in the order rhun makes them
type wsGroup struct {
	path    []byte
	version int64
	edits   []wsEdit
	t       place
}

// parseWorkspaceEdit reads a workspace edit (12g) as rhun does before it looks at any document or
// file (ws_apply): the grammar and the caps. A refused edit has the code rhun answers an apply
// request with (syntax or toobig) and the reason.
func parseWorkspaceEdit(b []byte) ([]wsGroup, string, string) {
	bad := func(code, format string, a ...any) ([]wsGroup, string, string) {
		return nil, code, fmt.Sprintf(format, a...)
	}
	if len(b) > 4<<20 {
		return bad("toobig", "the workspace edit is over 4 MB")
	}
	c := &cursor{b: b}
	l, ok := c.line()
	n := -1
	if w := c.fields(l); ok && len(w) == 1 {
		n = count(w[0])
	}
	switch {
	case n < 0:
		return bad("syntax", "the first line %q is not N, the number of files", l)
	case n > 1000:
		return bad("toobig", "%d files: at most 1000", n)
	}
	var groups []wsGroup
	total := 0
	for g := 1; g <= n; g++ {
		l, ok := c.line()
		if !ok {
			return bad("syntax", "file %d has no header line (N says %d)", g, n)
		}
		w := c.fields(l)
		if len(w) != 3 {
			return bad("syntax", "file %d's header %q is not PATH_LEN VERSION M", g, l)
		}
		pl := count(w[0])
		if pl < 1 || pl > 4096 {
			return bad("syntax", "file %d: PATH_LEN %q is not 1 to 4096", g, w[0])
		}
		version := int64(-1)
		if v := w[1]; v != "-" {
			if len(v) > 18 || strings.Trim(v, "0123456789") != "" {
				return bad("syntax", "file %d: VERSION %q is neither - nor a number of at most 18 digits", g, v)
			}
			version, _ = strconv.ParseInt(v, 10, 64)
		}
		m := count(w[2])
		if m < 0 {
			return bad("syntax", "file %d: M %q is not a number of at most 10 digits", g, w[2])
		}
		path, ok := c.take(pl)
		if !ok {
			return bad("syntax", "file %d's path runs past the body", g)
		}
		if total += m; total > 100000 {
			return bad("toobig", "over 100000 edits")
		}
		edits := make([]wsEdit, 0, m)
		for e := 1; e <= m; e++ {
			l, ok := c.line()
			if !ok {
				return bad("syntax", "file %d, edit %d has no header line (M says %d)", g, e, m)
			}
			w := c.fields(l)
			if len(w) != 5 {
				return bad("syntax", "file %d, edit %d: %q is not L1 C1 L2 C2 LEN", g, e, l)
			}
			if why := numbers(w); why != "" {
				return bad("syntax", "file %d, edit %d: %s", g, e, why)
			}
			text, ok := c.take(count(w[4]))
			if !ok {
				return bad("syntax", "file %d, edit %d's text runs past the body", g, e)
			}
			edits = append(edits, wsEdit{at: w[:4], text: text})
		}
		groups = append(groups, wsGroup{path: path, version: version, edits: edits})
	}
	if c.rest() > 0 {
		return bad("syntax", "%d bytes after the %d files N says", c.rest(), n)
	}
	return groups, "", ""
}

// editGrammar is what rhun refuses in a workspace edit whatever the documents say: its grammar and
// caps, and each PATH's form
func editGrammar(b []byte) ([]wsGroup, string, string) {
	gs, code, why := parseWorkspaceEdit(b)
	if why != "" {
		return nil, code, why
	}
	for i, g := range gs {
		if !pathOK(g.path) {
			return nil, "nofile", fmt.Sprintf("file %d: %s", i+1, badPath(g.path))
		}
	}
	return gs, "", ""
}

// checkWorkspaceEdit validates a workspace edit (12g) as rhun does before it changes anything, in
// rhun's order: the whole grammar; then each file's document (an open one at its version, "-"
// only while it has no unsaved changes; "-" for a file that is not open); then the files that are
// not open, as rhun would open them; then each file's edits against its text, sorted, apart. only,
// when not nil, is the one document it may change (a format). A refused edit has the code rhun
// answers an apply request with (section 8) and the reason.
func (h *host) checkWorkspaceEdit(b []byte, only *doc) ([]wsGroup, string, string) {
	gs, code, why := parseWorkspaceEdit(b)
	if why != "" {
		return nil, code, why
	}
	bad := func(code, format string, a ...any) ([]wsGroup, string, string) {
		return nil, code, fmt.Sprintf(format, a...)
	}
	for i := range gs {
		g, n := &gs[i], i+1
		if !pathOK(g.path) {
			return bad("nofile", "file %d: %s", n, badPath(g.path))
		}
		d := h.openDoc(g.path)
		if only != nil && d != only {
			return bad("nofile", "file %d: %s is not the document asked about (#%d, %s)", n, g.path, only.id, only.path)
		}
		switch {
		case d == nil && g.path[0] == '#':
			return bad("nodoc", "file %d: %s names a document rhun never opened", n, g.path)
		case d == nil && g.version >= 0:
			return bad("nodoc", "file %d: VERSION %d for %s, which is not open: - is for a file that is not open", n, g.version, g.path)
		case d == nil:
			continue
		case g.version < 0 && d.dirty:
			return bad("stale", "file %d: VERSION - for %s, which is open with unsaved changes (at version %d): - is for its text on disk", n, g.path, d.version)
		case g.version >= 0 && g.version != d.version:
			return bad("stale", "file %d: VERSION %d for %s, which is at version %d", n, g.version, g.path, d.version)
		}
		g.t = place{d: d, path: string(g.path), text: d.text, crlf: d.crlf}
	}
	for i := range gs {
		g := &gs[i]
		if g.t.d != nil {
			continue
		}
		text, why, _, crlf := loadFileContext(string(g.path))
		if why != "" {
			return bad("nofile", "file %d: %s is not open, and %s", i+1, g.path, why)
		}
		g.t = place{path: string(g.path), text: text, crlf: crlf}
	}
	for i := range gs {
		g, n := &gs[i], i+1
		for _, p := range gs[:i] {
			if g.t.d != nil && p.t.d == g.t.d || g.t.d == nil && p.t.d == nil && p.t.path == g.t.path {
				return bad("syntax", "file %d: %s has a group already (one group per file)", n, g.path)
			}
		}
		d := g.t.at()
		for e := range g.edits {
			x := &g.edits[e]
			a, z, why := span(d, x.at)
			if why != "" {
				return bad("badpos", "file %d, edit %d: %s", n, e+1, why)
			}
			x.start, x.end = a, z
		}
		// rhun sorts them by start, then by end (an insertion before a replacement that starts
		// where it is), equal ones keeping their order (we_cmp), then refuses one that starts
		// before the one above it ends
		slices.SortStableFunc(g.edits, func(x, y wsEdit) int { return cmp.Or(x.start-y.start, x.end-y.end) })
		for e := 1; e < len(g.edits); e++ {
			if p, x := g.edits[e-1], g.edits[e]; p.end > x.start {
				return bad("badpos", "file %d: two edits overlap (bytes %d to %d and %d to %d of %s)", n,
					p.start, p.end, x.start, x.end, g.path)
			}
		}
	}
	return gs, "", ""
}

// apply makes a checked workspace edit as rhun does: the files that are not open open, then each
// file's edits are made from the last to the first and reported at the next frame: edits for a
// document the extension hears, opened (at the version after them) for a file that just opened.
// Nothing is saved: a document that changed has unsaved changes.
func (h *host) apply(groups []wsGroup) {
	for _, g := range groups {
		d, fresh := g.t.d, g.t.d == nil
		if fresh {
			d = h.newDoc(g.t.path, langOf(g.t.path), g.t.text)
			d.crlf = g.t.crlf
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
		if len(recs) > 0 {
			d.dirty = true
		}
		switch {
		case fresh:
			h.write(fmt.Sprintf("0 opened %d %d %s %s\n", d.id, d.version, dash(d.lang), d.path))
		case len(recs) > 0:
			h.afterEdit(d, recs...)
		}
	}
}

// chooseAction is the user choosing a code action, as rhun does it: its edit, when it has one,
// checked whole and made, then its data, when it has some, sent as the action event. An edit rhun
// refuses changes nothing and sends nothing: why is its code and reason.
func (h *host) chooseAction(a codeAction) string {
	if len(a.edit) > 0 {
		if _, code, _, why := h.takeEdit(a.edit, nil); why != "" {
			return code + ": " + why
		}
	}
	if len(a.data) > 0 {
		h.write(fmt.Sprintf("0 action %d\n%s", len(a.data), a.data))
	}
	return ""
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
		if f == "save-actions" {
			h.ok("answers save-actions with no edits")
			return true
		}
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
	"actions": 3 * time.Second, "rename": 15 * time.Second, "format": 10 * time.Second, "save-actions": 2 * time.Second,
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
		n, warns, why := h.checkLocations(m)
		for _, w := range warns {
			h.warn(f, w)
		}
		if why != "" {
			h.fail(f, why)
		} else {
			h.ok(fmt.Sprintf("answers %s requests with locations rhun can list (%d)", f, n))
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
			acts, warns, why := h.checkActions(m)
			for _, w := range warns {
				h.warn("actions", w)
			}
			if why != "" {
				h.fail("actions", why)
			} else {
				h.ok(fmt.Sprintf("answers code action requests with actions rhun can list (%d)", len(acts)))
			}
			// the user chooses the first action that sends data back, else the first with an edit:
			// its edit is checked and made, then the event carries the data
			chosen := slices.IndexFunc(acts, func(a codeAction) bool { return len(a.data) > 0 })
			if chosen < 0 {
				chosen = slices.IndexFunc(acts, func(a codeAction) bool { return len(a.edit) > 0 })
			}
			if chosen >= 0 {
				a := acts[chosen]
				if why := h.chooseAction(a); why != "" {
					h.fail("actions", fmt.Sprintf("its action %q, chosen at once, has an edit rhun refuses (nothing changes, and no action event is sent): %s", a.title, why))
				} else if gone("actions", fmt.Sprintf("its chosen action %q", a.title)) {
					return
				} else if len(a.data) > 0 {
					h.ok(fmt.Sprintf("takes the action event of its chosen action %q", a.title))
				} else {
					h.ok(fmt.Sprintf("its chosen action %q has an edit rhun can make", a.title))
				}
			}
		}
	}
	for _, f := range []string{"rename", "format", "save-actions"} {
		if !has(f) {
			continue
		}
		d := sample(f)
		line, only := fmt.Sprintf("rename %d %d 1 1 invoked renamed", d.id, d.version), (*doc)(nil)
		if f == "format" {
			line, only = fmt.Sprintf("format %d %d 4 yes", d.id, d.version), d
		}
		if f == "save-actions" {
			line, only = fmt.Sprintf("save-actions %d %d source", d.id, d.version), d
		}
		m, ok := h.langAsk(f, line)
		if !ok {
			continue
		}
		b, why := answer(m)
		n := 0
		if why == "" {
			n, _, _, why = h.takeEdit(b, only)
		}
		if why != "" {
			h.fail(f, why)
			continue
		}
		files := "1 file"
		if n != 1 {
			files = fmt.Sprintf("%d files", n)
		}
		h.ok(fmt.Sprintf("answers %s requests with a workspace edit rhun can make (%s)", f, files))
		if gone(f, "the edits of its "+f) {
			return
		}
	}
}

// applyRequest serves the apply request (section 8): a workspace edit checked whole, then made
func (h *host) applyRequest(b []byte, reply func(string), rerr func(string, string), id int64) {
	if h.features["workspace-edits"] {
		// a transaction: error ID CODE ENTRY TEXT, then made (documents first, then the reply)
		t, code, entry, why := h.checkTx(b)
		if why != "" {
			h.fail("apply", why)
			rerr(code, fmt.Sprintf("%d %s", entry, strings.TrimPrefix(why, fmt.Sprintf("entry %d: ", entry))))
			return
		}
		reply(fmt.Sprintf("ok %d", id))
		for _, w := range t.warns {
			h.warn("workspace-edits", w)
		}
		h.makeTx(t)
		return
	}
	gs, code, why := h.checkWorkspaceEdit(b, nil)
	if why != "" {
		h.fail("apply", why)
		rerr(code, why)
		return
	}
	reply(fmt.Sprintf("ok %d", id))
	h.apply(gs)
}
