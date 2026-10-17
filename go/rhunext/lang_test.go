package rhunext

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The records of sections 12d to 12h, written from the grammar: lengths come from the strings.

func locRec(rng, path, text string) string {
	return fmt.Sprintf("%s %d %d\n%s%s", rng, len(path), len(text), path, text)
}

func symRec(rng, kind string, depth int, name, detail string) string {
	return fmt.Sprintf("%s %s %d %d %d\n%s%s", rng, kind, depth, len(name), len(detail), name, detail)
}

func editRec(rng, text string) string { return fmt.Sprintf("%s %d\n%s", rng, len(text), text) }

func group(path, version string, edits ...string) string {
	return fmt.Sprintf("%d %s %d\n%s", len(path), version, len(edits), path) + strings.Join(edits, "")
}

func workspace(groups ...string) string {
	return fmt.Sprintf("%d\n", len(groups)) + strings.Join(groups, "")
}

func actionRec(kind, title, edit, data string) string {
	return fmt.Sprintf("%s %d %d %d\n%s%s%s", kind, len(title), len(edit), len(data), title, edit, data)
}

func list(recs ...string) string { return fmt.Sprintf("%d\n", len(recs)) + strings.Join(recs, "") }

func rg(l1, c1, l2, c2 int) Range { return Range{Pos{l1, c1}, Pos{l2, c2}} }

func TestLanguageRequestsParse(t *testing.T) {
	f := func(s string) []string { return strings.Split(s, " ") }
	if r, ok := parsePosRequest(f("3 7 10 5 invoked -")); !ok || r != (PosRequest{Doc: 3, Version: 7, At: Pos{10, 5}}) {
		t.Fatalf("pos: %+v %v", r, ok)
	}
	for _, bad := range []string{"3 7 10", "3 x 10 5 invoked -", "3 7 10 y invoked -"} {
		if _, ok := parsePosRequest(f(bad)); ok {
			t.Fatalf("pos %q parsed", bad)
		}
	}
	if r, ok := parseActionsRequest(f("3 7 1 2 3 4")); !ok || r != (ActionsRequest{Doc: 3, Version: 7, Range: rg(1, 2, 3, 4)}) {
		t.Fatalf("actions: %+v %v", r, ok)
	}
	if _, ok := parseActionsRequest(f("3 7 1 2 3")); ok {
		t.Fatal("actions without the range's end parsed")
	}
	want := RenameRequest{Doc: 3, Version: 7, At: Pos{1, 2}, Name: "new_name"}
	if r, ok := parseRenameRequest(f("3 7 1 2 invoked new_name")); !ok || r != want {
		t.Fatalf("rename: %+v %v", r, ok)
	}
	for _, bad := range []string{"3 7 1 2 invoked", "3 7 1 2 invoked ", "3 7 1 x invoked n"} {
		if _, ok := parseRenameRequest(f(bad)); ok {
			t.Fatalf("rename %q parsed", bad)
		}
	}
	if r, ok := parseFormatRequest(f("3 7 4 yes")); !ok || r != (FormatRequest{Doc: 3, Version: 7, TabWidth: 4, Spaces: true}) {
		t.Fatalf("format: %+v %v", r, ok)
	}
	if r, ok := parseFormatRequest(f("3 7 8 no")); !ok || r.Spaces || r.TabWidth != 8 {
		t.Fatalf("format no: %+v %v", r, ok)
	}
	for _, bad := range []string{"3 7 4 maybe", "3 7 -1 yes", "3 7 4", "3 7 x yes"} {
		if _, ok := parseFormatRequest(f(bad)); ok {
			t.Fatalf("format %q parsed", bad)
		}
	}
}

