package main

// The 1.1 features the checker agrees without playing them in full: workspace_watch
// (watch-workspace, unwatch-workspace), decorations (decoration-type, decorate), semantic-tokens'
// legends (semantic-legend) and diagnostic-tags (SEVERITY+TAG in diagnostics). The presentation
// providers (folding-ranges, semantic-tokens, inlay-hints, code-lens, document-highlights,
// document-colors, inline-complete) are asked by rhun; the checker agrees them and does not ask.
// markdown only changes how rhun shows texts, so there is nothing to read.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// presState is what the checker keeps of these features.
type presState struct {
	watch     int64           // workspace_watch: this runtime's handle, 0 for none
	decoTypes map[string]bool // decorations: this extension's types
	legends   map[string]bool // semantic-tokens: the legends sent
}

var (
	decoName   = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	legendName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	legendItem = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}(=[^ =]+)?$`)
	colorSpec  = regexp.MustCompile(`^([a-z][a-z0-9_]*(/(100|[0-9]{1,2}))?|#[0-9a-fA-F]{6}|#[0-9a-fA-F]{8})$`)
)

// decoColor is COLOR: SPEC or SPEC|SPEC
func decoColor(s string) bool {
	a, b, two := strings.Cut(s, "|")
	return colorSpec.MatchString(a) && (!two || colorSpec.MatchString(b))
}

var gutterShapes = map[string]bool{"dot": true, "circle": true, "ring": true, "square": true, "diamond": true,
	"triangle": true, "bar": true, "check": true, "cross": true, "plus": true, "minus": true, "arrow": true}

// checkDecorationType reads a type body: "" or the first bad line, as rhun's `syntax line N`
func checkDecorationType(b []byte) string {
	if len(b) > 4096 {
		return "a type body over 4 KB"
	}
	for i, l := range strings.Split(string(b), "\n") {
		if l == "" {
			continue
		}
		w := strings.Split(l, " ")
		ok := false
		switch k, a := w[0], w[1:]; k {
		case "color", "background", "line-background", "scrollbar":
			ok = len(a) == 1 && decoColor(a[0])
		case "border":
			n, err := strconv.Atoi(strings.Join(a[:min(1, len(a))], ""))
			ok = len(a) == 2 && err == nil && n >= 1 && n <= 3 && decoColor(a[1])
		case "underline":
			ok = len(a) == 2 && map[string]bool{"solid": true, "double": true, "dotted": true, "dashed": true, "squiggle": true}[a[0]] && decoColor(a[1])
		case "strike":
			ok = len(a) == 1 && (a[0] == "-" || decoColor(a[0]))
		case "faded":
			ok = len(a) == 0
		case "before", "after":
			ok = len(a) == 2 && decoColor(a[0]) && (a[1] == "-" || decoColor(a[1]))
		case "width":
			n, err := strconv.Atoi(strings.Join(a, ""))
			ok = len(a) == 1 && err == nil && n >= 0 && n <= 200
		case "gutter":
			ok = len(a) == 2 && gutterShapes[a[0]] && decoColor(a[1])
		case "grow":
			ok = len(a) == 1 && map[string]bool{"none": true, "start": true, "end": true, "both": true}[a[0]]
		case "priority":
			n, err := strconv.Atoi(strings.Join(a, ""))
			ok = len(a) == 1 && err == nil && n >= 0 && n <= 100
		}
		if !ok {
			return fmt.Sprintf("line %d: %q", i+1, l)
		}
	}
	return ""
}

