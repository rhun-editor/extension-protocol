// Package rhunext speaks rhun's extension protocol, version 1 (../../v1/v1.md), for a program
// started by rhun: it reads the events rhun writes, lets the extension send requests and wait for
// their replies, and takes care of the framing (lines, ids, byte-counted bodies).
//
// Usage: implement Handler, then call Run. Events arrive one at a time on a single goroutine;
// requests may be made from any goroutine, including from inside an event handler, and block
// until rhun answers.
package rhunext

import (
	"bufio"
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
	Name string   // ready, opened, active, saved, changed, cursor, selection, closed, project, run, bye
	Args []string // the fields after the name; the last may contain spaces
}

// Handler receives events. Methods are called on one goroutine, in order.
type Handler interface {
	// Ready is the first event: the project folder, or "" without one.
	Ready(ext *Ext, project string)
	// Run is one of the extension's commands, with the active document or 0.
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

// Error is a reply with an error code.
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
	w      io.Writer
	wmu    sync.Mutex
	nextID atomic.Int64
	pmu    sync.Mutex
	pend   map[int64]chan result
	bodies map[int64]bool // request ids whose ok reply carries a body (text, range)
	logw   io.Writer
	done   chan struct{}
}

type result struct {
	reply Reply
	err   error
}

// Run talks to rhun over stdin and stdout until rhun says bye or closes the pipe. It returns
// nil on bye, io.EOF when rhun went away, or the protocol error that ended the exchange.
func Run(h Handler) error {
	return RunOn(os.Stdin, os.Stdout, os.Stderr, h)
}

// RunOn is Run on explicit streams (tests).
func RunOn(r io.Reader, w io.Writer, logw io.Writer, h Handler) error {
	ext := &Ext{w: w, pend: map[int64]chan result{}, bodies: map[int64]bool{}, logw: logw, done: make(chan struct{})}
	if _, err := fmt.Fprintf(w, "hello %d\n", Protocol); err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 1<<16)
	events := make(chan Event, 64)
	errc := make(chan error, 1)
	go func() {
		defer close(events)
		for {
			ev, err := ext.readMessage(br)
			if err != nil {
				errc <- err
				return
			}
			if ev != nil {
				events <- *ev
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
			close(ext.done)
			ext.failPending(errors.New("rhun said bye"))
			return nil
		default:
			h.Event(ext, ev)
		}
	}
	err := <-errc
	close(ext.done)
	ext.failPending(err)
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
			// a body follows when the request was text or range: the waiter says so
			e.pmu.Lock()
			ch, hasBody := e.pend[id], false
			if ch != nil {
				hasBody = e.bodies[id]
				delete(e.bodies, id)
			}
			e.pmu.Unlock()
			if hasBody && len(res.reply.Args) >= 1 {
				n, _ := strconv.Atoi(res.reply.Args[len(res.reply.Args)-1])
				body := make([]byte, n)
				if _, err := io.ReadFull(br, body); err != nil {
					return nil, err
				}
				res.reply.Body = body
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
		delete(e.bodies, id)
		e.pmu.Unlock()
		e.deliver(id, res, ch)
		return nil, nil
	case "0":
		if len(fields) < 2 {
			return nil, nil
		}
		ev := &Event{Name: fields[1], Args: fields[2:]}
		// the last field of opened and project may contain spaces: fields are rejoined there
		switch ev.Name {
		case "opened":
			if len(ev.Args) > 4 {
				ev.Args = append(ev.Args[:3], strings.Join(ev.Args[3:], " "))
			}
		case "project":
			if len(ev.Args) > 1 {
				ev.Args = []string{strings.Join(ev.Args, " ")}
			}
		}
		return ev, nil
	}
	// a request from rhun (none in v1): unknown
	if id, perr := strconv.ParseInt(fields[0], 10, 64); perr == nil && id > 0 {
		e.write(fmt.Sprintf("error %d unknown no such request\n", id), nil)
	}
	return nil, nil
}

func (e *Ext) deliver(id int64, res result, ch chan result) {
	e.pmu.Lock()
	delete(e.pend, id)
	e.pmu.Unlock()
	if ch != nil {
		ch <- res
	}
}

func (e *Ext) failPending(err error) {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	for id, ch := range e.pend {
		ch <- result{err: err}
		delete(e.pend, id)
	}
}

func (e *Ext) write(line string, body []byte) error {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	if _, err := io.WriteString(e.w, line); err != nil {
		return err
	}
	if len(body) > 0 {
		if _, err := e.w.Write(body); err != nil {
			return err
		}
	}
	return nil
}

// Request sends "ID verb args…" (with the body, whose length the caller has put last in args
// when there is one) and waits for the reply.
func (e *Ext) Request(verb string, body []byte, args ...string) (Reply, error) {
	id := e.nextID.Add(1)
	if id > 2147483647 {
		e.nextID.Store(0)
		id = e.nextID.Add(1)
	}
	ch := make(chan result, 1)
	e.pmu.Lock()
	e.pend[id] = ch
	if verb == "text" || verb == "range" {
		e.bodies[id] = true
	}
	e.pmu.Unlock()
	line := strconv.FormatInt(id, 10) + " " + verb
	if len(args) > 0 {
		line += " " + strings.Join(args, " ")
	}
	if err := e.write(line+"\n", body); err != nil {
		e.deliver(id, result{}, nil)
		return Reply{}, err
	}
	select {
	case res := <-ch:
		return res.reply, res.err
	case <-e.done:
		return Reply{}, errors.New("rhun went away")
	}
}

// Text returns the document's text and version.
func (e *Ext) Text(doc Doc) ([]byte, int64, error) {
	r, err := e.Request("text", nil, itoa(int64(doc)))
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
