package rhunext

// Sections 12d to 12h of the protocol: go to definition, references, symbols, code actions,
// rename and format. What rhun asks, the answers encoded the way rhun reads them, the interfaces
// a handler implements to answer, and Ext.Apply, which asks rhun to make a workspace edit.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---- requests

// PosRequest is what definition, references and symbols ask about
// ("ID VERB DOC VERSION LINE COL invoked -"): every event about the document up to Version has
// been delivered; At is the cursor.
type PosRequest struct {
	Doc     Doc
	Version int64
	At      Pos
}

// ActionsRequest asks for the code actions of a range ("ID actions DOC VERSION L1 C1 L2 C2"):
// the selection, or an empty range at the cursor.
type ActionsRequest struct {
	Doc     Doc
	Version int64
	Range   Range
}

// RenameRequest asks to rename the symbol at the cursor ("ID rename DOC VERSION LINE COL invoked
// NAME"): Name is the new name the user typed (no spaces).
type RenameRequest struct {
	Doc     Doc
	Version int64
	At      Pos
	Name    string
}

// FormatRequest asks to format a document ("ID format DOC VERSION TAB_WIDTH SPACES") with its
// own settings: TabWidth, and Spaces to indent with spaces (else tabs).
type FormatRequest struct {
	Doc      Doc
	Version  int64
	TabWidth int
	Spaces   bool
}

func parseDocVersion(f []string) (Doc, int64, bool) {
	if len(f) < 2 {
		return 0, 0, false
	}
	d, e1 := strconv.ParseInt(f[0], 10, 64)
	v, e2 := strconv.ParseInt(f[1], 10, 64)
	return Doc(d), v, e1 == nil && e2 == nil
}

func parsePosRequest(f []string) (PosRequest, bool) {
	d, v, ok := parseDocVersion(f)
	if !ok || len(f) < 4 {
		return PosRequest{}, false
	}
	p, ok := parsePos(f[2], f[3])
	return PosRequest{Doc: d, Version: v, At: p}, ok
}

func parseActionsRequest(f []string) (ActionsRequest, bool) {
	d, v, ok := parseDocVersion(f)
	if !ok || len(f) < 6 {
		return ActionsRequest{}, false
	}
	a, ok1 := parsePos(f[2], f[3])
	b, ok2 := parsePos(f[4], f[5])
	return ActionsRequest{Doc: d, Version: v, Range: Range{Start: a, End: b}}, ok1 && ok2
}

func parseRenameRequest(f []string) (RenameRequest, bool) {
	p, ok := parsePosRequest(f)
	if !ok || len(f) < 6 || f[5] == "" {
		return RenameRequest{}, false
	}
	return RenameRequest{Doc: p.Doc, Version: p.Version, At: p.At, Name: f[5]}, true
}

func parseFormatRequest(f []string) (FormatRequest, bool) {
	d, v, ok := parseDocVersion(f)
	if !ok || len(f) < 4 {
		return FormatRequest{}, false
	}
	tw, err := strconv.Atoi(f[2])
	if err != nil || tw < 0 || f[3] != "yes" && f[3] != "no" {
		return FormatRequest{}, false
	}
	return FormatRequest{Doc: d, Version: v, TabWidth: tw, Spaces: f[3] == "yes"}, true
}

// ---- answers

// The caps of section 7. EncodeLocations and EncodeSymbols leave out the records past
// MaxRecords or MaxRecordsBody, EncodeActions the actions past MaxActions or MaxActionsBody; a
// workspace edit over MaxEditBody, MaxEditFiles or MaxEdits is not sent at all.
const (
	MaxRecords     = 10000
	MaxRecordsBody = 1 << 20
	MaxActions     = 1000
	MaxActionsBody = 4 << 20
	MaxEditBody    = 4 << 20
	MaxEditFiles   = 1000
	MaxEdits       = 100000
)

// The longest fields rhun reads (section 12d): one over makes it drop the whole answer, so the
// encoders cut names, titles, details and previews to MaxText bytes and kind words to MaxKind (at
// a character), leave out a symbol or action whose name or title is then empty and a location whose
// path is over MaxPath, and refuse to encode a workspace edit with such a path.
const (
	MaxText = 1000
	MaxKind = 40
	MaxPath = 4096
)

