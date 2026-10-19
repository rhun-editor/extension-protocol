package main

// Protocol 1.1: workspace edits as transactions (feature workspace-edits) and file operations
// (feature file-operations). With workspace-edits agreed the checker reads every workspace edit
// the extension sends (apply, a rename answer, a code action's edit; format and save-actions
// answers keep 12g's grammar) with the transaction grammar: text groups with PRE a version, "-"
// or fnv1a64:HEX, create, rename and delete entries with their flags, the label and confirm
// records and the skipping of unknown ones. It checks the rules rhun applies before it changes
// anything (two entries on one path, an edit of a deleted or renamed-onto file, an entry inside
// a folder another renames or deletes, .git, the project folder, open documents with unsaved
// changes, targets that exist, sources that do not, hashes, ranges) and then makes the edit as
// rhun does: open documents in their buffers, files that are not open on disk, the open
// documents it edits written when it touches disk, renamed documents reported closed and opened
// under a new id, deleted ones closed. An apply refused is answered error ID CODE ENTRY TEXT.

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

// txCaps is which grammar the checker reads: ws for workspace-edits, ops for file-operations
type txCaps struct{ ws, ops bool }

func (h *host) txCaps() txCaps {
	return txCaps{ws: h.features["workspace-edits"], ops: h.features["workspace-edits"] && h.features["file-operations"]}
}

// txOp is a file operation of a transaction
type txOp struct {
	kind     string // create, rename, delete
	path, to string
	flags    map[string]bool
	entry    int
}

// txGroup is a text group of a transaction: its PATH as written, PRE (version -1 for "-" and
// fnv1a64:HEX) and edits; once checked, the text it names and its edits in rhun's order
type txGroup struct {
	raw     []byte
	version int64
	hashed  bool
	hash    uint64
	edits   []wsEdit
	t       place
	entry   int
	path    string // the file it names on disk ("" for an untitled document)
	create  bool   // a create entry makes it
}

// tx is a parsed transaction
type tx struct {
	groups  []txGroup
	ops     []txOp
	label   string
	confirm bool
	warns   []string
}

var opFlags = map[string][]string{"create": {"overwrite", "ignore-existing"}, "rename": {"overwrite", "ignore-existing"}, "delete": {"recursive", "ignore-missing"}}

