package rhunext

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestFnv1a64IsTheStandardHash(t *testing.T) {
	for in, want := range map[string]uint64{"": 0xcbf29ce484222325, "a": 0xaf63dc4c8601ec8c, "foobar": 0x85944171f73967e8} {
		if got := Fnv1a64([]byte(in)); got != want {
			t.Fatalf("%q: %x, want %x", in, got, want)
		}
	}
}

func TestTransactionsAreFramedAsTheSpecSays(t *testing.T) {
	var w WorkspaceEdit
	w.Label = "Rename to 'total'\nnow"
	w.Delete("/p/old.go", DeleteOptions{Recursive: true, IgnoreMissing: true})
	w.AddFileHashed("/p/calc.go", []byte("a\r\nb\r\n"), TextEdit{rg(2, 1, 2, 2), "B"})
	w.Rename("/p/util.go", "/p/helpers.go", RenameOptions{Overwrite: true})
	w.AddDoc(3, 9, TextEdit{rg(1, 1, 1, 1), "x"})
	w.Create("/p/new/", CreateOptions{})
	w.Create("/p/new/a.go", CreateOptions{IgnoreExisting: true})
	w.Confirm = true
	h := fmt.Sprintf("%016x", Fnv1a64([]byte("a\r\nb\r\n")))
	want := "6\ncreate 7 -\n/p/new/create 11 ignore-existing\n/p/new/a.go" +
		"10 fnv1a64:" + h + " 1\n/p/calc.go2 1 2 2 1\nB2 9 1\n#31 1 1 1 1\nx" +
		"rename 10 13 overwrite\n/p/util.go/p/helpers.go" +
		"delete 9 recursive,ignore-missing\n/p/old.go" +
		"label 21\nRename to 'total' nowconfirm 0\n"
	b, err := w.Encode()
	if err != nil || string(b) != want {
		t.Fatalf("got %q %v\nwant %q", b, err, want)
	}
	for n, e := range map[int]Entry{1: {true, 2}, 2: {true, 3}, 3: {false, 0}, 4: {false, 1}, 5: {true, 1}, 6: {true, 0}} {
		if got, ok := w.Entry(n); !ok || got != e {
			t.Fatalf("entry %d: %+v %v, want %+v", n, got, ok, e)
		}
	}
	if _, ok := w.Entry(0); ok {
		t.Fatal("entry 0")
	}
	if _, ok := w.Entry(7); ok {
		t.Fatal("entry 7")
	}
	// a rhun without file-operations gets no body (TestTransactionPartsNeedTheFeature has the rest)
	if _, bad := w.encodeWith(editCaps{ws: true}); bad == nil || bad.Code != "unknown" {
		t.Fatalf("ops without the feature: %v", bad)
	}
	var plain WorkspaceEdit
	plain.Label, plain.Confirm = "L", true
	plain.AddFileHashed("/p/a", []byte("x"))
	if b, _ := plain.encodeWith(editCaps{ws: true}); string(b) != fmt.Sprintf("1\n4 fnv1a64:%016x 0\n/p/alabel 1\nLconfirm 0\n", Fnv1a64([]byte("x"))) {
		t.Fatalf("ws: %q", b)
	}
	// a label is one line of at most 200 bytes, cut at a character
	long := WorkspaceEdit{Label: strings.Repeat("x", 199) + "é"}
	if b, _ := long.Encode(); string(b) != "0\nlabel 199\n"+strings.Repeat("x", 199) {
		t.Fatalf("long label: %q", b)
	}
	// file operations count as entries, and their paths are checked
	var many WorkspaceEdit
	for i := 0; i < 1001; i++ {
		many.Delete(fmt.Sprintf("/p/%d", i), DeleteOptions{})
	}
	if _, err := many.Encode(); err == nil || err.Error() != "1001 entries: at most 1000" {
		t.Fatalf("many: %v", err)
	}
	var empty WorkspaceEdit
	empty.Create("", CreateOptions{})
	if _, err := empty.Encode(); err == nil || empty.Empty() {
		t.Fatal("an empty path")
	}
}

func TestApplyErrorsNameTheEntry(t *testing.T) {
	e := applyError(&Error{Code: "stale", Text: "2 /p/a.go changed on disk"})
	if e.Code != "stale" || e.Entry != 2 || e.Text != "/p/a.go changed on disk" {
		t.Fatalf("%+v", e)
	}
	if !errors.Is(e, e) || !Cancelled(applyError(&Error{Code: "cancelled", Text: "0 the user cancelled"})) {
		t.Fatal("unwrap")
	}
	if e := applyError(&Error{Code: "io", Text: "disk full"}); e.Entry != 0 || e.Text != "disk full" {
		t.Fatalf("no entry: %+v", e)
	}
}

