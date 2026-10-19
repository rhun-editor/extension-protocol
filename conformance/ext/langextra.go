package main

// Protocol 1.1's hover-query, selection-ranges, hierarchy, workspace-symbols, document-links and
// file-diagnostics (v1.1/v1.1.md, "Language features"): the checker sends each request whose
// feature was agreed and whose [provides] key matches a document (hover, selection_ranges,
// call_hierarchy with calls-in and calls-out about the first root, type_hierarchy with supertypes
// and subtypes, workspace_symbols, document_links) and reads the answer as rhun does, and it serves
// the extension's file-diagnostics and file-diagnostics-reset. As in langfeat.go, what rhun drops
// or refuses fails the check and what it copes with is a warning.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- reading the answers ----

// before says whether position a (LINE COL words) comes before b
func before(a, b []string) bool {
	al, ac, bl, bc := count(a[0]), count(a[1]), count(b[0]), count(b[1])
	return al < bl || al == bl && ac < bc
}

// rangeWords says why four words are not a range L1 C1 L2 C2 that does not run backwards, or ""
func rangeWords(w []string) string {
	if why := numbers(w); why != "" {
		return why
	}
	if before(w[2:4], w[0:2]) {
		return fmt.Sprintf("the range %s ends before it starts", strings.Join(w, " "))
	}
	return ""
}

// inside says whether the range a (four words) contains b
func inside(a, b []string) bool { return !before(b[0:2], a[0:2]) && !before(a[2:4], b[2:4]) }

// checkHover validates a hover-query answer about d: empty (nothing to say), or "L1 C1 L2 C2" LF and
// the text, at most 64 KiB; what is odd but allowed, or why rhun drops it
func checkHover(m message, d *doc) ([]string, string) {
	b, why := answer(m)
	if why != "" || len(b) == 0 {
		return nil, why
	}
	if len(b) > 64<<10 {
		return nil, "the answer is over 64 KiB"
	}
	c := &cursor{b: b}
	l, ok := c.line()
	w := c.fields(l)
	if !ok || len(w) != 4 {
		return nil, fmt.Sprintf("the first line %q is not L1 C1 L2 C2", l)
	}
	if why := rangeWords(w); why != "" {
		return nil, why
	}
	if strings.Join(w, " ") != "0 0 0 0" {
		if _, _, why := span(d, w); why != "" {
			c.warn("the range the text describes: %s", why)
		}
	}
	if c.rest() == 0 {
		c.warn("a range without a text: ok ID 0 says nothing")
	}
	if !utf8.Valid(b[c.pos:]) {
		c.warn("the text is not UTF-8")
	}
	return c.warns, ""
}

// checkSelectionRanges validates a selection-ranges answer about d for n cursors: the number of
// groups and what is odd but allowed, or why rhun drops the answer or a group's ranges
func checkSelectionRanges(m message, d *doc, n int) (int, []string, string) {
	b, why := answer(m)
	if why != "" {
		return 0, nil, why
	}
	if len(b) > 64<<10 {
		return 0, nil, "the answer is over 64 KiB"
	}
	c := &cursor{b: b}
	groups := 0
	for c.rest() > 0 {
		groups++
		if groups > n {
			return 0, nil, fmt.Sprintf("more groups than the %d cursors asked about", n)
		}
		l, _ := c.line()
		w := c.fields(l)
		mm := -1
		if len(w) == 1 {
			mm = count(w[0])
		}
		if mm < 1 || mm > 64 {
			return 0, nil, fmt.Sprintf("group %d: %q is not M, 1 to 64", groups, l)
		}
		var prev []string
		for r := 1; r <= mm; r++ {
			l, ok := c.line()
			if !ok {
				return 0, nil, fmt.Sprintf("group %d has no range %d (M says %d)", groups, r, mm)
			}
			w := c.fields(l)
			if len(w) != 4 {
				return 0, nil, fmt.Sprintf("group %d, range %d: %q is not L1 C1 L2 C2", groups, r, l)
			}
			if why := rangeWords(w); why != "" {
				return 0, nil, fmt.Sprintf("group %d, range %d: %s", groups, r, why)
			}
			if _, _, why := span(d, w); why != "" {
				c.warn("group %d, range %d: %s", groups, r, why)
			}
			switch {
			case prev != nil && strings.Join(prev, " ") == strings.Join(w, " "):
				c.warn("group %d, range %d equals the one before: rhun skips it", groups, r)
			case prev != nil && !inside(w, prev):
				return 0, nil, fmt.Sprintf("group %d, range %d (%s) does not contain the one before (%s): rhun keeps only the ranges before it",
					groups, r, l, strings.Join(prev, " "))
			}
			prev = w
		}
	}
	return groups, c.warns, ""
}