// parseTx reads a transaction as rhun does before it looks at any document or file: the grammar
// and the caps; a refusal has its code, the 1-based entry at fault (0 for none) and why
func parseTx(b []byte, caps txCaps) (tx, string, int, string) {
	var t tx
	bad := func(code string, entry int, format string, a ...any) (tx, string, int, string) {
		return tx{}, code, entry, fmt.Sprintf(format, a...)
	}
	if len(b) > 4<<20 {
		return bad("toobig", 0, "the workspace edit is over 4 MB")
	}
	c := &cursor{b: b}
	l, ok := c.line()
	n := -1
	if w := c.fields(l); ok && len(w) == 1 {
		n = count(w[0])
	}
	switch {
	case n < 0:
		return bad("syntax", 0, "the first line %q is not N, the number of entries", l)
	case n > 1000:
		return bad("toobig", 0, "%d entries: at most 1000", n)
	}
	total := 0
	for e := 1; e <= n; e++ {
		l, ok := c.line()
		if !ok {
			return bad("syntax", e, "entry %d has no header line (N says %d)", e, n)
		}
		w := c.fields(l)
		if len(w) == 0 {
			return bad("syntax", e, "entry %d's header %q has an empty field", e, l)
		}
		if _, isOp := opFlags[w[0]]; isOp {
			if !caps.ops {
				return bad("syntax", e, "entry %d is a %s, which needs file-operations agreed after hello", e, w[0])
			}
			want := 3
			if w[0] == "rename" {
				want = 4
			}
			if len(w) != want {
				return bad("syntax", e, "entry %d's header %q is not %s", e, l, map[string]string{"create": "create PATH_LEN FLAGS",
					"rename": "rename FROM_LEN TO_LEN FLAGS", "delete": "delete PATH_LEN FLAGS"}[w[0]])
			}
			op := txOp{kind: w[0], flags: map[string]bool{}, entry: e}
			if f := w[len(w)-1]; f != "-" {
				for _, x := range strings.Split(f, ",") {
					if !slices.Contains(opFlags[w[0]], x) || op.flags[x] {
						return bad("syntax", e, "entry %d: %q is not a flag of %s (%s)", e, x, w[0], strings.Join(opFlags[w[0]], ", "))
					}
					op.flags[x] = true
				}
			}
			lens := w[1 : len(w)-1]
			var paths []string
			for _, x := range lens {
				pl := count(x)
				if pl < 1 || pl > 4096 {
					return bad("syntax", e, "entry %d: a path length %q is not 1 to 4096", e, x)
				}
				p, ok := c.take(pl)
				if !ok {
					return bad("syntax", e, "entry %d's path runs past the body", e)
				}
				paths = append(paths, string(p))
			}
			op.path = paths[0]
			if len(paths) > 1 {
				op.to = paths[1]
			}
			t.ops = append(t.ops, op)
			continue
		}
		if len(w) != 3 {
			return bad("syntax", e, "entry %d's header %q is not PATH_LEN PRE M", e, l)
		}
		pl := count(w[0])
		if pl < 1 || pl > 4096 {
			return bad("syntax", e, "entry %d: PATH_LEN %q is not 1 to 4096", e, w[0])
		}
		g := txGroup{entry: e, version: -1}
		switch v := w[1]; {
		case v == "-":
		case strings.HasPrefix(v, "fnv1a64:") && caps.ws:
			hex := v[len("fnv1a64:"):]
			if len(hex) != 16 || strings.Trim(hex, "0123456789abcdef") != "" {
				return bad("syntax", e, "entry %d: %q is not fnv1a64: and 16 lowercase hexadecimal digits", e, v)
			}
			g.hashed = true
			g.hash, _ = strconv.ParseUint(hex, 16, 64)
		case len(v) <= 18 && strings.Trim(v, "0123456789") == "":
			g.version, _ = strconv.ParseInt(v, 10, 64)
		default:
			return bad("syntax", e, "entry %d: PRE %q is not a version, - or fnv1a64:HEX", e, v)
		}
		m := count(w[2])
		if m < 0 {
			return bad("syntax", e, "entry %d: M %q is not a number of at most 10 digits", e, w[2])
		}
		path, ok := c.take(pl)
		if !ok {
			return bad("syntax", e, "entry %d's path runs past the body", e)
		}
		if total += m; total > 100000 {
			return bad("toobig", e, "over 100000 edits")
		}
		g.raw = path
		for k := 1; k <= m; k++ {
			l, ok := c.line()
			if !ok {
				return bad("syntax", e, "entry %d, edit %d has no header line (M says %d)", e, k, m)
			}
			w := c.fields(l)
			if len(w) != 5 {
				return bad("syntax", e, "entry %d, edit %d: %q is not L1 C1 L2 C2 LEN", e, k, l)
			}
			if why := numbers(w); why != "" {
				return bad("syntax", e, "entry %d, edit %d: %s", e, k, why)
			}
			text, ok := c.take(count(w[4]))
			if !ok {
				return bad("syntax", e, "entry %d, edit %d's text runs past the body", e, k)
			}
			g.edits = append(g.edits, wsEdit{at: w[:4], text: text})
		}
		t.groups = append(t.groups, g)
	}
	// extra records to the end of the body: NAME ARGS... LEN and LEN bytes
	for c.rest() > 0 {
		l, _ := c.line()
		w := c.fields(l)
		if !caps.ws {
			return bad("syntax", 0, "%d bytes after the %d entries N says", c.rest()+len(l)+1, n)
		}
		if len(w) < 2 || count(w[len(w)-1]) < 0 {
			return bad("syntax", 0, "the record %q is not NAME ARGS... LEN", l)
		}
		x, ok := c.take(count(w[len(w)-1]))
		if !ok {
			return bad("syntax", 0, "the record %q runs past the body", l)
		}
		switch w[0] {
		case "label":
			if len(w) != 2 {
				return bad("syntax", 0, "the record %q is not label LEN", l)
			}
			if why := oneLine("the label", x); why != "" {
				return bad("syntax", 0, "%s", why)
			}
			if len(x) > 200 {
				t.warns = append(t.warns, fmt.Sprintf("a label of %d bytes: rhun shows 200", len(x)))
			}
			t.label = string(x)
		case "confirm":
			if len(w) != 2 || len(x) != 0 {
				return bad("syntax", 0, "the record %q is not confirm 0", l)
			}
			t.confirm = true
		}
	}
	return t, "", 0, ""
}

