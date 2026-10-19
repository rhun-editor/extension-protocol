package main

// Retained views (protocol 1.1, feature view-tree): the checker validates the manifest's [views],
// opens an instance for each declared view after the catch-up, checks every vpatch against the
// grammar and the content as the ops before it leave it (applied whole or not at all), answers
// vshow and vreveal, and later sends events for nodes it was given (and for a removed node and a
// stale revision), asks vkids for a collapsed node whose children were not given and vmore where
// a parent has more, cancels one request before its answer, and says visible 0.

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var viewKeyRe = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
var viewPlaces = map[string]bool{"sidebar": true, "panel": true, "side": true}
var viewKinds = map[string]bool{"tree": true, "table": true, "log": true, "form": true, "doc": true, "picture": true}
var viewFields = map[string]bool{"title": true, "place": true, "kind": true, "icon": true, "when": true, "open": true}
var viewColors = map[string]bool{"fg": true, "muted": true, "accent": true, "error": true, "warning": true,
	"success": true, "add": true, "mod": true, "del": true, "-": true}

type rview struct {
	id     int64
	key    string
	place  string
	vkind  string // tree or log
	lines  int    // a log's lines given
	rev    int64
	parent map[string]string   // node -> parent ("" the root)
	kids   map[string][]string // parent -> children in order
	kind   map[string]string   // node -> leaf|closed|open
	given  map[string]bool     // children given
	more   map[string]bool     // a "Load more" after its children
}

func newRview(id int64, key, place string) *rview {
	return &rview{id: id, key: key, place: place, parent: map[string]string{}, kids: map[string][]string{},
		kind: map[string]string{}, given: map[string]bool{"": true}, more: map[string]bool{}}
}

func (v *rview) clone() *rview {
	c := newRview(v.id, v.key, v.place)
	c.vkind, c.lines = v.vkind, v.lines
	c.rev = v.rev
	for k, x := range v.parent {
		c.parent[k] = x
	}
	for k, x := range v.kids {
		c.kids[k] = append([]string(nil), x...)
	}
	for k, x := range v.kind {
		c.kind[k] = x
	}
	for k, x := range v.given {
		c.given[k] = x
	}
	for k, x := range v.more {
		c.more[k] = x
	}
	return c
}

func (v *rview) exists(id string) bool { _, ok := v.parent[id]; return id == "" || ok }

func (v *rview) remove(id string) {
	for _, c := range v.kids[id] {
		v.remove(c)
	}
	delete(v.kids, id)
	delete(v.kind, id)
	delete(v.given, id)
	delete(v.more, id)
	if p, ok := v.parent[id]; ok {
		k := v.kids[p]
		for i, c := range k {
			if c == id {
				v.kids[p] = append(k[:i:i], k[i+1:]...)
				break
			}
		}
	}
	delete(v.parent, id)
}

func (v *rview) count() int { return len(v.parent) }