// hItem is an item of a hierarchy answer: what the checker asks about next
type hItem struct {
	kind, name string
	path, data []byte
	sites      int
}

// readItem reads item i of a hierarchy answer at c (with calls, its R and call sites, which are in
// sites' file: the caller's for calls-in, the asked item's for calls-out, its own when nil); why
// rhun drops the answer
func (h *host) readItem(c *cursor, i int, calls bool, sites []byte, odd map[string]bool) (hItem, string) {
	what := "item"
	if calls {
		what = "call"
	}
	l, ok := c.line()
	if !ok {
		return hItem{}, fmt.Sprintf("%s %d has no header line", what, i)
	}
	w := c.fields(l)
	want := 13
	if calls {
		want = 14
	}
	if len(w) != want {
		head := "KIND L1 C1 L2 C2 S1 SC1 S2 SC2 PATH_LEN NAME_LEN DETAIL_LEN DATA_LEN"
		if calls {
			head += " R"
		}
		return hItem{}, fmt.Sprintf("%s %d's header %q is not %s", what, i, l, head)
	}
	if len(w[0]) > 40 {
		return hItem{}, fmt.Sprintf("%s %d: KIND %q is over 40 bytes", what, i, w[0])
	}
	if why := numbers(w[1:]); why != "" {
		return hItem{}, fmt.Sprintf("%s %d: %s", what, i, why)
	}
	pl, nl, dl, xl := count(w[9]), count(w[10]), count(w[11]), count(w[12])
	switch {
	case pl < 1 || pl > 4096:
		return hItem{}, fmt.Sprintf("%s %d: PATH_LEN %q is not 1 to 4096", what, i, w[9])
	case nl < 1 || nl > 1000:
		return hItem{}, fmt.Sprintf("%s %d: NAME_LEN %q is not 1 to 1000", what, i, w[10])
	case dl > 1000:
		return hItem{}, fmt.Sprintf("%s %d: DETAIL_LEN %q is not 0 to 1000", what, i, w[11])
	case xl > 16<<10:
		return hItem{}, fmt.Sprintf("%s %d: DATA_LEN %d is over 16 KiB", what, i, xl)
	}
	path, ok1 := c.take(pl)
	name, ok2 := c.take(nl)
	detail, ok3 := c.take(dl)
	data, ok4 := c.take(xl)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return hItem{}, fmt.Sprintf("%s %d's bytes run past the body", what, i)
	}
	if !pathOK(path) {
		return hItem{}, fmt.Sprintf("%s %d: %s", what, i, badPath(path))
	}
	if !symbolKinds[w[0]] && !odd[w[0]] {
		odd[w[0]] = true
		c.warn("%s %d: KIND %q is not one 12e lists (rhun shows it as given)", what, i, w[0])
	}
	if why := oneLine("the name", name); why != "" {
		c.warn("%s %d: %s", what, i, why)
	}
	if why := oneLine("the detail", detail); why != "" {
		c.warn("%s %d: %s", what, i, why)
	}
	whole, sel := w[1:5], w[5:9]
	if why := rangeWords(whole); why != "" {
		c.warn("%s %d: the symbol's range: %s", what, i, why)
	} else if rangeWords(sel) != "" || !inside(whole, sel) {
		c.warn("%s %d: its name's range %s is not inside the symbol's %s", what, i, strings.Join(sel, " "), strings.Join(whole, " "))
	}
	t, missing := h.lookup(path)
	if missing != "" {
		c.warn("%s %d: %s: rhun lists it, but cannot go there", what, i, missing)
	} else if _, _, why := span(t.at(), whole); why != "" {
		c.warn("%s %d in %s: %s (rhun goes to the nearest place)", what, i, path, why)
	}
	it := hItem{kind: w[0], name: string(name), path: path, data: data}
	if !calls {
		return it, ""
	}
	r := count(w[13])
	if r < 1 || r > 1000 {
		return hItem{}, fmt.Sprintf("call %d: R %q is not 1 to 1000 call sites", i, w[13])
	}
	in := path
	if sites != nil {
		in = sites
	}
	st, missing := h.lookup(in)
	for s := 1; s <= r; s++ {
		l, ok := c.line()
		if !ok {
			return hItem{}, fmt.Sprintf("call %d has no call site %d (R says %d)", i, s, r)
		}
		w := c.fields(l)
		if len(w) != 4 {
			return hItem{}, fmt.Sprintf("call %d, call site %d: %q is not L1 C1 L2 C2", i, s, l)
		}
		if why := rangeWords(w); why != "" {
			return hItem{}, fmt.Sprintf("call %d, call site %d: %s", i, s, why)
		}
		if missing == "" {
			if _, _, why := span(st.at(), w); why != "" {
				c.warn("call %d, call site %d in %s: %s", i, s, in, why)
			}
		}
	}
	it.sites = r
	return it, ""
}

