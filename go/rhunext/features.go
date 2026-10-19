package rhunext

// Section 12 of the protocol: features an extension asks for in hello and rhun agrees to before
// ready. Views are tabs the extension draws in; completion, signature help, go to definition,
// references, symbols, code actions, rename and format (lang.go) are requests rhun sends and the
// extension answers. A handler implements Viewer, Completer, SignatureHelper, Definer,
// ReferenceFinder, SymbolLister, ActionProvider, Renamer or Formatter to receive them; without
// the interface rhun's request is answered "unknown". An ActionProvider also gets the data of
// the code action the user chose (the action event).

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The features of section 12.
const (
	FeatureViews      = "views"
	FeatureComplete   = "complete"
	FeatureSignature  = "signature"
	FeatureDefinition = "definition"
	FeatureReferences = "references"
	FeatureSymbols    = "symbols"
	FeatureActions    = "actions"
	FeatureRename     = "rename"
	FeatureFormat     = "format"
	FeatureCommands   = "commands"
)

// hostRequest is the internal event name of a request from rhun.
const hostRequest = "\x00request"

// Has reports whether rhun agreed to a feature.
func (e *Ext) Has(feature string) bool {
	e.fmu.Lock()
	defer e.fmu.Unlock()
	return e.agreed[feature]
}

// ---- views (12a)

// View is a view's id.
type View int64

// Metric is a text size's character advance and line height, in logical pixels.
type Metric struct{ Advance, LineHeight float64 }

// Metrics are the four text sizes of a view. rhun's interface font is monospaced: a text's width
// is its characters times the advance (a wide character counts twice).
type Metrics struct{ Small, Normal, Large, Mono Metric }

// DefaultMetrics is a guess at scale 1, for drawing before the first metrics event.
var DefaultMetrics = Metrics{Small: Metric{6, 16}, Normal: Metric{7, 18}, Large: Metric{10, 26}, Mono: Metric{7, 19}}

// ViewEvent is what a view tells its extension: Kind is size, metrics, hover, down, drag, up,
// click, leave, wheel or closed.
type ViewEvent struct {
	Kind          string
	X, Y, DY      float64 // the pointer; DY for wheel
	Width, Height float64 // size
	Metrics       Metrics // metrics
}

// Viewer receives its views' events.
type Viewer interface {
	View(ext *Ext, view View, ev ViewEvent)
}

func parseViewEvent(ev Event) (View, ViewEvent, bool) {
	if len(ev.Args) < 2 {
		return 0, ViewEvent{}, false
	}
	id, err := strconv.ParseInt(ev.Args[0], 10, 64)
	if err != nil {
		return 0, ViewEvent{}, false
	}
	f := func(i int) float64 {
		if i < len(ev.Args) {
			v, _ := strconv.ParseFloat(ev.Args[i], 64)
			return v
		}
		return 0
	}
	vev := ViewEvent{Kind: ev.Args[1]}
	switch vev.Kind {
	case "size":
		vev.Width, vev.Height = f(2), f(3)
	case "metrics":
		m := DefaultMetrics
		for i := 2; i+2 < len(ev.Args); i += 3 {
			mt := Metric{f(i + 1), f(i + 2)}
			switch ev.Args[i] {
			case "small":
				m.Small = mt
			case "normal":
				m.Normal = mt
			case "large":
				m.Large = mt
			case "mono":
				m.Mono = mt
			}
		}
		vev.Metrics = m
	case "hover", "down", "drag", "up", "click":
		vev.X, vev.Y = f(2), f(3)
	case "wheel":
		vev.X, vev.Y, vev.DY = f(2), f(3), f(4)
	}
	return View(id), vev, true
}

// OpenView opens a tab the extension draws in and returns its id.
func (e *Ext) OpenView(title string) (View, error) {
	r, err := e.Request("view", nil, oneLine(title))
	if err != nil {
		return 0, err
	}
	return View(argInt(r, 0)), nil
}

// Draw replaces a view's picture.
func (e *Ext) Draw(v View, p *Picture) error {
	body := p.Bytes()
	_, err := e.Request("draw", body, itoa(int64(v)), itoa(int64(len(body))))
	return err
}

