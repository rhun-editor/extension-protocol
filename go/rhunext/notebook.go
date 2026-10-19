package rhunext

// Notebooks (feature "notebooks", v1.1/v1.1.md): the extension reads a notebook file into cells
// (NotebookProvider.NbOpen), writes them back (NbSave) and runs cells (NbRun), answering with
// Ext.NbState, NbOutput, NbStream and NbKernelLabel from any goroutine. rhun keeps the cells,
// their metadata and outputs; it never parses the file. NotebookEvents receives interrupt,
// restart, the kernel button and the tab closing.

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FeatureNotebooks asks for notebooks in hello.
const FeatureNotebooks = "notebooks"

// Limits of the notebooks feature.
const (
	NbBodyMax    = 64 << 20 // nb-open's answer and nb-save's body
	NbCellsMax   = 5000
	NbSourceMax  = 4 << 20
	NbOutputsMax = 1000
	NbItemsMax   = 32
	NbItemMax    = 16 << 20
	NbStreamMax  = 64 << 10 // an nb-stream message
	NbLabelMax   = 200      // the kernel button's text
)

// CellKind is "code" or "markup".
type CellKind string

const (
	CellCode   CellKind = "code"
	CellMarkup CellKind = "markup"
)

// Cell is a notebook cell: Lang is a grammar name with spaces as _ ("-" for none and for markup),
// Count the execution count (-1 none), Meta opaque bytes rhun returns.
type Cell struct {
	Kind    CellKind
	Lang    string
	Count   int64
	Source  []byte
	Meta    []byte
	Outputs []Output
}

// Notebook is a notebook body: its opaque metadata and its cells.
type Notebook struct {
	Meta  []byte
	Cells []Cell
}

// Output is one output: Kind stdout, stderr, result, display or error; ID Jupyter's display id or
// ""; Keep opaque bytes rhun returns on save; Items in order of preference.
type Output struct {
	Kind  string
	ID    string
	Keep  []byte
	Items []Item
}

// Item is one representation: Type text, ansi, png, jpeg, gif, rich, picture or another token;
// Width and Height an image's logical size (0 0 natural).
type Item struct {
	Type          string
	Width, Height int
	Data          []byte
}

// NbOpen is nb-open: the file's path and bytes (empty for a file that does not exist yet).
type NbOpen struct {
	ID   int64
	NB   int64
	Path string
	Data []byte
}

// NbSave is nb-save: the path to write for and the notebook as rhun has it.
type NbSave struct {
	ID       int64
	NB       int64
	Path     string
	Notebook *Notebook
}

// NbRun is the nb-run event: run the cell's source and answer for this run.
type NbRun struct {
	NB, Cell, Run int64
	Lang          string
	Source        []byte
}

// NotebookProvider reads, writes and runs notebooks.
type NotebookProvider interface {
	NbOpen(ext *Ext, req *NbOpen) (*Notebook, error)
	NbSave(ext *Ext, req *NbSave) ([]byte, error)
	NbRun(ext *Ext, run *NbRun)
}

// NotebookEvents receives the notebook events without a run.
type NotebookEvents interface {
	NbInterrupt(ext *Ext, nb int64)
	NbRestart(ext *Ext, nb int64)
	NbKernel(ext *Ext, nb int64)
	NbClosed(ext *Ext, nb int64)
}

func nbToken(t string) bool {
	if t == "" || len(t) > 64 {
		return false
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("_.+/-", c) >= 0) {
			return false
		}
	}
	return true
}

type nbReader struct {
	d []byte
	p int
}

func (r *nbReader) line() ([]string, error) {
	i := bytes.IndexByte(r.d[r.p:], '\n')
	if i < 0 {
		return nil, errors.New("a line without its end")
	}
	l := string(r.d[r.p : r.p+i])
	r.p += i + 1
	return strings.Fields(l), nil
}

func (r *nbReader) take(n int) ([]byte, error) {
	if len(r.d)-r.p < n {
		return nil, errors.New("a body shorter than its lengths")
	}
	b := append([]byte(nil), r.d[r.p:r.p+n]...)
	r.p += n
	return b, nil
}

