package main

// Notifications and progress (protocol 1.1, features notify and progress): the checker reads
// every card and progress request against the grammar and the limits, answers them as rhun does
// (a NOTE or P that is gone is noview), and later presses a button without a command,
// closes the other cards and presses Cancel on cancellable progress.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type noteState struct {
	pressable int // a button without a command (1-based), 0 none
}

type progState struct {
	cancel bool
}

var noteSeverities = map[string]bool{"info": true, "warning": true, "error": true}

// checkNoteBody reads MSG_LEN EXTRA_LEN LF MSG EXTRA: the first button without a command, or why not
func (h *host) checkNoteBody(body []byte) (int, string) {
	nl := bytes.IndexByte(body, '\n')
	if nl < 0 {
		return 0, "the body is MSG_LEN EXTRA_LEN LF, the message and the records"
	}
	head := strings.Fields(string(body[:nl]))
	if len(head) != 2 {
		return 0, "the body's first line is MSG_LEN EXTRA_LEN"
	}
	ml, e1 := strconv.Atoi(head[0])
	xl, e2 := strconv.Atoi(head[1])
	rest := body[nl+1:]
	if e1 != nil || e2 != nil || ml < 0 || xl < 0 || ml+xl != len(rest) {
		return 0, "MSG_LEN and EXTRA_LEN do not take the body's bytes exactly"
	}
	if ml > 2000 {
		h.warn("notify", "a message over 2000 bytes is cut")
	}
	x := rest[ml:]
	var hasCmd []bool // per button kept
	for len(x) > 0 {
		f, data, r, why := readRecord(x)
		if why != "" {
			return 0, "a record: " + why
		}
		x = r
		switch f[0] {
		case "detail":
			if len(data) > 1000 {
				h.warn("notify", "a detail over 1000 bytes is cut")
			}
		case "sticky":
		case "button":
			if len(hasCmd) == 3 {
				h.warn("notify", "more than 3 buttons: rhun leaves the rest out")
				continue
			}
			hasCmd = append(hasCmd, false)
		case "command":
			if len(hasCmd) == 0 || hasCmd[len(hasCmd)-1] {
				h.warn("notify", "a command record that follows no button is ignored")
				continue
			}
			hasCmd[len(hasCmd)-1] = true
			if len(f) < 3 {
				return 0, "command NAME ARGS_LEN"
			}
			known := false
			for _, c := range h.m.commands {
				if c == f[1] {
					known = true
				}
			}
			for _, c := range h.rtcmds {
				if strings.HasSuffix(c, "."+f[1]) {
					known = true
				}
			}
			if !known {
				h.warn("notify", fmt.Sprintf("a button's command %q is not one of its commands: pressing it shows a toast", f[1]))
			}
		default:
			h.warn("notify", fmt.Sprintf("unknown record %q is skipped", f[0]))
		}
	}
	for i, c := range hasCmd {
		if !c {
			return i + 1, ""
		}
	}
	return 0, ""
}