// applyAll makes edits as rhun does: sorted stably by start then end, from the last to the first.
func applyAll(text string, edits []TextEdit) string {
	off := func(p Pos) int {
		o := 0
		for l := 1; l < p.Line; l++ {
			o += strings.IndexByte(text[o:], '\n') + 1
		}
		return o + p.Col - 1
	}
	es := slices.Clone(edits)
	slices.SortStableFunc(es, func(x, y TextEdit) int {
		if c := posCmp(x.Range.Start, y.Range.Start); c != 0 {
			return c
		}
		return posCmp(x.Range.End, y.Range.End)
	})
	out := text
	for i := len(es) - 1; i >= 0; i-- {
		out = out[:off(es[i].Range.Start)] + es[i].Text + out[off(es[i].Range.End):]
	}
	return out
}

func te(l1, c1, l2, c2 int, text string) TextEdit { return TextEdit{rg(l1, c1, l2, c2), text} }

func TestComposingMatchesMakingTheGroupsInTurn(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	cases := [][2][]TextEdit{
		{{te(1, 1, 1, 1, "zero\n")}, {te(3, 1, 3, 5, "BETA")}},
		{{te(2, 1, 2, 5, "B")}, {te(2, 2, 2, 2, "!"), te(2, 1, 2, 1, "<")}},
		{{te(1, 3, 1, 3, "XY")}, {te(1, 3, 1, 3, "["), te(1, 5, 1, 5, "]")}},
		{{te(1, 3, 3, 2, "")}, {te(1, 3, 1, 4, "M"), te(2, 1, 2, 1, "end")}},
		{{te(2, 1, 2, 1, "a\nb\n"), te(3, 6, 4, 1, "")}, {te(5, 6, 5, 6, "tail")}},
		{{te(3, 1, 3, 2, "GG"), te(3, 4, 3, 6, "")}, {te(3, 3, 3, 4, "_")}},
	}
	for _, c := range cases {
		want := applyAll(applyAll(text, c[0]), c[1])
		merged, err := composeEdits(c[0], c[1])
		if err != nil {
			t.Fatalf("%v then %v: %v", c[0], c[1], err)
		}
		if got := applyAll(text, merged); got != want {
			t.Fatalf("%v then %v: merged %v makes %q, want %q", c[0], c[1], merged, got, want)
		}
	}
	base := []TextEdit{te(1, 3, 1, 3, "XYZ"), te(2, 1, 2, 3, "")}
	for _, bad := range []TextEdit{te(1, 4, 1, 4, "!"), te(1, 2, 1, 5, ""), te(1, 8, 2, 2, "")} {
		if _, err := composeEdits(base, []TextEdit{bad}); err == nil {
			t.Fatalf("%v composed", bad)
		}
	}
	if _, err := composeEdits(base, []TextEdit{te(1, 6, 1, 6, "ok")}); err != nil {
		t.Fatal(err)
	}
}

func edit(path string, edits ...TextEdit) Change {
	return Change{Kind: ChangeEdit, Path: path, Version: OnDisk, Hashed: true, Hash: 7, Edits: edits}
}

func TestEditsAfterARenameNameTheFileBeforeIt(t *testing.T) {
	changes := []Change{
		edit("/p/a.rs", te(1, 1, 1, 1, "x")),
		{Kind: ChangeRename, Path: "/p/a.rs", To: "/p/b.rs"},
		edit("/p/b.rs", te(1, 2, 1, 2, "y")),
		edit("/p/c.rs", te(1, 1, 1, 1, "c")),
		{Kind: ChangeCreate, Path: "/p/new.rs"},
		edit("/p/new.rs", te(1, 1, 1, 1, "fn main() {}\n")),
		{Kind: ChangeRename, Path: "/p/dir", To: "/p/dir2"},
		edit("/p/dir2/m.rs", te(1, 1, 1, 1, "m")),
		edit("/p/gone.rs", te(1, 1, 1, 1, "g")),
		{Kind: ChangeDelete, Path: "/p/gone.rs"},
	}
	w, origins, err := NormalizeOrderedOrigins(changes)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := w.Encode()
	want := "8\ncreate 9 -\n/p/new.rs7 fnv1a64:0000000000000007 2\n/p/a.rs1 1 1 1 1\nx1 1 1 1 1\ny" +
		"7 fnv1a64:0000000000000007 1\n/p/c.rs1 1 1 1 1\nc9 - 1\n/p/new.rs1 1 1 1 13\nfn main() {}\n" +
		"11 fnv1a64:0000000000000007 1\n/p/dir/m.rs1 1 1 1 1\nm" +
		"rename 7 7 -\n/p/a.rs/p/b.rsrename 6 7 -\n/p/dir/p/dir2delete 10 -\n/p/gone.rs"
	if string(b) != want {
		t.Fatalf("got  %q\nwant %q", b, want)
	}
	if !slices.Equal(origins, []int{4, 0, 3, 5, 7, 1, 6, 9}) {
		t.Fatalf("origins %v", origins)
	}
}