func nbNum(f string, max int64) (int, error) {
	if f == "" || len(f) > 18 {
		return 0, fmt.Errorf("not a number: %q", f)
	}
	for i := 0; i < len(f); i++ {
		if f[i] < '0' || f[i] > '9' {
			return 0, fmt.Errorf("not a number: %q", f)
		}
	}
	v, err := strconv.ParseInt(f, 10, 64)
	if err != nil || v > max {
		return 0, fmt.Errorf("%q is over its cap %d", f, max)
	}
	return int(v), nil
}

// ParseNotebook reads a notebook body.
func ParseNotebook(body []byte) (*Notebook, error) {
	r := &nbReader{d: body}
	f, err := r.line()
	if err != nil {
		return nil, err
	}
	if len(f) != 2 {
		return nil, errors.New("the notebook's first line is N META_LEN")
	}
	n, err := nbNum(f[0], NbCellsMax)
	if err != nil {
		return nil, err
	}
	ml, err := nbNum(f[1], NbBodyMax)
	if err != nil {
		return nil, err
	}
	nb := &Notebook{}
	if nb.Meta, err = r.take(ml); err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		f, err := r.line()
		if err != nil {
			return nil, err
		}
		if len(f) != 6 || (f[0] != "code" && f[0] != "markup") {
			return nil, errors.New("a cell's line is KIND LANG COUNT SOURCE_LEN META_LEN NOUT")
		}
		c := Cell{Kind: CellKind(f[0]), Lang: f[1], Count: -1}
		if f[2] != "-" {
			v, err := nbNum(f[2], 1<<62)
			if err != nil {
				return nil, err
			}
			c.Count = int64(v)
		}
		sl, err := nbNum(f[3], NbSourceMax)
		if err != nil {
			return nil, err
		}
		cml, err := nbNum(f[4], NbBodyMax)
		if err != nil {
			return nil, err
		}
		no, err := nbNum(f[5], NbOutputsMax)
		if err != nil {
			return nil, err
		}
		if c.Source, err = r.take(sl); err != nil {
			return nil, err
		}
		if c.Meta, err = r.take(cml); err != nil {
			return nil, err
		}
		for j := 0; j < no; j++ {
			o, err := readNbOutput(r)
			if err != nil {
				return nil, err
			}
			c.Outputs = append(c.Outputs, *o)
		}
		nb.Cells = append(nb.Cells, c)
	}
	if r.p != len(body) {
		return nil, errors.New("bytes after the last cell")
	}
	return nb, nil
}

func readNbOutput(r *nbReader) (*Output, error) {
	f, err := r.line()
	if err != nil {
		return nil, err
	}
	if len(f) != 4 {
		return nil, errors.New("an output's line is KIND OUTID KEEP_LEN NITEM")
	}
	switch f[0] {
	case "stdout", "stderr", "result", "display", "error":
	default:
		return nil, fmt.Errorf("unknown output kind %q", f[0])
	}
	o := &Output{Kind: f[0]}
	if f[1] != "-" {
		if !nbToken(f[1]) {
			return nil, fmt.Errorf("bad output id %q", f[1])
		}
		o.ID = f[1]
	}
	kl, err := nbNum(f[2], NbBodyMax)
	if err != nil {
		return nil, err
	}
	ni, err := nbNum(f[3], NbItemsMax)
	if err != nil {
		return nil, err
	}
	if o.Keep, err = r.take(kl); err != nil {
		return nil, err
	}
	for i := 0; i < ni; i++ {
		f, err := r.line()
		if err != nil {
			return nil, err
		}
		if len(f) != 4 || !nbToken(f[0]) {
			return nil, errors.New("an item's line is TYPE W H LEN")
		}
		w, err := nbNum(f[1], 65536)
		if err != nil {
			return nil, err
		}
		h, err := nbNum(f[2], 65536)
		if err != nil {
			return nil, err
		}
		l, err := nbNum(f[3], NbItemMax)
		if err != nil {
			return nil, err
		}
		it := Item{Type: f[0], Width: w, Height: h}
		if it.Data, err = r.take(l); err != nil {
			return nil, err
		}
		o.Items = append(o.Items, it)
	}
	return o, nil
}

