package rhunext

// When clauses (v1.1/v1.1.md "When clauses"): WhenCheck compiles a clause as rhun does and reports
// the first error with its byte offset, so an extension (and rhun-ext-check) can find a bad clause
// before rhun drops the item it guards. The grammar, limits, messages and offsets are rhun's
// (src/app/when.s); v1.1/when-vectors.txt holds the shared test vectors.

import (
	"strings"
	"unicode/utf8"
)

// The limits of a clause.
const (
	WhenMax      = 512 // bytes
	WhenOpsMax   = 64  // operations
	WhenDepthMax = 8   // levels of ! and parentheses
)

// WhenBuiltinKeys are rhun's built-in context keys (a typo in one is an error).
var WhenBuiltinKeys = map[string]bool{}

// WhenSettings are rhun's settings, SECTION.KEY, as config.SECTION.KEY may name them.
var WhenSettings = map[string]bool{}

func init() {
	for _, k := range []string{
		"editorFocus", "textInputFocus", "paletteFocus", "findFocus", "explorerFocus", "agentsFocus",
		"terminalFocus", "scmInputFocus", "settingsFocus", "findVisible", "completionVisible", "signatureVisible",
		"snippetActive", "activeTab", "activeTabPinned", "tabCount", "resourceLang", "resourceExt",
		"resourceFilename", "resourceDirname", "resourcePath", "resourceScheme", "resourceInProject", "editorReadonly",
		"editorDirty", "editorHasSelection", "editorHasMultipleSelections", "editorProviders", "vimMode", "sidebarVisible",
		"agentsVisible", "terminalVisible", "projectOpen", "projectPath", "gitRepo", "gitBranch",
		"gitChanges", "isLinux", "isMac", "isWindows", "isWayland", "targetKind",
		"explorerResourceIsFolder", "explorerResourceIsRoot", "tabKind", "viewFocus", "view", "viewItem",
		"debugState", "inDebugMode", "debugType", "gitTrusted", "scmProvider", "scmResourceGroup",
		"scmResourceStatus", "notebookFocus", "notebookCellEditing", "notebookCellType", "notebookKernelState", "remoteName",
		"remoteHost",
	} {
		WhenBuiltinKeys[k] = true
	}
	for _, k := range []string{
		"ui.follow_system", "ui.theme", "ui.dark_theme", "ui.light_theme",
		"ui.scale", "ui.font_size", "ui.font", "ui.file_icons",
		"ui.sidebar", "ui.sidebar_width", "ui.agents_panel", "ui.agents_width",
		"ui.tooltips", "ui.auto_hide_scrollbars", "ui.decorations", "ui.scroll_sensitivity",
		"ui.fast_scroll_sensitivity", "editor.font_size", "editor.font", "editor.line_height",
		"editor.tab_width", "editor.insert_spaces", "editor.line_numbers", "editor.highlight_line",
		"editor.animate_disk_changes", "editor.match_brackets", "editor.indent_guides", "editor.word_wrap",
		"editor.whitespace", "editor.cursor_blink", "editor.smooth_caret", "editor.auto_pairs",
		"editor.multi_cursor_paste", "editor.multi_cursor_modifier", "editor.scroll_past_end", "editor.vim_mode",
		"editor.complete_replace", "editor.complete_commit_characters", "editor.lightbulb", "editor.format_on_type",
		"editor.format_on_paste", "editor.folding", "files.trim_trailing_whitespace", "files.final_newline",
		"files.format_on_save", "files.format_on_save_timeout", "files.code_actions_on_save", "files.code_actions_on_save_timeout",
		"files.restore_session", "files.restore_project", "files.exclude", "agents.claude",
		"agents.codex", "agents.grok", "agents.opencode", "terminal.shell",
		"terminal.font_size", "terminal.font", "terminal.line_height", "terminal.scrollback",
		"terminal.height", "git.enabled", "git.commit_ai", "git.commit_model",
		"git.ai_setup", "extensions.secrets", "updates.check", "updates.check_now",
		"updates.extensions", "updates.check_extensions", "debug.show_on_start", "debug.keys_in_terminal",
		"debug.console_lines", "remote.agent_binary", "testing.gutter",
	} {
		WhenSettings[k] = true
	}
}