func TestWhatThePhasesCannotExpressIsRefused(t *testing.T) {
	ren := func(f, to string) Change { return Change{Kind: ChangeRename, Path: f, To: to} }
	del := func(p string) Change { return Change{Kind: ChangeDelete, Path: p} }
	x := te(1, 1, 1, 1, "x")
	cases := []struct {
		changes []Change
		at      int
		part    string
	}{
		{[]Change{del("/p/a"), edit("/p/a", x)}, 1, "after /p/a was deleted"},
		{[]Change{ren("/p/a", "/p/b"), edit("/p/a", x)}, 1, "after /p/a was renamed"},
		{[]Change{del("/p/d"), edit("/p/d/f", x)}, 1, "was deleted"},
		{[]Change{ren("/p/a", "/p/b"), ren("/p/b", "/p/c")}, 1, "two file operations"},
		{[]Change{edit("/p/b", x), ren("/p/a", "/p/b")}, 1, "after an edit of /p/b"},
		{[]Change{edit("/p/n", x), {Kind: ChangeCreate, Path: "/p/n"}}, 1, "created after"},
		{[]Change{ren("/p/d", "/p/e"), del("/p/d/f")}, 1, "folder"},
		{[]Change{del("/p/d/f"), del("/p/d")}, 1, "folder"},
		{[]Change{edit("/p/a", te(1, 1, 1, 1, "XY")), edit("/p/a", te(1, 2, 1, 2, "!"))}, 1, "earlier edit"},
		{[]Change{{Kind: "move", Path: "/p/a"}}, 0, "unknown kind"},
	}
	for _, c := range cases {
		_, err := NormalizeOrdered(c.changes)
		var oe *OrderError
		if !errors.As(err, &oe) || oe.Change != c.at || !strings.Contains(oe.Text, c.part) {
			t.Fatalf("%+v: %v", c.changes, err)
		}
	}
	w, err := NormalizeOrdered([]Change{{Kind: ChangeCreate, Path: "/p/d/"}, {Kind: ChangeCreate, Path: "/p/d/f", Create: CreateOptions{true, true}}})
	if b, _ := w.Encode(); err != nil || string(b) != "2\ncreate 5 -\n/p/d/create 6 overwrite,ignore-existing\n/p/d/f" {
		t.Fatalf("%q %v", b, err)
	}
	// LSP's changes (no operations, a version for an open document) come through as they were
	w, err = NormalizeOrdered([]Change{{Kind: ChangeEdit, Path: "/p/open.go", Version: 4, Edits: []TextEdit{x}}})
	if b, _ := w.Encode(); err != nil || string(b) != "1\n10 4 1\n/p/open.go1 1 1 1 1\nx" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestLocatingTheTextAnEditRefersTo(t *testing.T) {
	before := []Change{{Kind: ChangeRename, Path: "/p/a", To: "/p/b"}, {Kind: ChangeCreate, Path: "/p/c"}}
	for path, want := range map[string]Located{"/p/b": {"/p/a", false}, "/p/c": {"/p/c", true}, "/p/z": {"/p/z", false}} {
		if got, err := Locate(before, path); err != nil || got != want {
			t.Fatalf("%s: %+v %v", path, got, err)
		}
	}
	if _, err := Locate(before, "/p/a"); err == nil {
		t.Fatal("located a file renamed away")
	}
}

func TestTransactionPartsNeedTheFeature(t *testing.T) {
	x := TextEdit{rg(1, 1, 1, 1), "x"}
	var hashed, labelled, confirmed, ops WorkspaceEdit
	hashed.AddFileHashed("/p/a", []byte("x"), x)
	labelled.Label = "L"
	labelled.AddDoc(3, 9, x)
	confirmed.Confirm = true
	confirmed.AddDoc(3, 9, x)
	ops.Create("/p/n", CreateOptions{})
	for _, c := range []struct {
		w    WorkspaceEdit
		part string
	}{{hashed, "fnv1a64 precondition"}, {labelled, "label"}, {confirmed, "confirm record"}} {
		if b, bad := c.w.encodeWith(editCaps{}); bad == nil || b != nil || bad.Code != "unknown" ||
			bad.Text != "the edit's "+c.part+" needs the workspace-edits feature, which was not agreed" {
			t.Fatalf("%s without workspace-edits: %q %v", c.part, b, bad)
		}
		if _, bad := c.w.encodeWith(editCaps{ws: true, ops: true, fixed: "a format answer"}); bad == nil ||
			bad.Text != "a format answer keeps 12g's grammar, which has no "+c.part {
			t.Fatalf("%s in a format answer: %v", c.part, bad)
		}
		if _, bad := c.w.encodeWith(editCaps{ws: true}); bad != nil {
			t.Fatalf("%s with workspace-edits: %v", c.part, bad)
		}
	}
	for _, caps := range []editCaps{{}, {ws: true}, {ops: true}} {
		if _, bad := ops.encodeWith(caps); bad == nil || bad.Text != "file operations need the workspace-edits and file-operations features" {
			t.Fatalf("ops with %+v: %v", caps, bad)
		}
	}
	if _, bad := ops.encodeWith(editCaps{fixed: "propose"}); bad == nil || bad.Text != "propose keeps 12g's grammar, which has no file operations" {
		t.Fatalf("ops in propose: %v", bad)
	}
	// plain text edits are 12g's body whatever was agreed; a hash beside a version is not sent
	var plain WorkspaceEdit
	plain.AddDoc(3, 9, x)
	plain.AddFile("/p/a", OnDisk, x)
	plain.Files = append(plain.Files, FileEdit{Target: Target{Doc: 4}, Version: 2, Edits: []TextEdit{x}, Hashed: true, Hash: 1})
	want := workspace(group("#3", "9", editRec("1 1 1 1", "x")), group("/p/a", "-", editRec("1 1 1 1", "x")), group("#4", "2", editRec("1 1 1 1", "x")))
	for _, caps := range []editCaps{{}, {ws: true, ops: true}, {fixed: "a format answer"}} {
		if b, bad := plain.encodeWith(caps); bad != nil || string(b) != want {
			t.Fatalf("plain with %+v: %q %v", caps, b, bad)
		}
	}
	// the errors of the public encoders: an *Error with code unknown
	var re *Error
	if _, err := ops.Encode(); err != nil {
		t.Fatalf("Encode is the whole grammar: %v", err)
	}
	if _, err := (ResolvedAction{Edit: &ops}).Encode(); err != nil {
		t.Fatalf("a resolve answer's Encode is the whole grammar: %v", err)
	}
	if _, bad := (ResolvedAction{Edit: &hashed}).encodeWith(editCaps{}); bad == nil || bad.Code != "unknown" {
		t.Fatalf("a resolve answer without workspace-edits: %v", bad)
	}
	if err := encodeErr(&Error{Code: "unknown", Text: "x"}); !errors.As(err, &re) || re.Code != "unknown" {
		t.Fatalf("encodeErr: %v", err)
	}
}

// txProbe answers with and makes edits that need the transaction grammar
type txProbe struct{ got chan string }

func (p *txProbe) Event(ext *Ext, ev Event)              {}
func (p *txProbe) Run(ext *Ext, command string, doc Doc) {}
func (p *txProbe) Action(ext *Ext, data []byte)          {}

func errCode(err error) string {
	var re *Error
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &re):
		return re.Code + ": " + re.Text
	}
	return err.Error()
}

