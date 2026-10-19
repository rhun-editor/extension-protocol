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
	// a rhun without file-operations gets no body; without workspace-edits a hash is "-" and the
	// label and confirm are left out
	if _, bad := w.encodeWith(editCaps{ws: true}); bad == nil || bad.Code != "unknown" {
		t.Fatalf("ops without the feature: %v", bad)
	}
	var plain WorkspaceEdit
	plain.Label, plain.Confirm = "L", true
	plain.AddFileHashed("/p/a", []byte("x"))
	if b, _ := plain.encodeWith(editCaps{}); string(b) != "1\n4 - 0\n/p/a" {
		t.Fatalf("v1: %q", b)
	}
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