// inProject says whether a path lies in the project or the extension's state folder
func (h *host) inProject(p string) bool {
	for _, root := range []string{h.project, h.state} {
		if root == "" {
			continue
		}
		if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func clean(p string) string { return filepath.Clean(p) }

// inside says whether p lies inside the folder dir
func inside(p, dir string) bool {
	rel, err := filepath.Rel(clean(dir), clean(p))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkTx checks a transaction as rhun does before it changes anything: the grammar, the static
// rules, the documents, the files on disk in the order of the phases, and the ranges
func (h *host) checkTx(b []byte) (tx, string, int, string) {
	t, code, entry, why := parseTx(b, h.txCaps())
	if why != "" {
		return tx{}, code, entry, why
	}
	bad := func(code string, entry int, format string, a ...any) (tx, string, int, string) {
		return tx{}, code, entry, fmt.Sprintf(format, a...)
	}
	// paths: a text group's an absolute path or #DOC, a file operation's absolute
	for i := range t.groups {
		g := &t.groups[i]
		if !pathOK(g.raw) {
			return bad("nofile", g.entry, "entry %d: %s", g.entry, badPath(g.raw))
		}
		if g.raw[0] == '#' {
			if d := h.openDoc(g.raw); d != nil {
				g.path = d.path
			}
		} else {
			g.path = clean(string(g.raw))
		}
	}
	for _, o := range t.ops {
		for _, p := range []string{o.path, o.to} {
			if p != "" && (!pathOK([]byte(p)) || p[0] == '#') {
				return bad("nofile", o.entry, "entry %d: %q is not an absolute path", o.entry, p)
			}
		}
	}
	// the static rules
	opPaths := map[string]int{}
	for _, o := range t.ops {
		for _, p := range []string{o.path, o.to} {
			if p == "" {
				continue
			}
			if e, dup := opPaths[clean(p)]; dup {
				return bad("refused", o.entry, "entry %d: %s is also the path of entry %d", o.entry, p, e)
			}
			opPaths[clean(p)] = o.entry
		}
	}
	for _, g := range t.groups {
		for _, o := range t.ops {
			if g.path != "" && (o.kind == "delete" && clean(o.path) == g.path || o.kind == "rename" && clean(o.to) == g.path) {
				return bad("refused", g.entry, "entry %d: a text group for %s, which entry %d %s", g.entry, g.path, o.entry,
					map[string]string{"delete": "deletes", "rename": "renames another file onto"}[o.kind])
			}
		}
	}
	for _, o := range t.ops {
		for _, q := range t.ops {
			if q.entry == o.entry || q.kind == "create" {
				continue
			}
			for _, p := range []string{o.path, o.to} {
				if p != "" && inside(p, q.path) {
					return bad("refused", o.entry, "entry %d: %s is inside %s, which entry %d %ss", o.entry, p, q.path, q.entry, q.kind)
				}
			}
		}
		for _, p := range []string{o.path, o.to} {
			if p == "" {
				continue
			}
			if (o.kind != "create") && p == o.path && (clean(p) == clean(h.project) || inside(h.project, p)) {
				return bad("refused", o.entry, "entry %d: a %s of the project folder or a folder above it", o.entry, o.kind)
			}
			if rel, err := filepath.Rel(h.project, clean(p)); err == nil && slices.Contains(strings.Split(rel, string(filepath.Separator)), ".git") {
				return bad("refused", o.entry, "entry %d: %s is inside .git", o.entry, p)
			}
		}
	}
	disk := len(t.ops) > 0
	for _, g := range t.groups {
		if rel, err := filepath.Rel(h.project, g.path); g.path != "" && err == nil && slices.Contains(strings.Split(rel, string(filepath.Separator)), ".git") {
			return bad("refused", g.entry, "entry %d: %s is inside .git", g.entry, g.path)
		}
		if h.openDoc(g.raw) == nil {
			disk = true
		}
	}
	// disk changes lie in the project or the extension's state folder
	if disk {
		for _, o := range t.ops {
			for _, p := range []string{o.path, o.to} {
				if p != "" && !h.inProject(clean(p)) {
					return bad("needs", o.entry, "entry %d: %s is outside the project (needs project=write)", o.entry, p)
				}
			}
		}
		for _, g := range t.groups {
			// a file that is not open, or an open document without unsaved changes (written too)
			if d := h.openDoc(g.raw); g.path != "" && (d == nil || !d.dirty) && !h.inProject(g.path) {
				return bad("needs", g.entry, "entry %d: %s is outside the project (needs project=write)", g.entry, g.path)
			}
		}
	}
	// the documents
	created := map[string]bool{}
	for _, o := range t.ops {
		if o.kind == "create" {
			created[clean(o.path)] = true
		}
	}
	for i := range t.groups {
		g := &t.groups[i]
		d := h.openDoc(g.raw)
		g.create = created[g.path]
		switch {
		case d == nil && g.raw[0] == '#':
			return bad("nodoc", g.entry, "entry %d: %s names a document rhun never opened", g.entry, g.raw)
		case g.create && (g.version >= 0 || g.hashed && g.hash != rhunext.Fnv1a64(nil)):
			return bad("stale", g.entry, "entry %d: %s is made by a create entry: its PRE is - (its text is empty)", g.entry, g.path)
		case d == nil && g.version >= 0:
			return bad("nodoc", g.entry, "entry %d: VERSION %d for %s, which is not open: - or fnv1a64:HEX is for a file that is not open", g.entry, g.version, g.path)
		case d == nil:
			continue
		case g.version < 0 && d.dirty:
			return bad("stale", g.entry, "entry %d: PRE %s for %s, which is open with unsaved changes (at version %d)", g.entry,
				map[bool]string{true: "fnv1a64", false: "-"}[g.hashed], g.path, d.version)
		case g.version >= 0 && g.version != d.version:
			return bad("stale", g.entry, "entry %d: VERSION %d for %s, which is at version %d", g.entry, g.version, g.path, d.version)
		}
		g.t = place{d: d, path: string(g.raw), text: d.text, crlf: d.crlf}
	}
	for _, o := range t.ops {
		for _, id := range h.docIDs() {
			d := h.docs[id]
			if !d.dirty {
				continue
			}
			gone := o.kind == "delete" && (clean(o.path) == d.path || inside(d.path, o.path)) ||
				(o.kind == "create" || o.kind == "rename") && o.flags["overwrite"] && clean(map[string]string{"create": o.path, "rename": o.to}[o.kind]) == d.path
			if gone {
				return bad("refused", o.entry, "entry %d: %s an open document with unsaved changes (%s)", o.entry,
					map[string]string{"delete": "deletes", "create": "overwrites", "rename": "overwrites"}[o.kind], d.path)
			}
		}
	}
	// the files on disk, as the phases leave them: creates, text groups, renames, deletes
	exists := func(p string) (bool, bool) {
		st, err := os.Stat(p)
		return err == nil, err == nil && st.IsDir()
	}
	state := map[string]int{} // 1 a file, 2 a folder, -1 gone, as the entries before leave it
	at := func(p string) (bool, bool) {
		switch state[clean(p)] {
		case 1:
			return true, false
		case 2:
			return true, true
		case -1:
			return false, false
		}
		return exists(p)
	}
	for _, o := range t.ops {
		if o.kind != "create" {
			continue
		}
		folder := strings.HasSuffix(o.path, "/")
		there, dir := at(o.path)
		switch {
		case there && (o.flags["overwrite"] && !dir && !folder):
		case there && o.flags["ignore-existing"]:
			continue
		case there:
			return bad("refused", o.entry, "entry %d: %s exists, and the create has neither overwrite nor ignore-existing", o.entry, o.path)
		}
		state[clean(o.path)] = map[bool]int{true: 2, false: 1}[folder]
	}
	for i := range t.groups {
		g := &t.groups[i]
		if g.t.d != nil {
			continue
		}
		if g.create {
			g.t = place{path: g.path}
			continue
		}
		text, why, _, crlf := loadFileContext(g.path)
		if why != "" {
			return bad("nofile", g.entry, "entry %d: %s is not open, and %s", g.entry, g.path, why)
		}
		if g.hashed {
			raw, _ := os.ReadFile(g.path)
			if rhunext.Fnv1a64(raw) != g.hash {
				return bad("stale", g.entry, "entry %d: %s does not hash to %016x on disk (it changed since the extension read it)", g.entry, g.path, g.hash)
			}
		}
		g.t = place{path: g.path, text: text, crlf: crlf}
	}
	for _, o := range t.ops {
		switch o.kind {
		case "rename":
			if there, _ := at(o.path); !there {
				return bad("nofile", o.entry, "entry %d: %s, the rename's source, does not exist", o.entry, o.path)
			}
			there, dir := at(o.to)
			switch {
			case there && o.flags["overwrite"] && !dir:
			case there && o.flags["ignore-existing"]:
				continue
			case there:
				return bad("refused", o.entry, "entry %d: %s exists, and the rename has neither overwrite nor ignore-existing (a folder is never replaced)", o.entry, o.to)
			}
			_, srcDir := at(o.path)
			state[clean(o.path)] = -1
			state[clean(o.to)] = map[bool]int{true: 2, false: 1}[srcDir]
		case "delete":
			there, dir := at(o.path)
			if !there {
				if o.flags["ignore-missing"] {
					continue
				}
				return bad("nofile", o.entry, "entry %d: %s does not exist, and the delete has no ignore-missing", o.entry, o.path)
			}
			if dir && !o.flags["recursive"] {
				if names, _ := os.ReadDir(o.path); len(names) > 0 {
					return bad("refused", o.entry, "entry %d: %s is a folder with files in it, and the delete is not recursive", o.entry, o.path)
				}
			}
			state[clean(o.path)] = -1
		}
	}
	// the ranges, sorted and apart, as 12g's
	for i := range t.groups {
		g := &t.groups[i]
		for _, p := range t.groups[:i] {
			if g.path != "" && p.path == g.path || g.path == "" && string(p.raw) == string(g.raw) {
				return bad("syntax", g.entry, "entry %d: %s has a group already (one group per file)", g.entry, g.raw)
			}
		}
		d := g.t.at()
		for e := range g.edits {
			x := &g.edits[e]
			a, z, why := span(d, x.at)
			if why != "" {
				return bad("badpos", g.entry, "entry %d, edit %d: %s", g.entry, e+1, why)
			}
			x.start, x.end = a, z
		}
		slices.SortStableFunc(g.edits, func(x, y wsEdit) int { return cmp.Or(x.start-y.start, x.end-y.end) })
		for e := 1; e < len(g.edits); e++ {
			if p, x := g.edits[e-1], g.edits[e]; p.end > x.start {
				return bad("badpos", g.entry, "entry %d: two edits overlap (bytes %d to %d and %d to %d of %s)", g.entry,
					p.start, p.end, x.start, x.end, g.raw)
			}
		}
	}
	return t, "", 0, ""
}

// makeTx makes a checked transaction as rhun does
func (h *host) makeTx(t tx) {
	make1 := func(text []byte, edits []wsEdit, d *doc) ([]byte, []edit) {
		var recs []edit
		for i := len(edits) - 1; i >= 0; i-- {
			e := edits[i]
			text = append(append(append([]byte{}, text[:e.start]...), e.text...), text[e.end:]...)
			for _, r := range records(e.start, e.end-e.start, e.text) {
				if d != nil {
					d.cur, d.anchor = moved(d.cur, r), moved(d.anchor, r)
				}
				recs = append(recs, r)
			}
		}
		return text, recs
	}
	write := func(path string, text []byte, crlf bool) {
		if crlf {
			text = bytes.ReplaceAll(text, []byte("\n"), []byte("\r\n"))
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		os.WriteFile(path, text, mode)
	}
	disk := len(t.ops) > 0
	for _, g := range t.groups {
		if g.t.d == nil {
			disk = true
		}
	}
	// creates
	for _, o := range t.ops {
		if o.kind != "create" {
			continue
		}
		if strings.HasSuffix(o.path, "/") {
			os.MkdirAll(o.path, 0o755)
			continue
		}
		if _, err := os.Stat(o.path); err == nil && !o.flags["overwrite"] {
			continue
		}
		os.MkdirAll(filepath.Dir(o.path), 0o755)
		os.WriteFile(o.path, nil, 0o644)
	}
	// text groups: open documents in their buffers (written when the edit touches disk and they
	// had no unsaved changes), files that are not open on disk
	for _, g := range t.groups {
		if d := g.t.d; d != nil {
			wasClean := !d.dirty
			text, recs := make1(d.text, g.edits, d)
			d.text = text
			d.version += int64(len(recs))
			if len(recs) > 0 {
				d.dirty = true
				h.afterEdit(d, recs...)
			}
			if disk && wasClean && d.path != "" {
				write(d.path, d.text, d.crlf)
				d.dirty = false
			}
			continue
		}
		text, _ := make1(g.t.text, g.edits, nil)
		write(g.path, text, g.t.crlf)
	}
	// renames: documents follow under a new id; deletes: documents close
	for _, o := range t.ops {
		switch o.kind {
		case "rename":
			if _, err := os.Stat(o.to); err == nil && !o.flags["overwrite"] {
				continue
			}
			os.MkdirAll(filepath.Dir(o.to), 0o755)
			if os.Rename(o.path, o.to) != nil {
				continue
			}
			for _, id := range h.docIDs() {
				d := h.docs[id]
				var np string
				if d.path == clean(o.path) {
					np = clean(o.to)
				} else if inside(d.path, o.path) {
					rel, _ := filepath.Rel(clean(o.path), d.path)
					np = filepath.Join(o.to, rel)
				} else {
					continue
				}
				delete(h.docs, id)
				h.write(fmt.Sprintf("0 closed %d\n", id))
				h.nextDoc++
				d.id, d.path = h.nextDoc, np
				h.docs[d.id] = d
				if h.active == id {
					h.active = d.id
				}
				h.write(fmt.Sprintf("0 opened %d %d %s %s\n", d.id, d.version, dash(d.lang), d.path))
			}
		case "delete":
			var err error
			if o.flags["recursive"] {
				err = os.RemoveAll(o.path)
			} else {
				err = os.Remove(o.path)
			}
			if err != nil {
				continue
			}
			for _, id := range h.docIDs() {
				if d := h.docs[id]; d.path == clean(o.path) || inside(d.path, o.path) {
					delete(h.docs, id)
					h.write(fmt.Sprintf("0 closed %d\n", id))
				}
			}
		}
	}
}

// takeEdit checks a workspace edit the extension sent as rhun reads it from this extension, and
// makes it: a transaction with workspace-edits agreed (format and save-actions answers, only not
// nil, excepted), else 12g's. It returns the number of entries, or the code, the entry at fault
// and why rhun refuses it.
func (h *host) takeEdit(b []byte, only *doc) (int, string, int, string) {
	if only == nil && h.features["workspace-edits"] {
		t, code, entry, why := h.checkTx(b)
		if why != "" {
			return 0, code, entry, why
		}
		for _, w := range t.warns {
			h.warn("workspace-edits", w)
		}
		disk := len(t.ops) > 0 || slices.ContainsFunc(t.groups, func(g txGroup) bool { return g.t.d == nil })
		if disk && h.m.needs["project"] != "write" && !h.warnedConsent {
			h.warnedConsent = true
			h.warn("workspace-edits", "the edit changes files on disk and the manifest has no project = write: rhun asks the user (the checker answers Apply)")
		}
		h.makeTx(t)
		return len(t.groups) + len(t.ops), "", 0, ""
	}
	gs, code, why := h.checkWorkspaceEdit(b, only)
	if why != "" {
		return 0, code, 0, why
	}
	h.apply(gs)
	return len(gs), "", 0, ""
}

// editGrammarFor is what rhun refuses in an edit of this extension whatever the documents say
func (h *host) editGrammarFor(b []byte) (string, string) {
	if !h.features["workspace-edits"] {
		_, code, why := editGrammar(b)
		return code, why
	}
	t, code, _, why := parseTx(b, h.txCaps())
	if why != "" {
		return code, why
	}
	for _, g := range t.groups {
		if !pathOK(g.raw) {
			return "nofile", fmt.Sprintf("entry %d: %s", g.entry, badPath(g.raw))
		}
	}
	return "", ""
}