func TestLocationsAreFramedAsRhunReadsThem(t *testing.T) {
	got := string(EncodeLocations([]Location{
		{Target: Target{Path: "/a/b.rs"}, Range: rg(1, 4, 1, 8), Text: "  fn main() {\n"},
		{Target: Target{Doc: 7}, Range: rg(2, 1, 2, 1)},
		{Target: Target{Path: "/a/c d.rs"}, Range: rg(3, 1, 3, 2), Text: "a\nb\r"},
	}))
	want := list(locRec("1 4 1 8", "/a/b.rs", "fn main() {"), locRec("2 1 2 1", "#7", ""), locRec("3 1 3 2", "/a/c d.rs", "a b"))
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if got := string(EncodeLocations(nil)); got != "0\n" {
		t.Fatalf("none: %q", got)
	}
	many := make([]Location, MaxRecords+1)
	if got := EncodeLocations(many); !strings.HasPrefix(string(got), "10000\n") {
		t.Fatalf("10001 locations: %q", got[:10])
	}
	// records of 5018 bytes (a path of 4000, a preview of 1000): 208 fit in 1 MB, the rest is left out
	big := make([]Location, 300)
	for i := range big {
		big[i] = Location{Target: Target{Path: "/" + strings.Repeat("p", 3999)}, Range: rg(1, 1, 1, 1), Text: strings.Repeat("t", 1000)}
	}
	if got := EncodeLocations(big); !strings.HasPrefix(string(got), "208\n") || len(got) != 4+208*5018 {
		t.Fatalf("over 1 MB: %q, %d bytes", got[:4], len(got))
	}
}

// TestFieldsRhunWouldRefuseAreCutOrLeftOut: rhun drops a whole answer for one name, title or
// detail over 1000 bytes, kind over 40 or path over 4096, so the encoders cut or leave them out.
func TestFieldsRhunWouldRefuseAreCutOrLeftOut(t *testing.T) {
	x999, y := strings.Repeat("x", 999), strings.Repeat("y", 10)
	straddle := x999 + "é" + y // é is bytes 999 and 1000: cut before it
	exact := x999 + "z"        // 1000 bytes: kept whole
	line := "a\nb" + strings.Repeat("c", 1000)
	path4096 := "/" + strings.Repeat("p", 4095)

	// numbers are not negative and have at most 10 digits
	huge, hugeWord := math.MaxInt, "4294967295"
	if strconv.IntSize == 32 {
		hugeWord = strconv.Itoa(huge)
	}
	got := string(EncodeLocations([]Location{
		{Target: Target{Path: path4096 + "q"}, Range: rg(1, 1, 1, 1), Text: "left out"},
		{Target: Target{Path: path4096}, Range: rg(1, 1, 1, 2), Text: "  " + straddle + "  "},
		{Target: Target{Doc: 2}, Range: rg(-1, 0, huge, 3), Text: line},
	}))
	want := list(locRec("1 1 1 2", path4096, x999), locRec("0 0 "+hugeWord+" 3", "#2", "a b"+strings.Repeat("c", 997)))
	if got != want {
		t.Fatalf("locations:\ngot  %.120q\nwant %.120q", got, want)
	}

	got = string(EncodeSymbols([]Symbol{
		{Range: rg(1, 1, 1, 1), Kind: SymbolKind(strings.Repeat("k", 39) + "é"), Depth: 3, Name: straddle, Detail: exact + "!"},
		{Range: rg(2, 1, 2, 1), Name: "\n"},            // a space: a name
		{Range: rg(3, 1, 3, 1), Name: ""},              // left out
		{Range: rg(4, 1, 4, 1), Name: "v", Kind: "\t"}, // no kind left: function
	}))
	want = list(symRec("1 1 1 1", strings.Repeat("k", 39), 3, x999, exact),
		symRec("2 1 2 1", "function", 0, " ", ""), symRec("4 1 4 1", "function", 0, "v", ""))
	if got != want {
		t.Fatalf("symbols:\ngot  %.200q\nwant %.200q", got, want)
	}
	if got := string(EncodeSymbols([]Symbol{{Name: ""}})); got != "0\n" {
		t.Fatalf("a symbol without a name: %q", got)
	}

	var e WorkspaceEdit
	e.AddDoc(1, 2, TextEdit{rg(1, 1, 1, 1), "x"})
	got = string(EncodeActions([]CodeAction{
		{Kind: ActionKind(strings.Repeat("k", 50)), Title: straddle, Data: []byte("d")},
		{Kind: ActionSource, Title: "", Edit: &e},
		{Kind: "a\x01b c", Title: exact},
	}))
	want = list(actionRec(strings.Repeat("k", 40), x999, "", "d"), actionRec("abc", exact, "", ""))
	if got != want {
		t.Fatalf("actions:\ngot  %.120q\nwant %.120q", got, want)
	}

	var far WorkspaceEdit
	far.AddDoc(1, 2, TextEdit{rg(1, 1, 1, 1), "x"})
	far.AddFile(path4096+"q", OnDisk)
	if _, err := far.Encode(); err == nil || err.Error() != "a path over 4096 bytes" {
		t.Fatalf("a path over 4096 bytes: %v", err)
	}
	var near WorkspaceEdit
	near.AddFile(path4096, OnDisk, TextEdit{rg(1, 1, 1, 1), "x"})
	if got, err := near.Encode(); err != nil || string(got) != workspace(group(path4096, "-", editRec("1 1 1 1", "x"))) {
		t.Fatalf("a path of 4096 bytes: %v", err)
	}
	// an action whose edit names such a path is left out, as one whose edit is too big
	if got := string(EncodeActions([]CodeAction{{Title: "far", Edit: &far}})); got != "0\n" {
		t.Fatalf("an action with a far path: %q", got)
	}
}