// CloseView closes a view's tab.
func (e *Ext) CloseView(v View) error {
	_, err := e.Request("unview", nil, itoa(int64(v)))
	return err
}

// Color is a picture's color: a theme name (ColorFg …), a chart series (Series), or RGB/RGBA.
type Color string

const (
	ColorFg      Color = "fg"
	ColorMuted   Color = "muted"
	ColorBg      Color = "bg"
	ColorPanel   Color = "panel"
	ColorBorder  Color = "border"
	ColorAccent  Color = "accent"
	ColorError   Color = "error"
	ColorWarning Color = "warning"
	ColorSuccess Color = "success"
)

// Series is the n-th chart color, 1 to 8 (wrapping); it follows the theme.
func Series(n int) Color {
	if n < 1 {
		n = 1
	}
	return Color("c" + strconv.Itoa((n-1)%8+1))
}

// RGB is an opaque color.
func RGB(r, g, b uint8) Color { return Color(fmt.Sprintf("#%02x%02x%02x", r, g, b)) }

// RGBA is a color blended over what is below.
func RGBA(r, g, b, a uint8) Color { return Color(fmt.Sprintf("#%02x%02x%02x%02x", r, g, b, a)) }

// Text sizes and alignments of a picture's text.
const (
	Small  = "small"
	Normal = "normal"
	Large  = "large"
	Mono   = "mono"
	Left   = "left"
	Center = "center"
	Right  = "right"
)

// Picture is a view's picture: shapes in drawing order, logical pixels (section 12a).
type Picture struct {
	b bytes.Buffer
	n int
}

// Num writes a number as the protocol does: at most two decimals, in the range rhun reads.
func Num(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		v = 0
	}
	v = math.Max(-9999999, math.Min(9999999, v))
	s := strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	if s == "-0" {
		s = "0"
	}
	return s
}

func (p *Picture) shape(parts ...string) *Picture {
	p.b.WriteString(strings.Join(parts, " "))
	p.b.WriteByte('\n')
	p.n++
	return p
}

func nn(v float64) string { return Num(math.Max(0, v)) }

// Bytes is the picture as rhun reads it.
func (p *Picture) Bytes() []byte { return p.b.Bytes() }

// Len is the number of shapes.
func (p *Picture) Len() int { return p.n }

// Rect is a filled rectangle.
func (p *Picture) Rect(x, y, w, h float64, c Color) *Picture {
	return p.shape("rect", Num(x), Num(y), nn(w), nn(h), string(c))
}

// RRect is a filled rectangle with rounded corners.
func (p *Picture) RRect(x, y, w, h, r float64, c Color) *Picture {
	return p.shape("rrect", Num(x), Num(y), nn(w), nn(h), nn(r), string(c))
}

// Line is a line with round ends.
func (p *Picture) Line(x1, y1, x2, y2, width float64, c Color) *Picture {
	return p.shape("line", Num(x1), Num(y1), Num(x2), Num(y2), nn(width), string(c))
}

func points(pts [][2]float64) []string {
	out := make([]string, 0, 2*len(pts))
	for i, pt := range pts {
		if i == 10000 {
			break
		}
		out = append(out, Num(pt[0]), Num(pt[1]))
	}
	return out
}

// Poly is a polyline with round joins (2 to 10000 points).
func (p *Picture) Poly(width float64, c Color, pts [][2]float64) *Picture {
	if len(pts) < 2 {
		return p
	}
	return p.shape(append([]string{"poly", nn(width), string(c)}, points(pts)...)...)
}

// Fill is a filled polygon (3 to 10000 points), nonzero rule.
func (p *Picture) Fill(c Color, pts [][2]float64) *Picture {
	if len(pts) < 3 {
		return p
	}
	return p.shape(append([]string{"fill", string(c)}, points(pts)...)...)
}

// Circle is a filled circle.
func (p *Picture) Circle(x, y, r float64, c Color) *Picture {
	return p.shape("circle", Num(x), Num(y), nn(r), string(c))
}

// Ring is a circle's outline.
func (p *Picture) Ring(x, y, r, width float64, c Color) *Picture {
	return p.shape("ring", Num(x), Num(y), nn(r), nn(width), string(c))
}

