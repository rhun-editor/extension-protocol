package rhunext

// Protocol 1.1's navigation, prepare-rename, partial-format and code-actions (v1.1/v1.1.md,
// "Language features"): handlers answer rhun's requests; a handler implements only what it
// provides, and rhun's requests of a feature the handler lacks are answered "unknown".

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ---- navigation

// Declarer answers declaration requests (feature navigation).
type Declarer interface {
	Declaration(ext *Ext, req PosRequest) []Location
}

// TypeDefiner answers type-definition requests (feature navigation).
type TypeDefiner interface {
	TypeDefinition(ext *Ext, req PosRequest) []Location
}

// Implementer answers implementation requests (feature navigation).
type Implementer interface {
	Implementation(ext *Ext, req PosRequest) []Location
}

// ---- prepare-rename

// PreparedRename is what F2 shows: the range selected and the name the prompt starts with.
type PreparedRename struct {
	Range       Range
	Placeholder string
}

// ErrNothingToRename refuses a prepare-rename with a generic sentence; any other error's text is
// shown to the user as the reason.
var ErrNothingToRename = errors.New("Nothing to rename here")

// RenamePreparer answers prepare-rename requests (feature prepare-rename): nil, nil for the word
// at the cursor; an error refuses (its text is shown and no rename follows).
type RenamePreparer interface {
	PrepareRename(ext *Ext, req PosRequest) (*PreparedRename, error)
}

// Encode is the answer's body (empty for nil).
func (p *PreparedRename) Encode() []byte {
	if p == nil {
		return nil
	}
	ph := cut(oneLine(p.Placeholder), 200)
	return []byte(fmt.Sprintf("%s %d\n%s", rangeFields(p.Range), len(ph), ph))
}

// ---- partial-format

// RangeFormatRequest is "range-format DOC VERSION L1 C1 L2 C2 TAB_WIDTH SPACES TRIM FINAL".
type RangeFormatRequest struct {
	Doc      Doc
	Version  int64
	Range    Range
	TabWidth int
	Spaces   bool
	Trim     bool // trim_trailing_whitespace in force
	Final    bool // final_newline in force
}

// TypeFormatRequest is "type-format DOC VERSION LINE COL CHAR TAB_WIDTH SPACES TRIM FINAL": Char is
// the typed character, or "newline" for Enter; At the cursor after it.
type TypeFormatRequest struct {
	Doc      Doc
	Version  int64
	At       Pos
	Char     string
	TabWidth int
	Spaces   bool
	Trim     bool
	Final    bool
}

// RangeFormatter answers range-format requests (feature partial-format).
type RangeFormatter interface {
	RangeFormat(ext *Ext, req RangeFormatRequest) WorkspaceEdit
}

// TypeFormatter answers type-format requests (feature partial-format); rhun applies the edit only
// when the document is still at the request's version.
type TypeFormatter interface {
	TypeFormat(ext *Ext, req TypeFormatRequest) WorkspaceEdit
}

func yesNo(s string) (bool, bool) {
	switch s {
	case "yes":
		return true, true
	case "no":
		return false, true
	}
	return false, false
}

func parseOptions(f []string) (tab int, spaces, trim, final, ok bool) {
	if len(f) != 4 {
		return
	}
	t, err := strconv.Atoi(f[0])
	s, ok1 := yesNo(f[1])
	tr, ok2 := yesNo(f[2])
	fi, ok3 := yesNo(f[3])
	if err != nil || t < 1 || !ok1 || !ok2 || !ok3 {
		return
	}
	return t, s, tr, fi, true
}

func parseRangeFormat(f []string) (RangeFormatRequest, bool) {
	if len(f) != 10 {
		return RangeFormatRequest{}, false
	}
	doc, version, ok := parseDocVersion(f)
	a, ok1 := parsePos(f[2], f[3])
	b, ok2 := parsePos(f[4], f[5])
	tab, spaces, trim, final, ok3 := parseOptions(f[6:])
	if !ok || !ok1 || !ok2 || !ok3 {
		return RangeFormatRequest{}, false
	}
	return RangeFormatRequest{doc, version, Range{a, b}, tab, spaces, trim, final}, true
}

func parseTypeFormat(f []string) (TypeFormatRequest, bool) {
	if len(f) != 9 {
		return TypeFormatRequest{}, false
	}
	doc, version, ok := parseDocVersion(f)
	at, ok1 := parsePos(f[2], f[3])
	tab, spaces, trim, final, ok2 := parseOptions(f[5:])
	if !ok || !ok1 || !ok2 || f[4] == "" {
		return TypeFormatRequest{}, false
	}
	return TypeFormatRequest{doc, version, at, f[4], tab, spaces, trim, final}, true
}