// checkHierarchy validates a hierarchy answer: roots (call-hierarchy, type-hierarchy: at most 64
// items and 256 KiB), items (supertypes, subtypes) or calls (calls-in, calls-out; sites as
// readItem's), at most 1000 and 1 MiB; the items and what is odd but allowed, or why rhun drops it
func (h *host) checkHierarchy(m message, verb string, sites []byte) ([]hItem, []string, string) {
	b, why := answer(m)
	if why != "" {
		return nil, nil, why
	}
	max, maxBody := 1000, 1<<20
	if verb == "call-hierarchy" || verb == "type-hierarchy" {
		max, maxBody = 64, 256<<10
	}
	if len(b) > maxBody {
		return nil, nil, fmt.Sprintf("the answer is over %d KiB", maxBody>>10)
	}
	calls := verb == "calls-in" || verb == "calls-out"
	if verb == "calls-in" {
		sites = nil
	}
	c := &cursor{b: b}
	n, why := c.head(max, "items")
	if why != "" {
		return nil, nil, why
	}
	odd := map[string]bool{}
	var items []hItem
	for i := 1; i <= n; i++ {
		it, why := h.readItem(c, i, calls, sites, odd)
		if why != "" {
			return nil, nil, why
		}
		items = append(items, it)
	}
	if c.rest() > 0 {
		return nil, nil, fmt.Sprintf("%d bytes after the %d records N says", c.rest(), n)
	}
	return items, c.warns, ""
}

// checkWorkspaceSymbols validates a workspace-symbols answer or part: the number of symbols and
// what is odd but allowed, or why rhun drops it
func (h *host) checkWorkspaceSymbols(m message) (int, []string, string) {
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
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, nil, fmt.Sprintf("symbol %d has no header line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 8 {
			return 0, nil, fmt.Sprintf("symbol %d's header %q is not L1 C1 L2 C2 KIND PATH_LEN NAME_LEN DETAIL_LEN", i, l)
		}
		if why := numbers(append(w[:4:4], w[5:]...)); why != "" {
			return 0, nil, fmt.Sprintf("symbol %d: %s", i, why)
		}
		if len(w[4]) > 40 {
			return 0, nil, fmt.Sprintf("symbol %d: KIND %q is over 40 bytes", i, w[4])
		}
		pl, nl, dl := count(w[5]), count(w[6]), count(w[7])
		switch {
		case pl < 1 || pl > 4096:
			return 0, nil, fmt.Sprintf("symbol %d: PATH_LEN %q is not 1 to 4096", i, w[5])
		case nl < 1 || nl > 1000:
			return 0, nil, fmt.Sprintf("symbol %d: NAME_LEN %q is not 1 to 1000", i, w[6])
		case dl > 1000:
			return 0, nil, fmt.Sprintf("symbol %d: DETAIL_LEN %q is not 0 to 1000", i, w[7])
		}
		path, ok1 := c.take(pl)
		name, ok2 := c.take(nl)
		detail, ok3 := c.take(dl)
		if !ok1 || !ok2 || !ok3 {
			return 0, nil, fmt.Sprintf("symbol %d's bytes run past the body", i)
		}
		if !pathOK(path) {
			return 0, nil, fmt.Sprintf("symbol %d: %s", i, badPath(path))
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
		if t, why := h.lookup(path); why != "" {
			c.warn("symbol %d: %s: rhun lists it, but cannot go there", i, why)
		} else if _, _, why := span(t.at(), w); why != "" {
			c.warn("symbol %d in %s: %s (rhun goes to the nearest place)", i, path, why)
		}
	}
	if c.rest() > 0 {
		return 0, nil, fmt.Sprintf("%d bytes after the %d symbols N says", c.rest(), n)
	}
	return n, c.warns, ""
}