// notifyRequest answers notify, notify-set, unnotify, progress, progress-set and progress-end
func (h *host) notifyRequest(id int64, verb string, args []string, body []byte, reply func(string), rerr func(string, string)) {
	feat := "notify"
	if strings.HasPrefix(verb, "progress") {
		feat = "progress"
	}
	if !h.features[feat] {
		h.fail("features", fmt.Sprintf("%s without %s agreed after hello (hello 1 %s)", verb, feat, feat))
		rerr("unknown", "no such request")
		return
	}
	if h.notes == nil {
		h.notes, h.progs = map[int64]*noteState{}, map[int64]*progState{}
	}
	num := func(s string) (int64, bool) { n, err := strconv.ParseInt(s, 10, 64); return n, err == nil }
	syntax := func(why string) {
		h.fail(feat, fmt.Sprintf("%s: %s", verb, why))
		rerr("syntax", "cannot parse the line")
	}
	switch verb {
	case "notify":
		if len(args) != 2 || !noteSeverities[args[0]] {
			syntax("notify info|warning|error LEN")
			return
		}
		pressable, why := h.checkNoteBody(body)
		if why != "" {
			syntax(why)
			return
		}
		if len(h.notes) >= 16 {
			rerr("toobig", "16 notifications")
			return
		}
		h.noteSeq++
		h.notes[h.noteSeq] = &noteState{pressable: pressable}
		reply(fmt.Sprintf("ok %d %d", id, h.noteSeq))
	case "notify-set":
		n, ok := num(firstArg(args))
		if len(args) != 2 || !ok {
			syntax("notify-set NOTE LEN")
			return
		}
		pressable, why := h.checkNoteBody(body)
		if why != "" {
			syntax(why)
			return
		}
		st := h.notes[n]
		if st == nil {
			rerr("noview", "no such notification")
			return
		}
		st.pressable = pressable
		reply(fmt.Sprintf("ok %d", id))
	case "unnotify":
		n, ok := num(firstArg(args))
		if len(args) != 1 || !ok {
			syntax("unnotify NOTE")
			return
		}
		if h.notes[n] == nil {
			rerr("noview", "no such notification")
			return
		}
		delete(h.notes, n)
		reply(fmt.Sprintf("ok %d", id))
	case "progress":
		if len(args) != 3 || (args[1] != "cancel" && args[1] != "-") {
			syntax("progress status|note|view:VIEW cancel|- LEN")
			return
		}
		where := args[0]
		if strings.HasPrefix(where, "view:") {
			v, ok := num(where[5:])
			if !ok || h.rviews[v] == nil {
				syntax(fmt.Sprintf("%s is not one of its retained views", where))
				return
			}
		} else if where != "status" && where != "note" {
			syntax("WHERE is status, note or view:VIEW")
			return
		}
		if len(body) == 0 || bytes.IndexByte(body, '\n') >= 0 {
			syntax("the title is one line, not empty")
			return
		}
		if len(body) > 200 {
			h.warn("progress", "a title over 200 bytes is cut")
		}
		if len(h.progs) >= 8 {
			rerr("toobig", "8 progress")
			return
		}
		h.noteSeq++
		h.progs[h.noteSeq] = &progState{cancel: args[1] == "cancel"}
		reply(fmt.Sprintf("ok %d %d", id, h.noteSeq))
	case "progress-set":
		pn, ok := num(firstArg(args))
		if len(args) != 3 || !ok {
			syntax("progress-set P PERCENT LEN")
			return
		}
		if h.progs[pn] == nil {
			rerr("noview", "no such progress")
			return
		}
		if args[1] != "-" {
			pct, ok := num(args[1])
			if !ok || pct < 0 || pct > 100 {
				syntax("PERCENT is 0-100 or -")
				return
			}
		}
		reply(fmt.Sprintf("ok %d", id))
	case "progress-end":
		n, ok := num(firstArg(args))
		if len(args) != 1 || !ok {
			syntax("progress-end P")
			return
		}
		if h.progs[n] == nil {
			rerr("noview", "no such progress")
			return
		}
		delete(h.progs, n)
		reply(fmt.Sprintf("ok %d", id))
	}
}

func firstArg(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

// checkNotify presses a button without a command on each card that has one, closes the others,
// and presses Cancel on cancellable progress
func (h *host) checkNotify() {
	if len(h.notes) == 0 && len(h.progs) == 0 {
		return
	}
	for n, st := range h.notes {
		if st.pressable > 0 {
			h.write(fmt.Sprintf("0 notify %d pressed %d\n", n, st.pressable))
		} else {
			h.write(fmt.Sprintf("0 notify %d closed\n", n))
		}
		delete(h.notes, n)
	}
	cancelled := 0
	for p, st := range h.progs {
		if st.cancel {
			h.write(fmt.Sprintf("0 progress %d cancel\n", p))
			cancelled++
		}
	}
	h.serve(500 * time.Millisecond)
	if h.cmdExited() {
		h.fail("notify", "the program exited on a notification or progress event")
		return
	}
	h.ok("takes pressed and closed events of its cards and Cancel of its progress")
	if len(h.progs) > 0 && cancelled < len(h.progs) {
		h.warn("progress", fmt.Sprintf("%d progress entries never ended: rhun shows them until the extension stops", len(h.progs)-cancelled))
	}
}
