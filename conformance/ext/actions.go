package main

// Protocol 1.1's navigation, prepare-rename, partial-format and code-actions (v1.1/v1.1.md,
// "Language features"): the checker sends each request whose feature was agreed and whose
// [provides] key matches a document (declaration, type_definition, implementation; rename for
// prepare-rename; range_format, type_format with type_format.triggers; actions for code-actions)
// and reads the answer as rhun does. As in langfeat.go, what rhun drops fails the check and what it
// copes with is a warning.

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"
)

// navigation is each navigation request with its [provides] key
var navigation = []struct{ verb, key string }{
	{"declaration", "declaration"}, {"type-definition", "type_definition"}, {"implementation", "implementation"},
}

// askedKeyFeatures are the 1.1 features whose [provides] keys the checker asks about (besides
// providerFeatures), with those keys
var askedKeyFeatures = []string{"navigation", "partial-format", "hover-query", "selection-ranges", "hierarchy",
	"workspace-symbols", "document-links"}

var featureKeys = map[string][]string{
	"navigation":        {"declaration", "type_definition", "implementation"},
	"partial-format":    {"range_format", "type_format"},
	"hover-query":       {"hover"},
	"selection-ranges":  {"selection_ranges"},
	"hierarchy":         {"call_hierarchy", "type_hierarchy"},
	"workspace-symbols": {"workspace_symbols"},
	"document-links":    {"document_links"},
}

// checkKeyFeatures checks that each 1.1 feature agreed has the [provides] keys that make rhun ask
func (h *host) checkKeyFeatures() {
	for _, f := range askedKeyFeatures {
		if h.features[f] && !slices.ContainsFunc(featureKeys[f], func(k string) bool { return h.manifestPatterns(k) != "" }) {
			h.fail("provides", fmt.Sprintf("%s agreed after hello but [provides] has none of %s: rhun never asks", f, strings.Join(featureKeys[f], ", ")))
		}
	}
	if h.features["partial-format"] {
		patterns, triggers := h.manifestPatterns("type_format") != "", h.manifestPatterns("type_format.triggers") != ""
		switch {
		case patterns && !triggers:
			h.warn("provides", "[provides] type_format without type_format.triggers: rhun never sends type-format")
		case triggers && !patterns:
			h.warn("provides", "[provides] type_format.triggers without type_format patterns: rhun never sends type-format")
		}
	}
	if h.features["code-actions"] && h.manifestPatterns("actions") == "" {
		h.fail("provides", "code-actions agreed after hello but [provides] has no actions patterns: rhun never asks")
	}
	if d := h.m.needs["documents"]; h.features["file-diagnostics"] && d != "read" && d != "write" {
		h.warn("needs", "file-diagnostics agreed after hello, but [needs] documents is not read: rhun answers every set needs")
	}
	if h.features["prepare-rename"] && (!h.features["rename"] || h.manifestPatterns("rename") == "") {
		h.warn("provides", "prepare-rename agreed after hello, but the extension is no rename provider (rename agreed and [provides] rename patterns): rhun never asks")
	}
}

// hierarchicalKind says whether s is a hierarchical kind (quickfix, refactor.extract,
// source.organizeImports): dot-separated segments of ASCII letters, digits, _ and -, at most 128
// bytes
func hierarchicalKind(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, seg := range strings.Split(s, ".") {
		if seg == "" || strings.Trim(seg, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
			return false
		}
	}
	return true
}

// kindWanted says whether an action of kind matches one of the kinds asked (it equals or descends
// from one), any kind when none were asked
func kindWanted(kinds []string, kind string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, k := range kinds {
		if kind == k || strings.HasPrefix(kind, k+".") {
			return true
		}
	}
	return false
}

// commandKnown says whether NAME is one of the extension's own commands: a manifest command, or
// one it registered and still has
func (h *host) commandKnown(name string) bool {
	return slices.Contains(h.m.commands, name) || slices.Contains(h.rtcmds, h.m.name+"."+name)
}

// commandName says why NAME is not a command reference's name ([a-z0-9_]{1,40}), or ""
func commandName(name string) string {
	if name == "" || len(name) > 40 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return fmt.Sprintf("the command name %q is not [a-z0-9_]{1,40} (rhun refuses the whole answer)", name)
	}
	return ""
}

// ---- prepare-rename ----

