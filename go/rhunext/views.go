package rhunext

// Retained views (protocol 1.1, feature view-tree): views whose content rhun keeps. Declare them
// in the manifest's [views] (KEY.title, KEY.place = sidebar|panel|side, KEY.kind = tree,
// KEY.open = yes); after ready rhun opens one instance per declared view (RetainedViewer's
// ViewOpen). Describe the content with a Patch and send it with Ext.PatchView: rhun applies it
// whole or not at all, keeps the user's expansion, selection and scroll by node id, and reports
// what the user does (RetainedViewer's ViewEvent).

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// FeatureViewTree is retained views: manifest [views], PatchView, ShowView, RetainedViewer.
const FeatureViewTree = "view-tree"

// FeatureViewLog is log views (kind = log, with view-tree): Patch.Log and Patch.Clear.
const FeatureViewLog = "view-log"

// Log line severities (feature view-log).
const (
	LogNone  = "-"
	LogTrace = "trace"
	LogDebug = "debug"
	LogInfo  = "info"
	LogWarn  = "warn"
	LogError = "error"
)

// LogLine is a log view's line: a severity (Log*) and one line of text (SGR colors kept).
type LogLine struct {
	Level string
	Text  string
}

// MaxViewNodes is the most nodes one retained view holds (400000 in all views).
const MaxViewNodes = 100000

// Kids says whether a node has children and whether it starts expanded.
type Kids int

const (
	KidsLeaf   Kids = iota
	KidsClosed      // collapsible, collapsed when it first appears
	KidsOpen        // collapsible, expanded when it first appears
)

func (k Kids) word() string {
	switch k {
	case KidsClosed:
		return "closed"
	case KidsOpen:
		return "open"
	}
	return "leaf"
}

// ViewColor is a color by theme name: "fg", "muted", "accent", "error", "warning", "success",
// "add", "mod", "del", "c1" … "c8".
type ViewColor string

const (
	ViewFg      ViewColor = "fg"
	ViewMuted   ViewColor = "muted"
	ViewAccent  ViewColor = "accent"
	ViewError   ViewColor = "error"
	ViewWarning ViewColor = "warning"
	ViewSuccess ViewColor = "success"
	ViewAdd     ViewColor = "add"
	ViewMod     ViewColor = "mod"
	ViewDel     ViewColor = "del"
)

// IconNamed is a built-in icon (file folder folder-open search check close plus minus dot
// refresh branch pin puzzle more terminal spark user arrow-up arrow-down discard gear).
func IconNamed(name string) string { return name }

// IconFile is the Explorer's icon and color for a file of that name.
func IconFile(name string) string { return "file:" + name }

// IconExt is the extension's own icon KEY from its manifest's [icons].
func IconExt(key string) string { return "ext:" + key }

func viewRecord(out []byte, head string, data []byte) []byte {
	out = append(out, head...)
	out = append(out, ' ')
	out = strconv.AppendInt(out, int64(len(data)), 10)
	out = append(out, '\n')
	return append(out, data...)
}

