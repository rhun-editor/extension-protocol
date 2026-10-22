package rhunext

// Protocol 1.1: workspace edits as transactions (feature workspace-edits) with file operations
// (feature file-operations): the fnv1a64 precondition of a file that is not open, create, rename
// and delete entries, the label and confirm records, the entry an apply error names, and
// NormalizeOrdered, which maps LSP's ordered documentChanges onto rhun's phases.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// The features of protocol 1.1's editor model.
const (
	FeatureWorkspaceEdits = "workspace-edits"
	FeatureFileOperations = "file-operations"
	FeatureSelections     = "selections"
	FeatureReveal         = "reveal"
	FeatureDiff           = "diff"
	FeatureLinkedEditing  = "linked-editing"
)

// MaxLabel is the longest label rhun shows; Encode cuts a longer one at a character.
const MaxLabel = 200

// Fnv1a64 is the 64-bit FNV-1a hash of data: offset basis 0xcbf29ce484222325, prime
// 0x100000001b3. A text group's fnv1a64:HEX precondition is this hash of the file's bytes on disk
// (CR included).
func Fnv1a64(data []byte) uint64 {
	h := uint64(0xcbf29ce484222325)
	for _, b := range data {
		h ^= uint64(b)
		h *= 0x100000001b3
	}
	return h
}

// CreateOptions say how a create entry treats a path that exists: Overwrite replaces an existing
// file with an empty one, IgnoreExisting skips the entry; Overwrite wins.
type CreateOptions struct{ Overwrite, IgnoreExisting bool }

// RenameOptions say how a rename entry treats a target that exists: Overwrite replaces a file
// (never a folder), IgnoreExisting skips the entry; Overwrite wins.
type RenameOptions struct{ Overwrite, IgnoreExisting bool }

// DeleteOptions: Recursive deletes a folder and everything in it (without it only a file or an
// empty folder); IgnoreMissing skips the entry when the path does not exist.
type DeleteOptions struct{ Recursive, IgnoreMissing bool }

// FileOpKind is what a file operation does.
type FileOpKind string

const (
	OpCreate FileOpKind = "create"
	OpRename FileOpKind = "rename"
	OpDelete FileOpKind = "delete"
)

// FileOp is a file operation of a workspace edit (feature file-operations). Path is the file
// created or deleted, or a rename's source, To a rename's target; paths are absolute, and a
// create path ending in "/" makes a folder. Only the options of its kind count.
type FileOp struct {
	Kind   FileOpKind
	Path   string
	To     string
	Create CreateOptions
	Rename RenameOptions
	Delete DeleteOptions
}

func (o FileOp) phase() int {
	switch o.Kind {
	case OpCreate:
		return 0
	case OpRename:
		return 2
	}
	return 3
}

func (o FileOp) paths() []string {
	if o.Kind == OpRename {
		return []string{o.Path, o.To}
	}
	return []string{o.Path}
}

func opFlags(pairs ...any) string {
	var w []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i].(bool) {
			w = append(w, pairs[i+1].(string))
		}
	}
	if len(w) == 0 {
		return "-"
	}
	return strings.Join(w, ",")
}

func (o FileOp) encode(b *bytes.Buffer) {
	switch o.Kind {
	case OpCreate:
		fmt.Fprintf(b, "create %d %s\n%s", len(o.Path), opFlags(o.Create.Overwrite, "overwrite", o.Create.IgnoreExisting, "ignore-existing"), o.Path)
	case OpRename:
		fmt.Fprintf(b, "rename %d %d %s\n%s%s", len(o.Path), len(o.To), opFlags(o.Rename.Overwrite, "overwrite", o.Rename.IgnoreExisting, "ignore-existing"), o.Path, o.To)
	default:
		fmt.Fprintf(b, "delete %d %s\n%s", len(o.Path), opFlags(o.Delete.Recursive, "recursive", o.Delete.IgnoreMissing, "ignore-missing"), o.Path)
	}
}

// AddFileHashed adds edits for a file that is not open, made against data, its bytes as read
// from disk (CR included): with workspace-edits the file must still hash to them, else the edit
// is stale. Positions refer to rhun's text of the file (CR before LF dropped).
func (w *WorkspaceEdit) AddFileHashed(path string, data []byte, edits ...TextEdit) {
	w.Files = append(w.Files, FileEdit{Target: Target{Path: path}, Version: OnDisk, Edits: edits, Hashed: true, Hash: Fnv1a64(data)})
}

