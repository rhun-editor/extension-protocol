package main

// Protocol 1.1 contributions in the manifest and the data files, checked as rhun reads them:
// [keys], [menu.*], [submenu.*], [status], [context], [icons], [language.*], the language keys of
// syntax/*.syn and icon-themes/*.icons. Every when clause is compiled (rhunext.WhenCheck, rhun's
// grammar and offsets). rhun drops a bad entry and lists it on the extension's page; here it is a
// failure, since an author wants to see it before users do.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

type iniLine struct {
	sec, key, val string
	n             int
}

func readINI(data []byte) []iniLine {
	var out []iniLine
	sec := ""
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			sec = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out = append(out, iniLine{sec, strings.TrimSpace(k), strings.TrimSpace(v), i + 1})
	}
	return out
}

var menuLocations = map[string]bool{"editor": true, "explorer": true, "tab": true, "terminal": true, "extension": true,
	"editor_title": true, "view_title": true, "view_item": true, "view_item_inline": true,
	"history_commit": true, "history_file": true, "scm_resource": true, "scm_group": true, "scm_title": true}

var langKeys = map[string]bool{"brackets": true, "autoclose": true, "autoclose_code": true, "surround": true,
	"indent_open": true, "indent_close": true, "indent_middle": true, "indent_dedent_after": true,
	"continue": true, "doc_block": true, "fold": true, "fold_markers": true}

func validContextKey(k string) bool {
	if k == "" || len(k) > 40 || k[0] < 'a' || k[0] > 'z' {
		return false
	}
	return strings.Trim(k, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") == ""
}

func validLocal(s string) bool {
	return s != "" && len(s) <= 40 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
}

// pngSize reads a PNG's width and height from its header
func pngSize(b []byte) (int, int, bool) {
	if len(b) < 24 || !bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")) || string(b[12:16]) != "IHDR" {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24])), true
}

// checkPNG: a relative path inside dir to a PNG of at most max bytes and dim pixels a side
func checkPNG(dir, rel string, max, dim int) string {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") || strings.Contains(rel, "\\") {
		return fmt.Sprintf("%q is not a path inside the folder", rel)
	}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	switch {
	case err != nil:
		return fmt.Sprintf("%s: %v", rel, err)
	case len(b) > max:
		return fmt.Sprintf("%s: %d bytes, more than %d", rel, len(b), max)
	}
	w, h, ok := pngSize(b)
	switch {
	case !ok:
		return rel + ": not a PNG"
	case w < 1 || h < 1 || w > dim || h > dim:
		return fmt.Sprintf("%s: %dx%d, more than %d pixels a side", rel, w, h, dim)
	}
	return ""
}

// langValue: a language key's value as rhun takes it, "" when good
func langValue(key, v string) string {
	f := strings.Fields(v)
	longest := 0
	for _, t := range f {
		longest = max(longest, len(t))
	}
	switch {
	case len(f) == 0:
		return "empty"
	case key == "brackets":
		for _, t := range f {
			if len(t) != 2 {
				return "brackets are two-byte pairs"
			}
		}
		if len(f) > 16 {
			return "at most 16 pairs"
		}
	case key == "autoclose":
		if len(f)%2 != 0 || len(f) > 32 || longest > 8 {
			return "OPEN CLOSE pairs, each 1 to 8 bytes, at most 16"
		}
	case key == "autoclose_code":
		if longest > 8 || len(f) > 16 {
			return "OPEN tokens of 1 to 8 bytes"
		}
	case key == "surround":
		if len(f)%2 != 0 || longest != 1 || len(f) > 32 {
			return "one-byte OPEN CLOSE pairs"
		}
	case key == "doc_block":
		if len(f) != 3 || longest > 14 {
			return "START MIDDLE END, each at most 14 bytes"
		}
	case key == "fold_markers":
		if len(f) != 2 || longest > 14 {
			return "START END, each at most 14 bytes"
		}
	case key == "fold":
		if v != "brackets" && v != "indent" {
			return "brackets or indent"
		}
	case key == "comment":
		if len(f) != 1 || longest > 14 {
			return "one token of at most 14 bytes"
		}
	case key == "block":
		if len(f) != 2 || longest > 14 {
			return "START END, each at most 14 bytes"
		}
	case key == "ident":
		if len(f) != 1 || longest > 31 {
			return "at most 31 bytes"
		}
	case key == "colon_indent":
		if v != "yes" && v != "no" {
			return "yes or no"
		}
	default: // the token lists
		if len(f) > 32 || longest > 15 {
			return "at most 32 tokens of at most 14 bytes"
		}
	}
	return ""
}

