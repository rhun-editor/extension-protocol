package rhunext

// Protocol 1.1's editor model: every selection (feature selections),
// scrolling a document to a place and listing locations (feature reveal), diff tabs (feature
// diff) and linked editing ranges (feature linked-editing).

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// The caps of the editor model: selections a document has and select-many sends, ranges of a
// the bytes of a show-locations or diff title, ranges of a linked answer.
const (
	MaxSelections = 512
	MaxTitle      = 200
	MaxLinked     = 64
)

// Selection is a selection: the anchor and the cursor (equal for a caret).
type Selection struct{ Anchor, Cursor Pos }

// SelectMany sets a document's selections, the first becoming primary; overlapping ones merge
// (feature selections). 1 to MaxSelections: other counts are toobig without asking.
func (e *Ext) SelectMany(doc Doc, sels []Selection) error {
	return e.SelectManyContext(context.Background(), doc, sels)
}

// SelectManyContext is SelectMany with cancellation.
func (e *Ext) SelectManyContext(ctx context.Context, doc Doc, sels []Selection) error {
	if !e.Has(FeatureSelections) {
		return &Error{Code: "unknown", Text: "selections was not agreed"}
	}
	body, err := EncodeSelections(sels)
	if err != nil {
		return err
	}
	_, err = e.RequestContext(ctx, "select-many", body, itoa(int64(doc)), itoa(int64(len(sels))), itoa(int64(len(body))))
	return err
}

// EncodeSelections is select-many's body: a line "L1 C1 L2 C2" a selection, anchor then cursor.
func EncodeSelections(sels []Selection) ([]byte, error) {
	if len(sels) < 1 || len(sels) > MaxSelections {
		return nil, &Error{Code: "toobig", Text: fmt.Sprintf("%d selections: 1 to %d", len(sels), MaxSelections)}
	}
	var b bytes.Buffer
	for _, s := range sels {
		fmt.Fprintf(&b, "%s %s\n", pos(s.Anchor), pos(s.Cursor))
	}
	return b.Bytes(), nil
}

// ParseSelections reads a selections event ("0 selections DOC N LEN" and N lines L1 C1 L2 C2):
// every selection of the document, the primary first.
func ParseSelections(ev Event) (Doc, []Selection, bool) {
	if ev.Name != "selections" || len(ev.Args) != 3 {
		return 0, nil, false
	}
	d, err1 := strconv.ParseInt(ev.Args[0], 10, 64)
	n, err2 := strconv.Atoi(ev.Args[1])
	if err1 != nil || err2 != nil || n < 1 || n > MaxSelections {
		return 0, nil, false
	}
	lines := strings.Split(strings.TrimSuffix(string(ev.Body), "\n"), "\n")
	if len(lines) != n {
		return 0, nil, false
	}
	sels := make([]Selection, 0, n)
	for _, l := range lines {
		f := strings.Split(l, " ")
		if len(f) != 4 {
			return 0, nil, false
		}
		a, ok1 := parsePos(f[0], f[1])
		c, ok2 := parsePos(f[2], f[3])
		if !ok1 || !ok2 {
			return 0, nil, false
		}
		sels = append(sels, Selection{a, c})
	}
	return Doc(d), sels, true
}

// RevealHow is where reveal puts the position in the view.
type RevealHow string

const (
	RevealTop             RevealHow = "top"
	RevealCenter          RevealHow = "center"
	RevealNearest         RevealHow = "nearest" // as little scrolling as needed
	RevealCenterIfOutside RevealHow = "center-if-outside"
)

// Reveal scrolls a document's view so the position shows, without moving the cursor or
// activating its tab (feature reveal).
func (e *Ext) Reveal(doc Doc, at Pos, how RevealHow) error {
	if !e.Has(FeatureReveal) {
		return &Error{Code: "unknown", Text: "reveal was not agreed"}
	}
	_, err := e.Request("reveal", nil, itoa(int64(doc)), pos(at), string(how))
	return err
}

// uriField checks a URI given where rhun takes a document (feature uris): one field, without
// spaces or line breaks (write a space as %20).
func uriField(uri string) error {
	if uri == "" || len(uri) > 4128 || strings.ContainsAny(uri, " \n\r\x00") {
		return &Error{Code: "syntax", Text: "a URI field is 1 to 4128 bytes without spaces or line breaks"}
	}
	return nil
}

// RevealURI is Reveal for the open document with that URI (features reveal and uris): a virtual
// document's URI, or a file: URI of an open file.
func (e *Ext) RevealURI(uri string, at Pos, how RevealHow) error {
	if !e.Has(FeatureReveal) {
		return &Error{Code: "unknown", Text: "reveal was not agreed"}
	}
	if err := uriField(uri); err != nil {
		return err
	}
	_, err := e.Request("reveal", nil, uri, pos(at), string(how))
	return err
}

// title is a one-line title of at most MaxTitle bytes, cut at a character.
func title(s string) string { return cut(oneLine(s), MaxTitle) }