// cut is s cut to at most max bytes, at the start of a character.
func cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// field is a name, title, detail or preview as rhun reads it: one line, at most MaxText bytes.
func field(s string) string { return cut(oneLine(s), MaxText) }

// num is a line, column or depth as a number rhun reads (at most 10 digits, not negative).
func num(v int) string {
	n := int64(v)
	n = min(max(n, 0), 1<<32-1)
	return strconv.FormatInt(n, 10)
}

// Target is where a location or a file's edits are: a file by its absolute Path, or when Path is
// "" an open document by its id (an untitled one).
type Target struct {
	Path string
	Doc  Doc
}

// String is the target as rhun reads it: the path, or "#DOC".
func (t Target) String() string {
	if t.Path != "" {
		return t.Path
	}
	return "#" + itoa(int64(t.Doc))
}

func rangeFields(r Range) string {
	return num(r.Start.Line) + " " + num(r.Start.Col) + " " + num(r.End.Line) + " " + num(r.End.Col)
}

// Location is a place in a file: the answer to definition and references is a list of them.
// Range is in the open document when the file is open, else in the file on disk; Text is a
// one-line preview (the line's text, trimmed and cut to MaxText bytes when encoded), or "".
type Location struct {
	Target Target
	Range  Range
	Text   string
}