var txHashed = func() WorkspaceEdit {
	var w WorkspaceEdit
	w.AddFileHashed("/p/a.go", []byte("x"), TextEdit{rg(1, 1, 1, 1), "y"})
	return w
}()

var txMove = func() WorkspaceEdit {
	w := WorkspaceEdit{Label: "Move"}
	w.Rename("/p/a.go", "/p/b.go", RenameOptions{})
	return w
}()

var txPlain = func() WorkspaceEdit {
	var w WorkspaceEdit
	w.AddDoc(4, 7, TextEdit{rg(1, 1, 1, 1), "y"})
	return w
}()

func (p *txProbe) Ready(ext *Ext, project string) {
	p.got <- errCode(ext.Apply(txHashed))
	_, err := ext.Propose("Move", "", txMove)
	p.got <- errCode(err)
	b, err := ext.EncodeEdit(txMove)
	p.got <- fmt.Sprintf("%q %s", b, errCode(err))
}

func (p *txProbe) Rename(ext *Ext, req RenameRequest) (WorkspaceEdit, error) { return txMove, nil }

func (p *txProbe) Format(ext *Ext, req FormatRequest) WorkspaceEdit {
	w := txPlain
	w.Label = "Format"
	return w
}

func (p *txProbe) Actions(ext *Ext, req ActionsRequest) []CodeAction {
	return []CodeAction{{Title: "Plain", Edit: &txPlain}, {Title: "Hashed", Edit: &txHashed}}
}