func TestSymbolsAreFramedAsRhunReadsThem(t *testing.T) {
	got := string(EncodeSymbols([]Symbol{
		{Range: rg(1, 1, 3, 2), Kind: SymbolStruct, Name: "Point"},
		{Range: rg(2, 5, 2, 11), Kind: SymbolField, Depth: 1, Name: "x", Detail: "i32"},
		{Range: rg(4, 1, 4, 9), Name: "main\n", Depth: -2},
		{Range: rg(5, 1, 5, 2), Kind: "my kind", Name: "k", Detail: "a\nb"},
	}))
	want := list(
		symRec("1 1 3 2", "struct", 0, "Point", ""),
		symRec("2 5 2 11", "field", 1, "x", "i32"),
		symRec("4 1 4 9", "function", 0, "main ", ""),
		symRec("5 1 5 2", "mykind", 0, "k", "a b"),
	)
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	many := make([]Symbol, MaxRecords+1)
	for i := range many {
		many[i].Name = "v"
	}
	if got := EncodeSymbols(many); !strings.HasPrefix(string(got), "10000\n") {
		t.Fatalf("10001 symbols: %q", got[:10])
	}
}

func TestSymbolKindsAreTheSpecsWords(t *testing.T) {
	kinds := []SymbolKind{SymbolFile, SymbolModule, SymbolNamespace, SymbolPackage, SymbolClass, SymbolMethod,
		SymbolProperty, SymbolField, SymbolConstructor, SymbolEnum, SymbolInterface, SymbolFunction, SymbolVariable,
		SymbolConstant, SymbolString, SymbolNumber, SymbolBoolean, SymbolArray, SymbolObject, SymbolKey, SymbolNull,
		SymbolEnumMember, SymbolStruct, SymbolEvent, SymbolOperator, SymbolTypeParameter}
	var words []string
	for _, k := range kinds {
		words = append(words, string(k))
	}
	spec := "file module namespace package class method property field constructor enum interface function " +
		"variable constant string number boolean array object key null enum_member struct event operator type_parameter"
	if got := strings.Join(words, " "); got != spec {
		t.Fatalf("got  %s\nwant %s", got, spec)
	}
}