// keyStroke: one stroke as [keys] writes it (modifiers and a key joined by +)
func keyStroke(s string) (typing bool, ok bool) {
	parts := strings.Split(s, "+")
	key := parts[len(parts)-1]
	mod := false
	for _, m := range parts[:len(parts)-1] {
		switch strings.ToLower(m) {
		case "ctrl", "alt", "super", "cmd", "meta":
			mod = true
		case "shift":
		default:
			return false, false
		}
	}
	if key == "" {
		return false, false
	}
	named := map[string]bool{"Enter": true, "Tab": true, "Escape": true, "Backspace": true, "Delete": true, "Up": true,
		"Down": true, "Left": true, "Right": true, "Home": true, "End": true, "PageUp": true, "PageDown": true,
		"Insert": true, "Space": true}
	typingKey := len(key) == 1 || named[key]
	return typingKey && !mod, true
}

// keyGuard: a clause with !textInputFocus, viewFocus or view == ID as a top-level && term
func keyGuard(clause string) bool {
	for _, t := range strings.Split(clause, "&&") {
		t = strings.TrimSpace(t)
		if t == "!textInputFocus" || t == "viewFocus" {
			return true
		}
		if rest, ok := strings.CutPrefix(t, "view"); ok {
			rest = strings.TrimSpace(rest)
			if v, ok := strings.CutPrefix(rest, "=="); ok {
				v = strings.TrimSpace(v)
				if v != "" && v != "''" && v != `""` {
					return true
				}
			}
		}
	}
	return false
}

func (h *host) clause(where, c string) {
	if e := rhunext.WhenCheck(c); e != nil {
		h.fail("when", fmt.Sprintf("%s: %v", where, e))
	}
}

