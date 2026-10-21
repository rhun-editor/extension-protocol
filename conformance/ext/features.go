package main

// Section 12 of the spec: features. The checker agrees to the ones the extension names in hello,
// answers view, draw and unview (validating every picture), sends the views it opened their
// events, and asks completion and signature help on a document its [provides] patterns match,
// validating the answers against the grammar and the document (definition, references, symbols,
// code actions, rename and format are in langfeat.go).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

var knownFeatures = []string{"views", "complete", "signature", "definition", "references", "symbols", "actions", "rename", "format", "commands", "save-actions", "snippets", "snippet-variants", "snippet-transforms",
	"git-data", "commit-message", "quickdiff", "scm", // source control: scm.go, scmpanel.go
	"view-tree", "view-log", "notify", "progress", "view-keys", "view-images", // retained views, notifications, progress: views.go, notify.go
	"idle", "secrets", "uris", "urls", "files", "scoped-settings", // resources: resources.go
	"agents", "edit-review", "tools", "agent-prompts", // ai: ai.go
	"workspace-edits", "file-operations", "selections", "reveal", "diff", "linked-editing", // wsedit.go, editor.go
	"notebooks", "custom-editors", // notebooks: notebook.go
	"query-stream", "provider-scope", "complete-items", "signature-list", "navigation", "prepare-rename", "partial-format", "code-actions", // language features: query.go, actions.go
	"hover-query", "selection-ranges", "hierarchy", "workspace-symbols", "document-links", "file-diagnostics", // langfeat.go
	"command-args", "context-keys", "status-items", // commands.go
	"tests",              // test controllers: tests.go
	"terminals", "tasks"} // terminals and tasks: terminals.go (tasks only with terminals)

// agree records the features hello named that rhun knows, and says them back before ready.
func (h *host) agree(hello string) {
	h.features = map[string]bool{}
	asked := map[string]bool{}
	for _, f := range strings.Fields(hello)[2:] {
		asked[f] = true
	}
	h.asked = asked
	var said []string
	for _, f := range knownFeatures {
		if f == "tasks" && !asked["terminals"] {
			continue
		}
		// file-operations is agreed only together with workspace-edits
		if asked[f] && (f != "file-operations" || asked["workspace-edits"]) {
			h.features[f] = true
			said = append(said, f)
		}
	}
	if len(said) > 0 {
		h.write("0 features " + strings.Join(said, " ") + "\n")
		h.ok("hello names features: " + strings.Join(said, " "))
	}
}

// hostReply takes an extension's ok or error for one of rhun's requests: true when it was one
func (h *host) hostReply(fields []string, msg message) bool {
	if len(fields) < 2 {
		return false
	}
	id, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return false
	}
	if _, ok := h.hostReqs[id]; !ok {
		if h.withdrawn[id] {
			h.note("a late answer to %d, which rhun withdrew: dropped", id)
			return true
		}
		return false
	}
	delete(h.hostReqs, id)
	switch {
	case msg.err != nil:
		h.fail("answers", fmt.Sprintf("the answer %q: %v", msg.line, msg.err))
	case fields[0] == "error" && len(fields) < 3:
		h.fail("answers", fmt.Sprintf("%q is not error ID CODE TEXT", msg.line))
	case fields[0] == "ok" && len(fields) != 3:
		h.fail("answers", fmt.Sprintf("%q is not ok ID LEN: an answer to rhun's request always says its length", msg.line))
	}
	h.hostAnswers[id] = msg
	return true
}