// checkPrepareRename validates a prepare-rename answer about d: the placeholder (empty with ok ID
// 0, the word at the cursor), what is odd but allowed, or why rhun ignores it and opens the prompt
// with the word at the cursor
func checkPrepareRename(m message, d *doc) (string, []string, string) {
	b, why := answer(m)
	if why != "" {
		return "", nil, why
	}
	if len(b) == 0 {
		return "", nil, ""
	}
	if len(b) > 4096 {
		return "", nil, "the answer is over 4 KiB"
	}
	c := &cursor{b: b}
	l, ok := c.line()
	if !ok {
		return "", nil, "the body has no line L1 C1 L2 C2 PLEN"
	}
	w := c.fields(l)
	if len(w) != 5 {
		return "", nil, fmt.Sprintf("the first line %q is not L1 C1 L2 C2 PLEN", l)
	}
	if why := numbers(w); why != "" {
		return "", nil, why
	}
	if n := count(w[4]); n > 200 {
		return "", nil, fmt.Sprintf("PLEN %d: the placeholder is at most 200 bytes", n)
	} else if n != c.rest() {
		return "", nil, fmt.Sprintf("PLEN says %d bytes, but %d come", n, c.rest())
	}
	ph := b[c.pos:]
	if why := oneLine("the placeholder", ph); why != "" {
		return "", nil, why
	}
	if _, _, why := span(d, w[:4]); why != "" {
		return "", nil, "the range: " + why
	}
	return string(ph), c.warns, ""
}

// askPrepareRename sends prepare-rename about d, as F2 does before the prompt opens
func (h *host) askPrepareRename(d *doc) {
	const f = "prepare-rename"
	m, ok := h.askWithin(fmt.Sprintf("prepare-rename %d %d 1 1 invoked -", d.id, d.version), 2*time.Second)
	if !ok {
		h.fail(f, "no answer to a prepare-rename request within 2 s (rhun's wait): F2 opens the prompt with the word at the cursor")
		return
	}
	if w := strings.SplitN(m.line, " ", 4); w[0] == "error" {
		text := ""
		if len(w) == 4 {
			text = strings.TrimSpace(w[3])
		}
		switch {
		case len(w) < 3:
			// hostReply failed it already
		case w[2] == "refused":
			if text == "" {
				h.warn(f, "error ID refused without TEXT: rhun shows TEXT to say why nothing can be renamed there")
			} else if len(text) > 200 {
				h.warn(f, fmt.Sprintf("the refusal's TEXT is %d bytes: rhun shows 200", len(text)))
			}
			h.ok("refuses prepare-rename with error ID refused TEXT (rhun shows the text, and no rename follows)")
		case w[2] == "unknown":
			h.warn(f, fmt.Sprintf("it agreed prepare-rename after hello, yet answers %q: F2 opens the prompt with the word at the cursor", m.line))
		default:
			h.ok(fmt.Sprintf("answers a prepare-rename request %q (F2 opens the prompt with the word at the cursor)", m.line))
		}
		return
	}
	ph, warns, why := checkPrepareRename(m, d)
	for _, w := range warns {
		h.warn(f, w)
	}
	switch {
	case why != "":
		h.fail(f, "a prepare-rename answer rhun ignores (F2 opens the prompt with the word at the cursor): "+why)
	case len(m.body) == 0:
		h.ok("answers prepare-rename with ok ID 0 (the word at the cursor)")
	default:
		h.ok(fmt.Sprintf("answers prepare-rename with a range and the placeholder %q", ph))
	}
}

// ---- partial-format ----

// lineEnd is the byte offset of the end of d's first line
func lineEnd(d *doc) int {
	if i := bytes.IndexByte(d.text, '\n'); i >= 0 {
		return i
	}
	return len(d.text)
}