// Pie is a filled wedge from degrees `from` to `to`, 0 at the top, clockwise.
func (p *Picture) Pie(x, y, r, from, to float64, c Color) *Picture {
	return p.shape("pie", Num(x), Num(y), nn(r), Num(from), Num(to), string(c))
}

// Text is a line of text: y is the line's top, align is about x (Left, Center, Right).
func (p *Picture) Text(x, y float64, size, align string, c Color, text string) *Picture {
	return p.shape("text", Num(x), Num(y), size, align, string(c), oneLine(text))
}

// Clip limits what follows to a rectangle, until Unclip or the next Clip.
func (p *Picture) Clip(x, y, w, h float64) *Picture {
	return p.shape("clip", Num(x), Num(y), nn(w), nn(h))
}

// Unclip is the whole view again.
func (p *Picture) Unclip() *Picture { return p.shape("unclip") }

// ---- completion (12b) and signature help (12c)

// Request is what rhun asks about: the document at a version, the cursor, and why: Kind typed,
// trigger (Char is the character), invoked, incomplete or update.
type Request struct {
	Doc     Doc
	Version int64
	At      Pos
	Kind    string
	Char    string // the trigger character, "" otherwise
}

func parseRequest(f []string) (Request, bool) {
	if len(f) < 6 {
		return Request{}, false
	}
	d, e1 := strconv.ParseInt(f[0], 10, 64)
	v, e2 := strconv.ParseInt(f[1], 10, 64)
	p, ok := parsePos(f[2], f[3])
	if e1 != nil || e2 != nil || !ok {
		return Request{}, false
	}
	ch := f[5]
	if f[4] != "trigger" {
		ch = ""
	}
	return Request{Doc: Doc(d), Version: v, At: p, Kind: f[4], Char: ch}, true
}

// CompletionKind is what an item is; rhun shows a badge for it.
type CompletionKind string

const (
	KindText      CompletionKind = "text"
	KindMethod    CompletionKind = "method"
	KindFunction  CompletionKind = "function"
	KindField     CompletionKind = "field"
	KindVariable  CompletionKind = "variable"
	KindClass     CompletionKind = "class"
	KindStruct    CompletionKind = "struct"
	KindInterface CompletionKind = "interface"
	KindModule    CompletionKind = "module"
	KindProperty  CompletionKind = "property"
	KindEnum      CompletionKind = "enum"
	KindKeyword   CompletionKind = "keyword"
	KindSnippet   CompletionKind = "snippet"
	KindConstant  CompletionKind = "constant"
	KindValue     CompletionKind = "value"
	KindFile      CompletionKind = "file"
	KindFolder    CompletionKind = "folder"
)

// TextEdit is one replacement in a text: a completion item's extra edit (an import), in the
// document at the request's version, or one of a FileEdit's edits.
type TextEdit struct {
	Range Range
	Text  string
}

// CompletionItem is one item of a list: Range is what it replaces (usually the word before the
// cursor), Label what the list shows and typing filters on, Insert the text put in (Label when
// empty), Detail shown beside the label, Doc beside the list when the item is selected.
type CompletionItem struct {
	Range  Range
	Kind   CompletionKind
	Label  string
	Insert string
	Detail string
	Doc    string
	Edits  []TextEdit
	// where the cursor goes (WithCursor); without it, after Insert
	cursor          int
	hasCursor       bool
	prepared        bool
	fields          []Field
	choices         []SnippetChoice
	initialVariants []int
	transforms      []SnippetFieldTransform
}

// WithCursor puts the cursor offset bytes into Insert when the item is chosen (a snippet's $0,
// between parentheses). An offset past Insert or inside a character is left out.
func (it CompletionItem) WithCursor(offset int) CompletionItem {
	it.cursor, it.hasCursor = offset, true
	return it
}

// CompletionList answers a completion request; Incomplete: typing should ask again.
type CompletionList struct {
	Items      []CompletionItem
	Incomplete bool
}

// MaxItems and MaxBody are a list's caps; Encode cuts longer lists and marks them incomplete.
const (
	MaxItems = 4096
	MaxBody  = 1 << 20
)