// urlOK says whether s is an http or https URL without white space or control characters
func urlOK(s []byte) bool {
	l := bytes.ToLower(s)
	rest, ok := bytes.CutPrefix(l, []byte("https://"))
	if !ok {
		rest, ok = bytes.CutPrefix(l, []byte("http://"))
	}
	if !ok || len(rest) == 0 {
		return false
	}
	for _, c := range s {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// linkTarget says why a link's TARGET is not an http(s) URL, an absolute path optionally followed
// by :LINE or :LINE:COL, or #DOC, or ""
func linkTarget(t []byte) string {
	if urlOK(t) || pathOK(t) {
		return ""
	}
	return fmt.Sprintf("the target %q is neither an http(s) URL, an absolute path (with :LINE or :LINE:COL) nor #DOC", t)
}

// checkLinks validates a document-links answer about d: the number of links and what is odd but
// allowed, or why rhun drops it
func checkLinks(m message, d *doc) (int, []string, string) {
	b, why := answer(m)
	if why != "" {
		return 0, nil, why
	}
	if len(b) > 1<<20 {
		return 0, nil, "the links are over 1 MB"
	}
	c := &cursor{b: b}
	n, why := c.head(10000, "links")
	if why != "" {
		return 0, nil, why
	}
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, nil, fmt.Sprintf("link %d has no header line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 6 {
			return 0, nil, fmt.Sprintf("link %d's header %q is not L1 C1 L2 C2 TARGET_LEN TIP_LEN", i, l)
		}
		if why := numbers(w); why != "" {
			return 0, nil, fmt.Sprintf("link %d: %s", i, why)
		}
		tl, pl := count(w[4]), count(w[5])
		if tl < 1 || tl > 4096+22 {
			return 0, nil, fmt.Sprintf("link %d: TARGET_LEN %q is not 1 to 4118", i, w[4])
		}
		if pl > 1000 {
			return 0, nil, fmt.Sprintf("link %d: TIP_LEN %q is not 0 to 1000", i, w[5])
		}
		target, ok1 := c.take(tl)
		tip, ok2 := c.take(pl)
		if !ok1 || !ok2 {
			return 0, nil, fmt.Sprintf("link %d's bytes run past the body", i)
		}
		if why := linkTarget(target); why != "" {
			return 0, nil, fmt.Sprintf("link %d: %s", i, why)
		}
		if why := oneLine("the tip", tip); why != "" {
			c.warn("link %d: %s", i, why)
		}
		if _, _, why := span(d, w); why != "" {
			c.warn("link %d: %s (rhun cannot underline it)", i, why)
		}
	}
	if c.rest() > 0 {
		return 0, nil, fmt.Sprintf("%d bytes after the %d links N says", c.rest(), n)
	}
	return n, c.warns, ""
}

// ---- asking ----

// langAskBody is langAsk for a request with a body (its line ends in LEN)
func (h *host) langAskBody(f, line string, body []byte) (message, bool) {
	wait := langWaits[f]
	m, ok := h.askBodyWithin(line, body, wait)
	if !ok {
		h.fail(f, fmt.Sprintf("no answer to a %s request within %v (rhun's wait)", f, wait))
		return m, false
	}
	if h.errorAnswer(f, m) || h.emptyAnswer(f, m) {
		return m, false
	}
	return m, true
}

// askItems asks for the calls or items about a hierarchy item (its data), and reports them
func (h *host) askItems(verb string, it hItem) {
	h.askedItem = it.path
	m, ok := h.langAskBody(verb, fmt.Sprintf("%s %d", verb, len(it.data)), it.data)
	if !ok {
		return
	}
	items, warns, why := h.checkHierarchy(m, verb, it.path)
	for _, w := range warns {
		h.warn(verb, w)
	}
	if why != "" {
		h.fail(verb, fmt.Sprintf("an answer about %q: %s", it.name, why))
		return
	}
	what := "items"
	if strings.HasPrefix(verb, "calls-") {
		what = "calls"
	}
	h.ok(fmt.Sprintf("answers %s about %q with %s rhun can list (%d%s)", verb, it.name, what, len(items), h.partsSaid(h.lastAsk, what)))
}

// askHierarchy asks for the roots at d's start, then about the first root in both directions
func (h *host) askHierarchy(d *doc, verb string, more [2]string) {
	m, ok := h.langAsk(verb, fmt.Sprintf("%s %d %d 1 1 invoked -", verb, d.id, d.version))
	if !ok {
		return
	}
	roots, warns, why := h.checkHierarchy(m, verb, nil)
	for _, w := range warns {
		h.warn(verb, w)
	}
	if why != "" {
		h.fail(verb, why)
		return
	}
	h.ok(fmt.Sprintf("answers %s requests with roots rhun can show (%d)", verb, len(roots)))
	if len(roots) > 0 {
		for _, v := range more {
			h.askItems(v, roots[0])
		}
	}
}

// askMoreLangFeatures sends the requests of this file whose feature was agreed and whose key
// matches a document
func (h *host) askMoreLangFeatures(sample func(string) *doc) {
	key := func(f, k string) bool { return h.features[f] && h.patterns(k) != "" }
	if key("hover-query", "hover") {
		const f = "hover-query"
		d := sample("hover")
		m, ok := h.askWithin(fmt.Sprintf("hover-query %d %d 1 1 pointer -", d.id, d.version), langWaits[f])
		switch {
		case !ok:
			h.fail(f, "no answer to a hover-query request within 3 s (rhun's limit)")
		case h.errorAnswer(f, m):
		default:
			warns, why := checkHover(m, d)
			for _, w := range warns {
				h.warn(f, w)
			}
			switch {
			case why != "":
				h.fail(f, "a hover answer rhun drops: "+why)
			case len(m.body) == 0:
				h.ok("answers hover-query with ok ID 0 (nothing to say)")
			default:
				h.ok("answers hover-query with a range and a text rhun can show")
			}
		}
	}
	if key("selection-ranges", "selection_ranges") {
		const f = "selection-ranges"
		d := sample("selection_ranges")
		cursors := []byte("1 1\n1 2\n")
		m, ok := h.askBodyWithin(fmt.Sprintf("selection-ranges %d %d 2 %d", d.id, d.version, len(cursors)), cursors, langWaits[f])
		switch {
		case !ok:
			h.fail(f, "no answer to a selection-ranges request within 1 s (rhun expands by words and lines instead)")
		case h.errorAnswer(f, m):
		default:
			n, warns, why := checkSelectionRanges(m, d, 2)
			for _, w := range warns {
				h.warn(f, w)
			}
			if why != "" {
				h.fail(f, why)
			} else {
				h.ok(fmt.Sprintf("answers selection-ranges with chains rhun can expand along (%d of 2 cursors)", n))
			}
		}
	}
	if key("hierarchy", "call_hierarchy") {
		h.askHierarchy(sample("call_hierarchy"), "call-hierarchy", [2]string{"calls-in", "calls-out"})
	}
	if key("hierarchy", "type_hierarchy") {
		h.askHierarchy(sample("type_hierarchy"), "type-hierarchy", [2]string{"supertypes", "subtypes"})
	}
	if key("workspace-symbols", "workspace_symbols") {
		const f = "workspace-symbols"
		sample("workspace_symbols") // rhun asks while a document its patterns match is open
		query := []byte("pr")
		if m, ok := h.langAskBody(f, fmt.Sprintf("workspace-symbols 500 %d", len(query)), query); ok {
			n, warns, why := h.checkWorkspaceSymbols(m)
			for _, w := range warns {
				h.warn(f, w)
			}
			if why != "" {
				h.fail(f, why)
			} else {
				h.ok(fmt.Sprintf("answers workspace-symbols with symbols rhun can list (%d%s)", n, h.partsSaid(h.lastAsk, "symbols")))
			}
		}
	}
	if key("document-links", "document_links") {
		const f = "document-links"
		d := sample("document_links")
		if m, ok := h.langAsk(f, fmt.Sprintf("document-links %d %d", d.id, d.version)); ok {
			n, warns, why := checkLinks(m, d)
			for _, w := range warns {
				h.warn(f, w)
			}
			if why != "" {
				h.fail(f, why)
			} else {
				h.ok(fmt.Sprintf("answers document-links with links rhun can open (%d)", n))
			}
		}
	}
}

// ---- file-diagnostics ----

// diagExtra checks a diagnostic's EXTRA records (each header's last word is its length, but
// related's bytes are PATH_LEN + MSG_LEN); its id ("" for none), or why rhun refuses the set
func (h *host) diagExtra(x []byte, ids map[string]bool, c *cursor, i int) (string, string) {
	id, related := "", 0
	for len(x) > 0 {
		e := bytes.IndexByte(x, '\n')
		if e < 0 {
			return "", "an extra record without its header line"
		}
		head := string(x[:e])
		w := strings.Split(head, " ")
		if len(w) < 2 || strings.Contains(head, "  ") || strings.HasPrefix(head, " ") || strings.HasSuffix(head, " ") {
			return "", fmt.Sprintf("the extra record %q is not NAME ... LEN", head)
		}
		n := count(w[len(w)-1])
		if w[0] == "related" {
			if len(w) != 7 || numbers(w[1:]) != "" {
				return "", fmt.Sprintf("%q is not related L1 C1 L2 C2 PATH_LEN MSG_LEN", head)
			}
			if pl := count(w[5]); pl < 1 || pl > 4096 {
				return "", fmt.Sprintf("related: PATH_LEN %q is not 1 to 4096", w[5])
			}
			n = count(w[5]) + count(w[6])
		}
		if n < 0 || n > len(x)-e-1 {
			return "", fmt.Sprintf("the extra record %q: its length is not within the extra records", head)
		}
		data := x[e+1 : e+1+n]
		x = x[e+1+n:]
		bare := len(w) == 2
		switch w[0] {
		case "id":
			if !bare || len(data) < 1 || len(data) > 64 || strings.Trim(string(data), "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.:-") != "" {
				return "", fmt.Sprintf("the id %q is not 1 to 64 of [A-Za-z0-9_.:-]", data)
			}
			if ids[string(data)] {
				return "", fmt.Sprintf("the id %s is used twice in the set (ids are unique)", data)
			}
			if id != "" {
				return "", "two id records in one diagnostic"
			}
			id = string(data)
			ids[id] = true
		case "code", "source":
			if !bare || len(data) > 100 {
				return "", fmt.Sprintf("the %s %q is over 100 bytes", w[0], data)
			}
			if why := oneLine("the "+w[0], data); why != "" {
				c.warn("diagnostic %d: %s", i, why)
			}
		case "href":
			if !bare || len(data) > 1000 || !urlOK(data) {
				return "", fmt.Sprintf("the href %q is not an http(s) URL of at most 1000 bytes", data)
			}
		case "tag":
			if len(w) != 3 || w[2] != "0" {
				return "", fmt.Sprintf("%q is not tag WORD 0", head)
			}
			if w[1] != "unnecessary" && w[1] != "deprecated" {
				c.warn("diagnostic %d: the tag %q is neither unnecessary nor deprecated (rhun skips it)", i, w[1])
			}
		case "related":
			related++
			path := data[:count(w[5])]
			if !pathOK(path) {
				return "", "related: " + badPath(path)
			}
			if count(w[1]) < 1 || count(w[2]) < 1 || before(w[3:5], w[1:3]) {
				c.warn("diagnostic %d: the related range %s is not a range of positive positions", i, strings.Join(w[1:5], " "))
			}
			if why := oneLine("the related message", data[len(path):]); why != "" {
				c.warn("diagnostic %d: %s", i, why)
			}
		default:
			c.warn("diagnostic %d: an extra record %q rhun does not know (it skips it)", i, w[0])
		}
	}
	if related > 16 {
		c.warn("diagnostic %d: %d related places: at most 16", i, related)
	}
	return id, ""
}

// checkFileDiagnostics validates n records of file-diagnostics, in d's text when the target is
// open; how many carry an id and what is odd but allowed, or why rhun refuses the set (syntax)
func (h *host) checkFileDiagnostics(b []byte, n int, d *doc) (int, []string, string) {
	c := &cursor{b: b}
	ids := map[string]bool{}
	sevs := map[string]bool{"error": true, "warning": true, "info": true, "hint": true}
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, nil, fmt.Sprintf("diagnostic %d has no header line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 7 {
			return 0, nil, fmt.Sprintf("diagnostic %d's header %q is not L1 C1 L2 C2 SEVERITY MSG_LEN EXTRA_LEN", i, l)
		}
		if why := numbers(append(w[:4:4], w[5:]...)); why != "" {
			return 0, nil, fmt.Sprintf("diagnostic %d: %s", i, why)
		}
		for _, p := range w[:4] {
			if count(p) < 1 {
				return 0, nil, fmt.Sprintf("diagnostic %d: the range %s has a position below 1", i, strings.Join(w[:4], " "))
			}
		}
		if before(w[2:4], w[0:2]) {
			return 0, nil, fmt.Sprintf("diagnostic %d: the range %s ends before it starts", i, strings.Join(w[:4], " "))
		}
		if !sevs[w[4]] {
			return 0, nil, fmt.Sprintf("diagnostic %d: SEVERITY %q is not error, warning, info or hint", i, w[4])
		}
		ml := count(w[5])
		if ml < 1 || ml > 4000 {
			return 0, nil, fmt.Sprintf("diagnostic %d: MSG_LEN %q is not 1 to 4000", i, w[5])
		}
		msg, ok1 := c.take(ml)
		extra, ok2 := c.take(count(w[6]))
		if !ok1 || !ok2 {
			return 0, nil, fmt.Sprintf("diagnostic %d's bytes run past the body", i)
		}
		if !utf8.Valid(msg) {
			c.warn("diagnostic %d: the message is not UTF-8", i)
		}
		if _, why := h.diagExtra(extra, ids, c, i); why != "" {
			return 0, nil, fmt.Sprintf("diagnostic %d: %s", i, why)
		}
		if d != nil {
			if _, _, why := span(d, w); why != "" {
				c.warn("diagnostic %d: %s (rhun does not show it there)", i, why)
			}
		}
	}
	if c.rest() > 0 {
		return 0, nil, fmt.Sprintf("%d bytes after the %d diagnostics N says", c.rest(), n)
	}
	return len(ids), c.warns, ""
}