func viewLine(s string, max int) string {
	s = strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
	if len(s) <= max {
		return s
	}
	for max > 0 && !isRuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func isRuneStart(b byte) bool { return b&0xc0 != 0x80 }

// Node is a tree node: an id (1 to 1000 bytes, no LF or NUL, unique in its view, stable across
// patches), a one-line label and optional records added by its methods.
type Node struct {
	ID    []byte
	Label string
	Kids  Kids
	extra []byte
}

// NewNode is a leaf with that id and label.
func NewNode(id, label string) *Node {
	return &Node{ID: []byte(id), Label: viewLine(label, 1000)}
}

func (n *Node) rec(head string, data []byte) *Node { n.extra = viewRecord(n.extra, head, data); return n }

// WithKids sets whether it has children.
func (n *Node) WithKids(k Kids) *Node { n.Kids = k; return n }

// Desc is muted text after the label.
func (n *Node) Desc(s string) *Node { return n.rec("desc", []byte(viewLine(s, 1000))) }

// Tip is the tooltip, plain text (at most 4000 bytes).
func (n *Node) Tip(s string) *Node {
	if len(s) > 4000 {
		s = s[:4000]
	}
	return n.rec("tip", []byte(s))
}

// Icon is its icon (IconNamed, IconFile, IconExt) in a color, "" for the default.
func (n *Node) Icon(icon string, color ViewColor) *Node {
	c := string(color)
	if c == "" {
		c = "-"
	}
	return n.rec("icon "+icon+" "+c, nil)
}

// Color is the label's color.
func (n *Node) Color(c ViewColor) *Node { return n.rec("color "+string(c), nil) }

// Highlight puts label bytes [from, to) in the accent color (at most 16 ranges).
func (n *Node) Highlight(from, to int) *Node {
	return n.rec(fmt.Sprintf("hl %d %d", from, to), nil)
}

// Badge is a count at the row's right.
func (n *Node) Badge(count int) *Node { return n.rec(fmt.Sprintf("badge %d", count), nil) }

// Check is a checkbox.
func (n *Node) Check(on bool) *Node {
	if on {
		return n.rec("check 1", nil)
	}
	return n.rec("check 0", nil)
}

// Context is the value menus test as viewItem.
func (n *Node) Context(v string) *Node { return n.rec("ctx", []byte(v)) }

// Data are bytes given back as ARGS when a manifest menu command runs for the node.
func (n *Node) Data(b []byte) *Node { return n.rec("data", b) }

// Open makes a click (on a leaf) or Enter open the file at line, col (1-based), as open does.
func (n *Node) Open(path string, line, col int) *Node {
	return n.rec(fmt.Sprintf("open %d %d", line, col), []byte(path))
}

// Command makes a click (on a leaf) or Enter run one of the extension's commands with args.
func (n *Node) Command(name string, args []byte) *Node { return n.rec("command "+name, args) }

// Inline shows action keys as icon buttons on the row while it is hovered or has the cursor (4 at
// most; actions defined with Patch.Action and WhereNode).
func (n *Node) Inline(keys ...string) *Node {
	if len(keys) > 4 {
		keys = keys[:4]
	}
	return n.rec("inline", []byte(strings.Join(keys, " ")))
}

// Menu puts action keys in its context menu (16 at most).
func (n *Node) Menu(keys ...string) *Node {
	if len(keys) > 16 {
		keys = keys[:16]
	}
	return n.rec("menu", []byte(strings.Join(keys, " ")))
}

// Record adds a record this package has no method for: "NAME ARGS..." and its data.
func (n *Node) Record(head string, data []byte) *Node { return n.rec(head, data) }

func (n *Node) encode(out []byte) ([]byte, error) {
	if len(n.ID) == 0 || len(n.ID) > 1000 || strings.ContainsAny(string(n.ID), "\n\x00") {
		return out, fmt.Errorf("bad node id %q", n.ID)
	}
	out = append(out, fmt.Sprintf("%d %d %d %s\n", len(n.ID), len(n.Label), len(n.extra), n.Kids.word())...)
	out = append(out, n.ID...)
	out = append(out, n.Label...)
	return append(out, n.extra...), nil
}

// Patch is changes to a retained view, applied whole or not at all, in order.
type Patch struct {
	ops   [][]byte
	err   error
	nodes int
}

// NewPatch is an empty patch.
func NewPatch() *Patch { return &Patch{} }

func (p *Patch) op(head string, data []byte) *Patch {
	p.ops = append(p.ops, viewRecord(nil, head, data))
	return p
}

// Title is the view's title (at most 80 bytes; "" restores the manifest's).
func (p *Patch) Title(s string) *Patch { return p.op("title", []byte(viewLine(s, 80))) }

// Badge is the count beside the title (0 hides it).
func (p *Patch) Badge(n int) *Patch { return p.op(fmt.Sprintf("badge %d", n), nil) }

// Message is a muted line above the content ("" clears it).
func (p *Patch) Message(s string) *Patch { return p.op("message", []byte(viewLine(s, 1000))) }

// State is "ready", "loading", "empty" or "error"; text and up to 3 action keys for the last two.
func (p *Patch) State(state, text string, keys ...string) *Patch {
	text = viewLine(text, 1000)
	if len(keys) > 3 {
		keys = keys[:3]
	}
	return p.op(fmt.Sprintf("state %s %d", state, len(text)), []byte(text+strings.Join(keys, " ")))
}

// Busy is the indeterminate bar at the top of the view.
func (p *Patch) Busy(on bool) *Patch {
	if on {
		return p.op("busy 1", nil)
	}
	return p.op("busy 0", nil)
}

func (p *Patch) children(name string, parent []byte, nodes []*Node, more bool) *Patch {
	data := append([]byte(nil), parent...)
	for _, n := range nodes {
		var err error
		if data, err = n.encode(data); err != nil && p.err == nil {
			p.err = err
		}
	}
	p.nodes += len(nodes)
	m := 0
	if more {
		m = 1
	}
	return p.op(fmt.Sprintf("%s %d %d %d", name, len(parent), len(nodes), m), data)
}

// Kids replaces parent's children (an empty parent: the root); a child whose id it already had
// keeps its expansion and its own children. more puts a "Load more" row after them.
func (p *Patch) Kids(parent []byte, nodes []*Node, more bool) *Patch {
	return p.children("kids", parent, nodes, more)
}

// Add appends children.
func (p *Patch) Add(parent []byte, nodes []*Node, more bool) *Patch {
	return p.children("add", parent, nodes, more)
}

// Set replaces an existing node's label, kind of children and records.
func (p *Patch) Set(n *Node) *Patch {
	data, err := n.encode(nil)
	if err != nil && p.err == nil {
		p.err = err
	}
	return p.op("set", data)
}

// Del removes a node and its subtree.
func (p *Patch) Del(id []byte) *Patch { return p.op("del", id) }

// Expand and Collapse set a node's expansion.
func (p *Patch) Expand(id []byte) *Patch   { return p.op("expand", id) }
func (p *Patch) Collapse(id []byte) *Patch { return p.op("collapse", id) }

// Where an action shows: a button in the view's header, offered to nodes, or removed.
const (
	WhereTitle = "title"
	WhereNode  = "node"
	WhereNone  = "none"
)

// Action defines or replaces an action: key ([a-z0-9_]{1,40}), a built-in icon name ("" for
// none), where it shows (WhereTitle, WhereNode, WhereNone), its tooltip or menu label.
func (p *Patch) Action(key, icon, where, title string) *Patch {
	if icon == "" {
		icon = "-"
	}
	return p.op(fmt.Sprintf("action %s %s %s", key, icon, where), []byte(viewLine(title, 200)))
}

// Log appends lines to a log view (feature view-log).
func (p *Patch) Log(lines ...LogLine) *Patch {
	var data []byte
	for _, l := range lines {
		lv := l.Level
		if lv == "" {
			lv = LogNone
		}
		data = append(data, lv...)
		data = append(data, ' ')
		data = append(data, strings.ReplaceAll(l.Text, "\n", " ")...)
		data = append(data, '\n')
	}
	return p.op("log "+strconv.Itoa(len(lines)), data)
}

// Clear removes every line of a log view.
func (p *Patch) Clear() *Patch { return p.op("clear", nil) }

// SelectMode lets several nodes be selected (Ctrl and Shift).
func (p *Patch) SelectMode(multi bool) *Patch {
	if multi {
		return p.op("select-mode multi", nil)
	}
	return p.op("select-mode single", nil)
}

// Raw adds an op this package has no method for.
func (p *Patch) Raw(head string, data []byte) *Patch { return p.op(head, data) }

// Bytes is the vpatch body, or why it cannot be sent.
func (p *Patch) Bytes() ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.nodes > MaxViewNodes {
		return nil, fmt.Errorf("%d nodes in one patch (at most %d)", p.nodes, MaxViewNodes)
	}
	out := strconv.AppendInt(nil, int64(len(p.ops)), 10)
	out = append(out, '\n')
	for _, op := range p.ops {
		out = append(out, op...)
	}
	if len(out) > 4<<20 {
		return nil, errors.New("a patch over 4 MB")
	}
	return out, nil
}