// checkDecorate reads decorate's N records against d's text when version is its current one:
// "" or why rhun refuses them, with rhun's error code
func checkDecorate(b []byte, n int, d *doc, current bool) (string, string) {
	if n < 0 || n > 20000 {
		return "toobig", fmt.Sprintf("%d records (at most 20000)", n)
	}
	c := &cursor{b: b}
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return "syntax", fmt.Sprintf("record %d has no header line (N says %d)", i, n)
		}
		w := strings.Split(l, " ")
		if len(w) != 8 {
			return "syntax", fmt.Sprintf("record %d's header %q is not L1 C1 L2 C2 BEFORE_LEN AFTER_LEN TIP_LEN EXTRA_LEN", i, l)
		}
		var lens [4]int
		for j := range lens {
			v, err := strconv.Atoi(w[4+j])
			if err != nil || v < 0 {
				return "syntax", fmt.Sprintf("record %d: %q is not a length", i, w[4+j])
			}
			lens[j] = v
		}
		if lens[0] > 200 || lens[1] > 200 || lens[2] > 1000 || lens[3] > 9000 {
			return "toobig", fmt.Sprintf("record %d: BEFORE and AFTER take 200 bytes, TIP 1000, EXTRA 9000", i)
		}
		texts := make([][]byte, 4)
		for j, ln := range lens {
			if texts[j], ok = c.take(ln); !ok {
				return "syntax", fmt.Sprintf("record %d's texts run past the body", i)
			}
		}
		for j := 0; j < 2; j++ {
			if strings.ContainsFunc(string(texts[j]), func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				return "syntax", fmt.Sprintf("record %d: a before or after text with a control character", i)
			}
		}
		if current && d != nil {
			a, why := d.position(w[0], w[1])
			e := 0
			if why == "" {
				e, why = d.position(w[2], w[3])
			}
			if why == "" && e < a {
				why = "the range ends before it starts"
			}
			if why != "" {
				return "badpos", fmt.Sprintf("record %d: %s", i, why)
			}
		}
	}
	if c.rest() != 0 {
		return "syntax", fmt.Sprintf("%d bytes after the %d records", c.rest(), n)
	}
	return "", ""
}

