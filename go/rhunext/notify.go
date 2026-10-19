package rhunext

// Notifications and progress (protocol 1.1, features notify and progress): cards at the bottom
// right with up to 3 buttons, and progress in the status bar, in a card or in one of the
// extension's views.

import (
	"strconv"
	"strings"
)

// FeatureNotify is notification cards: Notify, NotifySet, Unnotify, NoteListener.
const FeatureNotify = "notify"

// FeatureProgress is progress: Progress, ProgressSet, ProgressEnd, ProgressCanceller.
const FeatureProgress = "progress"

// Card severities.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// Where progress shows: ProgressStatus, ProgressNote, or ProgressView(v).
const (
	ProgressStatus = "status"
	ProgressNote   = "note"
)

// ProgressView is progress shown as a bar at the top of one of the extension's views.
func ProgressView(v View) string { return "view:" + strconv.FormatInt(int64(v), 10) }

type noteButton struct {
	title   string
	command string // "" for a button that reports NoteEvent pressed
	args    []byte
}

// Note is a notification's content.
type Note struct {
	msg     string
	detail  string
	sticky  bool
	buttons []noteButton
}

func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !isRuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// NewNote is a card with that message (at most 2000 bytes; LF breaks a line).
func NewNote(msg string) *Note { return &Note{msg: cutUTF8(msg, 2000)} }

// Detail is a muted line under the message.
func (n *Note) Detail(s string) *Note { n.detail = viewLine(s, 1000); return n }

// Sticky keeps it until closed (info cards go after 8 s, warnings after 12 s otherwise).
func (n *Note) Sticky() *Note { n.sticky = true; return n }

// Button adds a button that reports NoteEvent pressed (3 at most).
func (n *Note) Button(title string) *Note {
	if len(n.buttons) < 3 {
		n.buttons = append(n.buttons, noteButton{title: viewLine(title, 60)})
	}
	return n
}

// ButtonCommand adds a button that runs one of the extension's commands with args.
func (n *Note) ButtonCommand(title, command string, args []byte) *Note {
	if len(n.buttons) < 3 {
		n.buttons = append(n.buttons, noteButton{title: viewLine(title, 60), command: command, args: args})
	}
	return n
}

// Bytes is the body of notify and notify-set.
func (n *Note) Bytes() []byte {
	var extra []byte
	if n.detail != "" {
		extra = viewRecord(extra, "detail", []byte(n.detail))
	}
	if n.sticky {
		extra = viewRecord(extra, "sticky", nil)
	}
	for _, b := range n.buttons {
		extra = viewRecord(extra, "button", []byte(b.title))
		if b.command != "" {
			extra = viewRecord(extra, "command "+b.command, b.args)
		}
	}
	out := []byte(strconv.Itoa(len(n.msg)) + " " + strconv.Itoa(len(extra)) + "\n")
	out = append(out, n.msg...)
	return append(out, extra...)
}

// Notify shows a card; its NOTE.
func (e *Ext) Notify(severity string, n *Note) (int64, error) {
	body := n.Bytes()
	r, err := e.Request("notify", body, severity, itoa(int64(len(body))))
	if err != nil {
		return 0, err
	}
	return argInt(r, 0), nil
}

// NotifySet replaces a card's content (noview when it is gone: closed by the user, timed out).
func (e *Ext) NotifySet(note int64, n *Note) error {
	body := n.Bytes()
	_, err := e.Request("notify-set", body, itoa(note), itoa(int64(len(body))))
	return err
}

// Unnotify closes a card without an event.
func (e *Ext) Unnotify(note int64) error {
	_, err := e.Request("unnotify", nil, itoa(note))
	return err
}

// Progress starts progress (nothing shows if it ends within 500 ms); cancel offers a Cancel
// button. Its P.
func (e *Ext) Progress(where string, cancel bool, title string) (int64, error) {
	c := "-"
	if cancel {
		c = "cancel"
	}
	t := viewLine(title, 200)
	if t == "" {
		t = "…"
	}
	r, err := e.Request("progress", []byte(t), where, c, itoa(int64(len(t))))
	if err != nil {
		return 0, err
	}
	return argInt(r, 0), nil
}

// ProgressSet sets its share done (percent -1: indeterminate) and a message.
func (e *Ext) ProgressSet(p int64, percent int, msg string) error {
	pct := "-"
	if percent >= 0 {
		if percent > 100 {
			percent = 100
		}
		pct = strconv.Itoa(percent)
	}
	m := viewLine(strings.ReplaceAll(msg, "\n", " "), 200)
	_, err := e.Request("progress-set", []byte(m), itoa(p), pct, itoa(int64(len(m))))
	return err
}

// ProgressEnd removes it.
func (e *Ext) ProgressEnd(p int64) error {
	_, err := e.Request("progress-end", nil, itoa(p))
	return err
}

// NoteEvent is what the user did with a card: Pressed is button N (from 1) without a command;
// Closed: closed by the user, a timeout or Clear All.
type NoteEvent struct {
	Note    int64
	Pressed int
	Closed  bool
}

// NoteListener receives "0 notify NOTE pressed N" and "0 notify NOTE closed".
type NoteListener interface {
	NoteEvent(ext *Ext, ev NoteEvent)
}

// ProgressCanceller receives "0 progress P cancel": rhun removes it 10 s later unless
// ProgressEnd comes first.
type ProgressCanceller interface {
	ProgressCancel(ext *Ext, p int64)
}

func parseNoteEvent(ev Event) (NoteEvent, bool) {
	if len(ev.Args) < 2 {
		return NoteEvent{}, false
	}
	id, err := strconv.ParseInt(ev.Args[0], 10, 64)
	if err != nil {
		return NoteEvent{}, false
	}
	switch ev.Args[1] {
	case "pressed":
		if len(ev.Args) < 3 {
			return NoteEvent{}, false
		}
		n, err := strconv.Atoi(ev.Args[2])
		if err != nil {
			return NoteEvent{}, false
		}
		return NoteEvent{Note: id, Pressed: n}, true
	case "closed":
		return NoteEvent{Note: id, Closed: true}, true
	}
	return NoteEvent{}, false
}

func parseProgressCancel(ev Event) (int64, bool) {
	if len(ev.Args) < 2 || ev.Args[1] != "cancel" {
		return 0, false
	}
	id, err := strconv.ParseInt(ev.Args[0], 10, 64)
	return id, err == nil
}