// EncodeShowLocations is show-locations' body: the title line, then the locations (12d).
func EncodeShowLocations(heading string, locs []Location) []byte {
	return append([]byte(title(heading)+"\n"), EncodeLocations(locs)...)
}

// ShowLocations lists locations in the palette under a title, as Find All References does
// (feature reveal); busy while a prompt or the palette is open.
func (e *Ext) ShowLocations(heading string, locs []Location) error {
	if !e.Has(FeatureReveal) {
		return &Error{Code: "unknown", Text: "reveal was not agreed"}
	}
	body := EncodeShowLocations(heading, locs)
	_, err := e.Request("show-locations", body, itoa(int64(len(body))))
	return err
}

// DiffFlags say whether a diff tab is activated when it opens.
type DiffFlags string

const (
	DiffShow       DiffFlags = "show"
	DiffBackground DiffFlags = "background"
)

// Diff opens (or reuses, for the same pair and title) a read-only tab with a unified diff from
// document left to document right (feature diff) and returns the tab's id, which is not a
// document.
func (e *Ext) Diff(left, right Doc, heading string, flags DiffFlags) (Doc, error) {
	return e.diff(itoa(int64(left)), itoa(int64(right)), heading, flags)
}

// DiffURIs is Diff with sides named by URI (features diff and uris): a virtual document's URI
// (opened without a tab when it is not open, and closed with the last diff tab showing it unless
// the user opened it meanwhile), a file: URI of an open file, or a document id in decimal.
func (e *Ext) DiffURIs(left, right, heading string, flags DiffFlags) (Doc, error) {
	if !e.Has(FeatureDiff) {
		return 0, &Error{Code: "unknown", Text: "diff was not agreed"}
	}
	for _, u := range []string{left, right} {
		if err := uriField(u); err != nil {
			return 0, err
		}
	}
	return e.diff(left, right, heading, flags)
}

func (e *Ext) diff(left, right, heading string, flags DiffFlags) (Doc, error) {
	if !e.Has(FeatureDiff) {
		return 0, &Error{Code: "unknown", Text: "diff was not agreed"}
	}
	t := title(heading)
	r, err := e.Request("diff", []byte(t), left, right, string(flags), itoa(int64(len(t))))
	if err != nil {
		return 0, err
	}
	if len(r.Args) != 1 {
		return 0, fmt.Errorf("rhun: invalid diff reply")
	}
	id, err := strconv.ParseInt(r.Args[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("rhun: invalid diff reply")
	}
	return Doc(id), nil
}

// DiffClose closes one of this extension's diff tabs (feature diff).
func (e *Ext) DiffClose(tab Doc) error {
	if !e.Has(FeatureDiff) {
		return &Error{Code: "unknown", Text: "diff was not agreed"}
	}
	_, err := e.Request("diff-close", nil, itoa(int64(tab)))
	return err
}

// LinkedRequest asks for the ranges that change together with the word at the cursor
// ("ID linked DOC VERSION LINE COL").
type LinkedRequest struct {
	Doc     Doc
	Version int64
	At      Pos
}

func parseLinkedRequest(f []string) (LinkedRequest, bool) {
	if len(f) != 4 {
		return LinkedRequest{}, false
	}
	d, v, ok := parseDocVersion(f)
	p, ok2 := parsePos(f[2], f[3])
	if !ok || !ok2 || p.Line < 1 || p.Col < 1 {
		return LinkedRequest{}, false
	}
	return LinkedRequest{Doc: d, Version: v, At: p}, true
}

// Linked answers a linked request: Ranges change together (2 to MaxLinked, at the request's
// version, with equal text, not overlapping, one holding the cursor; none for nothing), Word the
// ASCII characters that may be typed in them besides letters, digits and non-ASCII (nil: rhun's
// default "-_.:").
type Linked struct {
	Ranges []Range
	Word   *string
}

// Encode is the body rhun reads. A count rhun would refuse (one range, or more than MaxLinked)
// is sent as none; the word keeps printable ASCII characters, each once.
func (l Linked) Encode() []byte {
	if len(l.Ranges) < 2 || len(l.Ranges) > MaxLinked {
		return []byte("0\n")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%d\n", len(l.Ranges))
	for _, r := range l.Ranges {
		fmt.Fprintf(&b, "%s %s\n", pos(r.Start), pos(r.End))
	}
	if l.Word != nil {
		var kept []byte
		for i := 0; i < len(*l.Word); i++ {
			c := (*l.Word)[i]
			if c >= ' ' && c <= '~' && bytes.IndexByte(kept, c) < 0 {
				kept = append(kept, c)
			}
		}
		fmt.Fprintf(&b, "word %d\n%s", len(kept), kept)
	}
	return b.Bytes()
}

// LinkedEditor answers linked requests (feature linked-editing, [provides] linked).
type LinkedEditor interface {
	Linked(ext *Ext, req LinkedRequest) Linked
}
