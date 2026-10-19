// Package rhunext speaks rhun's extension protocol, version 1 (../../v1/v1.md), for a program
// started by rhun: it reads the events rhun writes, lets the extension send requests and wait for
// their replies, and takes care of the framing (lines, ids, byte-counted bodies).
//
// Usage: implement Handler, then call Run. Events arrive one at a time on a single goroutine;
// requests may be made from any goroutine, including from inside an event handler, and block
// until rhun answers.
//
// Built with GOOS=wasip1 GOARCH=wasm, an extension is one module rhun runs itself on every system
// (run.wasm32-wasip1 in the manifest); Run then reads standard input non-blocking, so the other
// goroutines keep running while it waits.
package rhunext

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Protocol is the version this package speaks.
const Protocol = 1

// Doc is a document id, session-unique.
type Doc int64

// Pos is a position: 1-based line, 1-based byte column.
type Pos struct{ Line, Col int }

// Range is half-open: from Start up to, not including, End.
type Range struct{ Start, End Pos }

// Event is anything rhun tells the extension without expecting an answer.
type Event struct {
	Name string   // ready, setting, opened, active, saved, changed, cursor, selection, closed, project, run, bye, edits, resync, hover, action
	Args []string // the fields after the name; the last may contain spaces
	Body []byte   // the edits event's records (section 6a); the action event's data (section 12f); the selections event's lines (1.1)
}

// Edit is one record of an edits event: at byte offset Start, Del bytes were deleted and Ins
// inserted, in order, each applying to the text as the previous one left it.
type Edit struct {
	Start, Del int
	Ins        []byte
}

// ParseEdits decodes an edits event's body.
func ParseEdits(body []byte) ([]Edit, error) {
	var out []Edit
	for len(body) > 0 {
		nl := bytes.IndexByte(body, '\n')
		if nl < 0 {
			return out, errors.New("edits: a record without a line end")
		}
		var e Edit
		var ins int
		if _, err := fmt.Sscanf(string(body[:nl]), "%d %d %d", &e.Start, &e.Del, &ins); err != nil {
			return out, fmt.Errorf("edits: bad record %q", body[:nl])
		}
		body = body[nl+1:]
		if ins > len(body) {
			return out, errors.New("edits: a record's bytes end early")
		}
		e.Ins = body[:ins]
		body = body[ins:]
		out = append(out, e)
	}
	return out, nil
}

// ApplyEdits applies records to a copy of the text.
func ApplyEdits(text []byte, edits []Edit) ([]byte, error) {
	for _, e := range edits {
		if e.Start < 0 || e.Start+e.Del > len(text) {
			return text, errors.New("edits: a record outside the text")
		}
		next := make([]byte, 0, len(text)-e.Del+len(e.Ins))
		next = append(next, text[:e.Start]...)
		next = append(next, e.Ins...)
		next = append(next, text[e.Start+e.Del:]...)
		text = next
	}
	return text, nil
}

// Severity of a diagnostic.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	SevInfo    Severity = "info"
	SevHint    Severity = "hint"
)

// Diagnostic is one mark: a half-open range, a severity and a one-line message. The other fields
// travel only with Ext.FileDiagnostics (protocol 1.1): ID, a token of [A-Za-z0-9_.:-] unique in the
// set, comes back in code-actions requests (CodeActionsRequest.Diagnostics); Code and Source show
// as source(code); Href is an http(s) link explaining the code; Tags; Related places.
type Diagnostic struct {
	Range    Range
	Severity Severity
	Message  string
	ID       string
	Code     string
	Source   string
	Href     string
	Tags     []DiagnosticTag
	Related  []RelatedInfo
}

// Handler receives events. Methods are called on one goroutine, in order, so an extension's
// own state needs no locking when only the handler touches it. A request made inside a handler
// (including a prompt the user takes time over) blocks the handler until rhun answers; events
// that arrive meanwhile wait in a queue without bound. Do slow work on a goroutine of your own.
type Handler interface {
	// Ready is the first event: the project folder, or "" without one.
	Ready(ext *Ext, project string)
	// Run is one of the extension's commands, by its full name (NAME.command, as the run
	// event carries it: "format.format_document", not the manifest key), with the active
	// document or 0.
	Run(ext *Ext, command string, doc Doc)
	// Event is any other event, including the ones with dedicated accessors below.
	Event(ext *Ext, ev Event)
}