// typeInto types text at byte at of d as the user does: the document changes, the cursor goes after
// it, and the extension hears the edit; the cursor's LINE COL
func (h *host) typeInto(d *doc, at int, text []byte) (int, int) {
	d.text = append(append(append([]byte{}, d.text[:at]...), text...), d.text[at:]...)
	recs := records(at, 0, text)
	d.version += int64(len(recs))
	d.dirty = true
	d.cur, d.anchor = at+len(text), at+len(text)
	h.afterEdit(d, recs...)
	line, col := 1, 1
	for _, c := range d.text[:d.cur] {
		if c == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}

// checkFormatEdit validates a range-format or type-format answer: a workspace edit for d only,
// checked as rhun checks it, then made; what it says, or "" after failing f
func (h *host) checkFormatEdit(f string, m message, d *doc, max int) string {
	b, why := answer(m)
	if why == "" && len(b) > max {
		why = fmt.Sprintf("the edit is over %d bytes, the cap of a %s answer", max, f)
	}
	var gs []wsGroup
	if why == "" {
		gs, _, why = h.checkWorkspaceEdit(b, d)
	}
	if why != "" {
		h.fail(f, why)
		return ""
	}
	h.apply(gs)
	if len(gs) == 0 {
		return "no edits"
	}
	return "an edit of its document"
}

// askPartialFormat sends range-format and type-format as Format Selection and typing a trigger do
func (h *host) askPartialFormat(sample func(string) *doc, gone func(f, after string) bool) bool {
	if h.patterns("range_format") != "" {
		d := sample("range_format")
		line := fmt.Sprintf("range-format %d %d 1 1 1 %d 4 yes yes yes", d.id, d.version, lineEnd(d)+1)
		if m, ok := h.langAsk("range-format", line); ok {
			if said := h.checkFormatEdit("range-format", m, d, 4<<20); said != "" {
				h.ok("answers range-format requests with a workspace edit rhun can make (" + said + ")")
			}
			if gone("range-format", "the edits of its range-format") {
				return true
			}
		}
	}
	if h.patterns("type_format") == "" {
		return false
	}
	triggers := strings.Fields(h.patterns("type_format.triggers"))
	if len(triggers) == 0 {
		return false // rhun never sends type-format (checkKeyFeatures says so)
	}
	d := sample("type_format")
	ch := triggers[0]
	typed := []byte(ch)
	if ch == "newline" {
		typed = []byte("\n")
	}
	line, col := h.typeInto(d, lineEnd(d), typed)
	h.serve(100 * time.Millisecond)
	req := fmt.Sprintf("type-format %d %d %d %d %s 4 yes yes yes", d.id, d.version, line, col, ch)
	if m, ok := h.langAsk("type-format", req); ok {
		if said := h.checkFormatEdit("type-format", m, d, 64<<10); said != "" {
			h.ok(fmt.Sprintf("answers type-format requests after %q with a workspace edit rhun can make (%s)", ch, said))
		}
		if gone("type-format", "the edits of its type-format") {
			return true
		}
	}
	return false
}

// ---- code-actions ----

// action is one action of a code-actions answer
type action struct {
	kind, title string
	edit        []byte
	preferred   bool
	off         bool // disabled, for disabled's reason
	disabled    string
	resolve     []byte
	command     string
	args        []byte
	fixes       string
}

// checkCommandRecord validates a command record's header words (command NAME ARGS_LEN) and args
func (h *host) checkCommandRecord(w []string, args []byte) string {
	if len(w) != 3 {
		return fmt.Sprintf("%q is not command NAME ARGS_LEN", strings.Join(w, " "))
	}
	if why := commandName(w[1]); why != "" {
		return why
	}
	if len(args) > 8192 {
		return fmt.Sprintf("the command's args are %d bytes: at most 8192 (rhun refuses the whole answer)", len(args))
	}
	if !h.commandKnown(w[1]) {
		return fmt.Sprintf("the command %s is not one of its commands ([commands] or registered): it can never run", w[1])
	}
	return ""
}

// walkRecords walks EXTRA records: each header line's last word is its length, that many bytes
// follow; each goes to fn, whose reason stops the walk
func walkRecords(x []byte, fn func(w []string, data []byte) string) string {
	for len(x) > 0 {
		e := bytes.IndexByte(x, '\n')
		if e < 0 {
			return "an extra record without its header line"
		}
		w := strings.Split(string(x[:e]), " ")
		n := count(w[len(w)-1])
		if len(w) < 2 || slices.Contains(w, "") || n < 0 || n > len(x)-e-1 {
			return fmt.Sprintf("the extra record %q: its length is not within the extra records", x[:e])
		}
		if why := fn(w, x[e+1:e+1+n]); why != "" {
			return why
		}
		x = x[e+1+n:]
	}
	return ""
}

// checkCodeActions validates a code-actions answer to a request for kinds (none: any): the
// actions and what is odd but allowed, or why rhun drops it
func (h *host) checkCodeActions(m message, kinds []string) ([]action, []string, string) {
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
	var acts []action
	for i := 1; i <= n; i++ {
		l, ok := c.line()
		if !ok {
			return nil, nil, fmt.Sprintf("action %d has no header line (N says %d)", i, n)
		}
		w := c.fields(l)
		if len(w) != 4 {
			return nil, nil, fmt.Sprintf("action %d's header %q is not KIND TITLE_LEN EDIT_LEN EXTRA_LEN", i, l)
		}
		if !hierarchicalKind(w[0]) {
			return nil, nil, fmt.Sprintf("action %d: KIND %q is not a hierarchical kind of at most 128 bytes", i, w[0])
		}
		if why := numbers(w[1:]); why != "" {
			return nil, nil, fmt.Sprintf("action %d: %s", i, why)
		}
		tl := count(w[1])
		if tl < 1 || tl > 1000 {
			return nil, nil, fmt.Sprintf("action %d: TITLE_LEN %q is not 1 to 1000", i, w[1])
		}
		title, ok1 := c.take(tl)
		edit, ok2 := c.take(count(w[2]))
		extra, ok3 := c.take(count(w[3]))
		if !ok1 || !ok2 || !ok3 {
			return nil, nil, fmt.Sprintf("action %d's bytes run past the body", i)
		}
		a := action{kind: w[0], title: string(title), edit: edit}
		if why := oneLine("the title", title); why != "" {
			c.warn("action %d: %s", i, why)
		}
		if len(edit) > 0 {
			if _, code, why := editGrammar(edit); why != "" {
				return nil, nil, fmt.Sprintf("action %d (%q): its edit cannot be made whatever the documents say (rhun would answer an apply %s): %s",
					i, title, code, why)
			}
		}
		why := walkRecords(extra, func(w []string, data []byte) string {
			bare := len(w) == 2
			switch w[0] {
			case "preferred":
				if !bare || w[1] != "0" {
					return fmt.Sprintf("%q is not preferred 0", strings.Join(w, " "))
				}
				a.preferred = true
			case "disabled":
				switch {
				case !bare:
					return fmt.Sprintf("%q is not disabled LEN", strings.Join(w, " "))
				case len(data) > 1000:
					return fmt.Sprintf("a reason of %d bytes: at most 1000", len(data))
				case len(data) == 0:
					c.warn("action %d (%q): disabled without a reason: rhun shows the reason when it is chosen", i, title)
				}
				if why := oneLine("the reason", data); why != "" {
					return why
				}
				a.off, a.disabled = true, string(data)
			case "resolve":
				if !bare {
					return fmt.Sprintf("%q is not resolve LEN", strings.Join(w, " "))
				}
				if len(data) > 16<<10 {
					return fmt.Sprintf("resolve data of %d bytes: at most 16 KiB", len(data))
				}
				a.resolve = data
			case "command":
				if why := h.checkCommandRecord(w, data); why != "" {
					return why
				}
				a.command, a.args = w[1], data
			case "fixes":
				if !bare {
					return fmt.Sprintf("%q is not fixes LEN", strings.Join(w, " "))
				}
				if why := oneLine("the diagnostic ids", data); why != "" {
					return why
				}
				a.fixes = string(data)
			}
			return ""
		})
		if why != "" {
			return nil, nil, fmt.Sprintf("action %d (%q): %s", i, title, why)
		}
		if len(a.edit) == 0 && len(a.resolve) == 0 && a.command == "" && !a.off {
			return nil, nil, fmt.Sprintf("action %d (%q) does nothing (no edit, resolve data or command, and not disabled): rhun leaves it out", i, title)
		}
		if !kindWanted(kinds, a.kind) {
			return nil, nil, fmt.Sprintf("action %d (%q): its kind %s is none of the kinds asked (%s): rhun drops it", i, title, a.kind, strings.Join(kinds, ","))
		}
		acts = append(acts, a)
	}
	if c.rest() > 0 {
		return nil, nil, fmt.Sprintf("%d bytes after the %d actions N says", c.rest(), n)
	}
	return acts, c.warns, ""
}

// checkActionResolve validates a code-action-resolve answer: EDIT_LEN EXTRA_LEN, the edit (as rhun
// reads it whatever the documents say) and command records only; the edit, or why rhun drops it
func (h *host) checkActionResolve(m message) ([]byte, string) {
	b, why := answer(m)
	if why != "" {
		return nil, why
	}
	if len(b) > 4<<20 {
		return nil, "the answer is over 4 MiB"
	}
	c := &cursor{b: b}
	l, ok := c.line()
	w := c.fields(l)
	if !ok || len(w) != 2 || numbers(w) != "" {
		return nil, fmt.Sprintf("the first line %q is not EDIT_LEN EXTRA_LEN", l)
	}
	edit, ok1 := c.take(count(w[0]))
	extra, ok2 := c.take(count(w[1]))
	if !ok1 || !ok2 {
		return nil, "the edit and the extra records run past the body"
	}
	if c.rest() > 0 {
		return nil, fmt.Sprintf("%d bytes after the edit and the extra records", c.rest())
	}
	if len(edit) > 0 {
		if _, code, why := editGrammar(edit); why != "" {
			return nil, fmt.Sprintf("its edit cannot be made whatever the documents say (rhun would answer an apply %s): %s", code, why)
		}
	}
	commands := 0
	why = walkRecords(extra, func(w []string, data []byte) string {
		if w[0] != "command" {
			return fmt.Sprintf("a %s record: a resolve answer holds command records only", w[0])
		}
		commands++
		return h.checkCommandRecord(w, data)
	})
	if why == "" && commands > 1 {
		why = fmt.Sprintf("%d command records: one runs after the edit", commands)
	}
	return edit, why
}

// askCodeActions sends code-actions about d for any kind and for quick fixes, then resolves the
// first enabled action with resolve data and no edit (else makes the edit of the first with one), as
// rhun does when it is chosen
func (h *host) askCodeActions(d *doc) {
	const f = "code-actions"
	var all []action
	for _, kinds := range [][]string{nil, {"quickfix"}} {
		k := "-"
		if kinds != nil {
			k = strings.Join(kinds, ",")
		}
		m, ok := h.langAsk(f, fmt.Sprintf("code-actions %d %d 1 1 1 3 invoked %s -", d.id, d.version, k))
		if !ok {
			continue
		}
		acts, warns, why := h.checkCodeActions(m, kinds)
		for _, w := range warns {
			h.warn(f, w)
		}
		if why != "" {
			h.fail(f, fmt.Sprintf("an answer to code-actions for kinds %s: %s", k, why))
			continue
		}
		h.ok(fmt.Sprintf("answers code-actions for kinds %s with actions rhun can list (%d)", k, len(acts)))
		if kinds == nil {
			all = acts
		}
	}
	enabled := func(a action) bool { return !a.off }
	if i := slices.IndexFunc(all, func(a action) bool { return enabled(a) && len(a.resolve) > 0 && len(a.edit) == 0 }); i >= 0 {
		a := all[i]
		m, ok := h.askBodyWithin(fmt.Sprintf("code-action-resolve %d %d %d", d.id, d.version, len(a.resolve)), a.resolve, 5*time.Second)
		switch w := strings.Fields(m.line); {
		case !ok:
			h.fail(f, fmt.Sprintf("no answer to code-action-resolve for %q within 5 s (rhun's wait)", a.title))
		case len(w) >= 3 && w[0] == "error":
			h.ok(fmt.Sprintf("answers code-action-resolve for %q %q (rhun applies nothing)", a.title, m.line))
		default:
			edit, why := h.checkActionResolve(m)
			if why != "" {
				h.fail(f, fmt.Sprintf("a code-action-resolve answer for %q rhun drops: %s", a.title, why))
				return
			}
			if len(edit) > 0 {
				gs, code, why := h.checkWorkspaceEdit(edit, nil)
				if why != "" {
					h.fail(f, fmt.Sprintf("the resolved edit of %q is one rhun refuses (nothing changes): %s: %s", a.title, code, why))
					return
				}
				h.apply(gs)
			}
			h.ok(fmt.Sprintf("answers code-action-resolve for %q with an edit and command rhun can use", a.title))
		}
		return
	}
	if i := slices.IndexFunc(all, func(a action) bool { return enabled(a) && len(a.edit) > 0 }); i >= 0 {
		a := all[i]
		gs, code, why := h.checkWorkspaceEdit(a.edit, nil)
		if why != "" {
			h.fail(f, fmt.Sprintf("its action %q, chosen at once, has an edit rhun refuses (nothing changes): %s: %s", a.title, code, why))
			return
		}
		h.apply(gs)
		h.ok(fmt.Sprintf("its chosen action %q has an edit rhun can make", a.title))
	}
}