// view, draw and unview (12a)
func (h *host) viewRequest(id int64, verb string, args []string, body []byte, reply func(string), rerr func(string, string)) {
	if !h.features["views"] {
		h.fail("features", verb+" without views agreed after hello (hello 1 views)")
		rerr("unknown", "no such request")
		return
	}
	switch verb {
	case "view":
		if len(args) == 0 {
			h.fail("views", "view without a title")
			rerr("syntax", "cannot parse the line")
			return
		}
		if len(h.views) == 8 {
			rerr("toobig", "8 views")
			return
		}
		h.nextView++
		h.views[h.nextView] = true
		reply(fmt.Sprintf("ok %d %d", id, h.nextView))
	case "draw":
		if len(args) != 2 {
			h.fail("views", fmt.Sprintf("draw takes VIEW LEN, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		v, _ := strconv.ParseInt(args[0], 10, 64)
		if rv := h.rviews[v]; !h.views[v] && (rv == nil || rv.vkind != "picture") {
			h.fail("views", fmt.Sprintf("draw names view %s, which is not open", args[0]))
			rerr("noview", "no such view")
			return
		}
		if line, why := checkPicture(body); why != "" {
			h.fail("views", fmt.Sprintf("picture line %d: %s", line, why))
			rerr("syntax", fmt.Sprintf("line %d", line))
			return
		}
		h.pictures++
		reply(fmt.Sprintf("ok %d", id))
	case "unview":
		v, _ := strconv.ParseInt(strings.Join(args, ""), 10, 64)
		if !h.views[v] {
			rerr("noview", "no such view")
			return
		}
		delete(h.views, v)
		reply(fmt.Sprintf("ok %d", id))
	}
}

var shapeArgs = map[string][3]int{ // numbers before the color, has text, minimum points
	"rect": {4, 0, 0}, "rrect": {5, 0, 0}, "line": {5, 0, 0}, "poly": {1, 0, 2}, "fill": {0, 0, 3},
	"circle": {3, 0, 0}, "ring": {4, 0, 0}, "pie": {5, 0, 0}, "text": {2, 1, 0},
}

// sizes are the fixed numbers that are widths, heights, radii or stroke widths: never negative
var sizes = map[string][]int{
	"rect": {2, 3}, "rrect": {2, 3, 4}, "line": {4}, "poly": {0}, "circle": {2}, "ring": {2, 3},
	"pie": {2}, "clip": {2, 3},
}
var numRe = regexp.MustCompile(`^-?[0-9]{1,7}(\.[0-9]{1,9})?$`)
var colorRe = regexp.MustCompile(`^(#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?|fg|muted|bg|panel|border|accent|error|warning|success|c[1-8])$`)

// checkPicture validates a draw body against 12a: the line number and why, or "" for a good one
func checkPicture(body []byte) (int, string) {
	if len(body) > 1<<20 {
		return 0, "the picture is over 1 MB"
	}
	shapes, points := 0, 0
	for i, l := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		n := i + 1
		if l == "" {
			continue
		}
		w := strings.Split(l, " ")
		switch w[0] {
		case "unclip":
			shapes++
			continue
		case "clip":
			if len(w) != 5 {
				return n, "clip takes X Y W H"
			}
			for _, x := range w[1:] {
				if !numRe.MatchString(x) {
					return n, fmt.Sprintf("%q is not a number", x)
				}
			}
			for _, j := range sizes["clip"] {
				if strings.HasPrefix(w[1+j], "-") {
					return n, "clip: a negative size"
				}
			}
			shapes++
			continue
		}
		spec, ok := shapeArgs[w[0]]
		if !ok {
			continue // a later rhun may draw it: skipped
		}
		shapes++
		k := 1
		for j := 0; j < spec[0]; j++ {
			if k >= len(w) || !numRe.MatchString(w[k]) {
				return n, fmt.Sprintf("%s: number %d is missing or not one", w[0], j+1)
			}
			k++
		}
		for _, j := range sizes[w[0]] {
			if strings.HasPrefix(w[1+j], "-") {
				return n, fmt.Sprintf("%s: number %d is a size and negative", w[0], j+1)
			}
		}
		if spec[1] == 1 { // text: SIZE ALIGN COLOR TEXT
			if k+2 >= len(w) {
				return n, "text takes X Y SIZE ALIGN COLOR TEXT"
			}
			if !strings.Contains(" small normal large mono ", " "+w[k]+" ") {
				return n, fmt.Sprintf("text size %q", w[k])
			}
			if !strings.Contains(" left center right ", " "+w[k+1]+" ") {
				return n, fmt.Sprintf("text align %q", w[k+1])
			}
			k += 2
		}
		if k >= len(w) || !colorRe.MatchString(w[k]) {
			return n, fmt.Sprintf("%s: no color where one goes", w[0])
		}
		k++
		if spec[1] == 1 {
			continue
		}
		rest := w[k:]
		if spec[2] == 0 && len(rest) != 0 {
			return n, fmt.Sprintf("%s: %d words too many", w[0], len(rest))
		}
		if spec[2] > 0 {
			if len(rest)%2 != 0 || len(rest)/2 < spec[2] || len(rest)/2 > 10000 {
				return n, fmt.Sprintf("%s: %d numbers is not %d to 10000 points", w[0], len(rest), spec[2])
			}
			for _, x := range rest {
				if !numRe.MatchString(x) {
					return n, fmt.Sprintf("%q is not a number", x)
				}
			}
			points += len(rest) / 2
		}
	}
	if shapes > 20000 {
		return 0, "over 20000 shapes"
	}
	if points > 200000 {
		return 0, "over 200000 points"
	}
	return 0, ""
}

// sampleFor writes a file whose name matches the first of the patterns and opens it, the cursor's
// word "pr"; a document already open under that name is used again (rhun opens a file once)
func (h *host) sampleFor(patterns string) *doc {
	p := strings.Fields(patterns)
	name := "sample.txt"
	if len(p) > 0 {
		switch {
		case p[0] == "*":
		case strings.HasPrefix(p[0], "*"):
			name = "sample" + p[0][1:]
		default:
			name = p[0]
		}
	}
	path := filepath.Join(h.project, name)
	for _, id := range h.docIDs() {
		if d := h.docs[id]; d.path == path {
			return d
		}
	}
	os.WriteFile(path, []byte("pr\n"), 0o644)
	d := h.newDoc(path, langOf(name), []byte("pr\n"))
	h.write(fmt.Sprintf("0 opened %d %d %s %s\n", d.id, d.version, dash(d.lang), d.path))
	return d
}

// ask sends one of rhun's requests and waits for its answer (served meanwhile)
func (h *host) ask(line string) (message, bool) {
	return h.askWithin(line, 3*time.Second)
}

// askWithin is ask with rhun's own wait for that request
func (h *host) askWithin(line string, wait time.Duration) (message, bool) {
	return h.askBodyWithin(line, nil, wait)
}

// askBodyWithin is askWithin for a request with a body (its line ends in LEN): the answer, served
// meanwhile, before the request's hard limit, which progress reports may move (query-stream)
func (h *host) askBodyWithin(line string, body []byte, wait time.Duration) (message, bool) {
	h.hostSeq++
	id := h.hostSeq
	h.lastAsk = id
	h.hostReqs[id] = line
	now := time.Now()
	q := &query{line: line, verb: strings.Fields(line)[0], base: wait, started: now, due: now.Add(wait)}
	if h.queries == nil {
		h.queries = map[int64]*query{}
	}
	h.queries[id] = q
	if f := strings.Fields(line); len(f) == 4 && f[0] == "save-actions" {
		docID, _ := strconv.ParseInt(f[1], 10, 64)
		version, _ := strconv.ParseInt(f[2], 10, 64)
		h.saveScope = &saveScope{id: id, doc: docID, version: version}
		defer func() { h.saveScope = nil }()
	}
	h.write(fmt.Sprintf("%d %s\n%s", id, line, body))
	for time.Now().Before(q.due) {
		if m, ok := h.hostAnswers[id]; ok {
			delete(h.hostAnswers, id)
			if s := h.saveScope; s != nil && s.id == id {
				if d := h.docs[s.doc]; d == nil || d.version != s.version {
					h.fail("save-actions", "document changed outside the credited save invocation")
					return message{line: fmt.Sprintf("error %d stale document changed", id)}, true
				}
			}
			return m, true
		}
		msg, err := h.readMessage(100 * time.Millisecond)
		if err == nil && msg.line != "" {
			h.handle(msg)
		}
	}
	delete(h.hostReqs, id)
	if h.withdrawn == nil {
		h.withdrawn = map[int64]bool{}
	}
	h.withdrawn[id] = true
	h.write(fmt.Sprintf("0 cancel %d\n", id))
	return message{}, false
}

// checkFeatures is step 12: the requests rhun sends, and the events of the views opened
func (h *host) checkFeatures() {
	for _, f := range providerFeatures {
		if h.features[f] && h.manifestPatterns(f) == "" {
			h.fail("provides", f+" agreed after hello but [provides] has no "+provideKey(f)+" patterns: rhun never asks")
		}
		// a code-actions provider needs no actions feature (the key stays actions)
		if !h.features[f] && h.manifestPatterns(f) != "" && !(f == "actions" && h.features["code-actions"]) {
			h.fail("provides", "[provides] "+provideKey(f)+" without saying "+f+" after hello 1: rhun never asks")
		}
	}
	h.checkKeyFeatures()
	keys := make([]string, 0, len(h.m.provides))
	for k := range h.m.provides {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		f, known := providesKeys[k]
		if v := strings.TrimSpace(h.m.provides[k]); known && v != "" && v != "no" && !slices.Contains(providerFeatures, f) {
			if !h.asked[f] {
				h.warn("provides", fmt.Sprintf("[provides] %s without %s after hello 1: rhun never asks", k, f))
			} else if !slices.Contains(askedKeyFeatures, f) {
				h.note("[provides] %s: the checker does not ask %s requests", k, f)
			}
		}
	}
	if h.features["complete"] && h.patterns("complete") != "" {
		d := h.sampleFor(h.patterns("complete"))
		h.serve(300 * time.Millisecond)
		version := d.version
		m, ok := h.ask(fmt.Sprintf("complete %d %d 1 3 invoked -", d.id, d.version))
		switch {
		case !ok:
			h.fail("complete", "no answer to a completion request within 3 s")
		case strings.HasPrefix(m.line, "error "):
			h.fail("complete", "a completion request answered "+m.line)
		default:
			info, why := checkCompletionList(m, d, h.features)
			if why != "" {
				h.fail("complete", why)
				break
			}
			h.ok("answers completion requests with a list rhun can read" + h.partsSaid(h.lastAsk, "items"))
			if h.features["complete-items"] && info.resolve != nil {
				h.checkResolveRequest(d, version, *info.resolve)
			}
		}
		h.hostSeq++
		h.write(fmt.Sprintf("0 cancel %d\n", h.hostSeq))
		h.checkUnknownRequest(d)
	}
	if h.features["signature"] && h.patterns("signature") != "" {
		d := h.sampleFor(h.patterns("signature"))
		h.serve(300 * time.Millisecond)
		// with signature-list agreed rhun sends signatures, and signature no more
		verb := "signature"
		if h.features["signature-list"] {
			verb = "signatures"
		}
		m, ok := h.ask(fmt.Sprintf("%s %d %d 1 3 invoked -", verb, d.id, d.version))
		switch {
		case !ok:
			h.fail("signature", "no answer to a "+verb+" request within 3 s")
		case strings.HasPrefix(m.line, "error "):
			h.fail("signature", "a "+verb+" request answered "+m.line)
		case verb == "signatures":
			warns, why := checkSignatures(m)
			for _, w := range warns {
				h.warn("signature-list", w)
			}
			if why != "" {
				h.fail("signature-list", why+" (rhun hides the help)")
			} else {
				h.ok("answers signatures requests with overloads rhun can show")
			}
		default:
			if why := checkSignature(m); why != "" {
				h.fail("signature", why)
			} else {
				h.ok("answers signature requests")
			}
		}
	}
	h.checkLangFeatures()
	h.checkResources()
	h.checkScm()
	h.checkCommands()
	if h.cmdExited() {
		return
	}
	h.checkLinked()
	if h.cmdExited() {
		h.fail("linked-editing", "the program exited on a linked request")
		return
	}
	h.checkNotebooks()
	h.checkEditors()
	if h.cmdExited() {
		return
	}
	h.checkTests()
	if h.cmdExited() {
		return
	}
	if !h.features["complete"] || h.patterns("complete") == "" {
		for _, f := range providerFeatures {
			if h.features[f] && h.patterns(f) != "" {
				h.checkUnknownRequest(h.sampleFor(h.patterns(f)))
				break
			}
		}
	}
	h.checkRetained()
	if h.cmdExited() {
		return
	}
	h.checkNotify()
	if h.cmdExited() {
		return
	}
	if len(h.views) > 0 {
		for v := range h.views {
			h.write(fmt.Sprintf("0 view %d size 640 480\n0 view %d metrics small 6 16 normal 7 18 large 10 26 mono 7 19\n", v, v))
			h.write(fmt.Sprintf("0 view %d hover 10 10\n0 view %d down 10 10\n0 view %d up 10 10\n0 view %d click 10 10\n", v, v, v, v))
			h.write(fmt.Sprintf("0 view %d wheel 10 10 -3\n0 view %d leave\n", v, v))
		}
		h.serve(700 * time.Millisecond)
		if h.cmdExited() {
			h.fail("views", "the program exited on its views' events")
			return
		}
		h.ok(fmt.Sprintf("its %d views take size, metrics and pointer events (%d pictures drawn)", len(h.views), h.pictures))
		for v := range h.views {
			h.write(fmt.Sprintf("0 view %d closed\n", v))
			delete(h.views, v)
		}
		h.serve(300 * time.Millisecond)
	}
}

// checkUnknownRequest asks something no feature defines: the answer is error ID unknown
func (h *host) checkUnknownRequest(d *doc) {
	m, ok := h.ask(fmt.Sprintf("frobnicate %d %d 1 3 invoked -", d.id, d.version))
	if f := strings.Fields(m.line); ok && len(f) >= 3 && f[0] == "error" && f[2] == "unknown" {
		h.ok("answers a request it does not know with error ID unknown")
	} else {
		h.fail("requests", fmt.Sprintf("a request it does not know must be answered error ID unknown; got %q", m.line))
	}
}

// checkCompletion validates an answer against 12b and the document it was asked about
func checkCompletion(m message, d *doc, features map[string]bool) string {
	_, why := checkCompletionList(m, d, features)
	return why
}

// completionInfo is what a list rhun can read holds: its items, and the first item with resolve
// data (complete-items)
type completionInfo struct {
	items   int
	resolve *resolvable
}

// checkCompletionList is checkCompletion, with what the list holds
func checkCompletionList(m message, d *doc, features map[string]bool) (completionInfo, string) {
	var info completionInfo
	f := strings.Fields(m.line)
	if len(f) != 3 || f[0] != "ok" {
		return info, fmt.Sprintf("the answer is %q, not ok ID LEN", m.line)
	}
	if m.err != nil {
		return info, m.err.Error()
	}
	b := m.body
	if len(b) > 1<<20 {
		return info, "the list is over 1 MB"
	}
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return info, "the body has no first line N MORE"
	}
	head := strings.Fields(string(b[:nl]))
	if len(head) != 2 || (head[1] != "0" && head[1] != "1") {
		return info, fmt.Sprintf("the first line %q is not N MORE", b[:nl])
	}
	n, err := strconv.Atoi(head[0])
	if err != nil || n < 0 || n > 4096 {
		return info, fmt.Sprintf("N %q is not 0 to 4096", head[0])
	}
	pos := nl + 1
	for i := 0; i < n; i++ {
		e := bytes.IndexByte(b[pos:], '\n')
		if e < 0 {
			return info, fmt.Sprintf("item %d has no header line", i+1)
		}
		h := strings.Fields(string(b[pos : pos+e]))
		if len(h) != 9 {
			return info, fmt.Sprintf("item %d's header %q is not L1 C1 L2 C2 KIND LABEL_LEN INSERT_LEN DETAIL_LEN EXTRA_LEN", i+1, b[pos:pos+e])
		}
		start, why := d.position(h[0], h[1])
		if why != "" {
			return info, fmt.Sprintf("item %d's range starts badly: %s", i+1, why)
		}
		end, why := d.position(h[2], h[3])
		if why != "" {
			return info, fmt.Sprintf("item %d's range ends badly: %s", i+1, why)
		}
		if end < start {
			return info, fmt.Sprintf("item %d's range ends before it starts", i+1)
		}
		pos += e + 1
		sizes := make([]int, 4)
		for j := range sizes {
			v, err := strconv.Atoi(h[5+j])
			if err != nil || v < 0 || v > len(b)-pos {
				return info, fmt.Sprintf("item %d: %q is not a length within the body", i+1, h[5+j])
			}
			sizes[j] = v
		}
		if sizes[0] > len(b)-pos || sizes[1] > len(b)-pos-sizes[0] || sizes[2] > len(b)-pos-sizes[0]-sizes[1] ||
			sizes[3] > len(b)-pos-sizes[0]-sizes[1]-sizes[2] {
			return info, fmt.Sprintf("item %d's bytes run past the body", i+1)
		}
		label := b[pos : pos+sizes[0]]
		detail := b[pos+sizes[0]+sizes[1] : pos+sizes[0]+sizes[1]+sizes[2]]
		if bytes.ContainsAny(label, "\r\n") || bytes.ContainsAny(detail, "\r\n") {
			return info, fmt.Sprintf("item %d's label or detail holds a line break", i+1)
		}
		if !utf8.Valid(label) {
			return info, fmt.Sprintf("item %d's label is not UTF-8", i+1)
		}
		extra := b[pos+sizes[0]+sizes[1]+sizes[2] : pos+sizes[0]+sizes[1]+sizes[2]+sizes[3]]
		insert := b[pos+sizes[0] : pos+sizes[0]+sizes[1]]
		var r resolvable
		if why := checkExtraItem(extra, d, start, end, insert, features, &r); why != "" {
			return info, fmt.Sprintf("item %d: %s", i+1, why)
		}
		if r.data != nil && info.resolve == nil {
			info.resolve = &r
		}
		pos += sizes[0] + sizes[1] + sizes[2] + sizes[3]
	}
	if pos != len(b) {
		return info, fmt.Sprintf("%d bytes after the %d items N says", len(b)-pos, n)
	}
	info.items = n
	return info, ""
}

// checkExtra validates an item's EXTRA records: each a header line whose last word is its length,
// then that many bytes; an edit's range in the document, overlapping neither the item nor another
func checkExtra(x []byte, d *doc, istart, iend int, insert []byte, features map[string]bool) string {
	return checkExtraItem(x, d, istart, iend, insert, features, &resolvable{})
}

// checkExtraItem is checkExtra, with the complete-items records when the feature is agreed: r
// gets the item's resolve data, range and edits
func checkExtraItem(x []byte, d *doc, istart, iend int, insert []byte, features map[string]bool, r *resolvable) string {
	taken := []extent{{istart, iend}}
	var fields []rhunext.Field
	var choices []rhunext.SnippetChoice
	var variants []int
	var transforms []rhunext.SnippetFieldTransform
	transformsSeen := false
	for len(x) > 0 {
		e := bytes.IndexByte(x, '\n')
		if e < 0 {
			return "an EXTRA record without its header line"
		}
		w := strings.Fields(string(x[:e]))
		if len(w) < 2 {
			return fmt.Sprintf("the EXTRA record %q has no length", x[:e])
		}
		n, err := strconv.Atoi(w[len(w)-1])
		if err != nil || n < 0 || n > len(x)-e-1 {
			return fmt.Sprintf("the EXTRA record %q: its length is not within the item", x[:e])
		}
		if w[0] == "transforms" {
			if transformsSeen || !features["snippets"] || !features["snippet-transforms"] {
				return "duplicate or unnegotiated snippet transforms"
			}
			transformsSeen = true
			bindings, err := snippetTransforms(w, x[e+1:e+1+n])
			if err != nil {
				return err.Error()
			}
			transforms = bindings
		}
		if w[0] == "variant" {
			if !features["snippets"] || !features["snippet-variants"] {
				return "initial variant features were not agreed"
			}
			if len(variants) == rhunext.MaxSnippetFields {
				return "too many initial variants"
			}
			index, err := snippetVariant(w)
			if err != nil {
				return err.Error()
			}
			variants = append(variants, index)
		}
		if w[0] == "field" {
			field, err := snippetField(w)
			if err != nil || len(fields) == rhunext.MaxSnippetFields {
				return "invalid snippet field header or count"
			}
			fields = append(fields, field)
		}
		if w[0] == "choice" {
			if len(choices) == rhunext.MaxSnippetGroups {
				return "too many snippet choices"
			}
			c, err := snippetChoice(w, x[e+1:e+1+n])
			if err != nil {
				return err.Error()
			}
			choices = append(choices, c)
		}
		if w[0] == "cursor" {
			// cursor OFFSET 0: the cursor that far into INSERT, at a character
			off, err := strconv.Atoi(w[1])
			if len(w) != 3 || w[2] != "0" || err != nil || off < 0 {
				return fmt.Sprintf("%q is not cursor OFFSET 0", x[:e])
			}
			if off > len(insert) || (off < len(insert) && !utf8.RuneStart(insert[off])) {
				return fmt.Sprintf("cursor %d is past INSERT or inside a character: rhun ignores it", off)
			}
		}
		if w[0] == "edit" {
			if len(w) != 6 {
				return fmt.Sprintf("%q is not edit L1 C1 L2 C2 LEN", x[:e])
			}
			a, why := d.position(w[1], w[2])
			if why != "" {
				return "an extra edit starts badly: " + why
			}
			b, why := d.position(w[3], w[4])
			if why != "" {
				return "an extra edit ends badly: " + why
			}
			if b < a {
				return "an extra edit ends before it starts"
			}
			for _, t := range taken {
				if a < t.b && t.a < b {
					return "an extra edit overlaps the item's range or another edit"
				}
			}
			taken = append(taken, extent{a, b})
		}
		if features["complete-items"] {
			if why := checkItemRecord(w, x[e+1:e+1+n], d, istart, r); why != "" {
				return why
			}
		}
		x = x[e+1+n:]
	}
	r.taken = taken
	if len(fields) > 0 || len(choices) > 0 || len(variants) > 0 || transformsSeen {
		if err := (rhunext.SnippetProgram{Snippet: rhunext.PreparedSnippet{Text: string(insert), Fields: fields}, Choices: choices, InitialVariants: variants, Transforms: transforms}).Validate(); err != nil {
			return "invalid snippet fields: " + err.Error()
		}
	}
	return ""
}

// checkSignature validates an answer against 12c
func checkSignature(m message) string {
	f := strings.Fields(m.line)
	if len(f) != 3 || f[0] != "ok" {
		return fmt.Sprintf("the answer is %q, not ok ID LEN", m.line)
	}
	if n, err := strconv.Atoi(f[2]); err != nil || n < 0 {
		return fmt.Sprintf("%q: LEN is not a length", m.line)
	}
	if m.err != nil {
		return m.err.Error()
	}
	if len(m.body) == 0 {
		return ""
	}
	if len(m.body) > 65536 {
		return "over 64 KB"
	}
	parts := strings.SplitN(string(m.body), "\n", 3)
	if len(parts) < 2 {
		return "the body is not FROM TO, then the signature"
	}
	ft := strings.Fields(parts[0])
	if len(ft) != 2 {
		return fmt.Sprintf("the first line %q is not FROM TO", parts[0])
	}
	a, e1 := strconv.Atoi(ft[0])
	b, e2 := strconv.Atoi(ft[1])
	if e1 != nil || e2 != nil {
		return fmt.Sprintf("the first line %q is not two numbers", parts[0])
	}
	if !(a == -1 && b == -1) && (a < 0 || b < a || b > len(parts[1])) {
		return fmt.Sprintf("FROM TO %d %d is not within the signature's %d bytes", a, b, len(parts[1]))
	}
	return ""
}