// Opened is the opened event's fields.
type Opened struct {
	Doc     Doc
	Version int64
	Lang    string // "" when none; spaces travel as underscores
	Path    string // "" for an untitled document
}

// Error is a reply with an error code: syntax, unknown, needs, nodoc, nofile, readonly, stale,
// badpos, toobig, busy, cancelled, refused (section 3 of the protocol).
type Error struct {
	Code string // syntax, unknown, needs, nodoc, nofile, readonly, stale, badpos, busy, cancelled, refused
	Text string
}

func (e *Error) Error() string { return "rhun: " + e.Code + ": " + e.Text }

// Reply is a successful reply: its fields after the id, and its body when it has one.
type Reply struct {
	Args []string
	Body []byte
}

// Ext is the connection to rhun.
type Ext struct {
	w              io.Writer
	wmu            sync.Mutex
	writerOnce     sync.Once
	stopOnce       sync.Once
	writeQueue     chan *writeFrame
	writeChanged   chan struct{}
	writeBytes     int
	readCloser     io.Closer
	sourceMu       sync.Mutex
	sourceRequests map[int64]*sourceRequest
	nextID         atomic.Int64
	pmu            sync.Mutex
	pend           map[int64]chan result
	bodies         map[int64]bool  // request ids whose ok reply carries a body
	replyBodyCaps  map[int64]int64 // per-request limits, retained with canceled body tombstones
	outstanding    map[int64]bool  // includes every sent cancellation until its reply is consumed
	logw           io.Writer
	done           chan struct{}
	fmu            sync.Mutex
	agreed         map[string]bool // the features rhun agreed to (section 12), from the features event
	closed         error           // set (under pmu) once rhun's end of the pipe is gone: requests fail at once
	tmu            sync.Mutex
	tprocs         map[int64]*testProc // test-exec processes by EXEC (tests.go)
}

type result struct {
	reply Reply
	err   error
}

// Run talks to rhun over stdin and stdout until rhun says bye or closes the pipe. It returns
// nil on bye, io.EOF when rhun went away, or the protocol error that ended the exchange.
func Run(h Handler) error {
	return RunOn(stdin(), os.Stdout, os.Stderr, h)
}

// RunOn is Run on explicit streams (tests).
func RunOn(r io.Reader, w io.Writer, logw io.Writer, h Handler) error {
	return RunOnWith(r, w, logw, nil, h)
}

// RunWith is Run asking for features (section 12 of the protocol): FeatureViews, FeatureComplete,
// FeatureSignature, FeatureDefinition, FeatureReferences, FeatureSymbols, FeatureActions,
// FeatureRename, FeatureFormat, and protocol 1.1's (FeatureWorkspaceEdits, FeatureFileOperations,
// FeatureSelections, FeatureReveal, FeatureDiff, FeatureLinkedEditing, …). Those rhun agrees to
// are in Ext.Has before Ready is called.
func RunWith(features []string, h Handler) error {
	return RunOnWith(stdin(), os.Stdout, os.Stderr, features, h)
}