// Encode is the body rhun reads (section 12b).
func (l CompletionList) Encode() []byte {
	var items bytes.Buffer
	n, incomplete := 0, l.Incomplete
	for _, it := range l.Items {
		var extra bytes.Buffer
		for _, ed := range it.Edits {
			fmt.Fprintf(&extra, "edit %s %s %d\n", pos(ed.Range.Start), pos(ed.Range.End), len(ed.Text))
			extra.WriteString(ed.Text)
		}
		if it.Doc != "" {
			fmt.Fprintf(&extra, "doc %d\n", len(it.Doc))
			extra.WriteString(it.Doc)
		}
		label, detail, insert := oneLine(it.Label), oneLine(it.Detail), it.Insert
		if insert == "" && !it.prepared {
			insert = it.Label
		}
		if it.prepared {
			program := SnippetProgram{Snippet: PreparedSnippet{Text: insert, Fields: it.fields}, Choices: it.choices, InitialVariants: it.initialVariants, Transforms: it.transforms}
			if program.Validate() == nil {
				extra.Write(program.extra())
			}
		}
		if it.hasCursor && it.cursor >= 0 && it.cursor <= len(insert) &&
			(it.cursor == len(insert) || utf8.RuneStart(insert[it.cursor])) {
			fmt.Fprintf(&extra, "cursor %d 0\n", it.cursor)
		}
		kind := it.Kind
		if kind == "" {
			kind = KindText
		}
		head := fmt.Sprintf("%s %s %s %d %d %d %d\n", pos(it.Range.Start), pos(it.Range.End), kind,
			len(label), len(insert), len(detail), extra.Len())
		size := len(head) + len(label) + len(insert) + len(detail) + extra.Len()
		if n == MaxItems || items.Len()+size+32 > MaxBody {
			incomplete = true
			break
		}
		items.WriteString(head)
		items.WriteString(label)
		items.WriteString(insert)
		items.WriteString(detail)
		items.Write(extra.Bytes())
		n++
	}
	more := 0
	if incomplete {
		more = 1
	}
	return append([]byte(fmt.Sprintf("%d %d\n", n, more)), items.Bytes()...)
}

// SignatureHelp answers a signature request: the signature line, the active parameter's bytes in
// it (ActiveFrom -1 for none), documentation.
type SignatureHelp struct {
	Signature            string
	ActiveFrom, ActiveTo int
	Doc                  string
}

// Encode is the body rhun reads (section 12c), at most 64 KB: the documentation is cut at a
// character to fit; a signature that does not fit on its own is no answer (it hides the help).
func (s SignatureHelp) Encode() []byte {
	sig := oneLine(s.Signature)
	a, b := s.ActiveFrom, s.ActiveTo
	if a < 0 || b < a || b > len(sig) {
		a, b = -1, -1
	}
	head := fmt.Sprintf("%d %d\n%s\n", a, b, sig)
	if len(head) > 65536 {
		return nil
	}
	doc := s.Doc
	if len(head)+len(doc) > 65536 {
		cut := 65536 - len(head)
		for cut > 0 && !utf8.RuneStart(doc[cut]) {
			cut--
		}
		doc = doc[:cut]
	}
	return []byte(head + doc)
}

// Completer answers completion requests (feature complete).
type Completer interface {
	Complete(ext *Ext, req Request) CompletionList
}

// SignatureHelper answers signature help requests (feature signature); nil hides the help.
type SignatureHelper interface {
	Signature(ext *Ext, req Request) *SignatureHelp
}