// Create adds a create entry: an empty file, or a folder for a path ending in "/".
func (w *WorkspaceEdit) Create(path string, o CreateOptions) {
	w.Ops = append(w.Ops, FileOp{Kind: OpCreate, Path: path, Create: o})
}

// Rename adds a rename entry; text groups name the file by from.
func (w *WorkspaceEdit) Rename(from, to string, o RenameOptions) {
	w.Ops = append(w.Ops, FileOp{Kind: OpRename, Path: from, To: to, Rename: o})
}

// Delete adds a delete entry.
func (w *WorkspaceEdit) Delete(path string, o DeleteOptions) {
	w.Ops = append(w.Ops, FileOp{Kind: OpDelete, Path: path, Delete: o})
}

// Entry is what an entry of a workspace edit's body is: Files[Index], or with Op, Ops[Index].
type Entry struct {
	Op    bool
	Index int
}

// order is the entries as the body lists them: rhun's phases.
func (w WorkspaceEdit) order() []Entry {
	out := make([]Entry, 0, len(w.Files)+len(w.Ops))
	for phase := 0; phase < 4; phase++ {
		if phase == 1 {
			for i := range w.Files {
				out = append(out, Entry{Index: i})
			}
			continue
		}
		for i, o := range w.Ops {
			if o.phase() == phase {
				out = append(out, Entry{Op: true, Index: i})
			}
		}
	}
	return out
}

// Entry says what the body's 1-based entry n (an apply error's ENTRY) is; false for 0 and past
// the last.
func (w WorkspaceEdit) Entry(n int) (Entry, bool) {
	o := w.order()
	if n < 1 || n > len(o) {
		return Entry{}, false
	}
	return o[n-1], true
}

// editCaps are the parts of the workspace edit grammar a body may use: ws and ops what rhun
// agreed (workspace-edits, file-operations). fixed names a body that keeps 12g's grammar whatever
// was agreed (a format or save-actions answer, save-apply, propose).
type editCaps struct {
	ws, ops bool
	fixed   string
}

// encodeErr is a refusal of encodeWith as an error from Encode, Apply or SaveApply: its text
// alone for a limit (as Encode always said), the *Error for a feature rhun did not agree.
func encodeErr(bad *Error) error {
	if bad.Code == "unknown" {
		return bad
	}
	return errors.New(bad.Text)
}

func (e *Ext) editCaps() editCaps {
	return editCaps{ws: e.Has(FeatureWorkspaceEdits), ops: e.Has(FeatureFileOperations)}
}

// txPart names the first part of the edit that only the transaction grammar of workspace-edits
// has, or is "": file operations, a file's fnv1a64 precondition, the label, confirm.
func (w WorkspaceEdit) txPart() string {
	switch {
	case len(w.Ops) > 0:
		return "file operations"
	case slices.ContainsFunc(w.Files, func(f FileEdit) bool { return f.Hashed && f.Version < 0 }):
		return "fnv1a64 precondition"
	case w.Label != "":
		return "label"
	case w.Confirm:
		return "confirm record"
	}
	return ""
}