// ---- code-actions

// CodeActionsRequest is "code-actions DOC VERSION L1 C1 L2 C2 TRIGGER KINDS DIAGS": Trigger is
// invoked or auto (the lightbulb), Kinds the hierarchical kinds wanted (none: any), Diagnostics
// the ids of this extension's diagnostics in the range. Context is cancelled by rhun's cancel.
type CodeActionsRequest struct {
	Doc         Doc
	Version     int64
	Range       Range
	Trigger     string
	Kinds       []string
	Diagnostics []string
	Context     context.Context
}

// Wants reports whether an action of that kind matches the request's kinds (it equals or descends
// from one; any kind when none were asked). rhun drops actions that do not.
func (r CodeActionsRequest) Wants(kind string) bool {
	if len(r.Kinds) == 0 {
		return true
	}
	for _, k := range r.Kinds {
		if kind == k || strings.HasPrefix(kind, k+".") {
			return true
		}
	}
	return false
}

// Action is a 1.1 code action: Edit applies when chosen, else Resolve is handed back in a
// code-action-resolve request; Command runs after the edit (a command reference, feature
// command-args); Disabled gives the reason it cannot apply; Fixes the diagnostic ids it fixes.
type Action struct {
	Kind      string
	Title     string
	Edit      *WorkspaceEdit
	Preferred bool
	Disabled  string
	Resolve   []byte
	Command   *CommandRef
	Fixes     []string
}

// CommandRef is a reference to one of the extension's own commands, with ARGS rhun hands back
// verbatim (at most 8192 bytes).
type CommandRef struct {
	Name string
	Args []byte
}

// Encode is the record "command NAME ARGS_LEN" + ARGS.
func (c *CommandRef) Encode() []byte {
	if c == nil || c.Name == "" || len(c.Args) > 8192 {
		return nil
	}
	return append([]byte(fmt.Sprintf("command %s %d\n", c.Name, len(c.Args))), c.Args...)
}

// ResolvedAction is a code-action-resolve answer: the edit and the command to run after it.
type ResolvedAction struct {
	Edit    *WorkspaceEdit
	Command *CommandRef
}

// CodeActionResolveRequest asks for the rest of an action that had Resolve data.
type CodeActionResolveRequest struct {
	Doc     Doc
	Version int64
	Data    []byte
	Context context.Context
}

// CodeActionProvider2 answers code-actions requests (feature code-actions) instead of Actions.
type CodeActionProvider2 interface {
	CodeActions(ext *Ext, req CodeActionsRequest) []Action
}

// CodeActionResolver answers code-action-resolve requests; an error says nothing comes.
type CodeActionResolver interface {
	ResolveCodeAction(ext *Ext, req CodeActionResolveRequest) (ResolvedAction, error)
}

// EncodeCodeActions is the code-actions answer. Actions without a title, doing nothing (no edit,
// resolve data or command, and not disabled), or whose edit cannot be encoded are left out, as
// those past MaxActions or MaxActionsBody.
func EncodeCodeActions(actions []Action) []byte {
	var recs bytes.Buffer
	n := 0
	for _, a := range actions {
		title := field(a.Title)
		if title == "" {
			continue
		}
		var edit []byte
		if a.Edit != nil {
			b, err := a.Edit.Encode()
			if err != nil {
				continue
			}
			edit = b
		}
		var extra bytes.Buffer
		if a.Preferred {
			extra.WriteString("preferred 0\n")
		}
		if a.Disabled != "" {
			d := cut(oneLine(a.Disabled), 1000)
			fmt.Fprintf(&extra, "disabled %d\n%s", len(d), d)
		}
		if len(a.Resolve) > 0 && len(a.Resolve) <= MaxResolveData {
			fmt.Fprintf(&extra, "resolve %d\n", len(a.Resolve))
			extra.Write(a.Resolve)
		}
		cmd := a.Command.Encode()
		extra.Write(cmd)
		if len(a.Fixes) > 0 {
			f := strings.Join(a.Fixes, " ")
			fmt.Fprintf(&extra, "fixes %d\n%s", len(f), f)
		}
		if len(edit) == 0 && len(a.Resolve) == 0 && len(cmd) == 0 && a.Disabled == "" {
			continue
		}
		kind := word(a.Kind, string(ActionQuickFix))
		if len(kind) > 128 {
			kind = kind[:128]
		}
		head := fmt.Sprintf("%s %d %d %d\n", kind, len(title), len(edit), extra.Len())
		if n == MaxActions || recs.Len()+len(head)+len(title)+len(edit)+extra.Len()+16 > MaxActionsBody {
			break
		}
		recs.WriteString(head)
		recs.WriteString(title)
		recs.Write(edit)
		recs.Write(extra.Bytes())
		n++
	}
	return append([]byte(fmt.Sprintf("%d\n", n)), recs.Bytes()...)
}