// EncodeLocations is the body rhun reads for a list of locations (section 12d). Those whose path is
// over MaxPath bytes, and those past MaxRecords or MaxRecordsBody, are left out.
func EncodeLocations(locs []Location) []byte {
	var recs bytes.Buffer
	n := 0
	for _, l := range locs {
		p, t := l.Target.String(), field(strings.TrimSpace(l.Text))
		if len(p) > MaxPath {
			continue
		}
		head := fmt.Sprintf("%s %d %d\n", rangeFields(l.Range), len(p), len(t))
		if n == MaxRecords || recs.Len()+len(head)+len(p)+len(t)+16 > MaxRecordsBody {
			break
		}
		recs.WriteString(head)
		recs.WriteString(p)
		recs.WriteString(t)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// SymbolKind is what a symbol is. rhun shows a kind it does not know as given.
type SymbolKind string

// The kinds of section 12e.
const (
	SymbolFile          SymbolKind = "file"
	SymbolModule        SymbolKind = "module"
	SymbolNamespace     SymbolKind = "namespace"
	SymbolPackage       SymbolKind = "package"
	SymbolClass         SymbolKind = "class"
	SymbolMethod        SymbolKind = "method"
	SymbolProperty      SymbolKind = "property"
	SymbolField         SymbolKind = "field"
	SymbolConstructor   SymbolKind = "constructor"
	SymbolEnum          SymbolKind = "enum"
	SymbolInterface     SymbolKind = "interface"
	SymbolFunction      SymbolKind = "function"
	SymbolVariable      SymbolKind = "variable"
	SymbolConstant      SymbolKind = "constant"
	SymbolString        SymbolKind = "string"
	SymbolNumber        SymbolKind = "number"
	SymbolBoolean       SymbolKind = "boolean"
	SymbolArray         SymbolKind = "array"
	SymbolObject        SymbolKind = "object"
	SymbolKey           SymbolKind = "key"
	SymbolNull          SymbolKind = "null"
	SymbolEnumMember    SymbolKind = "enum_member"
	SymbolStruct        SymbolKind = "struct"
	SymbolEvent         SymbolKind = "event"
	SymbolOperator      SymbolKind = "operator"
	SymbolTypeParameter SymbolKind = "type_parameter"
)

// Symbol is a symbol of a document: the answer to symbols is a list of them, in document order.
// Range is the whole symbol (the cursor goes to its start); Kind is SymbolFunction when "";
// Depth is 0 at the top level and one more for each level it is nested in; Name and Detail (a
// signature, a type, shown beside it) are one line each, at most MaxText bytes (cut when encoded).
type Symbol struct {
	Range  Range
	Kind   SymbolKind
	Depth  int
	Name   string
	Detail string
}

// word is a kind as one field: without white space and control characters, at most MaxKind
// bytes, def when nothing is left. Bytes that are not UTF-8 stay as they are.
func word(s, def string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if invalid := r == utf8.RuneError && size == 1; invalid || !unicode.IsSpace(r) && !unicode.IsControl(r) {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	w := cut(b.String(), MaxKind)
	if w == "" {
		return def
	}
	return w
}

// EncodeSymbols is the body rhun reads for a list of symbols (section 12e). Those without a name,
// and those past MaxRecords or MaxRecordsBody, are left out.
func EncodeSymbols(syms []Symbol) []byte {
	var recs bytes.Buffer
	n := 0
	for _, s := range syms {
		name, detail := field(s.Name), field(s.Detail)
		if name == "" {
			continue
		}
		head := fmt.Sprintf("%s %s %s %d %d\n", rangeFields(s.Range), word(string(s.Kind), string(SymbolFunction)),
			num(s.Depth), len(name), len(detail))
		if n == MaxRecords || recs.Len()+len(head)+len(name)+len(detail)+16 > MaxRecordsBody {
			break
		}
		recs.WriteString(head)
		recs.WriteString(name)
		recs.WriteString(detail)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// OnDisk is a FileEdit's Version for a file that is not open: the edits refer to its text on
// disk, and rhun opens it.
const OnDisk int64 = -1

// FileEdit is the edits of one file, all made against one text: the open document at Version,
// or the file on disk (Version OnDisk, or any negative). They must not overlap; insertions at
// one position keep their order. With feature workspace-edits, Hashed says the file on disk must
// still hash to Hash (Fnv1a64 of its bytes: PRE fnv1a64:HEX); without it such an edit is refused
// (an *Error with code unknown) and not sent.
type FileEdit struct {
	Target  Target
	Version int64
	Edits   []TextEdit
	Hashed  bool
	Hash    uint64
}

// WorkspaceEdit is edits across files (section 12g): rhun checks all of them before it changes
// anything. Without feature workspace-edits it opens the files that are not open and applies each
// file's edits as one undo step, saving nothing; with it the edit is one transaction (files that
// are not open are edited on disk, and it undoes as one step), named by Label, with the
// confirmation list forced by Confirm; with file-operations Ops create, rename and delete files.
// The body lists creates, then Files, then renames, then deletes (rhun's phases); Entry maps an
// error's ENTRY back. Apply and the answers that carry an edit follow the features rhun agreed:
// an edit with Ops, a hashed file, Label or Confirm is refused (an *Error with code unknown) when
// they were not, and in the bodies that keep 12g's grammar (format and save-actions answers,
// SaveApply). Propose sends 12g's grammar too, a hash as "-" and without Label and Confirm.
type WorkspaceEdit struct {
	Files   []FileEdit
	Ops     []FileOp
	Label   string
	Confirm bool
}

// AddDoc adds edits for an open document, made against its text at version.
func (w *WorkspaceEdit) AddDoc(doc Doc, version int64, edits ...TextEdit) {
	w.Files = append(w.Files, FileEdit{Target: Target{Doc: doc}, Version: version, Edits: edits})
}

// AddFile adds edits for a file by its absolute path: the open document at version, or with
// OnDisk the file as it is on disk (rhun opens it).
func (w *WorkspaceEdit) AddFile(path string, version int64, edits ...TextEdit) {
	w.Files = append(w.Files, FileEdit{Target: Target{Path: path}, Version: version, Edits: edits})
}

// Empty reports whether the edit changes nothing: no file operation and no edit.
func (w WorkspaceEdit) Empty() bool {
	if len(w.Ops) > 0 {
		return false
	}
	for _, f := range w.Files {
		if len(f.Edits) > 0 {
			return false
		}
	}
	return true
}

// Encode is the body rhun reads with workspace-edits and file-operations agreed (section 12g and
// protocol 1.1), or why it cannot be one: over MaxEditFiles entries (files and operations),
// MaxEdits or MaxEditBody, or a path that is empty or over MaxPath bytes. An edit cut short would
// leave the files half changed, so none is sent. Without Ops, hashes, Label and Confirm it is
// exactly the 12g body. Ext.EncodeEdit is the body for the features a rhun agreed.
func (w WorkspaceEdit) Encode() ([]byte, error) {
	b, bad := w.encodeWith(editCaps{ws: true, ops: true})
	if bad != nil {
		return nil, encodeErr(bad)
	}
	return b, nil
}

// EncodeEdit is the body of a workspace edit in the grammar this rhun agreed, as Apply, a rename
// answer and a code action's edit send it: the transaction with FeatureWorkspaceEdits, 12g's
// without. An edit that needs what was not agreed (Ops without FeatureWorkspaceEdits and
// FeatureFileOperations; a hashed file, Label or Confirm without FeatureWorkspaceEdits) is an
// *Error with code unknown, and a limit an error as from Encode.
func (e *Ext) EncodeEdit(w WorkspaceEdit) ([]byte, error) {
	b, bad := w.encodeWith(e.editCaps())
	if bad != nil {
		return nil, encodeErr(bad)
	}
	return b, nil
}

// Apply asks rhun to make a workspace edit (the apply request; needs documents = write): checked
// whole, then each document changed as one undo step, files that are not open opened in tabs,
// unsaved. For a language server's workspace/applyEdit. An edit that cannot be encoded is
// returned as an error without asking (an *Error with code unknown for file operations, a hashed
// file, a label or confirm rhun did not agree, as EncodeEdit); rhun refusing it is an *Error
// (syntax, nofile, nodoc, stale, readonly, badpos, toobig), and then nothing changed. With FeatureWorkspaceEdits agreed the edit is a transaction
// (files that are not open edited on disk, one undo) and a refusal is an *ApplyError naming the
// entry at fault (it unwraps to the *Error).
func (e *Ext) Apply(edit WorkspaceEdit) error {
	return e.ApplyContext(context.Background(), edit)
}

// ApplyContext is Apply with cancellation.
func (e *Ext) ApplyContext(ctx context.Context, edit WorkspaceEdit) error {
	body, bad := edit.encodeWith(e.editCaps())
	if bad != nil {
		return encodeErr(bad)
	}
	_, err := e.RequestContext(ctx, "apply", body, itoa(int64(len(body))))
	var re *Error
	if err != nil && e.Has(FeatureWorkspaceEdits) && errors.As(err, &re) {
		return applyError(re)
	}
	return err
}

// ActionKind is what a code action is: rhun lists quick fixes first and shows another kind
// beside the title.
type ActionKind string

const (
	ActionQuickFix ActionKind = "quickfix"
	ActionRefactor ActionKind = "refactor"
	ActionSource   ActionKind = "source"
)

// CodeAction is one action of the list rhun shows by its Title (one line, at most MaxText bytes:
// cut when encoded). Choosing it applies Edit, when not nil, and then sends Data, when not empty,
// back to the handler's Action method. Kind is ActionQuickFix when "".
type CodeAction struct {
	Kind  ActionKind
	Title string
	Edit  *WorkspaceEdit
	Data  []byte
}

// EncodeActions is the body rhun reads for a list of code actions (section 12f), their edits in
// the grammar of a rhun that agreed workspace-edits and file-operations (an ActionProvider's answer
// follows what was agreed). Actions without a title, those whose edit cannot be encoded, and those
// past MaxActions or MaxActionsBody are left out.
func EncodeActions(actions []CodeAction) []byte {
	return encodeActionsWith(actions, editCaps{ws: true, ops: true})
}

// encodeActionsWith is EncodeActions for a rhun that agreed caps: an action whose edit needs a
// feature it did not agree is left out (rhun would refuse the edit when it is chosen).
func encodeActionsWith(actions []CodeAction, caps editCaps) []byte {
	var recs bytes.Buffer
	n := 0
	for _, a := range actions {
		title := field(a.Title)
		if title == "" {
			continue
		}
		var edit []byte
		if a.Edit != nil {
			b, err := a.Edit.encodeWith(caps)
			if err != nil {
				continue
			}
			edit = b
		}
		head := fmt.Sprintf("%s %d %d %d\n", word(string(a.Kind), string(ActionQuickFix)), len(title), len(edit), len(a.Data))
		if n == MaxActions || recs.Len()+len(head)+len(title)+len(edit)+len(a.Data)+16 > MaxActionsBody {
			break
		}
		recs.WriteString(head)
		recs.WriteString(title)
		recs.Write(edit)
		recs.Write(a.Data)
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// ---- handlers

// Definer answers definition requests (feature definition): where the symbol at the cursor is
// defined. One location opens at once, several make a list, none says there is none.
type Definer interface {
	Definition(ext *Ext, req PosRequest) []Location
}

// ReferenceFinder answers references requests (feature references): where the symbol at the
// cursor is used.
type ReferenceFinder interface {
	References(ext *Ext, req PosRequest) []Location
}

// SymbolLister answers symbols requests (feature symbols): the document's symbols, in document
// order.
type SymbolLister interface {
	Symbols(ext *Ext, req PosRequest) []Symbol
}

// ActionProvider answers actions requests (feature actions), and gets the Data of the action
// the user chose (the action event, after its edit is applied): an action that has to run
// something does it then.
type ActionProvider interface {
	Actions(ext *Ext, req ActionsRequest) []CodeAction
	Action(ext *Ext, data []byte)
}

// Renamer answers rename requests (feature rename): the edits, or an error whose text rhun shows
// (nothing can be renamed there).
type Renamer interface {
	Rename(ext *Ext, req RenameRequest) (WorkspaceEdit, error)
}

// Formatter answers format requests (feature format): the edits for the document, usually one
// FileEdit; an empty WorkspaceEdit changes nothing.
type Formatter interface {
	Format(ext *Ext, req FormatRequest) WorkspaceEdit
}

// answerLang answers the requests of sections 12d to 12h: the body, or the error to send
// (rename refused, an edit that cannot be encoded); known is false for a verb the handler does
// not answer or a request that does not parse.
func (e *Ext) answerLang(h Handler, verb string, f []string) (body []byte, rerr *Error, known bool) {
	switch verb {
	case "definition":
		d, is := h.(Definer)
		if req, ok := parsePosRequest(f); is && ok {
			return EncodeLocations(d.Definition(e, req)), nil, true
		}
	case "references":
		r, is := h.(ReferenceFinder)
		if req, ok := parsePosRequest(f); is && ok {
			return EncodeLocations(r.References(e, req)), nil, true
		}
	case "symbols":
		s, is := h.(SymbolLister)
		if req, ok := parsePosRequest(f); is && ok {
			return EncodeSymbols(s.Symbols(e, req)), nil, true
		}
	case "actions":
		a, is := h.(ActionProvider)
		if req, ok := parseActionsRequest(f); is && ok {
			return encodeActionsWith(a.Actions(e, req), e.editCaps()), nil, true
		}
	case "rename":
		r, is := h.(Renamer)
		if req, ok := parseRenameRequest(f); is && ok {
			w, err := r.Rename(e, req)
			if err != nil {
				return nil, &Error{Code: "refused", Text: err.Error()}, true
			}
			return e.encodeEdit(w)
		}
	case "format":
		fm, is := h.(Formatter)
		if req, ok := parseFormatRequest(f); is && ok {
			return encodeEditV1(fm.Format(e, req), "a format answer")
		}
	case "linked":
		l, is := h.(LinkedEditor)
		if req, ok := parseLinkedRequest(f); is && ok && e.Has(FeatureLinkedEditing) {
			return l.Linked(e, req).Encode(), nil, true
		}
	}
	return nil, nil, false
}

// encodeEditV1 is a format or save-actions answer, what names it: 12g's grammar whatever was
// agreed (one open document), so file operations, a hash, a label or confirm are unknown.
func encodeEditV1(w WorkspaceEdit, what string) ([]byte, *Error, bool) {
	b, err := w.encodeWith(editCaps{fixed: what})
	if err != nil {
		return nil, err, true
	}
	return b, nil, true
}

// encodeEdit is a workspace edit's answer for the features rhun agreed: its body, or the error
// to answer with (toobig, or unknown for what needs a feature it did not agree).
func (e *Ext) encodeEdit(w WorkspaceEdit) ([]byte, *Error, bool) {
	b, err := w.encodeWith(e.editCaps())
	if err != nil {
		return nil, err, true
	}
	return b, nil, true
}

// maxErrorText keeps an error reply's line well inside the 64 KB a line may hold.
const maxErrorText = 1000

// errorLine is "error ID CODE TEXT" with the text on one line, cut at a character.
func errorLine(id string, re *Error) string {
	text := cut(strings.TrimSpace(oneLine(re.Text)), maxErrorText)
	if text == "" {
		return fmt.Sprintf("error %s %s\n", id, re.Code)
	}
	return fmt.Sprintf("error %s %s %s\n", id, re.Code, text)
}
