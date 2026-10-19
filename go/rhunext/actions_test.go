package rhunext

import (
	"errors"
	"testing"
)

type actionsProbe struct{}

func (a *actionsProbe) Ready(ext *Ext, project string)        {}
func (a *actionsProbe) Event(ext *Ext, ev Event)              {}
func (a *actionsProbe) Run(ext *Ext, command string, doc Doc) {}

func (a *actionsProbe) Declaration(ext *Ext, req PosRequest) []Location {
	return []Location{{Target: Target{Doc: req.Doc}, Range: rg(1, 1, 1, 2), Text: "d"}}
}

func (a *actionsProbe) PrepareRename(ext *Ext, req PosRequest) (*PreparedRename, error) {
	if req.At.Line == 9 {
		return nil, errors.New("You cannot rename a keyword")
	}
	if req.At.Line == 8 {
		return nil, nil
	}
	return &PreparedRename{Range: rg(2, 9, 2, 14), Placeholder: "total"}, nil
}

func (a *actionsProbe) RangeFormat(ext *Ext, req RangeFormatRequest) WorkspaceEdit {
	var w WorkspaceEdit
	if req.Trim && !req.Final && req.TabWidth == 4 && req.Spaces {
		w.AddDoc(req.Doc, req.Version, TextEdit{Range: req.Range, Text: "x"})
	}
	return w
}

func (a *actionsProbe) TypeFormat(ext *Ext, req TypeFormatRequest) WorkspaceEdit {
	var w WorkspaceEdit
	if req.Char == "newline" {
		w.AddDoc(req.Doc, req.Version, TextEdit{Range: Range{req.At, req.At}, Text: "  "})
	}
	return w
}

func (a *actionsProbe) CodeActions(ext *Ext, req CodeActionsRequest) []Action {
	return []Action{
		{Kind: "quickfix", Title: "Fix", Preferred: true, Resolve: []byte("R"), Fixes: []string{"d1"}},
		{Kind: "refactor.extract", Title: "Extract", Command: &CommandRef{Name: "extract", Args: []byte("a b")}},
		{Kind: "quickfix", Title: "Cannot", Disabled: "No fix here"},
		{Kind: "quickfix", Title: "Nothing"},
	}
}

func (a *actionsProbe) ResolveCodeAction(ext *Ext, req CodeActionResolveRequest) (ResolvedAction, error) {
	var w WorkspaceEdit
	w.AddDoc(req.Doc, req.Version, TextEdit{Range: rg(1, 1, 1, 1), Text: string(req.Data)})
	return ResolvedAction{Edit: &w, Command: &CommandRef{Name: "fix", Args: []byte("z")}}, nil
}

func TestNavigationRenameFormatAndActions(t *testing.T) {
	feats := []string{FeatureNavigation, FeaturePrepareRename, FeaturePartialFormat, FeatureCodeActions}
	s := startHost(t, feats, &actionsProbe{})
	s.expect("hello 1 navigation prepare-rename partial-format code-actions")
	s.send("0 features navigation prepare-rename partial-format code-actions\n0 ready -\n")
	s.send("2 declaration 4 7 1 1 invoked -\n")
	s.answer(2, list(locRec("1 1 1 2", "#4", "d")))
	s.send("3 prepare-rename 4 7 2 10 invoked -\n")
	s.answer(3, "2 9 2 14 5\ntotal")
	s.send("4 prepare-rename 4 7 9 1 invoked -\n")
	s.expect("error 4 refused You cannot rename a keyword")
	s.send("5 prepare-rename 4 7 8 1 invoked -\n")
	s.answer(5, "")
	s.send("6 range-format 4 7 1 1 2 1 4 yes yes no\n")
	s.answer(6, workspace(group("#4", "7", editRec("1 1 2 1", "x"))))
	s.send("7 type-format 4 7 3 5 newline 4 yes no yes\n")
	s.answer(7, workspace(group("#4", "7", editRec("3 5 3 5", "  "))))
	s.send("8 code-actions 4 7 1 1 1 1 invoked quickfix -\n")
	s.answer(8, "2\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1quickfix 6 0 23\nCannotdisabled 11\nNo fix here")
	s.send("9 code-actions 4 7 1 1 1 1 invoked - d1,d2\n")
	s.answer(9, "3\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1refactor.extract 7 0 21\nExtractcommand extract 3\na bquickfix 6 0 23\nCannotdisabled 11\nNo fix here")
	edit := workspace(group("#4", "7", editRec("1 1 1 1", "R")))
	s.send("10 code-action-resolve 4 7 1\nR")
	s.answer(10, "21 15\n"+edit+"command fix 1\nz")
}