// WhenError is a clause that does not compile: Offset is the byte rhun reports.
type WhenError struct {
	Offset int
	Text   string
}

func (e *WhenError) Error() string { return e.Text + " at byte " + itoa(int64(e.Offset)) }

type whenParser struct {
	s     string
	p     int
	ops   int
	depth int
	err   *WhenError
}

// WhenCheck reports whether the clause compiles; nil when it does.
func WhenCheck(clause string) *WhenError {
	w := &whenParser{s: clause}
	if len(clause) > WhenMax {
		return &WhenError{WhenMax, "too long (512 bytes at most)"}
	}
	w.ws()
	if w.p >= len(w.s) {
		return &WhenError{0, "empty clause"}
	}
	w.or()
	if w.err != nil {
		return w.err
	}
	w.ws()
	if w.p < len(w.s) {
		w.fail("expected && or ||")
	}
	return w.err
}

func (w *whenParser) failAt(at int, text string) {
	if w.err == nil {
		w.err = &WhenError{at, text}
	}
}

func (w *whenParser) fail(text string) { w.failAt(w.p, text) }

func (w *whenParser) ws() {
	for w.p < len(w.s) && (w.s[w.p] == ' ' || w.s[w.p] == '\t') {
		w.p++
	}
}

func (w *whenParser) peek2() (byte, byte) {
	var a, b byte
	if w.p < len(w.s) {
		a = w.s[w.p]
	}
	if w.p+1 < len(w.s) {
		b = w.s[w.p+1]
	}
	return a, b
}

func (w *whenParser) emit() {
	if w.ops >= WhenOpsMax {
		w.fail("too many operations (64 at most)")
		return
	}
	w.ops++
}

func (w *whenParser) or() {
	w.and()
	for w.err == nil {
		w.ws()
		if a, b := w.peek2(); a != '|' || b != '|' {
			return
		}
		w.p += 2
		w.and()
		w.emit()
	}
}

func (w *whenParser) and() {
	w.unary()
	for w.err == nil {
		w.ws()
		if a, b := w.peek2(); a != '&' || b != '&' {
			return
		}
		w.p += 2
		w.unary()
		w.emit()
	}
}

func (w *whenParser) deeper() bool {
	w.depth++
	if w.depth > WhenDepthMax {
		w.fail("nested too deeply (8 levels at most)")
		return false
	}
	return true
}

func (w *whenParser) unary() {
	if w.err != nil {
		return
	}
	w.ws()
	a, b := w.peek2()
	switch {
	case a == '!':
		if b == '=' {
			w.fail("expected a key, true, false, ! or (")
			return
		}
		w.p++
		if !w.deeper() {
			return
		}
		w.unary()
		w.depth--
		w.emit()
	case a == '(':
		w.p++
		if !w.deeper() {
			return
		}
		w.or()
		if w.err != nil {
			return
		}
		w.depth--
		w.ws()
		if a, _ := w.peek2(); a != ')' {
			w.fail("expected )")
			return
		}
		w.p++
	case a == '\'' || a == '"':
		w.quoted()
		if w.err != nil {
			return
		}
		w.emit() // the literal
		w.ws()
		op := w.inWord()
		if op == 0 {
			w.fail("expected in or not in")
			return
		}
		w.list()
		w.emit()
	default:
		start := w.p
		word := w.word()
		if word == "" {
			w.fail("expected a key, true, false, ! or (")
			return
		}
		if word == "true" || word == "false" {
			w.emit()
			return
		}
		w.key(start, word)
		if w.err == nil {
			w.tail()
		}
	}
}

func (w *whenParser) tail() {
	w.ws()
	a, b := w.peek2()
	two := false
	switch {
	case a == '=':
		if b != '=' && b != '~' {
			w.fail("expected == != < <= > >= =~ in or not in")
			return
		}
		two = true
	case a == '!':
		if b != '=' {
			w.fail("expected == != < <= > >= =~ in or not in")
			return
		}
		two = true
	case a == '<' || a == '>':
		two = b == '='
	default:
		if op := w.inWord(); op != 0 {
			w.list()
			w.emit()
		}
		return
	}
	w.p++
	if two {
		w.p++
	}
	w.value()
	w.emit()
}