func TestWorkspaceEditsAreFramedAndChecked(t *testing.T) {
	var w WorkspaceEdit
	w.AddDoc(3, 9, TextEdit{rg(1, 1, 1, 4), "new"})
	w.AddFile("/p/x.rs", OnDisk, TextEdit{rg(2, 1, 2, 1), "a\n"}, TextEdit{rg(5, 1, 5, 3), ""})
	w.AddFile("/p/y z.rs", 0, TextEdit{rg(1, 1, 1, 1), "é"})
	got, err := w.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := workspace(
		group("#3", "9", editRec("1 1 1 4", "new")),
		group("/p/x.rs", "-", editRec("2 1 2 1", "a\n"), editRec("5 1 5 3", "")),
		group("/p/y z.rs", "0", editRec("1 1 1 1", "é")),
	)
	if string(got) != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if w.Empty() {
		t.Fatal("an edit with edits is empty")
	}
	if got, err := (WorkspaceEdit{}).Encode(); err != nil || string(got) != "0\n" {
		t.Fatalf("empty: %q %v", got, err)
	}
	var none WorkspaceEdit
	none.AddDoc(1, 1)
	if !(WorkspaceEdit{}).Empty() || !none.Empty() {
		t.Fatal("an edit without edits is not empty")
	}
	files := WorkspaceEdit{Files: make([]FileEdit, MaxEditFiles+1)}
	if _, err := files.Encode(); err == nil || err.Error() != "1001 files: at most 1000" {
		t.Fatalf("files: %v", err)
	}
	var edits WorkspaceEdit
	edits.AddDoc(1, 1, make([]TextEdit, MaxEdits+1)...)
	if _, err := edits.Encode(); err == nil || err.Error() != "100001 edits: at most 100000" {
		t.Fatalf("edits: %v", err)
	}
	var big WorkspaceEdit
	big.AddDoc(1, 1, TextEdit{Text: strings.Repeat("x", MaxEditBody)})
	if _, err := big.Encode(); err == nil || err.Error() != "over 4194304 bytes" {
		t.Fatalf("bytes: %v", err)
	}
}

func TestActionsCarryTheirEditAndData(t *testing.T) {
	var w WorkspaceEdit
	w.AddDoc(1, 2, TextEdit{rg(1, 1, 1, 1), "x"})
	tooMany := WorkspaceEdit{Files: make([]FileEdit, MaxEditFiles+1)}
	got := string(EncodeActions([]CodeAction{
		{Kind: ActionQuickFix, Title: "Add x", Edit: &w},
		{Kind: ActionSource, Title: "Organize imports", Data: []byte("organize")},
		{Title: "Left out", Edit: &tooMany},
		{Kind: " ", Title: "Two\nlines", Data: []byte("a b\nc")},
		{Kind: ActionRefactor, Title: "Empty edit", Edit: &WorkspaceEdit{}},
	}))
	edit := workspace(group("#1", "2", editRec("1 1 1 1", "x")))
	want := list(
		actionRec("quickfix", "Add x", edit, ""),
		actionRec("source", "Organize imports", "", "organize"),
		actionRec("quickfix", "Two lines", "", "a b\nc"),
		actionRec("refactor", "Empty edit", "0\n", ""),
	)
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	many := make([]CodeAction, MaxActions+1)
	for i := range many {
		many[i].Title = "t"
	}
	if got := EncodeActions(many); !strings.HasPrefix(string(got), "1000\n") {
		t.Fatalf("1001 actions: %q", got[:10])
	}
	// 1 MB of data each: three fit in 4 MB, the rest is left out
	big := make([]CodeAction, 5)
	for i := range big {
		big[i] = CodeAction{Title: "t", Data: make([]byte, 1<<20)}
	}
	if got := EncodeActions(big); !strings.HasPrefix(string(got), "3\n") || len(got) > MaxActionsBody {
		t.Fatalf("over 4 MB: %q, %d bytes", got[:3], len(got))
	}
}

func TestErrorTextIsOneShortLine(t *testing.T) {
	if got := errorLine("6", &Error{Code: "refused", Text: "not a name\nhere\n"}); got != "error 6 refused not a name here\n" {
		t.Fatalf("%q", got)
	}
	if got := errorLine("6", &Error{Code: "refused"}); got != "error 6 refused\n" {
		t.Fatalf("%q", got)
	}
	long := errorLine("6", &Error{Code: "refused", Text: strings.Repeat("a", maxErrorText-1) + "é"})
	if want := "error 6 refused " + strings.Repeat("a", maxErrorText-1) + "\n"; long != want {
		t.Fatalf("cut: %d bytes", len(long))
	}
}

// host is rhun's side of a session in a test.
type host struct {
	t    *testing.T
	br   *bufio.Reader
	w    *io.PipeWriter
	done chan error
}

func startHost(t *testing.T, features []string, h Handler) *host {
	toExt, hostW := io.Pipe()
	hostR, fromExt := io.Pipe()
	s := &host{t: t, br: bufio.NewReader(hostR), w: hostW, done: make(chan error, 1)}
	go func() { s.done <- RunOnWith(toExt, fromExt, io.Discard, features, h) }()
	return s
}