// manifestViews validates [views]: the declared views it can open (key -> fields)
func (h *host) manifestViews() []string {
	var keys []string
	seen := map[string]bool{}
	for k := range h.m.views {
		key, field, ok := strings.Cut(k, ".")
		if !ok || !viewKeyRe.MatchString(key) {
			h.fail("manifest", fmt.Sprintf("[views] %q is not KEY.FIELD with KEY [a-z0-9_]{1,40}", k))
			continue
		}
		if !viewFields[field] {
			h.warn("manifest", fmt.Sprintf("[views] %s: unknown field %q (rhun ignores it)", key, field))
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > 16 {
		h.fail("manifest", fmt.Sprintf("[views] declares %d views: rhun keeps 16", len(keys)))
	}
	var good []string
	for _, key := range keys {
		f := func(n string) string { return h.m.views[key+"."+n] }
		switch {
		case f("title") == "":
			h.fail("manifest", fmt.Sprintf("[views] %s has no title: rhun leaves it out", key))
		case !viewPlaces[f("place")]:
			h.fail("manifest", fmt.Sprintf("[views] %s.place %q is not sidebar, panel or side", key, f("place")))
		case !viewKinds[f("kind")]:
			h.fail("manifest", fmt.Sprintf("[views] %s.kind %q is not tree, table, log, form, doc or picture", key, f("kind")))
		case len(f("title")) > 80:
			h.fail("manifest", fmt.Sprintf("[views] %s.title is over 80 bytes", key))
		default:
			good = append(good, key)
		}
	}
	return good
}

// openRetained opens an instance of each declared tree view (after the catch-up)
func (h *host) openRetained() {
	if !h.features["view-tree"] {
		if len(h.m.views) > 0 {
			h.warn("view-tree", "[views] declared but view-tree not said after hello 1: rhun shows them as unsupported")
		}
		return
	}
	if h.rviews == nil {
		h.rviews = map[int64]*rview{}
	}
	for _, key := range h.manifestViews() {
		kind := h.m.views[key+".kind"]
		if kind == "log" && !h.features["view-log"] {
			h.warn("view-log", fmt.Sprintf("[views] %s is a log but view-log was not said after hello 1: rhun shows it as unsupported", key))
			continue
		}
		if kind == "picture" && !h.features["views"] {
			h.warn("views", fmt.Sprintf("[views] %s is a picture but views was not said after hello 1: rhun shows it as unsupported", key))
			continue
		}
		if kind != "tree" && kind != "log" && kind != "picture" {
			continue
		}
		h.nextView++
		v := newRview(h.nextView, key, h.m.views[key+".place"])
		v.vkind = kind
		h.rviews[v.id] = v
		h.write(fmt.Sprintf("0 view %d open %s %s %s\n0 view %d visible 1\n", v.id, kind, v.place, key, v.id))
	}
}

// retainedRequest answers vpatch, vshow and vreveal
func (h *host) retainedRequest(id int64, verb string, args []string, body []byte, reply func(string), rerr func(string, string)) {
	if !h.features["view-tree"] {
		h.fail("features", verb+" without view-tree agreed after hello (hello 1 view-tree)")
		rerr("unknown", "no such request")
		return
	}
	switch verb {
	case "vpatch":
		if len(args) != 2 {
			h.fail("view-tree", fmt.Sprintf("vpatch takes VIEW LEN, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		n, _ := strconv.ParseInt(args[0], 10, 64)
		v := h.rviews[n]
		if v == nil {
			h.fail("view-tree", fmt.Sprintf("vpatch names view %s, which is not one of its retained views", args[0]))
			rerr("noview", "no such view")
			return
		}
		c := v.clone()
		if code, why := applyPatch(c, body); code != "" {
			h.fail("view-tree", fmt.Sprintf("vpatch %d: %s %s", n, code, why))
			rerr(code, why)
			return
		}
		c.rev++
		h.rviews[n] = c
		h.patches++
		reply(fmt.Sprintf("ok %d %d", id, c.rev))
	case "vshow":
		if len(args) != 2 || (args[1] != "focus" && args[1] != "-") {
			h.fail("view-tree", fmt.Sprintf("vshow takes NAME focus|-, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		for _, v := range h.rviews {
			if v.key == args[0] {
				reply(fmt.Sprintf("ok %d %d", id, v.id))
				return
			}
		}
		rerr("refused", "no such view")
	case "vtab":
		if len(args) < 2 {
			h.fail("view-tree", fmt.Sprintf("vtab takes KIND TITLE, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		kind := args[0]
		if kind != "tree" && !(kind == "log" && h.features["view-log"]) {
			h.fail("view-tree", fmt.Sprintf("vtab %s: a kind it did not agree (tree, or log with view-log)", kind))
			rerr("unknown", "no such request")
			return
		}
		if title := strings.Join(args[1:], " "); len(title) > 80 {
			h.warn("view-tree", "vtab: a title over 80 bytes is cut")
		}
		tabs := len(h.views)
		for _, v := range h.rviews {
			if v.place == "tab" {
				tabs++
			}
		}
		if tabs >= 8 {
			rerr("toobig", "8 views")
			return
		}
		h.nextView++
		v := newRview(h.nextView, "-", "tab")
		v.vkind = kind
		h.rviews[v.id] = v
		reply(fmt.Sprintf("ok %d %d", id, v.id))
		h.write(fmt.Sprintf("0 view %d open %s tab -\n0 view %d visible 1\n", v.id, kind, v.id))
	case "vreveal":
		if len(args) != 3 {
			h.fail("view-tree", fmt.Sprintf("vreveal takes VIEW HOW LEN, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		n, _ := strconv.ParseInt(args[0], 10, 64)
		v := h.rviews[n]
		if v == nil || v.vkind == "log" {
			rerr("noview", "no such view")
			return
		}
		for _, w := range strings.Split(args[1], ",") {
			if w != "-" && w != "select" && w != "focus" && w != "expand" {
				h.fail("view-tree", fmt.Sprintf("vreveal HOW %q: - or a comma list of select, focus, expand", args[1]))
			}
		}
		if !v.exists(string(body)) || len(body) == 0 {
			rerr("badpos", "no such node")
			return
		}
		reply(fmt.Sprintf("ok %d", id))
	}
}

// checkPictureView sends a picture view what rhun sends a 12a view (size, metrics, the pointer) and,
// with view-keys, the keyboard's events and a vcopy that must be answered
func (h *host) checkPictureView(v *rview) {
	id := v.id
	h.write(fmt.Sprintf("0 view %d size 300 120\n0 view %d metrics small 6 16 normal 7 18 large 10 26 mono 7 19\n", id, id))
	keys := h.features["view-keys"]
	mods := ""
	if keys {
		mods = " -"
		h.write(fmt.Sprintf("0 view %d focus 1\n", id))
	}
	h.write(fmt.Sprintf("0 view %d hover 10 10\n0 view %d down 10 10%s\n0 view %d up 10 10%s\n0 view %d click 10 10%s\n",
		id, id, mods, id, mods, id, mods))
	if keys {
		h.write(fmt.Sprintf("0 view %d dblclick 10 10 -\n0 view %d key ctrl+shift Return\n0 view %d key - a\n0 view %d text 1\na"+
			"0 view %d paste 5\nhello0 view %d selectall\n", id, id, id, id, id, id))
	}
	h.serve(400 * time.Millisecond)
	if h.cmdExited() {
		h.fail("views", "the program exited on its picture view's events")
		return
	}
	h.ok(fmt.Sprintf("picture %s takes size, metrics and pointer events", v.key))
	if keys {
		h.hostSeq++
		rid := h.hostSeq
		h.hostReqs[rid] = "vcopy"
		h.write(fmt.Sprintf("%d vcopy %d copy\n", rid, id))
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if m, ok := h.hostAnswers[rid]; ok {
				delete(h.hostAnswers, rid)
				if strings.HasPrefix(m.line, fmt.Sprintf("ok %d ", rid)) {
					h.ok("answers vcopy")
				} else {
					h.fail("view-keys", fmt.Sprintf("vcopy answered %q: ok ID LEN and the text (LEN 0 leaves the clipboard)", m.line))
				}
				break
			}
			msg, err := h.readMessage(100 * time.Millisecond)
			if err == nil && msg.line != "" {
				h.handle(msg)
			}
		}
		if _, waiting := h.hostReqs[rid]; waiting {
			delete(h.hostReqs, rid)
			h.fail("view-keys", "no answer to vcopy within 1 s: rhun leaves the clipboard")
		}
		h.write(fmt.Sprintf("0 view %d focus 0\n", id))
	}
	h.write(fmt.Sprintf("0 view %d visible 0\n", id))
}

// imageRequest answers vimage and unimage (view-images): a PNG, JPEG or GIF by its first bytes
func (h *host) imageRequest(id int64, verb string, args []string, body []byte, reply func(string), rerr func(string, string)) {
	if !h.features["view-images"] {
		h.fail("features", verb+" without view-images agreed after hello (hello 1 views view-images)")
		rerr("unknown", "no such request")
		return
	}
	if h.images == nil {
		h.images = map[int64]bool{}
	}
	switch verb {
	case "vimage":
		png := bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n"))
		jpeg := bytes.HasPrefix(body, []byte{0xff, 0xd8, 0xff})
		gif := bytes.HasPrefix(body, []byte("GIF87a")) || bytes.HasPrefix(body, []byte("GIF89a"))
		if len(args) != 1 || !(png || jpeg || gif) {
			h.fail("view-images", "vimage takes LEN and a PNG, JPEG or GIF")
			rerr("syntax", "cannot decode the image")
			return
		}
		if len(h.images) >= 64 {
			rerr("toobig", "64 images or 128 MB")
			return
		}
		h.noteSeq++
		h.images[h.noteSeq] = true
		reply(fmt.Sprintf("ok %d %d", id, h.noteSeq))
	case "unimage":
		n, err := strconv.ParseInt(strings.Join(args, ""), 10, 64)
		if err != nil || len(args) != 1 {
			h.fail("view-images", "unimage takes IMG")
			rerr("syntax", "cannot parse the line")
			return
		}
		if !h.images[n] {
			rerr("noview", "no such image")
			return
		}
		delete(h.images, n)
		reply(fmt.Sprintf("ok %d", id))
	}
}

// retainedUnview closes a retained view's own tab (vtab); false when VIEW is not one
func (h *host) retainedUnview(id int64, args []string, reply func(string)) bool {
	n, _ := strconv.ParseInt(strings.Join(args, ""), 10, 64)
	if v := h.rviews[n]; v != nil && v.place == "tab" {
		delete(h.rviews, n)
		reply(fmt.Sprintf("ok %d", id))
		return true
	}
	return false
}

var logSevs = map[string]bool{"trace": true, "debug": true, "info": true, "warn": true, "error": true, "-": true}

// record reads "NAME ARGS... LEN" LF and LEN bytes: fields, data, rest
func readRecord(b []byte) ([]string, []byte, []byte, string) {
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return nil, nil, nil, "a header line without LF"
	}
	f := strings.Split(string(b[:nl]), " ")
	if len(f) < 2 {
		return nil, nil, nil, fmt.Sprintf("%q has no LEN", string(b[:nl]))
	}
	n, err := strconv.Atoi(f[len(f)-1])
	if err != nil || n < 0 || n > len(b)-nl-1 {
		return nil, nil, nil, fmt.Sprintf("%q: LEN past the bytes", string(b[:nl]))
	}
	return f, b[nl+1 : nl+1+n], b[nl+1+n:], ""
}

// nodeRecord reads "ID_LEN LABEL_LEN EXTRA_LEN KIDS" LF and the bytes
func nodeRecord(b []byte) (id, label string, extra []byte, kids string, rest []byte, why string) {
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return "", "", nil, "", nil, "a node record without LF"
	}
	f := strings.Split(string(b[:nl]), " ")
	if len(f) != 4 {
		return "", "", nil, "", nil, fmt.Sprintf("node record %q is not ID_LEN LABEL_LEN EXTRA_LEN KIDS", string(b[:nl]))
	}
	var n [3]int
	for i := 0; i < 3; i++ {
		v, err := strconv.Atoi(f[i])
		if err != nil || v < 0 {
			return "", "", nil, "", nil, fmt.Sprintf("node record %q: not a length", string(b[:nl]))
		}
		n[i] = v
	}
	if f[3] != "leaf" && f[3] != "closed" && f[3] != "open" {
		return "", "", nil, "", nil, fmt.Sprintf("node KIDS %q is not leaf, closed or open", f[3])
	}
	b = b[nl+1:]
	if n[0]+n[1]+n[2] > len(b) {
		return "", "", nil, "", nil, "a node record longer than its op"
	}
	id, label, extra = string(b[:n[0]]), string(b[n[0]:n[0]+n[1]]), b[n[0]+n[1]:n[0]+n[1]+n[2]]
	if n[0] == 0 || n[0] > 1000 || strings.ContainsAny(id, "\n\x00") {
		return "", "", nil, "", nil, fmt.Sprintf("node id %q: 1 to 1000 bytes without LF or NUL", id)
	}
	if n[1] > 1000 || strings.Contains(label, "\n") {
		return "", "", nil, "", nil, fmt.Sprintf("label of %q: one line of at most 1000 bytes", id)
	}
	if why := checkNodeExtra(extra); why != "" {
		return "", "", nil, "", nil, fmt.Sprintf("node %q: %s", id, why)
	}
	return id, label, extra, f[3], b[n[0]+n[1]+n[2]:], ""
}

// checkNodeExtra validates a node's extra records (unknown ones are allowed: rhun skips them)
func checkNodeExtra(x []byte) string {
	for len(x) > 0 {
		f, data, rest, why := readRecord(x)
		if why != "" {
			return why
		}
		switch f[0] {
		case "desc":
			if len(data) > 1000 {
				return "desc over 1000 bytes"
			}
		case "tip":
			if len(data) > 4000 {
				return "tip over 4000 bytes"
			}
		case "icon":
			if len(f) != 4 || !(viewColors[f[2]] || regexp.MustCompile(`^c[1-8]$`).MatchString(f[2])) {
				return fmt.Sprintf("icon record %q: icon SPEC COLOR 0", strings.Join(f, " "))
			}
		case "hl", "open":
			if len(f) != 4 {
				return fmt.Sprintf("%s record %q: %s A B LEN", f[0], strings.Join(f, " "), f[0])
			}
		case "badge", "check", "color":
			if len(f) != 3 {
				return fmt.Sprintf("%s record %q: %s VALUE 0", f[0], strings.Join(f, " "), f[0])
			}
		case "command":
			if len(f) != 3 || !viewKeyRe.MatchString(f[1]) || len(data) > 8192 {
				return fmt.Sprintf("command record %q: command NAME ARGS_LEN, ARGS at most 8192 bytes", strings.Join(f, " "))
			}
		case "inline", "menu":
			for _, k := range strings.Fields(string(data)) {
				if !viewKeyRe.MatchString(k) {
					return fmt.Sprintf("%s key %q", f[0], k)
				}
			}
		}
		x = rest
	}
	return ""
}

// applyPatch checks and applies a patch to v (a copy): "" or the error code and why
func applyPatch(v *rview, body []byte) (string, string) {
	nl := bytes.IndexByte(body, '\n')
	if nl < 0 {
		return "syntax", "op 1"
	}
	n, err := strconv.Atoi(string(body[:nl]))
	if err != nil || n < 0 {
		return "syntax", "op 1"
	}
	b := body[nl+1:]
	for op := 1; op <= n; op++ {
		f, data, rest, why := readRecord(b)
		if why != "" {
			return "syntax", fmt.Sprintf("op %d (%s)", op, why)
		}
		b = rest
		bad := func(w string) (string, string) { return "syntax", fmt.Sprintf("op %d (%s)", op, w) }
		switch f[0] {
		case "kids", "add", "set", "del", "expand", "collapse", "select-mode":
			if v.vkind == "log" {
				return "noview", "an op for another kind of view"
			}
		case "log", "clear":
			if v.vkind != "log" {
				return "noview", "an op for another kind of view"
			}
		}
		if v.vkind == "picture" && f[0] != "title" && f[0] != "badge" && f[0] != "message" && f[0] != "busy" &&
			f[0] != "state" && f[0] != "action" {
			return "noview", "an op for another kind of view"
		}
		switch f[0] {
		case "log":
			if len(f) != 3 {
				return bad("log N LEN")
			}
			cnt, err := strconv.Atoi(f[1])
			if err != nil || cnt < 0 {
				return bad("log N LEN")
			}
			lines := bytes.Split(data, []byte("\n"))
			if len(data) == 0 || data[len(data)-1] != '\n' || len(lines)-1 != cnt {
				if cnt != 0 || len(data) != 0 {
					return bad("log: N lines, each SEV TEXT and LF")
				}
			}
			for _, l := range lines[:len(lines)-1] {
				sev, _, _ := bytes.Cut(l, []byte(" "))
				if !logSevs[string(sev)] {
					return bad(fmt.Sprintf("log: severity %q is not trace, debug, info, warn, error or -", sev))
				}
			}
			v.lines += cnt
		case "clear":
			if len(f) != 2 || len(data) != 0 {
				return bad("clear 0")
			}
			v.lines = 0
		case "title":
			if len(data) > 80 || bytes.ContainsAny(data, "\n") {
				return bad("title: one line of at most 80 bytes")
			}
		case "message":
			if len(data) > 1000 || bytes.ContainsAny(data, "\n") {
				return bad("message: one line of at most 1000 bytes")
			}
		case "badge", "busy":
			if len(f) != 3 {
				return bad(f[0] + " N 0")
			}
		case "state":
			if len(f) != 4 || (f[1] != "ready" && f[1] != "loading" && f[1] != "empty" && f[1] != "error") {
				return bad("state ready|loading|empty|error TEXT_LEN LEN")
			}
		case "action":
			if len(f) != 5 || !viewKeyRe.MatchString(f[1]) || (f[3] != "title" && f[3] != "node" && f[3] != "none") {
				return bad("action KEY ICON title|node|none LEN")
			}
		case "select-mode":
			if len(f) != 3 || (f[1] != "single" && f[1] != "multi") {
				return bad("select-mode single|multi 0")
			}
		case "kids", "add":
			if len(f) != 5 {
				return bad(f[0] + " PARENT_LEN N MORE LEN")
			}
			pl, e1 := strconv.Atoi(f[1])
			cnt, e2 := strconv.Atoi(f[2])
			if e1 != nil || e2 != nil || pl > len(data) || (f[3] != "0" && f[3] != "1") {
				return bad(f[0] + " PARENT_LEN N MORE LEN")
			}
			parent := string(data[:pl])
			if !v.exists(parent) {
				return "badpos", fmt.Sprintf("%s %s", f[0], parent)
			}
			q := data[pl:]
			listed := map[string]bool{}
			var order []string
			for i := 0; i < cnt; i++ {
				id, _, _, kind, r, why := nodeRecord(q)
				if why != "" {
					return bad(why)
				}
				q = r
				if listed[id] {
					return "badpos", fmt.Sprintf("%s %s (listed twice)", f[0], id)
				}
				listed[id] = true
				if v.exists(id) && (f[0] == "add" || v.parent[id] != parent) {
					return "badpos", fmt.Sprintf("%s %s (already elsewhere)", f[0], id)
				}
				if !v.exists(id) {
					v.parent[id] = parent
					v.kind[id] = kind
				} else if kind == "leaf" {
					for _, c := range append([]string(nil), v.kids[id]...) {
						v.remove(c)
					}
				}
				v.kind[id] = kind
				order = append(order, id)
			}
			if len(q) != 0 {
				return bad("the records do not take the op's bytes exactly")
			}
			if f[0] == "kids" {
				for _, c := range append([]string(nil), v.kids[parent]...) {
					if !listed[c] {
						v.remove(c)
					}
				}
				v.kids[parent] = order
			} else {
				v.kids[parent] = append(v.kids[parent], order...)
			}
			v.given[parent] = true
			v.more[parent] = f[3] == "1"
		case "set":
			id, _, _, kind, r, why := nodeRecord(data)
			if why != "" || len(r) != 0 {
				return bad("set takes one node record")
			}
			if id == "" || !v.exists(id) {
				return "badpos", "set " + id
			}
			v.kind[id] = kind
			if kind == "leaf" {
				for _, c := range append([]string(nil), v.kids[id]...) {
					v.remove(c)
				}
			}
		case "del":
			if len(data) == 0 || !v.exists(string(data)) {
				return "badpos", "del " + string(data)
			}
			v.remove(string(data))
		case "expand", "collapse":
			if !v.exists(string(data)) {
				return "badpos", f[0] + " " + string(data)
			}
		}
		if v.count() > 100000 {
			return "toobig", "nodes"
		}
	}
	if len(b) != 0 {
		return "syntax", fmt.Sprintf("op %d (bytes after the last op)", n+1)
	}
	return "", ""
}

// checkRetained exercises the retained views: events, vkids, vmore, a cancel, visible 0
func (h *host) checkRetained() {
	if len(h.rviews) == 0 {
		return
	}
	ids := make([]int64, 0, len(h.rviews))
	for id := range h.rviews {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		v := h.rviews[id]
		if v.vkind == "picture" {
			h.checkPictureView(v)
			continue
		}
		if v.vkind == "log" {
			// the user pressed Clear, then the log went off screen
			h.write(fmt.Sprintf("0 view %d focus 1\n0 view %d clear\n0 view %d focus 0\n0 view %d visible 0\n", id, id, id, id))
			h.serve(300 * time.Millisecond)
			if h.cmdExited() {
				h.fail("view-log", "the program exited on its log view's events")
				return
			}
			h.ok(fmt.Sprintf("log %s takes focus, clear and visible events", v.key))
			continue
		}
		if v.count() == 0 && h.patches == 0 {
			h.warn("view-tree", fmt.Sprintf("view %s got no patch after it opened: it shows Loading", v.key))
		}
		// events for a node it gave, for a removed one and with a stale revision
		var some string
		for _, c := range v.kids[""] {
			some = c
			break
		}
		if some != "" {
			h.write(fmt.Sprintf("0 view %d focus 1\n0 view %d select %d 1 %d\n%s\n", id, id, v.rev, len(some)+1, some))
			h.write(fmt.Sprintf("0 view %d activate %d %d\n%s", id, v.rev, len(some), some))
			h.write(fmt.Sprintf("0 view %d expand %d %d\n%s0 view %d collapse %d %d\n%s", id, v.rev, len(some), some, id, v.rev, len(some), some))
		}
		h.write(fmt.Sprintf("0 view %d activate %d 9\nno-such-x", id, v.rev+5))
		h.write(fmt.Sprintf("0 view %d select 0 1 6\ngone!\n", id))
		h.serve(500 * time.Millisecond)
		if h.cmdExited() {
			h.fail("view-tree", "the program exited on its retained view's events")
			return
		}
		h.ok(fmt.Sprintf("view %s takes select, activate, expand and collapse events (also for a node it does not have)", v.key))
		// a collapsed node whose children were not given: vkids
		for node, kind := range v.kind {
			if kind == "leaf" || v.given[node] {
				continue
			}
			h.vkids(v, node)
			break
		}
		for parent, more := range v.more {
			if more {
				h.hostSeq++
				rid := h.hostSeq
				h.hostReqs[rid] = "vmore"
				h.write(fmt.Sprintf("%d vmore %d %d %d %d\n%s", rid, id, v.rev, len(v.kids[parent]), len(parent), parent))
				h.answerOf(rid, v, "vmore", parent)
				break
			}
		}
		h.write(fmt.Sprintf("0 view %d focus 0\n0 view %d visible 0\n", id, id))
	}
	h.serve(300 * time.Millisecond)
}

// vkids asks for a node's children: the answer must be a patch that applies; then one is cancelled
func (h *host) vkids(v *rview, node string) {
	h.hostSeq++
	rid := h.hostSeq
	h.hostReqs[rid] = "vkids"
	h.write(fmt.Sprintf("%d vkids %d %d %d\n%s", rid, v.id, v.rev, len(node), node))
	h.answerOf(rid, v, "vkids", node)
	// asked again and cancelled at once: a late answer is fine, nothing else may break
	h.hostSeq++
	rid = h.hostSeq
	h.hostReqs[rid] = "vkids"
	h.write(fmt.Sprintf("%d vkids %d %d %d\n%s0 cancel %d\n", rid, v.id, v.rev, len(node), node, rid))
	h.serve(500 * time.Millisecond)
	delete(h.hostReqs, rid)
	delete(h.hostAnswers, rid)
	if !h.cmdExited() {
		h.ok("a vkids cancelled before its answer is fine")
	}
}

// answerOf waits for the answer to a vkids or vmore and applies it
func (h *host) answerOf(rid int64, v *rview, verb, node string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if m, ok := h.hostAnswers[rid]; ok {
			delete(h.hostAnswers, rid)
			if strings.HasPrefix(m.line, "error ") {
				h.warn("view-tree", fmt.Sprintf("%s for %q answered %s: rhun shows an error row with Retry", verb, node, m.line))
				return
			}
			c := v.clone()
			if code, why := applyPatch(c, m.body); code != "" {
				h.fail("view-tree", fmt.Sprintf("the %s answer for %q: %s %s", verb, node, code, why))
				return
			}
			c.rev++
			h.rviews[v.id] = c
			if verb == "vkids" && !c.given[node] {
				h.warn("view-tree", fmt.Sprintf("the vkids answer for %q gives no kids for it: it shows no children", node))
			}
			h.ok(fmt.Sprintf("answers %s with a patch that applies", verb))
			return
		}
		msg, err := h.readMessage(100 * time.Millisecond)
		if err == nil && msg.line != "" {
			h.handle(msg)
		}
		if h.cmdExited() {
			h.fail("view-tree", "the program exited on "+verb)
			return
		}
	}
	delete(h.hostReqs, rid)
	h.write(fmt.Sprintf("0 cancel %d\n", rid))
	h.fail("view-tree", fmt.Sprintf("no answer to %s for %q within 15 s: rhun shows an error row", verb, node))
}