// RunOnWith is RunWith on explicit streams (tests).
func RunOnWith(r io.Reader, w io.Writer, logw io.Writer, features []string, h Handler) error {
	ext := &Ext{w: w, pend: map[int64]chan result{}, bodies: map[int64]bool{}, logw: logw, done: make(chan struct{}),
		agreed: map[string]bool{}, outstanding: map[int64]bool{}}
	ext.readCloser, _ = r.(io.Closer)
	defer ext.closeConnection(io.EOF)
	hello := fmt.Sprintf("hello %d", Protocol)
	for _, f := range features {
		hello += " " + f
	}
	if err := ext.write(hello+"\n", nil); err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 1<<16)
	// the reader never blocks on the handler: events queue without bound, so a reply can be
	// read while the handler waits for it (a prompt inside an event handler)
	raw := make(chan Event)
	events := make(chan Event)
	errc := make(chan error, 1)
	go func() {
		defer close(raw)
		for {
			ev, err := ext.readMessage(br)
			if err != nil {
				// a request waiting in a handler (a Complete that asked for the text) fails now,
				// not when the handler returns
				ext.failPending(err)
				errc <- err
				return
			}
			if ev != nil {
				select {
				case raw <- *ev:
				case <-ext.done:
					return
				}
			}
		}
	}()
	go func() {
		defer close(events)
		var queue []Event
		for raw != nil || len(queue) > 0 {
			var out chan Event
			var next Event
			if len(queue) > 0 {
				out, next = events, queue[0]
			}
			select {
			case <-ext.done:
				return
			case ev, ok := <-raw:
				if !ok {
					raw = nil
					continue
				}
				queue = append(queue, ev)
			case out <- next:
				queue = queue[1:]
			}
		}
	}()
	for ev := range events {
		switch ev.Name {
		case "ready":
			p := ""
			if len(ev.Args) > 0 && ev.Args[0] != "-" {
				p = ev.Args[0]
			}
			h.Ready(ext, p)
		case "run":
			if len(ev.Args) >= 2 {
				d, _ := strconv.ParseInt(ev.Args[1], 10, 64)
				h.Run(ext, ev.Args[0], Doc(d))
			}
		case "bye":
			ext.closeConnection(errors.New("rhun said bye"))
			return nil
		case "features":
			ext.fmu.Lock()
			for _, f := range ev.Args {
				ext.agreed[f] = true
			}
			ext.fmu.Unlock()
			h.Event(ext, ev)
		case hostRequest:
			if len(ev.Args) > 1 && (ev.Args[1] == "vkids" || ev.Args[1] == "vmore") {
				ext.answerChildren(h, ev)
				continue
			}
			if len(ev.Args) > 1 && (ev.Args[1] == "tests-resolve" || ev.Args[1] == "test-run") {
				ext.answerTests(h, ev.Args, ev.Body)
				continue
			}
			if ext.answerNotebook(h, ev.Args, ev.Body) {
				continue
			}
			ext.answer(h, ev.Args, ev.Body)
		case "test-cancel":
			if c, ok := h.(TestController); ok {
				if run, ok := ParseTestCancel(ev); ok {
					c.CancelTests(ext, run)
					continue
				}
			}
			h.Event(ext, ev)
		case "agent-stop":
			if p, ok := h.(Prompter); ok {
				if sid, ok := ParseAgentStop(ev); ok {
					p.AgentStop(ext, sid)
					continue
				}
			}
			h.Event(ext, ev)
		case "view":
			if rv, ok := h.(RetainedViewer); ok {
				if open, ok := parseViewOpen(ev); ok {
					rv.ViewOpen(ext, open)
					continue
				}
				if view, rev, ok := parseRetainedEvent(ev); ok {
					rv.ViewEvent(ext, view, rev)
					if rev.Kind != "closed" {
						continue
					}
				}
			}
			if v, ok := h.(Viewer); ok {
				if view, vev, ok := parseViewEvent(ev); ok {
					v.View(ext, view, vev)
					continue
				}
			}
			h.Event(ext, ev)
		case "action":
			if a, ok := h.(ActionProvider); ok {
				a.Action(ext, ev.Body)
				continue
			}
			h.Event(ext, ev)
		default:
			if ext.notebookEvent(h, ev) {
				continue
			}
			h.Event(ext, ev)
		}
	}
	var err error
	select {
	case err = <-errc:
	default:
		err = ext.connectionError()
	}
	ext.closeConnection(err)
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	return err
}