func (s *host) line() string {
	s.t.Helper()
	got := make(chan string, 1)
	go func() {
		l, _ := s.br.ReadString('\n')
		got <- l
	}()
	select {
	case l := <-got:
		if !strings.HasSuffix(l, "\n") {
			s.t.Fatalf("no line: %q", l)
		}
		return strings.TrimSuffix(l, "\n")
	case <-time.After(3 * time.Second):
		s.t.Fatal("no answer")
		return ""
	}
}

func (s *host) send(str string) { io.WriteString(s.w, str) }

// answer reads "ok ID LEN" and the body, and checks both.
func (s *host) answer(id int, want string) {
	s.t.Helper()
	if got := s.line(); got != fmt.Sprintf("ok %d %d", id, len(want)) {
		s.t.Fatalf("reply %d: %q, want the body %q", id, got, want)
	}
	b := make([]byte, len(want))
	if _, err := io.ReadFull(s.br, b); err != nil {
		s.t.Fatal(err)
	}
	if string(b) != want {
		s.t.Fatalf("body %d:\ngot  %q\nwant %q", id, b, want)
	}
}

func (s *host) expect(want string) {
	s.t.Helper()
	if got := s.line(); got != want {
		s.t.Fatalf("got %q, want %q", got, want)
	}
}

type langProbe struct {
	data    chan string
	applied chan error
}

func (l *langProbe) Ready(ext *Ext, project string) {}
func (l *langProbe) Event(ext *Ext, ev Event)       {}

// Run applies an edit: lang.fix one for the document and a file on disk, lang.huge one over the
// limit of files.
func (l *langProbe) Run(ext *Ext, command string, doc Doc) {
	var w WorkspaceEdit
	switch command {
	case "lang.fix":
		w.AddDoc(doc, 9, TextEdit{rg(1, 1, 1, 1), "a\n"})
		w.AddFile("/p/b.go", OnDisk, TextEdit{rg(2, 1, 2, 3), ""})
	case "lang.huge":
		w.Files = make([]FileEdit, MaxEditFiles+1)
	}
	l.applied <- ext.Apply(w)
}

func (l *langProbe) appliedErr(t *testing.T) error {
	t.Helper()
	select {
	case err := <-l.applied:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Apply did not return")
		return nil
	}
}
func (l *langProbe) Definition(ext *Ext, req PosRequest) []Location {
	return []Location{{Target: Target{Doc: req.Doc}, Range: Range{req.At, req.At}, Text: "x"}}
}
func (l *langProbe) References(ext *Ext, req PosRequest) []Location {
	return []Location{
		{Target: Target{Doc: req.Doc}, Range: rg(req.At.Line, 1, req.At.Line, 4), Text: "\tfoo()\n"},
		{Target: Target{Path: "/p/a b.go"}, Range: rg(9, 2, 9, 5), Text: "foo(1)"},
	}
}
func (l *langProbe) Symbols(ext *Ext, req PosRequest) []Symbol {
	return []Symbol{{Name: "main"}, {Range: rg(2, 1, 2, 5), Kind: SymbolVariable, Depth: 1, Name: "x", Detail: "int"}}
}
func (l *langProbe) Actions(ext *Ext, req ActionsRequest) []CodeAction {
	var w WorkspaceEdit
	w.AddDoc(req.Doc, req.Version, TextEdit{req.Range, "y"})
	return []CodeAction{{Kind: ActionSource, Title: "Go", Data: []byte("a b\nc")}, {Title: "Fix", Edit: &w}}
}
func (l *langProbe) Action(ext *Ext, data []byte) { l.data <- string(data) }
func (l *langProbe) Rename(ext *Ext, req RenameRequest) (WorkspaceEdit, error) {
	switch req.Name {
	case "bad":
		return WorkspaceEdit{}, errors.New("not a name\nhere")
	case "huge":
		return WorkspaceEdit{Files: make([]FileEdit, MaxEditFiles+1)}, nil
	}
	var w WorkspaceEdit
	w.AddDoc(req.Doc, req.Version, TextEdit{Range{req.At, req.At}, req.Name})
	w.AddFile("/p/other.go", OnDisk, TextEdit{rg(3, 1, 3, 4), req.Name})
	return w, nil
}
func (l *langProbe) Format(ext *Ext, req FormatRequest) WorkspaceEdit {
	var w WorkspaceEdit
	if req.Spaces {
		w.AddDoc(req.Doc, req.Version, TextEdit{rg(2, 1, 2, 2), strings.Repeat(" ", req.TabWidth)})
	}
	return w
}