// answer replies to a request from rhun: "ok ID LEN" and the body; "error ID CODE TEXT" when
// the handler refuses a rename or its edit cannot be encoded; "error ID unknown" for a verb the
// handler does not answer or a request that does not parse.
func (e *Ext) answer(h Handler, f []string, reqBody []byte) {
	if len(f) < 2 {
		return
	}
	id := f[0]
	var body []byte
	var rerr *Error
	known := false
	switch f[1] {
	case "content":
		if c, is := h.(ContentProvider); is && e.Has(FeatureURIs) && len(f) >= 3 {
			known = true
			content, err := c.Content(e, strings.Join(f[2:], " "))
			if err != nil {
				rerr = &Error{Code: "nofile", Text: err.Error()}
			} else if body, err = content.Encode(); err != nil {
				rerr = &Error{Code: "toobig", Text: err.Error()}
			}
		}
	case "save-actions":
		id, _ := strconv.ParseInt(f[0], 10, 64)
		source := e.sourceFor(id)
		if source != nil {
			defer e.finishSource(source)
		}
		s, is := h.(SaveActionProvider)
		if is && source != nil && e.Has(FeatureSaveActions) {
			if req, ok := parseSaveActions(source.id, f[2:], source.ctx); ok {
				known = true
				if req.Context.Err() != nil {
					rerr = &Error{Code: "cancelled", Text: "save was cancelled"}
				} else {
					edit, err := s.SaveActions(e, req)
					if req.Context.Err() != nil {
						rerr = &Error{Code: "cancelled", Text: "save was cancelled"}
					} else if err != nil {
						rerr = &Error{Code: "refused", Text: err.Error()}
					} else {
						body, rerr, known = encodeEditV1(edit)
					}
				}
			}
		}
	case FeatureQuickDiff:
		q, is := h.(QuickDiffProvider)
		if is && e.Has(FeatureQuickDiff) && len(f) == 4 {
			id, e1 := strconv.ParseInt(f[0], 10, 64)
			doc, e2 := strconv.ParseInt(f[2], 10, 64)
			ver, e3 := strconv.ParseInt(f[3], 10, 64)
			if e1 == nil && e2 == nil && e3 == nil && doc > 0 && ver >= 0 {
				known = true
				if b, ok := q.QuickDiff(e, QuickDiffRequest{ID: id, Doc: Doc(doc), Version: ver}); ok {
					body = b
				}
			}
		}
	case FeatureCommitMessage:
		id, _ := strconv.ParseInt(f[0], 10, 64)
		source := e.sourceFor(id)
		if source != nil {
			defer e.finishSource(source)
		}
		p, is := h.(CommitMessageProvider)
		if is && source != nil && e.Has(FeatureCommitMessage) {
			if req, ok := ParseCommitMessageRequest(id, reqBody, source.ctx); ok {
				known = true
				m, err := p.CommitMessage(e, req)
				switch {
				case req.Context.Err() != nil:
					rerr = &Error{Code: "cancelled", Text: "generation was cancelled"}
				case err != nil:
					rerr = &Error{Code: "refused", Text: err.Error()}
				default:
					if m, err = CheckCommitMessage(m); err != nil {
						rerr = &Error{Code: "refused", Text: err.Error()}
					} else {
						body = []byte(m)
					}
				}
			}
		}
	case "complete":
		c, is := h.(Completer)
		if req, ok := parseRequest(f[2:]); is && ok {
			body, known = c.Complete(e, req).Encode(), true
		}
	case "signature":
		s, is := h.(SignatureHelper)
		if req, ok := parseRequest(f[2:]); is && ok {
			if help := s.Signature(e, req); help != nil {
				body = help.Encode()
			}
			known = true
		}
	case "agent-transcript", "tool", "agent-prompt":
		body, rerr, known = e.answerAI(h, f, reqBody)
	default:
		body, rerr, known = e.answerLang(h, f[1], f[2:])
	}
	switch {
	case !known:
		e.write(fmt.Sprintf("error %s unknown no such request\n", id), nil)
	case rerr != nil:
		e.write(errorLine(id, rerr), nil)
	default:
		e.write(fmt.Sprintf("ok %s %d\n", id, len(body)), body)
	}
}

// WordStart is the start of the word (letters, digits, _ and any non-ASCII) that ends at the
// position in text: the usual Range.Start of a completion item.
func WordStart(text []byte, at Pos) Pos {
	off := 0
	for n := 1; n < at.Line && off <= len(text); n++ {
		i := bytes.IndexByte(text[off:], '\n')
		if i < 0 {
			return at
		}
		off += i + 1
	}
	end := off + at.Col - 1
	if end > len(text) {
		end = len(text)
	}
	s := end
	for s > off {
		c := text[s-1]
		if c == '_' || c >= 0x80 || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z' {
			s--
			continue
		}
		break
	}
	return Pos{Line: at.Line, Col: s - off + 1}
}