// PatchView applies a patch to a retained view; the view's new revision.
func (e *Ext) PatchView(v View, p *Patch) (int64, error) {
	if !e.Has(FeatureViewTree) {
		return 0, &Error{Code: "unknown", Text: "view-tree feature was not agreed"}
	}
	body, err := p.Bytes()
	if err != nil {
		return 0, &Error{Code: "toobig", Text: err.Error()}
	}
	r, err := e.Request("vpatch", body, itoa(int64(v)), itoa(int64(len(body))))
	if err != nil {
		return 0, err
	}
	return argInt(r, 0), nil
}

// ShowView opens a declared view's place on it (its manifest key); focus also gives it the
// keyboard. Its view number.
func (e *Ext) ShowView(name string, focus bool) (View, error) {
	f := "-"
	if focus {
		f = "focus"
	}
	r, err := e.Request("vshow", nil, name, f)
	if err != nil {
		return 0, err
	}
	return View(argInt(r, 0)), nil
}

// RevealView expands a node's ancestors and scrolls it into view; how among "select", "focus",
// "expand".
func (e *Ext) RevealView(v View, id []byte, how ...string) error {
	h := "-"
	if len(how) > 0 {
		h = strings.Join(how, ",")
	}
	_, err := e.Request("vreveal", id, itoa(int64(v)), h, itoa(int64(len(id))))
	return err
}