// ParseOutput reads one output body.
func ParseOutput(body []byte) (*Output, error) {
	r := &nbReader{d: body}
	o, err := readNbOutput(r)
	if err != nil {
		return nil, err
	}
	if r.p != len(body) {
		return nil, errors.New("bytes after the output")
	}
	return o, nil
}

// Encode writes the notebook body.
func (nb *Notebook) Encode() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%d %d\n", len(nb.Cells), len(nb.Meta))
	b.Write(nb.Meta)
	for _, c := range nb.Cells {
		lang := c.Lang
		if c.Kind == CellMarkup || lang == "" {
			lang = "-"
		}
		count := "-"
		if c.Count >= 0 {
			count = strconv.FormatInt(c.Count, 10)
		}
		fmt.Fprintf(&b, "%s %s %s %d %d %d\n", c.Kind, strings.ReplaceAll(lang, " ", "_"), count, len(c.Source), len(c.Meta), len(c.Outputs))
		b.Write(c.Source)
		b.Write(c.Meta)
		for i := range c.Outputs {
			c.Outputs[i].encodeTo(&b)
		}
	}
	return b.Bytes()
}

// Encode writes the output body.
func (o *Output) Encode() []byte {
	var b bytes.Buffer
	o.encodeTo(&b)
	return b.Bytes()
}

func (o *Output) encodeTo(b *bytes.Buffer) {
	id := o.ID
	if !nbToken(id) {
		id = "-"
	}
	fmt.Fprintf(b, "%s %s %d %d\n", o.Kind, id, len(o.Keep), len(o.Items))
	b.Write(o.Keep)
	for _, it := range o.Items {
		t := it.Type
		if !nbToken(t) {
			t = "text"
		}
		fmt.Fprintf(b, "%s %d %d %d\n", t, it.Width, it.Height, len(it.Data))
		b.Write(it.Data)
	}
}

// nbSplitPath splits nb-open's and nb-save's bodies after their path.
func nbSplitPath(body []byte, withData bool) (path string, rest []byte, ok bool) {
	i := bytes.IndexByte(body, '\n')
	if i < 0 {
		return "", nil, false
	}
	f := strings.Split(string(body[:i]), " ")
	rest = body[i+1:]
	pl, err := strconv.Atoi(f[0])
	if err != nil || pl < 0 || pl > len(rest) {
		return "", nil, false
	}
	path, rest = string(rest[:pl]), rest[pl:]
	if withData {
		if len(f) != 2 {
			return "", nil, false
		}
		dl, err := strconv.Atoi(f[1])
		if err != nil || dl != len(rest) {
			return "", nil, false
		}
	} else if len(f) != 1 {
		return "", nil, false
	}
	return path, rest, true
}

// answerNotebook answers nb-open and nb-save (f: ID VERB NB LEN); false for any other request.
func (e *Ext) answerNotebook(h Handler, f []string, body []byte) bool {
	if len(f) < 3 || (f[1] != "nb-open" && f[1] != "nb-save") {
		return false
	}
	id := f[0]
	p, is := h.(NotebookProvider)
	nb, _ := strconv.ParseInt(f[2], 10, 64)
	rid, _ := strconv.ParseInt(id, 10, 64)
	if !is || !e.Has(FeatureNotebooks) {
		e.write(fmt.Sprintf("error %s unknown no such request\n", id), nil)
		return true
	}
	var out []byte
	var rerr *Error
	if f[1] == "nb-open" {
		path, data, ok := nbSplitPath(body, true)
		if !ok {
			rerr = &Error{Code: "syntax", Text: "nb-open's body"}
		} else if n, err := p.NbOpen(e, &NbOpen{ID: rid, NB: nb, Path: path, Data: data}); err != nil {
			rerr = &Error{Code: "refused", Text: err.Error()}
		} else {
			out = n.Encode()
		}
	} else {
		path, rest, ok := nbSplitPath(body, false)
		var n *Notebook
		var err error
		if ok {
			n, err = ParseNotebook(rest)
		}
		if !ok || err != nil {
			rerr = &Error{Code: "syntax", Text: "nb-save's body"}
		} else if out, err = p.NbSave(e, &NbSave{ID: rid, NB: nb, Path: path, Notebook: n}); err != nil {
			rerr = &Error{Code: "refused", Text: err.Error()}
		}
	}
	if rerr != nil {
		e.write(errorLine(id, rerr), nil)
	} else {
		e.write(fmt.Sprintf("ok %s %d\n", id, len(out)), out)
	}
	return true
}