// readMessage reads one line (and its body when it announces one). Events are returned;
// replies are delivered to the waiting request and nil is returned.
func (e *Ext) readMessage(br *bufio.Reader) (*Event, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		if err == io.EOF && line == "" {
			return nil, io.EOF
		}
		if err != io.EOF {
			return nil, err
		}
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, nil
	}
	fields := strings.Split(line, " ")
	switch fields[0] {
	case "ok", "error":
		if len(fields) < 2 {
			return nil, nil
		}
		id, perr := strconv.ParseInt(fields[1], 10, 64)
		if perr != nil {
			return nil, nil
		}
		var res result
		if fields[0] == "ok" {
			res.reply.Args = fields[2:]
			// Body classification and caps outlive cancellation until a reply is drained.
			e.pmu.Lock()
			ch, hasBody := e.pend[id], e.bodies[id]
			cap := int64(maxReplyBody)
			if limit := e.replyBodyCaps[id]; limit != 0 {
				cap = limit
			}
			e.pmu.Unlock()
			if hasBody && len(res.reply.Args) >= 1 {
				n, ok := bodyLen(res.reply.Args[len(res.reply.Args)-1], cap)
				if !ok {
					return nil, fmt.Errorf("a reply's length %q", res.reply.Args[len(res.reply.Args)-1])
				}
				if ch == nil {
					if _, err := io.CopyN(io.Discard, br, int64(n)); err != nil {
						return nil, err
					}
				} else {
					body := make([]byte, n)
					if _, err := io.ReadFull(br, body); err != nil {
						return nil, err
					}
					res.reply.Body = body
				}
			}
			e.deliver(id, res, ch)
			return nil, nil
		}
		code, text := "", ""
		if len(fields) >= 3 {
			code = fields[2]
		}
		if len(fields) >= 4 {
			text = strings.Join(fields[3:], " ")
		}
		res.err = &Error{Code: code, Text: text}
		e.pmu.Lock()
		ch := e.pend[id]
		e.pmu.Unlock()
		e.deliver(id, res, ch)
		return nil, nil
	case "0":
		if len(fields) < 2 {
			return nil, nil
		}
		ev := &Event{Name: fields[1], Args: fields[2:]}
		if ev.Name == "cancel" && len(ev.Args) == 1 {
			if id, err := strconv.ParseInt(ev.Args[0], 10, 64); err == nil && id > 0 {
				e.cancelSource(id)
				e.cancelLang(id)
			}
		}
		if ev.Name == "bye" {
			e.cancelSources(false)
			e.cancelAllLang()
		}
		// tests: a process's output and end go to its streams here, never through the handler
		if ev.Name == "test-out" && len(ev.Args) == 3 {
			n, ok := bodyLen(ev.Args[2], MaxEventBody)
			ex, err := strconv.ParseInt(ev.Args[0], 10, 64)
			if !ok || err != nil {
				return nil, fmt.Errorf("a test-out length %q", ev.Args[2])
			}
			body := make([]byte, n)
			if _, err := io.ReadFull(br, body); err != nil {
				return nil, err
			}
			e.testOut(ex, ev.Args[1], body)
			return nil, nil
		}
		if ev.Name == "test-exit" && len(ev.Args) >= 2 {
			if ex, err := strconv.ParseInt(ev.Args[0], 10, 64); err == nil {
				e.testExit(ex, strings.Join(ev.Args[1:], " "))
				return nil, nil
			}
		}
		// edits (DOC VERSION LEN), selections (DOC N LEN) and action (LEN) are followed by a body
		lenAt := -1
		if (ev.Name == "edits" || ev.Name == "selections") && len(ev.Args) >= 3 {
			lenAt = 2
		} else if ev.Name == "action" && len(ev.Args) >= 1 {
			lenAt = 0
		} else if ev.Name == "view" && len(ev.Args) >= 3 && viewEventHasBody(ev.Args[1]) {
			lenAt = len(ev.Args) - 1 // retained views: the last field is the body's length
		} else if ev.Name == "nb-run" && len(ev.Args) == 5 {
			lenAt = 4 // a cell's source, at most 4 MB
		}
		if lenAt >= 0 {
			// checked before anything is allocated: a length rhun cannot have sent ends the
			// session (the caller fails the requests waiting) rather than reading gigabytes
			n, ok := bodyLen(ev.Args[lenAt], MaxEventBody)
			if !ok {
				return nil, fmt.Errorf("an %s length %q: not a decimal of at most 10 digits up to %d", ev.Name, ev.Args[lenAt], MaxEventBody)
			}
			ev.Body = make([]byte, n)
			if _, err := io.ReadFull(br, ev.Body); err != nil {
				return nil, err
			}
		}
		// the last field of opened and project may contain spaces: fields are rejoined there
		switch ev.Name {
		case "opened":
			if len(ev.Args) > 4 {
				ev.Args = append(ev.Args[:3], strings.Join(ev.Args[3:], " "))
			}
		case "ready", "project":
			if len(ev.Args) > 1 {
				ev.Args = []string{strings.Join(ev.Args, " ")}
			}
		case "setting":
			if len(ev.Args) > 2 {
				ev.Args = append(ev.Args[:1], strings.Join(ev.Args[1:], " "))
			}
			if len(ev.Args) == 1 {
				ev.Args = append(ev.Args, "")
			}
		}
		return ev, nil
	}
	// a request from rhun (section 12): answered on the handler's goroutine, in order with events
	if id, perr := strconv.ParseInt(fields[0], 10, 64); perr == nil && id > 0 {
		ev := &Event{Name: hostRequest, Args: fields}
		if len(fields) > 2 && (fields[1] == "vkids" || fields[1] == "vmore") {
			// retained views: the node id follows (its length is the last field)
			n, ok := bodyLen(fields[len(fields)-1], 4096)
			if !ok {
				return nil, fmt.Errorf("a %s length %q", fields[1], fields[len(fields)-1])
			}
			ev.Body = make([]byte, n)
			if _, err := io.ReadFull(br, ev.Body); err != nil {
				return nil, err
			}
		}
		if len(fields) > 2 && (fields[1] == "tests-resolve" || fields[1] == "test-run") {
			n, ok := bodyLen(fields[len(fields)-1], MaxEventBody)
			if !ok {
				return nil, fmt.Errorf("a %s length %q", fields[1], fields[len(fields)-1])
			}
			ev.Body = make([]byte, n)
			if _, err := io.ReadFull(br, ev.Body); err != nil {
				return nil, err
			}
		}
		// a tool call and a prompt carry their input (at most 4 MB)
		if len(fields) > 2 && (fields[1] == "tool" || fields[1] == "agent-prompt") {
			n, ok := bodyLen(fields[len(fields)-1], MaxAIBody)
			if !ok {
				return nil, fmt.Errorf("a %s length %q", fields[1], fields[len(fields)-1])
			}
			ev.Body = make([]byte, n)
			if _, err := io.ReadFull(br, ev.Body); err != nil {
				return nil, err
			}
		}
		if len(fields) == 4 && (fields[1] == "nb-open" || fields[1] == "nb-save") {
			// a notebook body (and its path): 64 MB at most
			n, ok := bodyLen(fields[3], NbBodyMax+65536)
			if !ok {
				return nil, fmt.Errorf("a %s length %q", fields[1], fields[3])
			}
			ev.Body = make([]byte, n)
			if _, err := io.ReadFull(br, ev.Body); err != nil {
				return nil, err
			}
		}
		if len(fields) > 1 && fields[1] == "save-actions" {
			if _, err := e.beginSource(id); err != nil {
				return nil, err
			}
		}
		// commit-message (LEN) is followed by a body; "0 cancel ID" ends its context
		if len(fields) == 3 && fields[1] == FeatureCommitMessage {
			n, ok := bodyLen(fields[2], MaxEventBody)
			if !ok {
				return nil, fmt.Errorf("a commit-message length %q", fields[2])
			}
			ev.Body = make([]byte, n)
			if _, err := io.ReadFull(br, ev.Body); err != nil {
				return nil, err
			}
			if _, err := e.beginSource(id); err != nil {
				return nil, err
			}
		}
		if len(fields) > 2 && bodiedRequests[fields[1]] {
			n, ok := bodyLen(fields[len(fields)-1], MaxRequestBody)
			if !ok {
				return nil, fmt.Errorf("a %s request's length %q", fields[1], fields[len(fields)-1])
			}
			ev.Body = make([]byte, n)
			if _, err := io.ReadFull(br, ev.Body); err != nil {
				return nil, err
			}
		}
		e.beginLang(id)
		return ev, nil
	}
	return nil, nil
}