// checkSemanticLegend reads a legend's body: a line of types and one of modifiers
func checkSemanticLegend(b []byte) (string, string) {
	if len(b) > 16<<10 {
		return "toobig", "a legend body over 16 KB"
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 {
		return "syntax", fmt.Sprintf("%d lines, not a line of types and one of modifiers", len(lines))
	}
	for i, l := range lines {
		names := strings.Fields(l)
		if max := []int{256, 64}[i]; len(names) > max {
			return "toobig", fmt.Sprintf("%d %s (at most %d)", len(names), []string{"types", "modifiers"}[i], max)
		}
		for _, nm := range names {
			if !legendItem.MatchString(nm) {
				return "syntax", fmt.Sprintf("%q is not a name of [A-Za-z0-9_.-] with an optional =EFFECT", nm)
			}
		}
	}
	return "", ""
}

// diagSeverity is a diagnostics line's SEVERITY: v1's four, and with diagnostic-tags agreed
// SEVERITY+TAG[+TAG] (unknown tags are ignored)
func diagSeverity(s string, tags bool) bool {
	sev, rest, tagged := strings.Cut(s, "+")
	switch sev {
	case "error", "warning", "info", "hint":
	default:
		return false
	}
	if !tagged {
		return true
	}
	if !tags {
		return false
	}
	for _, t := range strings.Split(rest, "+") {
		if t == "" {
			return false
		}
	}
	return true
}

// presentRequest answers these features' requests; false for any other verb.
func (h *host) presentRequest(verb string, id int64, args []string, body func(string) ([]byte, bool),
	need func(string) bool, getDoc func(string) *doc, reply func(string), rerr func(code, text string)) bool {
	feat := map[string]string{"watch-workspace": "workspace_watch", "unwatch-workspace": "workspace_watch",
		"decoration-type": "decorations", "decorate": "decorations", "semantic-legend": "semantic-tokens"}[verb]
	if feat == "" {
		return false
	}
	if !h.features[feat] {
		h.fail("features", fmt.Sprintf("%s without %s agreed after hello", verb, feat))
		rerr("unknown", "no such request")
		return true
	}
	if h.pres.decoTypes == nil {
		h.pres.decoTypes = map[string]bool{}
		h.pres.legends = map[string]bool{}
	}
	grammar := func(want string) {
		h.fail(feat, verb+" takes "+want)
		rerr("syntax", "cannot parse the line")
	}
	switch verb {
	case "watch-workspace":
		if len(args) != 0 {
			grammar("nothing")
			return true
		}
		if p := h.m.needs["project"]; p != "read" && p != "write" {
			h.fail("needs", "watch-workspace needs project=read but the manifest does not say so")
			rerr("needs", "project=read")
			return true
		}
		if h.pres.watch == 0 {
			h.pres.watch = 1
			h.ok("workspace_watch: subscribed")
		}
		reply(fmt.Sprintf("ok %d %d", id, h.pres.watch))
	case "unwatch-workspace":
		n, err := strconv.ParseInt(strings.Join(args, ""), 10, 64)
		if len(args) != 1 || err != nil || n < 1 || n > 2147483647 {
			grammar("HANDLE")
			return true
		}
		if n != h.pres.watch {
			h.fail("workspace_watch", fmt.Sprintf("unwatch-workspace %d: not a handle it holds", n))
			rerr("refused", "no such subscription")
			return true
		}
		h.pres.watch = 0
		reply(fmt.Sprintf("ok %d", id))
	case "decoration-type":
		if len(args) != 2 || !decoName.MatchString(args[0]) {
			grammar("NAME LEN (NAME 1 to 40 of a-z 0-9 _ -)")
			return true
		}
		b, ok := body(args[1])
		if !ok || !need("read") {
			return true
		}
		if why := checkDecorationType(b); why != "" {
			h.fail("decorations", "decoration-type "+args[0]+": "+why)
			rerr("syntax", why)
			return true
		}
		if !h.pres.decoTypes[args[0]] && len(h.pres.decoTypes) >= 64 {
			h.fail("limits", "more than 64 decoration types")
			rerr("toobig", "64 types")
			return true
		}
		h.pres.decoTypes[args[0]] = true
		reply(fmt.Sprintf("ok %d", id))
	case "decorate":
		if len(args) != 7 {
			grammar("DOC VERSION NAME FROM TO N LEN")
			return true
		}
		b, ok := body(args[6])
		if !ok || !need("read") {
			return true
		}
		var nums [4]int64
		for i, s := range []string{args[1], args[3], args[4], args[5]} {
			v, err := strconv.ParseInt(s, 10, 64)
			if err != nil || v < 0 {
				grammar("DOC VERSION NAME FROM TO N LEN")
				return true
			}
			nums[i] = v
		}
		if (nums[1] == 0) != (nums[2] == 0) || nums[2] < nums[1] {
			h.fail("decorations", fmt.Sprintf("decorate: FROM %d TO %d is neither 0 0 nor a line range", nums[1], nums[2]))
			rerr("syntax", "cannot parse the line")
			return true
		}
		d := getDoc(args[0])
		if d == nil {
			return true
		}
		if !h.pres.decoTypes[args[2]] {
			h.fail("decorations", fmt.Sprintf("decorate: %s is not one of its decoration types", args[2]))
			rerr("syntax", "unknown decoration type")
			return true
		}
		if nums[0] > d.version {
			h.fail("versions", fmt.Sprintf("decorate: version %d is newer than the document's %d", nums[0], d.version))
			rerr("stale", strconv.FormatInt(d.version, 10))
			return true
		}
		if code, why := checkDecorate(b, int(nums[3]), d, nums[0] == d.version); code != "" {
			h.fail("decorations", "decorate: "+why)
			rerr(code, why)
			return true
		}
		reply(fmt.Sprintf("ok %d", id))
	case "semantic-legend":
		if len(args) != 2 || !legendName.MatchString(args[0]) {
			grammar("LEGEND LEN (LEGEND 1 to 64 of [A-Za-z0-9_.-])")
			return true
		}
		b, ok := body(args[1])
		if !ok {
			return true
		}
		if code, why := checkSemanticLegend(b); code != "" {
			h.fail("semantic-tokens", "semantic-legend "+args[0]+": "+why)
			rerr(code, why)
			return true
		}
		if !h.pres.legends[args[0]] && len(h.pres.legends) >= 8 {
			h.fail("limits", "more than 8 semantic legends")
			rerr("toobig", "8 legends")
			return true
		}
		h.pres.legends[args[0]] = true
		reply(fmt.Sprintf("ok %d", id))
	}
	return true
}