// encodeWith is the body for a rhun that agreed caps: 12g's grammar without workspace-edits. An
// edit that needs more than the grammar caps allow (file operations without both features; a
// hash, a label or confirm without workspace-edits or in a fixed 12g body) is refused as unknown,
// as is what does not fit as toobig: nothing rhun would refuse is sent. A refusal is the code to
// answer with (toobig, unknown) and why.
func (w WorkspaceEdit) encodeWith(caps editCaps) ([]byte, *Error) {
	if part := w.txPart(); part != "" {
		unknown := func(format string, a ...any) ([]byte, *Error) {
			return nil, &Error{Code: "unknown", Text: fmt.Sprintf(format, a...)}
		}
		switch {
		case caps.fixed != "":
			return unknown("%s keeps 12g's grammar, which has no %s", caps.fixed, part)
		case len(w.Ops) > 0 && !(caps.ws && caps.ops):
			return unknown("file operations need the workspace-edits and file-operations features")
		case !caps.ws:
			return unknown("the edit's %s needs the workspace-edits feature, which was not agreed", part)
		}
	}
	toobig := func(format string, a ...any) ([]byte, *Error) {
		return nil, &Error{Code: "toobig", Text: fmt.Sprintf(format, a...)}
	}
	entries := len(w.Files) + len(w.Ops)
	if entries > MaxEditFiles && len(w.Ops) == 0 {
		return toobig("%d files: at most %d", entries, MaxEditFiles)
	}
	if entries > MaxEditFiles {
		return toobig("%d entries: at most %d", entries, MaxEditFiles)
	}
	edits := 0
	for _, f := range w.Files {
		edits += len(f.Edits)
	}
	if edits > MaxEdits {
		return toobig("%d edits: at most %d", edits, MaxEdits)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%d\n", entries)
	for _, en := range w.order() {
		if en.Op {
			o := w.Ops[en.Index]
			for _, p := range o.paths() {
				if p == "" || len(p) > MaxPath {
					return toobig("a path of %d bytes: 1 to %d", len(p), MaxPath)
				}
			}
			o.encode(&b)
		} else {
			f := w.Files[en.Index]
			p, pre := f.Target.String(), "-"
			if len(p) > MaxPath {
				return toobig("a path over %d bytes", MaxPath)
			}
			switch {
			case f.Version >= 0:
				pre = itoa(f.Version)
			case f.Hashed: // refused above without workspace-edits
				pre = fmt.Sprintf("fnv1a64:%016x", f.Hash)
			}
			fmt.Fprintf(&b, "%d %s %d\n", len(p), pre, len(f.Edits))
			b.WriteString(p)
			for _, e := range f.Edits {
				head := fmt.Sprintf("%s %d\n", rangeFields(e.Range), len(e.Text))
				if b.Len()+len(head)+len(e.Text) > MaxEditBody {
					return toobig("over %d bytes", MaxEditBody)
				}
				b.WriteString(head)
				b.WriteString(e.Text)
			}
		}
		if b.Len() > MaxEditBody {
			return toobig("over %d bytes", MaxEditBody)
		}
	}
	// both refused above without workspace-edits
	if w.Label != "" {
		l := cut(oneLine(w.Label), MaxLabel)
		fmt.Fprintf(&b, "label %d\n%s", len(l), l)
	}
	if w.Confirm {
		b.WriteString("confirm 0\n")
	}
	if b.Len() > MaxEditBody {
		return toobig("over %d bytes", MaxEditBody)
	}
	return b.Bytes(), nil
}

// ApplyError is a refused workspace edit with workspace-edits agreed, "error ID CODE ENTRY TEXT":
// Entry is the 1-based number of the body's entry at fault (WorkspaceEdit.Entry), or 0. Codes:
// syntax, needs, nofile, nodoc, stale, readonly, badpos, toobig, refused, cancelled, busy, and io
// (the disk refused a write and rhun put everything back: the edit may be sent again). It unwraps
// to the *Error, so Cancelled and Busy see it.
type ApplyError struct {
	Code  string
	Entry int
	Text  string
}

func (e *ApplyError) Error() string {
	if e.Entry > 0 {
		return fmt.Sprintf("rhun: %s: entry %d: %s", e.Code, e.Entry, e.Text)
	}
	return "rhun: " + e.Code + ": " + e.Text
}

func (e *ApplyError) Unwrap() error { return &Error{Code: e.Code, Text: e.Text} }

// applyError splits an error reply's ENTRY TEXT.
func applyError(re *Error) *ApplyError {
	n, rest, _ := strings.Cut(re.Text, " ")
	if v, ok := bodyLen(n, 1<<31-1); ok {
		return &ApplyError{Code: re.Code, Entry: v, Text: rest}
	}
	return &ApplyError{Code: re.Code, Text: re.Text}
}

// ---- ordered changes

// ChangeKind is what one step of an ordered workspace edit does.
type ChangeKind string

const (
	ChangeEdit   ChangeKind = "edit"
	ChangeCreate ChangeKind = "create"
	ChangeRename ChangeKind = "rename"
	ChangeDelete ChangeKind = "delete"
)

// Change is one step of an ordered workspace edit (LSP's documentChanges). A ChangeEdit's Edits
// are in rhun's positions against the file's text at this point of the sequence (Locate), all
// against the same text, not overlapping; Version is the open document's version, or OnDisk
// with Hashed and Hash for the file on disk (Fnv1a64 of its bytes). Only the first step of a file
// sets the group's precondition; a file an earlier step creates takes "-". Path is the file
// edited, created or deleted, or a rename's source; To a rename's target.
type Change struct {
	Kind    ChangeKind
	Path    string
	To      string
	Version int64
	Hashed  bool
	Hash    uint64
	Edits   []TextEdit
	Create  CreateOptions
	Rename  RenameOptions
	Delete  DeleteOptions
}

// OrderError is why an ordered edit cannot be made in rhun's phases: Change is the 0-based index
// of the step at fault.
type OrderError struct {
	Change int
	Text   string
}

func (e *OrderError) Error() string { return fmt.Sprintf("change %d: %s", e.Change, e.Text) }

// Located is where the text of a file an edit names comes from at some point of the sequence:
// Path is its name before the sequence (read the open document or the file on disk), Created
// that an earlier step created it (its text starts empty).
type Located struct {
	Path    string
	Created bool
}

func isSep(c byte) bool { return c == '/' || os.PathSeparator == '\\' && c == '\\' }

func trimSep(p string) string {
	for len(p) > 1 && isSep(p[len(p)-1]) {
		p = p[:len(p)-1]
	}
	return p
}

func samePath(a, b string) bool { return trimSep(a) == trimSep(b) }

// under is the rest of path after the folder dir (starting with its separator), when it is in it.
func under(path, dir string) (string, bool) {
	d := trimSep(dir)
	if len(path) > len(d) && strings.HasPrefix(path, d) && isSep(path[len(d)]) {
		return path[len(d):], true
	}
	return "", false
}

// Locate says where the text of path comes from after the steps before: they are walked back,
// rewriting a rename's target to its source, up to a create of it; an earlier rename away or
// delete of it (or of a folder holding it) is an error.
func Locate(before []Change, path string) (Located, error) {
	p := path
	for i := len(before) - 1; i >= 0; i-- {
		c := before[i]
		switch c.Kind {
		case ChangeRename:
			if samePath(p, c.To) {
				p = c.Path
			} else if rest, ok := under(p, c.To); ok {
				p = trimSep(c.Path) + rest
			} else if _, in := under(p, c.Path); in || samePath(p, c.Path) {
				return Located{}, fmt.Errorf("an edit to %s after %s was renamed to %s", path, c.Path, c.To)
			}
		case ChangeDelete:
			if _, in := under(p, c.Path); in || samePath(p, c.Path) {
				return Located{}, fmt.Errorf("an edit to %s after %s was deleted", path, c.Path)
			}
		case ChangeCreate:
			if samePath(p, c.Path) {
				return Located{Path: p, Created: true}, nil
			}
		}
	}
	return Located{Path: p}, nil
}

// NormalizeOrdered is the workspace edit for an ordered list of changes: a text edit that
// follows a rename names the file by its name before it, repeated edits of one file become one
// group (the later ones' positions mapped back onto the first one's text), edits of a file a
// later delete removes are left out, and what rhun's phases cannot express (an edit after its
// file was renamed away or deleted, two file operations on one path, an operation inside a folder
// another renames or deletes, an edit of a later rename's target, a create after an edit of its
// path, edits of text an earlier edit of the file put in) is an *OrderError.
func NormalizeOrdered(changes []Change) (WorkspaceEdit, error) {
	w, _, err := NormalizeOrderedOrigins(changes)
	return w, err
}

// NormalizeOrderedOrigins is NormalizeOrdered with, for each entry of the body (as Entry numbers
// them), the index of the change it came from (a merged group's first): an apply error's ENTRY n
// came from origins[n-1].
func NormalizeOrderedOrigins(changes []Change) (WorkspaceEdit, []int, error) {
	type group struct {
		file  FileEdit
		first int
	}
	type op struct {
		op     FileOp
		change int
	}
	var groups []group
	byPath := map[string]int{}
	var ops []op
	fail := func(i int, format string, a ...any) (WorkspaceEdit, []int, error) {
		return WorkspaceEdit{}, nil, &OrderError{Change: i, Text: fmt.Sprintf(format, a...)}
	}
	for i, c := range changes {
		var mine []string
		switch c.Kind {
		case ChangeCreate, ChangeDelete:
			mine = []string{c.Path}
		case ChangeRename:
			mine = []string{c.Path, c.To}
		case ChangeEdit:
		default:
			return fail(i, "an unknown kind of change %q", c.Kind)
		}
		holds := c.Kind == ChangeRename || c.Kind == ChangeDelete
		for _, p := range mine {
			if p == "" {
				return fail(i, "a file operation without a path")
			}
			for _, o := range ops {
				theirs := o.op.paths()
				var folders []string
				if o.op.Kind != OpCreate {
					folders = theirs
				}
				for _, q := range theirs {
					if samePath(p, q) {
						return fail(i, "%s is the path of two file operations", p)
					}
					if _, in := under(q, p); holds && in {
						return fail(i, "%s and a folder another file operation renames or deletes", p)
					}
				}
				for _, q := range folders {
					if _, in := under(p, q); in {
						return fail(i, "%s and a folder another file operation renames or deletes", p)
					}
				}
			}
		}
		switch c.Kind {
		case ChangeEdit:
			at, err := Locate(changes[:i], c.Path)
			if err != nil {
				return fail(i, "%s", err.Error())
			}
			key := trimSep(at.Path)
			if g, ok := byPath[key]; ok {
				merged, err := composeEdits(groups[g].file.Edits, c.Edits)
				if err != nil {
					return fail(i, "%s: %s", at.Path, err.Error())
				}
				groups[g].file.Edits = merged
				continue
			}
			f := FileEdit{Target: Target{Path: at.Path}, Version: c.Version, Edits: slices.Clone(c.Edits), Hashed: c.Hashed, Hash: c.Hash}
			if at.Created {
				f.Version, f.Hashed, f.Hash = OnDisk, false, 0
			}
			byPath[key] = len(groups)
			groups = append(groups, group{file: f, first: i})
		case ChangeCreate:
			if _, ok := byPath[trimSep(c.Path)]; ok {
				return fail(i, "%s is created after an edit of it", c.Path)
			}
			ops = append(ops, op{FileOp{Kind: OpCreate, Path: c.Path, Create: c.Create}, i})
		case ChangeRename:
			if _, ok := byPath[trimSep(c.To)]; ok {
				return fail(i, "%s is renamed onto %s after an edit of %s", c.Path, c.To, c.To)
			}
			ops = append(ops, op{FileOp{Kind: OpRename, Path: c.Path, To: c.To, Rename: c.Rename}, i})
		case ChangeDelete:
			ops = append(ops, op{FileOp{Kind: OpDelete, Path: c.Path, Delete: c.Delete}, i})
		}
	}
	// edits of a file that is deleted afterwards change nothing anyone sees: left out (rhun
	// refuses a text group for a deleted path)
	var w WorkspaceEdit
	var firsts []int
	for _, g := range groups {
		gone := slices.ContainsFunc(ops, func(o op) bool { return o.op.Kind == OpDelete && samePath(o.op.Path, g.file.Target.Path) })
		if !gone {
			w.Files = append(w.Files, g.file)
			firsts = append(firsts, g.first)
		}
	}
	for _, o := range ops {
		w.Ops = append(w.Ops, o.op)
	}
	origins := make([]int, 0, len(w.Files)+len(w.Ops))
	for _, e := range w.order() {
		if e.Op {
			origins = append(origins, ops[e.Index].change)
		} else {
			origins = append(origins, firsts[e.Index])
		}
	}
	return w, origins, nil
}

func posLess(a, b Pos) bool { return a.Line < b.Line || a.Line == b.Line && a.Col < b.Col }

func posCmp(a, b Pos) int {
	switch {
	case posLess(a, b):
		return -1
	case posLess(b, a):
		return 1
	}
	return 0
}

// advance is where text puts the position after it when inserted at p.
func advance(p Pos, text string) Pos {
	i := strings.LastIndexByte(text, '\n')
	if i < 0 {
		return Pos{p.Line, p.Col + len(text)}
	}
	return Pos{p.Line + strings.Count(text, "\n"), len(text) - i}
}

// forward is a position at or after oe (the end of the last edit before it, in the text before)
// in the text after, where that end is pe; back is its inverse.
func forward(p, oe, pe Pos) Pos {
	if p.Line == oe.Line {
		return Pos{pe.Line, pe.Col + p.Col - oe.Col}
	}
	return Pos{p.Line - oe.Line + pe.Line, p.Col}
}

func back(p, oe, pe Pos) Pos {
	if p.Line == pe.Line {
		return Pos{oe.Line, oe.Col + p.Col - pe.Col}
	}
	return Pos{p.Line - pe.Line + oe.Line, p.Col}
}

// composeEdits is one group of edits made against a text, then next against the text that left:
// the edits of both against the first text, in an order rhun's stable sort keeps. An edit of next
// that touches text base put in, or spans text it removed, cannot be one of them.
func composeEdits(base, next []TextEdit) ([]TextEdit, error) {
	for _, e := range append(slices.Clone(base), next...) {
		if posLess(e.Range.End, e.Range.Start) || e.Range.Start.Line < 1 || e.Range.Start.Col < 1 {
			return nil, fmt.Errorf("an edit's range ends before it starts")
		}
	}
	b := slices.Clone(base)
	slices.SortStableFunc(b, func(x, y TextEdit) int {
		if c := posCmp(x.Range.Start, y.Range.Start); c != 0 {
			return c
		}
		return posCmp(x.Range.End, y.Range.End)
	})
	for k := 1; k < len(b); k++ {
		if posLess(b[k].Range.Start, b[k-1].Range.End) {
			return nil, fmt.Errorf("two edits overlap")
		}
	}
	type region struct{ ps, pe Pos }
	post := make([]region, len(b))
	oe, pe := Pos{1, 1}, Pos{1, 1}
	for k, e := range b {
		ps := forward(e.Range.Start, oe, pe)
		post[k] = region{ps, advance(ps, e.Text)}
		oe, pe = e.Range.End, post[k].pe
	}
	// a start where base removed text (and put none in) goes after what it removed, an end
	// before it, so the edit does not overlap base's
	inv := func(p Pos, start bool) (Pos, error) {
		oe, pe := Pos{1, 1}, Pos{1, 1}
		for k, e := range b {
			r := post[k]
			if posLess(p, r.ps) {
				return back(p, oe, pe), nil
			}
			if p == r.ps && !(start && r.ps == r.pe) {
				return e.Range.Start, nil
			}
			if posLess(p, r.pe) {
				return Pos{}, fmt.Errorf("an edit at %d:%d lands in text an earlier edit of the file put in", p.Line, p.Col)
			}
			oe, pe = e.Range.End, r.pe
		}
		return back(p, oe, pe), nil
	}
	type item struct {
		start, end, rank Pos
		grp, idx         int
		edit             TextEdit
	}
	var all []item
	for k, e := range b {
		all = append(all, item{e.Range.Start, e.Range.End, post[k].ps, 1, k, e})
	}
	for k, e := range next {
		a, z := e.Range.Start, e.Range.End
		for _, r := range post {
			if posLess(a, r.pe) && posLess(r.ps, z) || r.ps == r.pe && posLess(a, r.ps) && posLess(r.ps, z) {
				return nil, fmt.Errorf("an edit from %d:%d to %d:%d overlaps an earlier edit of the file", a.Line, a.Col, z.Line, z.Col)
			}
		}
		start, err := inv(a, true)
		if err != nil {
			return nil, err
		}
		end := start
		if a != z {
			if end, err = inv(z, false); err != nil {
				return nil, err
			}
		}
		all = append(all, item{start, end, a, 0, k, TextEdit{Range{start, end}, e.Text}})
	}
	slices.SortStableFunc(all, func(x, y item) int {
		for _, c := range []int{posCmp(x.start, y.start), posCmp(x.end, y.end), posCmp(x.rank, y.rank), x.grp - y.grp, x.idx - y.idx} {
			if c != 0 {
				return c
			}
		}
		return 0
	})
	out := make([]TextEdit, len(all))
	for i, it := range all {
		out[i] = it.edit
	}
	return out, nil
}