// MaxEventBody is the longest body an edits or action event may announce: rhun sends at most 1 MB
// of edits a frame, and an action's data came in an answer of at most 4 MB.
const MaxEventBody = 4 << 20

// maxReplyBody is the longest body a reply (text, range, ask) is read with.
const maxReplyBody = 1 << 30

// bodyLen is a LEN field as rhun writes it, a plain decimal of 1 to 10 digits, when it is at most
// max.
func bodyLen(s string, max int64) (int, bool) {
	if s == "" || len(s) > 10 {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int64(s[i]-'0')
	}
	if n > max {
		return 0, false
	}
	return int(n), true
}

func (e *Ext) deliver(id int64, res result, ch chan result) {
	e.pmu.Lock()
	current := e.pend[id]
	if current == ch || current == nil {
		delete(e.bodies, id)
		delete(e.replyBodyCaps, id)
		delete(e.outstanding, id)
		if ch != nil && current == ch {
			delete(e.pend, id)
			select {
			case ch <- res:
			default:
			}
		}
	}
	e.pmu.Unlock()
}

func (e *Ext) failPending(err error) {
	e.pmu.Lock()
	if e.closed == nil {
		e.closed = err
	}
	for id, ch := range e.pend {
		select {
		case ch <- result{err: err}:
		default:
		}
		delete(e.pend, id)
	}
	clear(e.bodies)
	clear(e.replyBodyCaps)
	clear(e.outstanding)
	e.pmu.Unlock()
	e.cancelSources(false)
	e.testsGone()
}