// underDir says whether path is dir or inside it
func underDir(path, dir string) bool {
	return dir != "" && (path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/") ||
		strings.HasPrefix(path, strings.TrimSuffix(dir, `\`)+`\`))
}

// fileDiagnostics serves ID file-diagnostics VERSION N TARGET_LEN LEN (the body read, documents =
// read checked): a set of diagnostics for a document or a file, replacing the extension's there
func (h *host) fileDiagnostics(args []string, b []byte, reply func(string), rerr func(string, string), id int64) {
	const f = "file-diagnostics"
	n, tl := count(args[1]), count(args[2])
	if n < 0 || tl < 1 || tl > 4096 || tl > len(b) {
		h.fail("request grammar", fmt.Sprintf("file-diagnostics %s: N and TARGET_LEN (1 to 4096, within the body) are not counts", strings.Join(args, " ")))
		rerr("syntax", "cannot parse the line")
		return
	}
	if n > 2000 {
		h.fail("limits", fmt.Sprintf("file-diagnostics with %d diagnostics (at most 2000 per target)", n))
		rerr("toobig", "2000")
		return
	}
	target := b[:tl]
	var d *doc
	if target[0] == '#' {
		if !pathOK(target) {
			h.fail(f, badPath(target))
			rerr("syntax", "the target")
			return
		}
		d = h.openDoc(target)
		v := args[0]
		switch {
		case d == nil:
			h.fail("document ids", fmt.Sprintf("file-diagnostics for %s, which was never opened to it", target))
			rerr("nodoc", "no such document")
			return
		case count(v) < 0:
			h.fail(f, fmt.Sprintf("file-diagnostics for %s with VERSION %q: an open document's set gives its version", target, v))
			rerr("syntax", "VERSION")
			return
		case int64(count(v)) != d.version:
			h.fail("versions", fmt.Sprintf("file-diagnostics for %s at version %s, the document is at %d", target, v, d.version))
			rerr("stale", strconv.FormatInt(d.version, 10))
			return
		case d.path == "":
			h.fail(f, fmt.Sprintf("file-diagnostics for %s, an untitled document: it takes v1 diagnostics", target))
			rerr("nofile", "an untitled document")
			return
		}
	} else {
		path := string(target)
		switch {
		case args[0] != "-":
			h.fail(f, fmt.Sprintf("file-diagnostics for the path %s with VERSION %q: a path's is -", target, args[0]))
			rerr("syntax", "VERSION")
			return
		case !pathOK(target):
			h.fail(f, badPath(target))
			rerr("syntax", "the target")
			return
		case !underDir(path, h.project) && !underDir(path, h.state) && h.m.needs["home"] != "read":
			h.warn(f, fmt.Sprintf("file-diagnostics for %s, outside the project and its state folder: rhun answers needs", target))
			rerr("needs", "a path it could not open")
			return
		}
		d = h.openDoc(target)
		if d != nil && d.dirty {
			h.warn(f, fmt.Sprintf("file-diagnostics for %s, which is open with unsaved changes: rhun answers stale (send them for #%d)", target, d.id))
			rerr("stale", strconv.FormatInt(d.version, 10))
			return
		}
	}
	withIDs, warns, why := h.checkFileDiagnostics(b[tl:], n, d)
	for _, w := range warns {
		h.warn(f, w)
	}
	if why != "" {
		h.fail(f, why+" (rhun refuses the whole set)")
		rerr("syntax", "a record breaks the grammar")
		return
	}
	if h.problems == nil {
		h.problems = map[string]int{}
	}
	if n == 0 {
		delete(h.problems, string(target))
	} else {
		h.problems[string(target)] = n
	}
	if !h.fileDiagsSaid {
		h.fileDiagsSaid = true
		h.ok(fmt.Sprintf("sends file-diagnostics rhun keeps (%d for %s, %d with ids)", n, target, withIDs))
	}
	reply(fmt.Sprintf("ok %d", id))
}

// langExtraWaits are rhun's limits for this file's requests (v1.1 "Queries: kinds, time and caps")
var langExtraWaits = map[string]time.Duration{
	"hover-query": 3 * time.Second, "selection-ranges": time.Second, "call-hierarchy": 5 * time.Second,
	"type-hierarchy": 5 * time.Second, "calls-in": 5 * time.Second, "calls-out": 5 * time.Second,
	"supertypes": 5 * time.Second, "subtypes": 5 * time.Second, "workspace-symbols": 5 * time.Second,
	"document-links": 2 * time.Second,
}

func init() {
	for k, v := range langExtraWaits {
		langWaits[k] = v
	}
	for _, k := range []string{"calls-in", "calls-out", "supertypes", "subtypes", "workspace-symbols"} {
		langCeilings[k] = 30 * time.Second
	}
}