// checkContributions: the manifest's 1.1 sections and the data files
func (h *host) checkContributions() {
	data, err := os.ReadFile(filepath.Join(h.dir, "extension.ini"))
	if err != nil {
		return
	}
	fails := len(h.fails)
	lines := readINI(data)
	own := map[string]bool{}
	for _, c := range h.m.commands {
		own[c] = true
	}
	menuItems := map[string]map[string]bool{}
	statusItems := map[string]bool{}
	contextKeys := 0
	icons := 0
	seen := 0
	for _, l := range lines {
		where := fmt.Sprintf("extension.ini:%d [%s] %s", l.n, l.sec, l.key)
		switch {
		case l.sec == "keys":
			seen++
			keys := l.key
			for _, p := range []string{"linux:", "mac:", "windows:"} {
				keys = strings.TrimPrefix(keys, p)
			}
			strokes := strings.Fields(keys)
			if len(strokes) < 1 || len(strokes) > 2 {
				h.fail("keys", where+": one stroke or two")
				continue
			}
			typing := false
			for i, s := range strokes {
				t, ok := keyStroke(s)
				if !ok {
					h.fail("keys", fmt.Sprintf("%s: %q is not a stroke", where, s))
				}
				if i == 0 {
					typing = t
				}
			}
			if len(strokes) == 2 && typing {
				h.fail("keys", where+": a chord's first stroke needs ctrl, alt or super")
			}
			value, clause, hasWhen := strings.Cut(l.val, " when ")
			if strings.HasPrefix(l.val, "when ") {
				value, clause, hasWhen = "", l.val[5:], true
			}
			cmd, args, _ := strings.Cut(strings.TrimSpace(value), " ")
			if cmd == "" {
				h.fail("keys", where+": no command")
			} else if !strings.Contains(cmd, ".") && cmd != "none" && !own[cmd] {
				h.warn("keys", fmt.Sprintf("%s: %s is not one of its [commands] (a runtime command must register it)", where, cmd))
			}
			if len(args) > 4096 {
				h.fail("keys", where+": ARGS over 4096 bytes")
			}
			if hasWhen {
				h.clause(where, clause)
			}
			if len(strokes) == 1 && typing && !(hasWhen && keyGuard(clause)) {
				h.fail("keys", where+": a typing key needs !textInputFocus, viewFocus or view == ... in its when clause")
			}
		case strings.HasPrefix(l.sec, "menu.") || strings.HasPrefix(l.sec, "submenu."):
			seen++
			if loc, ok := strings.CutPrefix(l.sec, "menu."); ok && !menuLocations[loc] {
				h.fail("menus", where+": unknown menu location "+loc)
				continue
			}
			if l.key == "title" && strings.HasPrefix(l.sec, "submenu.") {
				continue
			}
			item, field, ok := strings.Cut(l.key, ".")
			if !ok || !validLocal(item) {
				h.fail("menus", where+": not ITEM.FIELD")
				continue
			}
			if menuItems[l.sec] == nil {
				menuItems[l.sec] = map[string]bool{}
			}
			menuItems[l.sec][item] = true
			switch field {
			case "command", "group", "args":
			case "order":
				if _, err := strconv.Atoi(l.val); err != nil {
					h.fail("menus", where+": order is a whole number")
				}
			case "when":
				h.clause(where, l.val)
			case "submenu":
				if strings.HasPrefix(l.sec, "submenu.") {
					h.fail("menus", where+": a submenu inside a submenu")
				}
			default:
				h.fail("menus", where+": not ITEM.command, .group, .order, .when, .args or .submenu")
			}
		case l.sec == "status":
			seen++
			item, field, ok := strings.Cut(l.key, ".")
			if !ok || !validLocal(item) {
				h.fail("status", where+": not ITEM.FIELD")
				continue
			}
			statusItems[item] = true
			switch field {
			case "text":
				if len(l.val) > 120 || !rhunext.PlainText(l.val) {
					h.fail("status", where+": one line of at most 120 bytes")
				}
			case "tooltip", "args":
			case "command":
				if !own[l.val] {
					h.fail("status", where+": not one of its own [commands]")
				}
			case "align":
				if l.val != "left" && l.val != "right" {
					h.fail("status", where+": left or right")
				}
			case "priority":
				if p, err := strconv.Atoi(l.val); err != nil || p < -1000 || p > 1000 {
					h.fail("status", where+": -1000 to 1000")
				}
			case "color":
				if !map[string]bool{"fg": true, "muted": true, "accent": true, "error": true, "warning": true, "success": true}[l.val] {
					h.fail("status", where+": fg, muted, accent, error, warning or success")
				}
			case "background":
				if l.val != "error" && l.val != "warning" {
					h.fail("status", where+": error or warning")
				}
			case "when":
				h.clause(where, l.val)
			default:
				h.fail("status", where+": not a status item field")
			}
		case l.sec == "context":
			seen++
			contextKeys++
			if !validContextKey(l.key) {
				h.fail("context", where+": a lowercase letter, then letters, digits and _, at most 40 bytes")
			}
			if l.val == "" || len(l.val) > 256 || !rhunext.PlainText(l.val) {
				h.fail("context", where+": one line of at most 256 bytes")
			}
		case l.sec == "icons":
			seen++
			icons++
			if l.key == "" || len(l.key) > 40 || strings.Trim(l.key, "abcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
				h.fail("icons", where+": a name is 1 to 40 of a-z 0-9 _ -")
			}
			if why := checkPNG(h.dir, l.val, 65536, 256); why != "" {
				h.fail("icons", where+": "+why)
			}
		case strings.HasPrefix(l.sec, "language."):
			seen++
			name := strings.TrimPrefix(l.sec, "language.")
			if name == "" || len(name) > 64 {
				h.fail("language", where+": a language name is 1 to 64 bytes")
			}
			if !langKeys[l.key] && !map[string]bool{"comment": true, "block": true, "ident": true, "colon_indent": true}[l.key] {
				h.fail("language", where+": not a language key")
			} else if why := langValue(l.key, l.val); why != "" {
				h.fail("language", where+": "+why)
			}
		}
	}
	for sec, items := range menuItems {
		if len(items) > 16 {
			h.fail("menus", fmt.Sprintf("[%s]: %d items, rhun keeps 16", sec, len(items)))
		}
	}
	if len(statusItems) > 8 {
		h.fail("status", fmt.Sprintf("[status]: %d items, at most 8", len(statusItems)))
	}
	if contextKeys > 64 {
		h.fail("context", "[context]: more than 64 keys")
	}
	if icons > 32 {
		h.fail("icons", "[icons]: more than 32 icons")
	}
	// syntax/*.syn: the language keys
	syns, _ := filepath.Glob(filepath.Join(h.dir, "syntax", "*.syn"))
	for _, p := range syns {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		seen++
		for _, l := range readINI(b) {
			if l.sec == "" && langKeys[l.key] {
				if why := langValue(l.key, l.val); why != "" {
					h.fail("language", fmt.Sprintf("%s:%d %s: %s", filepath.Base(p), l.n, l.key, why))
				}
			}
		}
	}
	// icon-themes/*.icons
	themes, _ := filepath.Glob(filepath.Join(h.dir, "icon-themes", "*.icons"))
	for _, p := range themes {
		seen++
		h.checkIconTheme(p)
	}
	if seen > 0 && len(h.fails) == fails {
		h.ok(fmt.Sprintf("contributions (keys, menus, status, context, icons, languages, icon themes): %d entries as rhun reads them", seen))
	}
}