func (e *Ext) write(line string, body []byte) error {
	_, err := e.writeContext(context.Background(), line, body)
	return err
}

// Request sends "ID verb args…" (with the body, whose length the caller has put last in args
// when there is one) and waits for the reply.
func (e *Ext) Request(verb string, body []byte, args ...string) (Reply, error) {
	return e.RequestContext(context.Background(), verb, body, args...)
}

// Text returns the document's text and version.
func (e *Ext) Text(doc Doc) ([]byte, int64, error) {
	return e.TextContext(context.Background(), doc)
}

// TextContext is Text with cancellation. A late body is drained before subsequent replies.
func (e *Ext) TextContext(ctx context.Context, doc Doc) ([]byte, int64, error) {
	r, err := e.RequestContext(ctx, "text", nil, itoa(int64(doc)))
	if err != nil {
		return nil, 0, err
	}
	return r.Body, argInt(r, 0), nil
}

// RangeText returns the bytes of a range and the version.
func (e *Ext) RangeText(doc Doc, rg Range) ([]byte, int64, error) {
	r, err := e.Request("range", nil, itoa(int64(doc)), pos(rg.Start), pos(rg.End))
	if err != nil {
		return nil, 0, err
	}
	return r.Body, argInt(r, 0), nil
}

// Replace replaces a range if version is current; it returns the new version.
func (e *Ext) Replace(doc Doc, version int64, rg Range, text []byte) (int64, error) {
	r, err := e.Request("replace", text, itoa(int64(doc)), itoa(version), pos(rg.Start), pos(rg.End), itoa(int64(len(text))))
	if err != nil {
		return 0, err
	}
	return argInt(r, 0), nil
}

// Insert inserts at the cursor, replacing the selection; it returns the new version.
func (e *Ext) Insert(doc Doc, version int64, text []byte) (int64, error) {
	r, err := e.Request("insert", text, itoa(int64(doc)), itoa(version), itoa(int64(len(text))))
	if err != nil {
		return 0, err
	}
	return argInt(r, 0), nil
}

// Select selects a range with the cursor at its end.
func (e *Ext) Select(doc Doc, rg Range) error {
	_, err := e.Request("select", nil, itoa(int64(doc)), pos(rg.Start), pos(rg.End))
	return err
}

// Toast shows a message for two seconds.
func (e *Ext) Toast(text string) error {
	_, err := e.Request("toast", []byte(text), itoa(int64(len(text))))
	return err
}

// Status sets the extension's status-bar text ("" clears it).
func (e *Ext) Status(text string) error {
	_, err := e.Request("status", []byte(text), itoa(int64(len(text))))
	return err
}

// Open opens a file in a tab with the cursor at the position (Pos{} keeps the file's cursor)
// and returns its document id.
func (e *Ext) Open(path string, at Pos) (Doc, error) {
	r, err := e.Request("open", nil, itoa(int64(at.Line)), itoa(int64(at.Col)), path)
	if err != nil {
		return 0, err
	}
	return Doc(argInt(r, 0)), nil
}

// Diagnostics replaces this extension's marks on the document, if version is current. rhun
// keeps 2000 marks per document per extension: more are cut here (the first 2000 are sent).
func (e *Ext) Diagnostics(doc Doc, version int64, ds []Diagnostic) error {
	if len(ds) > 2000 {
		ds = ds[:2000]
	}
	var body bytes.Buffer
	for _, d := range ds {
		msg := strings.ReplaceAll(strings.ReplaceAll(d.Message, "\n", " "), "\r", " ")
		fmt.Fprintf(&body, "%s %s %s %s\n", pos(d.Range.Start), pos(d.Range.End), d.Severity, msg)
	}
	_, err := e.Request("diagnostics", body.Bytes(), itoa(int64(doc)), itoa(version), itoa(int64(len(ds))), itoa(int64(body.Len())))
	return err
}