// TabView opens a retained view of kind ("tree", or "log" with view-log) in an editor tab of its
// own, shown at once; ViewOpen follows with place "tab". Close it with CloseView.
func (e *Ext) TabView(kind, title string) (View, error) {
	r, err := e.Request("vtab", nil, kind, viewLine(title, 80))
	if err != nil {
		return 0, err
	}
	return View(argInt(r, 0)), nil
}

// ChildrenRequest is rhun asking for a node's children (vkids) or for more of them (vmore).
type ChildrenRequest struct {
	ID   int64 // a cancel event names it
	View View
	Rev  int64
	Node []byte // empty: the root
	Have int64  // vmore: how many children rhun has; -1 for vkids
}

// ChildrenProvider answers vkids (ViewChildren: a patch with a kids op for the node) and vmore
// (ViewMore: an add op).
type ChildrenProvider interface {
	ViewChildren(ext *Ext, req ChildrenRequest) (*Patch, error)
	ViewMore(ext *Ext, req ChildrenRequest) (*Patch, error)
}

func (e *Ext) answerChildren(h Handler, ev Event) {
	f := ev.Args
	id := f[0]
	num := func(i int) int64 {
		if i < len(f) {
			v, _ := strconv.ParseInt(f[i], 10, 64)
			return v
		}
		return 0
	}
	rid, _ := strconv.ParseInt(id, 10, 64)
	req := ChildrenRequest{ID: rid, View: View(num(2)), Rev: num(3), Node: ev.Body, Have: -1}
	cp, ok := h.(ChildrenProvider)
	if !ok || !e.Has(FeatureViewTree) {
		e.write("error "+id+" unknown no such request\n", nil)
		return
	}
	var p *Patch
	var err error
	if f[1] == "vmore" {
		req.Have = num(4)
		p, err = cp.ViewMore(e, req)
	} else {
		p, err = cp.ViewChildren(e, req)
	}
	if err != nil {
		e.write("error "+id+" refused "+viewLine(err.Error(), 1000)+"\n", nil)
		return
	}
	body, err := p.Bytes()
	if err != nil {
		e.write("error "+id+" toobig "+viewLine(err.Error(), 1000)+"\n", nil)
		return
	}
	e.write("ok "+id+" "+strconv.Itoa(len(body))+"\n", body)
}