func TestLanguageRequestsAndTheActionEvent(t *testing.T) {
	feats := []string{FeatureDefinition, FeatureReferences, FeatureSymbols, FeatureActions, FeatureRename, FeatureFormat}
	p := &langProbe{data: make(chan string, 2), applied: make(chan error, 2)}
	s := startHost(t, feats, p)
	s.expect("hello 1 definition references symbols actions rename format")
	s.send("0 features definition references symbols actions rename format\n0 ready -\n")

	s.send("1 definition 4 9 2 3 invoked -\n")
	s.answer(1, list(locRec("2 3 2 3", "#4", "x")))
	s.send("2 references 4 9 2 3 invoked -\n")
	s.answer(2, list(locRec("2 1 2 4", "#4", "foo()"), locRec("9 2 9 5", "/p/a b.go", "foo(1)")))
	s.send("3 symbols 4 9 2 3 invoked -\n")
	s.answer(3, list(symRec("0 0 0 0", "function", 0, "main", ""), symRec("2 1 2 5", "variable", 1, "x", "int")))
	s.send("4 actions 4 9 1 1 1 5\n")
	s.answer(4, list(actionRec("source", "Go", "", "a b\nc"),
		actionRec("quickfix", "Fix", workspace(group("#4", "9", editRec("1 1 1 5", "y"))), "")))
	// the action event's body, then a request straight after it
	data := "a b\nc"
	s.send(fmt.Sprintf("0 action %d\n%s5 rename 4 9 2 3 invoked y\n", len(data), data))
	select {
	case got := <-p.data:
		if got != data {
			t.Fatalf("action data %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no action")
	}
	s.answer(5, workspace(group("#4", "9", editRec("2 3 2 3", "y")), group("/p/other.go", "-", editRec("3 1 3 4", "y"))))
	s.send("6 rename 4 9 2 3 invoked bad\n")
	s.expect("error 6 refused not a name here")
	s.send("7 rename 4 9 2 3 invoked huge\n")
	s.expect("error 7 toobig 1001 files: at most 1000")
	s.send("8 format 4 9 4 no\n")
	s.answer(8, "0\n")
	s.send("9 format 4 9 2 yes\n")
	s.answer(9, workspace(group("#4", "9", editRec("2 1 2 2", "  "))))
	// the extension's apply request: the edit as its body, then rhun's ok or error
	fix := workspace(group("#4", "9", editRec("1 1 1 1", "a\n")), group("/p/b.go", "-", editRec("2 1 2 3", "")))
	for id, reply := range []string{"ok 1", "error 2 stale version 10 is current"} {
		s.send("0 run lang.fix 4\n")
		s.expect(fmt.Sprintf("%d apply %d", id+1, len(fix)))
		b := make([]byte, len(fix))
		if _, err := io.ReadFull(s.br, b); err != nil || string(b) != fix {
			t.Fatalf("apply body:\ngot  %q\nwant %q", b, fix)
		}
		s.send(reply + "\n")
		err := p.appliedErr(t)
		var re *Error
		switch {
		case id == 0 && err != nil:
			t.Fatalf("apply: %v", err)
		case id == 1 && (!errors.As(err, &re) || re.Code != "stale" || re.Text != "version 10 is current"):
			t.Fatalf("apply refused: %#v", err)
		}
	}
	// an edit over the limits is not sent: the next line is the answer to the next request
	s.send("0 run lang.huge 4\n")
	if err := p.appliedErr(t); err == nil || err.Error() != "1001 files: at most 1000" {
		t.Fatalf("huge apply: %v", err)
	}
	// malformed requests and an unknown verb
	for i, bad := range []string{"rename 4 9 2 3 invoked", "format 4 9 4 maybe", "definition 4 x 2 3 invoked -",
		"actions 4 9 1 1", "frobnicate 4 9"} {
		id := 10 + i
		s.send(fmt.Sprintf("%d %s\n", id, bad))
		s.expect(fmt.Sprintf("error %d unknown no such request", id))
	}
	s.send("0 bye\n")
	if err := <-s.done; err != nil {
		t.Fatal(err)
	}
}

// plain answers none of the language requests.
type plain struct{ events chan Event }

func (p *plain) Ready(ext *Ext, project string)        {}
func (p *plain) Run(ext *Ext, command string, doc Doc) {}
func (p *plain) Event(ext *Ext, ev Event)              { p.events <- ev }

func TestWithoutTheInterfacesRequestsAreUnknown(t *testing.T) {
	p := &plain{events: make(chan Event, 8)}
	s := startHost(t, nil, p)
	s.expect("hello 1")
	s.send("0 ready -\n")
	for i, req := range []string{"definition 4 9 2 3 invoked -", "references 4 9 2 3 invoked -",
		"symbols 4 9 2 3 invoked -", "actions 4 9 1 1 1 1", "rename 4 9 2 3 invoked y", "format 4 9 4 yes"} {
		s.send(fmt.Sprintf("%d %s\n", i+1, req))
		s.expect(fmt.Sprintf("error %d unknown no such request", i+1))
	}
	// an action event reaches Event with its body, and what follows it is read as usual
	s.send("0 action 3\nx\ny0 saved 4 10\n0 bye\n")
	next := func() Event {
		select {
		case ev := <-p.events:
			return ev
		case <-time.After(3 * time.Second):
			t.Fatal("no event")
			return Event{}
		}
	}
	if ev := next(); ev.Name != "action" || len(ev.Args) != 1 || ev.Args[0] != "3" || string(ev.Body) != "x\ny" {
		t.Fatalf("action: %+v", ev)
	}
	if ev := next(); ev.Name != "saved" || strings.Join(ev.Args, " ") != "4 10" {
		t.Fatalf("saved: %+v", ev)
	}
	if err := <-s.done; err != nil {
		t.Fatal(err)
	}
}

func TestABadActionLengthEndsTheSession(t *testing.T) {
	s := startHost(t, nil, &plain{events: make(chan Event, 8)})
	s.expect("hello 1")
	s.send("0 action -1\n")
	select {
	case err := <-s.done:
		if err == nil || !strings.Contains(err.Error(), "an action length") {
			t.Fatalf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the session went on")
	}
}

// waiter asks for a toast when ready: the request waits while the bad length arrives
type waiter struct {
	plain
	got chan error
}

func (w *waiter) Ready(ext *Ext, project string) { w.got <- ext.Toast("hi") }

// TestEventLengthsAreCheckedBeforeAllocating: an edits or action LEN over 4 MB, or one that is not
// a plain decimal of at most 10 digits, ends the session as end of input does (the request waiting
// fails) without reading or allocating its body.
func TestEventLengthsAreCheckedBeforeAllocating(t *testing.T) {
	for _, bad := range []string{"0 action 4194305", "0 action 99999999999", "0 action +5", "0 action 5x",
		"0 action ", "0 edits 1 2 4194305", "0 edits 1 2 -0", "0 edits 1 2 1e3"} {
		w := &waiter{plain: plain{events: make(chan Event, 8)}, got: make(chan error, 1)}
		s := startHost(t, nil, w)
		s.expect("hello 1")
		s.send("0 ready -\n")
		s.expect("1 toast 2")
		b := make([]byte, 2)
		if _, err := io.ReadFull(s.br, b); err != nil || string(b) != "hi" {
			t.Fatalf("the toast's body: %q %v", b, err)
		}
		// the body is not read: what follows is never taken for messages
		go s.send(bad + "\nok 1\n0 bye\n")
		select {
		case err := <-w.got:
			if err == nil {
				t.Fatalf("%q: the waiting request succeeded", bad)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%q: the waiting request still waits", bad)
		}
		select {
		case err := <-s.done:
			if err == nil || !strings.Contains(err.Error(), "length") {
				t.Fatalf("%q: the session ended with %v", bad, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%q: the session went on", bad)
		}
	}
	if n, ok := bodyLen("4194304", MaxEventBody); !ok || n != 4<<20 {
		t.Fatal("4 MB is a length")
	}
	if n, ok := bodyLen("0", MaxEventBody); !ok || n != 0 {
		t.Fatal("0 is a length")
	}
}