// Hover shows text at a position the pointer rests on (an answer to the hover event).
func (e *Ext) Hover(doc Doc, at Pos, text string) error {
	_, err := e.Request("hover", []byte(text), itoa(int64(doc)), pos(at), itoa(int64(len(text))))
	return err
}

// Register adds a command for this session: EXTENSION.name in the palette as "title (Extension)"
// with the default keys (space separated, "" for none); it arrives like a manifest command, as a
// Run call, and goes when the extension stops.
func (e *Ext) Register(name, keys, title string) error {
	if keys == "" {
		keys = "-"
	}
	_, err := e.Request("register", nil, name, keys, title)
	return err
}

// Unregister removes one of this extension's runtime commands and its shortcut.
// Manifest commands cannot be removed. Protocol 1.1's commands feature must be agreed.
func (e *Ext) Unregister(name string) error {
	if !e.Has(FeatureCommands) {
		return &Error{Code: "unknown", Text: "commands feature was not agreed"}
	}
	if name == "" || len(name) > 40 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return &Error{Code: "syntax", Text: "invalid command name"}
	}
	_, err := e.Request("unregister", nil, name)
	return err
}

// Option sets a per-document setting (section 6c): tab_width (1-16), insert_spaces,
// trim_trailing_whitespace, final_newline ("yes"/"no"); value "-" removes this extension's override.
func (e *Ext) Option(doc Doc, key, value string) error {
	_, err := e.Request("option", nil, itoa(int64(doc)), key, value)
	return err
}

// Choose shows a pick list with the prompt as its label and returns the index (0-based) of
// the item the user picked. The user dismissing it is an *Error with Code "cancelled"
// (see Cancelled); another prompt being open is "busy".
func (e *Ext) Choose(prompt string, items []string) (int, error) {
	var body bytes.Buffer
	body.WriteString(oneLine(prompt))
	body.WriteByte('\n')
	for _, it := range items {
		body.WriteString(oneLine(it))
		body.WriteByte('\n')
	}
	r, err := e.Request("choose", body.Bytes(), itoa(int64(body.Len())))
	if err != nil {
		return 0, err
	}
	return int(argInt(r, 0)) - 1, nil
}

// Ask shows a text prompt and returns what the user typed ("" when nothing); Esc is "cancelled".
func (e *Ext) Ask(prompt string) (string, error) {
	p := oneLine(prompt)
	r, err := e.Request("ask", []byte(p), itoa(int64(len(p))))
	if err != nil {
		return "", err
	}
	return string(r.Body), nil
}

// Term proposes a command line for rhun's terminal: the user sees it, may edit it, and runs it
// with Enter (nil) or dismisses it (Cancelled). Needs terminal = yes in the manifest.
func (e *Ext) Term(command string) error {
	_, err := e.Request("term", []byte(command), itoa(int64(len(command))))
	return err
}

// Cmd runs one of rhun's own commands, from the list in section 8a of the protocol.
func (e *Ext) Cmd(name string) error {
	_, err := e.Request("cmd", nil, name)
	return err
}

// Cancelled reports whether an error is a prompt the user dismissed.
func Cancelled(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Code == "cancelled"
}