// ViewOpen is a declared view's instance: "0 view VIEW open KIND PLACE NAME".
type ViewOpen struct {
	View  View
	Kind  string // tree
	Place string // sidebar, panel, side or tab
	Name  string // the manifest key, or "-"
}

// RetainedEvent is what the user did in a retained view. Kind is visible, focus, select,
// activate, expand, collapse, check, action, discarded, closed or clear (a log's Clear button); On for visible, focus and
// check; Rev the content the user acted on; IDs the selected nodes (select) or the node.
type RetainedEvent struct {
	Kind string
	On   bool
	Rev  int64
	IDs  [][]byte
	Key  string // action: its key; discarded: what was dropped
	Args []string
	Body []byte
}

// ID is the event's node id (the first one of a selection), or nil.
func (r RetainedEvent) ID() []byte {
	if len(r.IDs) == 0 {
		return nil
	}
	return r.IDs[0]
}

// RetainedViewer receives retained views' instances and events.
type RetainedViewer interface {
	ViewOpen(ext *Ext, open ViewOpen)
	ViewEvent(ext *Ext, view View, ev RetainedEvent)
}

// viewEventHasBody reports whether "0 view VIEW WHAT ..." is followed by a body (its last field).
func viewEventHasBody(what string) bool {
	switch what {
	case "select", "activate", "expand", "collapse", "check", "action", "discarded", "change", "link",
		"text", "paste":
		return true
	}
	return false
}

func parseViewOpen(ev Event) (ViewOpen, bool) {
	if len(ev.Args) < 5 || ev.Args[1] != "open" {
		return ViewOpen{}, false
	}
	id, err := strconv.ParseInt(ev.Args[0], 10, 64)
	if err != nil {
		return ViewOpen{}, false
	}
	return ViewOpen{View: View(id), Kind: ev.Args[2], Place: ev.Args[3], Name: ev.Args[4]}, true
}

func parseRetainedEvent(ev Event) (View, RetainedEvent, bool) {
	if len(ev.Args) < 2 {
		return 0, RetainedEvent{}, false
	}
	id, err := strconv.ParseInt(ev.Args[0], 10, 64)
	if err != nil {
		return 0, RetainedEvent{}, false
	}
	num := func(i int) int64 {
		if i < len(ev.Args) {
			v, _ := strconv.ParseInt(ev.Args[i], 10, 64)
			return v
		}
		return 0
	}
	r := RetainedEvent{Kind: ev.Args[1], Args: ev.Args[2:], Body: ev.Body}
	switch r.Kind {
	case "visible", "focus":
		r.On = num(2) == 1
	case "select":
		r.Rev = num(2)
		for _, s := range strings.Split(string(ev.Body), "\n") {
			if s != "" {
				r.IDs = append(r.IDs, []byte(s))
			}
		}
	case "activate", "expand", "collapse":
		r.Rev, r.IDs = num(2), [][]byte{ev.Body}
	case "check":
		r.Rev, r.On, r.IDs = num(2), num(3) == 1, [][]byte{ev.Body}
	case "action", "discarded":
		r.Rev, r.IDs = num(2), [][]byte{ev.Body}
		if len(ev.Args) > 3 {
			r.Key = ev.Args[3]
		}
	case "closed":
	case "size", "metrics", "hover", "down", "drag", "up", "click", "leave", "wheel", "open",
		"key", "text", "paste", "selectall", "dblclick":
		return 0, RetainedEvent{}, false
	}
	return View(id), r, true
}