// inWord: "in" or "not in" (moving past it), 1 or 2; 0 (not moving) otherwise
func (w *whenParser) inWord() int {
	start := w.p
	word := w.word()
	if word == "in" {
		return 1
	}
	if word == "not" {
		w.ws()
		after := w.p
		if w.word() == "in" {
			return 2
		}
		w.p = after
		w.fail("expected in or not in")
		return 0
	}
	w.p = start
	return 0
}

func (w *whenParser) value() {
	w.ws()
	if a, _ := w.peek2(); a == '\'' || a == '"' {
		w.quoted()
	} else if w.bare() == "" {
		w.fail("expected a value")
		return
	}
	if w.err == nil {
		w.emit()
	}
}

func (w *whenParser) list() {
	w.ws()
	if a, _ := w.peek2(); a == '\'' || a == '"' {
		w.quoted()
		if w.err == nil {
			w.emit()
		}
		return
	}
	start := w.p
	word := w.word()
	if word == "" {
		w.fail("expected a key or a quoted list")
		return
	}
	w.key(start, word)
}

func isWordByte(c byte) bool {
	return c == '_' || c == '.' || c == '/' || c == '-' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'
}

func (w *whenParser) word() string {
	start := w.p
	for w.p < len(w.s) && isWordByte(w.s[w.p]) {
		w.p++
	}
	return w.s[start:w.p]
}

func (w *whenParser) bare() string {
	start := w.p
	for w.p < len(w.s) {
		c := w.s[w.p]
		if !(strings.IndexByte("_-./:*?+#@$", c) >= 0 || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z') {
			break
		}
		w.p++
	}
	return w.s[start:w.p]
}

func (w *whenParser) quoted() {
	q := w.s[w.p]
	open := w.p
	i := w.p + 1
	for ; i < len(w.s); i++ {
		c := w.s[i]
		if c == q {
			break
		}
		if c < 0x20 || c == 0x7f {
			w.failAt(open, "unclosed quote")
			return
		}
	}
	if i >= len(w.s) {
		w.failAt(open, "unclosed quote") // (reported at the opening quote, as rhun does)
		return
	}
	if !utf8.ValidString(w.s[open+1 : i]) {
		w.p = open
		w.fail("a quoted value that is not UTF-8")
		return
	}
	w.p = i + 1
}

func whenLocalOK(k string) bool {
	if k == "" || len(k) > 40 || k[0] < 'a' || k[0] > 'z' {
		return false
	}
	for i := 1; i < len(k); i++ {
		c := k[i]
		if !(c == '_' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z') {
			return false
		}
	}
	return true
}

func whenSettingOK(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	return strings.Trim(k, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
}

// key: a built-in key, config.SECTION.KEY, NAME.KEY, OWNER/NAME.KEY or NAME.setting.KEY
func (w *whenParser) key(start int, k string) {
	unknown := func() {
		word := k
		if len(word) > 60 {
			word = word[:60]
		}
		w.failAt(start, "unknown key "+word)
	}
	if strings.HasPrefix(k, "config.") {
		rest := k[7:]
		dot := strings.IndexByte(rest, '.')
		if dot <= 0 || dot == len(rest)-1 || !WhenSettings[rest] {
			unknown()
			return
		}
		w.emit()
		return
	}
	dot := strings.IndexByte(k, '.')
	if dot < 0 {
		if !WhenBuiltinKeys[k] {
			unknown()
			return
		}
		w.emit()
		return
	}
	if dot == 0 || dot == len(k)-1 {
		unknown()
		return
	}
	rest := k[dot+1:]
	if strings.HasPrefix(rest, "setting.") {
		if !whenSettingOK(rest[8:]) {
			unknown()
			return
		}
	} else if !whenLocalOK(rest) {
		unknown()
		return
	}
	w.emit()
}
