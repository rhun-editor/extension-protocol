package main

// Protocol 1.1's editor model: select-many (feature selections), reveal and show-locations
// (feature reveal), diff and diff-close (feature diff) served and validated; the selections event
// sent to subscribers; the linked request (feature linked-editing) asked and its
// answer checked against the document: 0 or 2 to 64 ranges, well formed, with equal text, apart,
// one holding the cursor, and the word record.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// editorRequest serves select-many, reveal, show-locations, diff and diff-close
func (h *host) editorRequest(id int64, verb string, args []string, msg message, reply func(string), rerr func(string, string)) {
	feature := map[string]string{"select-many": "selections", "reveal": "reveal", "show-locations": "reveal", "diff": "diff", "diff-close": "diff"}[verb]
	if !h.features[feature] {
		h.fail("features", fmt.Sprintf("%s without %s agreed after hello", verb, feature))
		rerr("unknown", "no such request")
		return
	}
	body := func(lenField string) ([]byte, bool) {
		n := count(lenField)
		if n < 0 || msg.err != nil || len(msg.body) != n {
			h.fail("bodies", fmt.Sprintf("%s announced %q bytes, the stream ended first", verb, lenField))
			rerr("syntax", "cannot parse the line")
			return nil, false
		}
		return msg.body, true
	}
	getDoc := func(s string) *doc {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			h.fail("request grammar", fmt.Sprintf("%s: DOC %q is not a number", verb, s))
			rerr("syntax", "cannot parse the line")
			return nil
		}
		d := h.docs[n]
		if d == nil {
			h.fail("document ids", fmt.Sprintf("%s names document %d, which is not open", verb, n))
			rerr("nodoc", "no such document")
		}
		return d
	}
	switch verb {
	case "select-many":
		if len(args) != 3 {
			h.fail("request grammar", "select-many takes DOC N LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[2])
		if !ok {
			return
		}
		n := count(args[1])
		if n < 1 || n > 512 {
			h.fail("limits", fmt.Sprintf("select-many with N %q: 1 to 512", args[1]))
			rerr("toobig", "1 to 512 selections")
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(b) == 0 || !bytes.HasSuffix(b, []byte("\n")) || len(lines) != n {
			h.fail("bodies", fmt.Sprintf("select-many announced %d lines, the body has %d (each ends in LF)", n, len(lines)))
			rerr("syntax", "the body does not have N lines")
			return
		}
		for _, l := range lines {
			w := strings.Split(l, " ")
			if len(w) != 4 || numbers(w) != "" {
				h.fail("request grammar", fmt.Sprintf("a select-many line is not L1 C1 L2 C2: %q", l))
				rerr("syntax", "cannot parse a line of the body")
				return
			}
			for _, at := range [][2]string{{w[0], w[1]}, {w[2], w[3]}} {
				if _, why := d.position(at[0], at[1]); why != "" {
					h.fail("positions", "select-many: "+why)
					rerr("badpos", why)
					return
				}
			}
		}
		a, _ := d.position(strings.Fields(lines[0])[0], strings.Fields(lines[0])[1])
		c, _ := d.position(strings.Fields(lines[0])[2], strings.Fields(lines[0])[3])
		d.anchor, d.cur = a, c
		if h.selectionsSet == nil {
			h.selectionsSet = map[int64][]string{}
		}
		h.selectionsSet[d.id] = lines
		reply(fmt.Sprintf("ok %d", id))
	case "reveal":
		if len(args) != 4 {
			h.fail("request grammar", "reveal takes DOC LINE COL WHERE")
			rerr("syntax", "cannot parse the line")
			return
		}
		switch args[3] {
		case "top", "center", "nearest", "center-if-outside":
		default:
			h.fail("request grammar", fmt.Sprintf("reveal: WHERE %q is not top, center, nearest or center-if-outside", args[3]))
			rerr("syntax", "cannot parse the line")
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		if _, why := d.position(args[1], args[2]); why != "" {
			h.fail("positions", "reveal: "+why)
			rerr("badpos", why)
			return
		}
		reply(fmt.Sprintf("ok %d", id))
	case "show-locations":
		if len(args) != 1 {
			h.fail("request grammar", "show-locations takes LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[0])
		if !ok {
			return
		}
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 {
			h.fail("bodies", "show-locations: the body has no title line")
			rerr("syntax", "no title line")
			return
		}
		if why := oneLine("the title", b[:nl]); why != "" {
			h.fail("request grammar", "show-locations: "+why)
			rerr("syntax", why)
			return
		}
		if nl > 200 {
			h.warn("reveal", fmt.Sprintf("a show-locations title of %d bytes: rhun shows 200", nl))
		}
		n, warns, why := h.checkLocations(message{line: fmt.Sprintf("ok 0 %d", len(b)-nl-1), body: b[nl+1:]})
		for _, w := range warns {
			h.warn("reveal", w)
		}
		if why != "" {
			h.fail("reveal", "show-locations: "+why)
			rerr("syntax", why)
			return
		}
		h.prompts = append(h.prompts, fmt.Sprintf("show-locations: %s (%d)", b[:nl], n))
		reply(fmt.Sprintf("ok %d", id))
	case "diff":
		if len(args) != 4 {
			h.fail("request grammar", "diff takes LEFT RIGHT FLAGS LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[3])
		if !ok {
			return
		}
		if args[2] != "show" && args[2] != "background" {
			h.fail("request grammar", fmt.Sprintf("diff: FLAGS %q is not show or background", args[2]))
			rerr("syntax", "cannot parse the line")
			return
		}
		if why := oneLine("the title", b); why != "" {
			h.fail("request grammar", "diff: "+why)
			rerr("syntax", why)
			return
		}
		if len(b) > 200 {
			h.warn("diff", fmt.Sprintf("a diff title of %d bytes: rhun shows 200", len(b)))
		}
		if getDoc(args[0]) == nil || getDoc(args[1]) == nil {
			return
		}
		key := args[0] + " " + args[1] + " " + string(b)
		if h.diffs == nil {
			h.diffs = map[int64]string{}
		}
		for tab, k := range h.diffs {
			if k == key {
				reply(fmt.Sprintf("ok %d %d", id, tab))
				return
			}
		}
		if len(h.diffs) == 32 {
			h.fail("limits", "more than 32 diff tabs")
			rerr("toobig", "32 diff tabs")
			return
		}
		h.nextDoc++
		h.diffs[h.nextDoc] = key
		reply(fmt.Sprintf("ok %d %d", id, h.nextDoc))
	case "diff-close":
		if len(args) != 1 {
			h.fail("request grammar", "diff-close takes DOC")
			rerr("syntax", "cannot parse the line")
			return
		}
		tab, err := strconv.ParseInt(args[0], 10, 64)
		if _, open := h.diffs[tab]; err != nil || !open {
			rerr("nodoc", "not one of its diff tabs")
			return
		}
		delete(h.diffs, tab)
		reply(fmt.Sprintf("ok %d", id))
	}
}

// sendEditorEvents sends the selections event of the active document to an extension that
// agreed selections and subscribes to selection, as rhun does at catch-up and when they change
func (h *host) sendEditorEvents(d *doc) {
	if !h.features["selections"] || d == nil {
		return
	}
	if h.sub("selection") {
		body := "1 1 1 3\n2 1 2 1\n"
		h.write(fmt.Sprintf("0 selections %d 2 %d\n%s", d.id, len(body), body))
	}
}

// checkLinked asks the linked request on a document the [provides] linked patterns match, the
// cursor in its word, and checks the answer
func (h *host) checkLinked() {
	if !h.features["linked-editing"] || h.patterns("linked-editing") == "" {
		return
	}
	d := h.sampleFor(h.patterns("linked-editing"))
	h.serve(300 * time.Millisecond)
	m, ok := h.askWithin(fmt.Sprintf("linked %d %d 1 2", d.id, d.version), time.Second)
	switch {
	case !ok:
		h.fail("linked-editing", "no answer to a linked request within 1 s (rhun's wait)")
		return
	case strings.HasPrefix(m.line, "error "):
		h.ok(fmt.Sprintf("answers a linked request %q (rhun takes an error as nothing to link)", m.line))
		return
	}
	n, why := checkLinkedAnswer(m, d, 1, 2)
	if why != "" {
		h.fail("linked-editing", why+" (rhun drops the answer)")
		return
	}
	h.ok(fmt.Sprintf("answers linked requests with ranges rhun can link (%d)", n))
}

// checkLinkedAnswer validates a linked answer about d with the cursor at line col: the number of
// ranges, or why rhun drops it
func checkLinkedAnswer(m message, d *doc, line, col int) (int, string) {
	b, why := answer(m)
	if why != "" {
		return 0, why
	}
	c := &cursor{b: b}
	n, why := c.head(64, "ranges")
	if why != "" {
		return 0, why
	}
	if n == 1 {
		return 0, "one range: 0 for none, else 2 to 64"
	}
	type rng struct{ a, z int }
	var spans []rng
	cur, _ := d.position(strconv.Itoa(line), strconv.Itoa(col))
	holds := false
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return 0, fmt.Sprintf("range %d has no line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 4 || numbers(w) != "" {
			return 0, fmt.Sprintf("range %d: %q is not L1 C1 L2 C2", i, l)
		}
		a, z, why := span(d, w)
		if why != "" {
			return 0, fmt.Sprintf("range %d: %s", i, why)
		}
		for _, s := range spans {
			if a < s.z && s.a < z || a == s.a && z == s.z {
				return 0, fmt.Sprintf("range %d overlaps another", i)
			}
		}
		if len(spans) > 0 && !bytes.Equal(d.text[a:z], d.text[spans[0].a:spans[0].z]) {
			return 0, fmt.Sprintf("range %d's text %q is not the first's %q", i, d.text[a:z], d.text[spans[0].a:spans[0].z])
		}
		holds = holds || a <= cur && cur <= z
		spans = append(spans, rng{a, z})
	}
	if n > 0 && !holds {
		return 0, fmt.Sprintf("no range holds the cursor at %d:%d", line, col)
	}
	for c.rest() > 0 {
		l, _ := c.line()
		w := c.fields(l)
		if len(w) < 2 || count(w[len(w)-1]) < 0 {
			return 0, fmt.Sprintf("the record %q is not NAME ARGS... LEN", l)
		}
		x, ok := c.take(count(w[len(w)-1]))
		if !ok {
			return 0, fmt.Sprintf("the record %q runs past the body", l)
		}
		if w[0] == "word" {
			if len(w) != 2 {
				return 0, fmt.Sprintf("the record %q is not word LEN", l)
			}
			for _, ch := range x {
				if ch < ' ' || ch > '~' {
					return 0, fmt.Sprintf("the word record holds %q: printable ASCII only", ch)
				}
			}
		}
	}
	return n, ""
}