// checkIconTheme: an icon-themes/NAME.icons file
func (h *host) checkIconTheme(path string) {
	b, err := os.ReadFile(path)
	base := filepath.Base(path)
	if err != nil {
		h.fail("icon theme", base+": "+err.Error())
		return
	}
	if len(b) > 262144 {
		h.fail("icon theme", base+": over 256 KB")
		return
	}
	dir := filepath.Dir(path)
	icons := map[string]bool{}
	maps := 0
	var targets []iniLine
	for _, l := range readINI(b) {
		where := fmt.Sprintf("%s:%d", base, l.n)
		switch l.sec {
		case "":
			if l.key == "fallback" && l.val != "default" && l.val != "rhun" {
				h.fail("icon theme", where+": fallback is default or rhun")
			}
		case "icons":
			icons[l.key] = true
			f := strings.Fields(l.val)
			if len(f) == 0 || len(f) > 2 {
				h.fail("icon theme", where+": PNG [COLOR] or rhun:ICON [COLOR]")
				continue
			}
			if !strings.HasPrefix(f[0], "rhun:") {
				if why := checkPNG(dir, f[0], 65536, 256); why != "" {
					h.fail("icon theme", where+": "+why)
				}
			}
			if len(f) == 2 && !map[string]bool{"red": true, "orange": true, "yellow": true, "green": true, "blue": true,
				"purple": true, "pink": true, "cyan": true, "grey": true, "gray": true, "white": true, "accent": true,
				"muted": true}[f[1]] {
				h.fail("icon theme", where+": an unknown color "+f[1])
			}
		case "files", "extensions", "languages", "folders", "folders_open":
			maps++
			targets = append(targets, l)
		default:
			h.warn("icon theme", where+": a section rhun does not read: "+l.sec)
		}
	}
	if len(icons) > 512 {
		h.fail("icon theme", base+": more than 512 icons")
	}
	if maps > 4096 {
		h.fail("icon theme", base+": more than 4096 mapping lines")
	}
	for _, l := range targets {
		if !icons[l.val] {
			h.fail("icon theme", fmt.Sprintf("%s:%d: [%s] %s names no icon %q", base, l.n, l.sec, l.key, l.val))
		}
	}
}