func (p *txProbe) CodeActions(ext *Ext, req CodeActionsRequest) []Action {
	return []Action{{Title: "Plain", Edit: &txPlain}, {Title: "Hashed", Edit: &txHashed}, {Title: "Move", Edit: &txMove}}
}

func (p *txProbe) ResolveCodeAction(ext *Ext, req CodeActionResolveRequest) (ResolvedAction, error) {
	return ResolvedAction{Edit: &txHashed}, nil
}

func (p *txProbe) next(t *testing.T) string {
	t.Helper()
	m := &modelProbe{got: p.got}
	return m.next(t)
}

// txSession sends the requests whose answers carry an edit: v1 actions, rename, format, code-actions
// and code-action-resolve
func txSession(s *host) {
	s.send("2 actions 4 7 1 1 1 1\n3 rename 4 7 1 1 invoked b\n4 format 4 7 4 yes\n" +
		"5 code-actions 4 7 1 1 1 1 invoked - -\n6 code-action-resolve 4 7 1\nR")
}

func TestEditsFollowTheAgreedFeatures(t *testing.T) {
	plain := workspace(group("#4", "7", editRec("1 1 1 1", "y")))
	hashed := workspace(group("/p/a.go", fmt.Sprintf("fnv1a64:%016x", Fnv1a64([]byte("x"))), editRec("1 1 1 1", "y")))
	move := "1\nrename 7 7 -\n/p/a.go/p/b.golabel 4\nMove"
	rec := func(title, edit string) string {
		return fmt.Sprintf("quickfix %d %d 0\n%s%s", len(title), len(edit), title, edit)
	}
	propose := "unknown: propose keeps 12g's grammar, which has no file operations"
	format := "error 4 unknown a format answer keeps 12g's grammar, which has no label"

	// without workspace-edits: what needs it is refused, an action with such an edit left out
	p := &txProbe{got: make(chan string, 8)}
	v1 := []string{"actions", "rename", "format", FeatureCodeActions}
	s := startHost(t, v1, p)
	s.expect("hello 1 " + strings.Join(v1, " "))
	s.send("0 features " + strings.Join(v1, " ") + "\n0 ready -\n")
	for _, want := range []string{
		"unknown: the edit's fnv1a64 precondition needs the workspace-edits feature, which was not agreed",
		propose,
		`"" unknown: file operations need the workspace-edits and file-operations features`,
	} {
		if got := p.next(t); got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	}
	txSession(s)
	s.answer(2, list(actionRec("quickfix", "Plain", plain, "")))
	s.expect("error 3 unknown file operations need the workspace-edits and file-operations features")
	s.expect(format)
	s.answer(5, list(rec("Plain", plain)))
	s.expect("error 6 unknown the edit's fnv1a64 precondition needs the workspace-edits feature, which was not agreed")
	s.send("0 bye\n")
	<-s.done

	// with workspace-edits and file-operations: transactions, but format answers and proposals
	// keep 12g's grammar
	p = &txProbe{got: make(chan string, 8)}
	ws := append([]string{FeatureWorkspaceEdits, FeatureFileOperations}, v1...)
	s = startHost(t, ws, p)
	s.expect("hello 1 " + strings.Join(ws, " "))
	s.send("0 features " + strings.Join(ws, " ") + "\n0 ready -\n")
	s.expect(fmt.Sprintf("1 apply %d", len(hashed)))
	if b := s.body(len(hashed)); b != hashed {
		t.Fatalf("apply body %q", b)
	}
	s.send("ok 1\n")
	for _, want := range []string{"ok", propose, fmt.Sprintf("%q ok", move)} {
		if got := p.next(t); got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	}
	txSession(s)
	s.answer(2, list(actionRec("quickfix", "Plain", plain, ""), actionRec("quickfix", "Hashed", hashed, "")))
	s.answer(3, move)
	s.expect(format)
	s.answer(5, list(rec("Plain", plain), rec("Hashed", hashed), rec("Move", move)))
	s.answer(6, fmt.Sprintf("%d 0\n%s", len(hashed), hashed))
	s.send("0 bye\n")
	<-s.done
}