// notebookEvent delivers nb-run and the notebook events; false for any other event.
func (e *Ext) notebookEvent(h Handler, ev Event) bool {
	if !e.Has(FeatureNotebooks) {
		return false
	}
	switch ev.Name {
	case "nb-run":
		p, is := h.(NotebookProvider)
		if !is || len(ev.Args) != 5 {
			return false
		}
		r := &NbRun{Lang: ev.Args[3], Source: ev.Body}
		r.NB, _ = strconv.ParseInt(ev.Args[0], 10, 64)
		r.Cell, _ = strconv.ParseInt(ev.Args[1], 10, 64)
		r.Run, _ = strconv.ParseInt(ev.Args[2], 10, 64)
		p.NbRun(e, r)
		return true
	case "nb-interrupt", "nb-restart", "nb-kernel", "nb-closed":
		n, is := h.(NotebookEvents)
		if !is || len(ev.Args) != 1 {
			return false
		}
		nb, _ := strconv.ParseInt(ev.Args[0], 10, 64)
		switch ev.Name {
		case "nb-interrupt":
			n.NbInterrupt(e, nb)
		case "nb-restart":
			n.NbRestart(e, nb)
		case "nb-kernel":
			n.NbKernel(e, nb)
		default:
			n.NbClosed(e, nb)
		}
		return true
	}
	return false
}

func (e *Ext) nbAgreed() error {
	if !e.Has(FeatureNotebooks) {
		return &Error{Code: "unknown", Text: "notebooks was not agreed"}
	}
	return nil
}

// NbState reports a run's state (queued, running, done, failed, cancelled) and, with count >= 0,
// the cell's execution count. "stale" when the run was superseded.
func (e *Ext) NbState(nb, cell, run int64, state string, count int64) error {
	if err := e.nbAgreed(); err != nil {
		return err
	}
	c := "-"
	if count >= 0 {
		c = strconv.FormatInt(count, 10)
	}
	_, err := e.Request("nb-state", nil, itoa(nb), itoa(cell), itoa(run), state, c)
	return err
}

// NbOutput appends (op "append", target "-"), replaces (op "replace", target an index or an output
// id) or clears (op "clear" or "clear-wait", out nil) a cell's outputs for a run.
func (e *Ext) NbOutput(nb, cell, run int64, op, target string, out *Output) error {
	if err := e.nbAgreed(); err != nil {
		return err
	}
	var body []byte
	if out != nil {
		body = out.Encode()
	}
	if target == "" {
		target = "-"
	}
	_, err := e.Request("nb-output", body, itoa(nb), itoa(cell), itoa(run), op, target, itoa(int64(len(body))))
	return err
}

// NbStream appends stream text (stdout, or stderr) for a run, in messages of at most NbStreamMax
// bytes cut at character boundaries.
func (e *Ext) NbStream(nb, cell, run int64, stderr bool, data []byte) error {
	if err := e.nbAgreed(); err != nil {
		return err
	}
	name := "stdout"
	if stderr {
		name = "stderr"
	}
	for len(data) > 0 {
		n := len(data)
		if n > NbStreamMax {
			n = NbStreamMax
			for n > 0 && !utf8.RuneStart(data[n]) {
				n--
			}
			if n == 0 {
				n = NbStreamMax
			}
		}
		if _, err := e.Request("nb-stream", data[:n], itoa(nb), itoa(cell), itoa(run), name, itoa(int64(n))); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// NbKernelLabel sets the kernel button's text (one line, at most NbLabelMax bytes) and its state
// (idle, busy, starting, dead, none).
func (e *Ext) NbKernelLabel(nb int64, state, label string) error {
	if err := e.nbAgreed(); err != nil {
		return err
	}
	l := []byte(oneLine(label))
	if len(l) > NbLabelMax {
		n := NbLabelMax
		for n > 0 && !utf8.RuneStart(l[n]) {
			n--
		}
		l = l[:n]
	}
	_, err := e.Request("nb-kernel-label", l, itoa(nb), state, itoa(int64(len(l))))
	return err
}