// Busy reports whether an error is a prompt refused because one is already open.
func Busy(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Code == "busy"
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// ParseSetting reads a setting event: one of the extension's own settings ([settings] in its
// manifest) and its value. Each arrives after Ready and again whenever the user changes it.
func ParseSetting(ev Event) (key, value string, ok bool) {
	if ev.Name != "setting" || len(ev.Args) < 1 {
		return "", "", false
	}
	if len(ev.Args) > 1 {
		value = ev.Args[1]
	}
	return ev.Args[0], value, true
}

// Settings keeps an extension's own settings as they arrive: pass every event to Update.
type Settings struct {
	mu     sync.Mutex
	values map[string]string
}

// Update takes a setting event (anything else is ignored) and reports whether it was one.
func (s *Settings) Update(ev Event) bool {
	key, value, ok := ParseSetting(ev)
	if !ok {
		return false
	}
	s.mu.Lock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	s.mu.Unlock()
	return true
}

// String is a setting's value, or def before it has arrived.
func (s *Settings) String(key, def string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.values[key]; ok {
		return v
	}
	return def
}

// Bool is a bool setting ("yes" or "no").
func (s *Settings) Bool(key string, def bool) bool {
	d := "no"
	if def {
		d = "yes"
	}
	return s.String(key, d) == "yes"
}

// Int is an int setting.
func (s *Settings) Int(key string, def int) int {
	n, err := strconv.Atoi(s.String(key, strconv.Itoa(def)))
	if err != nil {
		return def
	}
	return n
}

// ParseDoc reads the document id of an event whose first field is one: active, saved,
// changed, cursor, selection, closed, edits, resync, hover.
func ParseDoc(ev Event) (Doc, bool) {
	if len(ev.Args) < 1 {
		return 0, false
	}
	d, err := strconv.ParseInt(ev.Args[0], 10, 64)
	return Doc(d), err == nil
}

// ParseVersion reads a saved, changed, edits or resync event: the document and its version.
func ParseVersion(ev Event) (Doc, int64, bool) {
	if len(ev.Args) < 2 {
		return 0, 0, false
	}
	d, err1 := strconv.ParseInt(ev.Args[0], 10, 64)
	v, err2 := strconv.ParseInt(ev.Args[1], 10, 64)
	return Doc(d), v, err1 == nil && err2 == nil
}

// ParseCursor reads a cursor event: the document and the cursor position.
func ParseCursor(ev Event) (Doc, Pos, bool) {
	if ev.Name != "cursor" {
		return 0, Pos{}, false
	}
	return parseDocPos(ev)
}

// ParseSelection reads a selection event: the document and the range (anchor to cursor).
func ParseSelection(ev Event) (Doc, Range, bool) {
	if ev.Name != "selection" || len(ev.Args) < 5 {
		return 0, Range{}, false
	}
	d, err := strconv.ParseInt(ev.Args[0], 10, 64)
	a, ok1 := parsePos(ev.Args[1], ev.Args[2])
	b, ok2 := parsePos(ev.Args[3], ev.Args[4])
	return Doc(d), Range{Start: a, End: b}, err == nil && ok1 && ok2
}

func parseDocPos(ev Event) (Doc, Pos, bool) {
	if len(ev.Args) < 3 {
		return 0, Pos{}, false
	}
	d, err := strconv.ParseInt(ev.Args[0], 10, 64)
	p, ok := parsePos(ev.Args[1], ev.Args[2])
	return Doc(d), p, err == nil && ok
}

func parsePos(line, col string) (Pos, bool) {
	l, err1 := strconv.Atoi(line)
	c, err2 := strconv.Atoi(col)
	return Pos{Line: l, Col: c}, err1 == nil && err2 == nil
}

// ParseHover reads a hover event: the document and the position the pointer rests on.
func ParseHover(ev Event) (Doc, Pos, bool) {
	if ev.Name != "hover" || len(ev.Args) < 3 {
		return 0, Pos{}, false
	}
	d, _ := strconv.ParseInt(ev.Args[0], 10, 64)
	l, _ := strconv.Atoi(ev.Args[1])
	c, _ := strconv.Atoi(ev.Args[2])
	return Doc(d), Pos{l, c}, true
}

// Logf writes to the extension's log (its standard error).
func (e *Ext) Logf(format string, args ...any) {
	if e.logw != nil {
		fmt.Fprintf(e.logw, format+"\n", args...)
	}
}

// ParseOpened reads an opened event.
func ParseOpened(ev Event) (Opened, bool) {
	if ev.Name != "opened" || len(ev.Args) < 4 {
		return Opened{}, false
	}
	d, _ := strconv.ParseInt(ev.Args[0], 10, 64)
	v, _ := strconv.ParseInt(ev.Args[1], 10, 64)
	o := Opened{Doc: Doc(d), Version: v}
	if ev.Args[2] != "-" {
		o.Lang = ev.Args[2]
	}
	if ev.Args[3] != "-" {
		o.Path = ev.Args[3]
	}
	return o, true
}

// WholeRange is the range of a whole text: from 1 1 to the position after its last byte.
func WholeRange(text []byte) Range {
	lines := 1
	last := 0
	for i, b := range text {
		if b == '\n' {
			lines++
			last = i + 1
		}
	}
	return Range{Start: Pos{1, 1}, End: Pos{lines, len(text) - last + 1}}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
func pos(p Pos) string    { return strconv.Itoa(p.Line) + " " + strconv.Itoa(p.Col) }
func argInt(r Reply, i int) int64 {
	if i < len(r.Args) {
		v, _ := strconv.ParseInt(r.Args[i], 10, 64)
		return v
	}
	return 0
}