// Encode is the code-action-resolve answer: "EDIT_LEN EXTRA_LEN" LF + EDIT + EXTRA.
func (r ResolvedAction) Encode() ([]byte, error) {
	var edit []byte
	if r.Edit != nil {
		b, err := r.Edit.Encode()
		if err != nil {
			return nil, err
		}
		edit = b
	}
	cmd := r.Command.Encode()
	out := []byte(fmt.Sprintf("%d %d\n", len(edit), len(cmd)))
	out = append(out, edit...)
	return append(out, cmd...), nil
}

func parseCodeActions(f []string) (CodeActionsRequest, bool) {
	if len(f) != 9 {
		return CodeActionsRequest{}, false
	}
	doc, version, ok := parseDocVersion(f)
	a, ok1 := parsePos(f[2], f[3])
	b, ok2 := parsePos(f[4], f[5])
	if !ok || !ok1 || !ok2 || (f[6] != "invoked" && f[6] != "auto") {
		return CodeActionsRequest{}, false
	}
	req := CodeActionsRequest{Doc: doc, Version: version, Range: Range{a, b}, Trigger: f[6]}
	if f[7] != "-" {
		req.Kinds = strings.Split(f[7], ",")
	}
	if f[8] != "-" {
		req.Diagnostics = strings.Split(f[8], ",")
	}
	return req, true
}

// answerActions answers the requests of this file; known is false when the handler lacks the
// interface or the request does not parse.
func (e *Ext) answerActions(h Handler, id int64, verb string, f []string, body []byte) (out []byte, rerr *Error, known bool) {
	ctx := e.langStream(id).Context
	switch verb {
	case "declaration":
		if d, is := h.(Declarer); is && e.Has(FeatureNavigation) {
			if req, ok := parsePosRequest(f); ok {
				return EncodeLocations(d.Declaration(e, req)), nil, true
			}
		}
	case "type-definition":
		if d, is := h.(TypeDefiner); is && e.Has(FeatureNavigation) {
			if req, ok := parsePosRequest(f); ok {
				return EncodeLocations(d.TypeDefinition(e, req)), nil, true
			}
		}
	case "implementation":
		if d, is := h.(Implementer); is && e.Has(FeatureNavigation) {
			if req, ok := parsePosRequest(f); ok {
				return EncodeLocations(d.Implementation(e, req)), nil, true
			}
		}
	case "prepare-rename":
		if p, is := h.(RenamePreparer); is && e.Has(FeaturePrepareRename) {
			if req, ok := parsePosRequest(f); ok {
				prep, err := p.PrepareRename(e, req)
				if err != nil {
					return nil, &Error{Code: "refused", Text: err.Error()}, true
				}
				return prep.Encode(), nil, true
			}
		}
	case "range-format":
		if r, is := h.(RangeFormatter); is && e.Has(FeaturePartialFormat) {
			if req, ok := parseRangeFormat(f); ok {
				return encodeEdit(r.RangeFormat(e, req))
			}
		}
	case "type-format":
		if t, is := h.(TypeFormatter); is && e.Has(FeaturePartialFormat) {
			if req, ok := parseTypeFormat(f); ok {
				return encodeEdit(t.TypeFormat(e, req))
			}
		}
	case "code-actions":
		if a, is := h.(CodeActionProvider2); is && e.Has(FeatureCodeActions) {
			if req, ok := parseCodeActions(f); ok {
				req.Context = ctx
				var keep []Action
				for _, act := range a.CodeActions(e, req) {
					if req.Wants(act.Kind) {
						keep = append(keep, act)
					}
				}
				return EncodeCodeActions(keep), nil, true
			}
		}
	case "code-action-resolve":
		if r, is := h.(CodeActionResolver); is && e.Has(FeatureCodeActions) && len(f) == 3 {
			doc, version, ok := parseDocVersion(f)
			if n, err := strconv.Atoi(f[2]); ok && err == nil && n == len(body) {
				res, err := r.ResolveCodeAction(e, CodeActionResolveRequest{doc, version, body, ctx})
				if err != nil {
					return nil, &Error{Code: "refused", Text: err.Error()}, true
				}
				b, err := res.Encode()
				if err != nil {
					return nil, &Error{Code: "toobig", Text: err.Error()}, true
				}
				return b, nil, true
			}
		}
	}
	return nil, nil, false
}
