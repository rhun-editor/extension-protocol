package main

// Protocol 1.1 commands: with command-args agreed every run carries FROM and a body (the checker
// also runs a command as a key binding and as a command reference would, with a body), and a
// [command-options] callable command is called once as another extension would; run-command,
// set-context (context-keys) and status-item / status-remove (status-items) are checked against
// their grammar and limits and answered.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

// runLine is how rhun invokes a command from the palette: v1's "0 run EXT.NAME DOC", with
// command-args "0 run EXT.NAME DOC palette 0".
func (h *host) runLine(full string, doc int64) string {
	if h.features["command-args"] {
		return fmt.Sprintf("0 run %s %d palette 0\n", full, doc)
	}
	return fmt.Sprintf("0 run %s %d\n", full, doc)
}

func validLocalName(s string) bool {
	return s != "" && len(s) <= 40 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
}

// ownCommand: NAME as run-command names it is one of the extension's own commands
func (h *host) ownCommand(name string) bool {
	if rest, ok := strings.CutPrefix(name, h.m.owner+"/"); ok {
		name = rest
	}
	local, ok := strings.CutPrefix(name, h.m.name+".")
	if !ok {
		return false
	}
	for _, c := range h.m.commands {
		if c == local {
			return true
		}
	}
	for _, c := range h.rtcmds {
		if c == h.m.name+"."+local {
			return true
		}
	}
	return false
}

// commandRequest answers run-command, set-context, status-item and status-remove
func (h *host) commandRequest(id int64, verb string, args []string, msg message, reply func(string), rerr func(string, string)) {
	feature := map[string]string{"run-command": "command-args", "set-context": "context-keys",
		"status-item": "status-items", "status-remove": "status-items"}[verb]
	if !h.features[feature] {
		h.fail("features", fmt.Sprintf("%s without %s agreed after hello (hello 1 %s)", verb, feature, feature))
		rerr("unknown", "no such request")
		return
	}
	bad := func(why string) {
		h.fail("request grammar", verb+": "+why)
		rerr("syntax", "cannot parse the line")
	}
	ok := func() { reply(fmt.Sprintf("ok %d", id)) }
	body := func(lenField string, max int) ([]byte, bool) {
		n, err := strconv.ParseInt(lenField, 10, 64)
		if err != nil || n < 0 || msg.err != nil || int64(len(msg.body)) != n {
			bad(fmt.Sprintf("LEN %q does not match the body", lenField))
			return nil, false
		}
		if n > int64(max) {
			h.fail("limits", fmt.Sprintf("%s: a body of %d bytes, more than %d", verb, n, max))
			rerr("toobig", fmt.Sprintf("%d bytes", max))
			return nil, false
		}
		return msg.body, true
	}
	switch verb {
	case "run-command":
		if len(args) != 2 || args[0] == "" {
			bad("run-command takes NAME LEN")
			return
		}
		a, good := body(args[1], rhunext.MaxRunArgs)
		if !good {
			return
		}
		name := strings.TrimPrefix(args[0], "rhun.")
		switch _, builtin := cmdAllowed[name]; {
		case h.ownCommand(args[0]):
			h.warn("run-command", args[0]+" is the extension's own command: rhun refuses it (call the handler directly)")
			rerr("refused", "its own command")
		case builtin:
			if len(a) > 0 && !map[string]bool{"goto_line": true, "find": true, "replace": true, "find_in_files": true,
				"quick_open": true, "command_palette": true}[name] {
				bad(name + " takes no args")
				return
			}
			if len(a) > 1024 || bytes.ContainsAny(a, "\r\n") {
				bad(name + ": args are one line of at most 1024 bytes")
				return
			}
			h.cmds = append(h.cmds, name)
			reply(fmt.Sprintf("ok %d 0", id))
		default:
			h.note("run-command %s: the checker knows no other extensions", args[0])
			rerr("refused", "no such command")
		}
	case "set-context":
		// the value is the rest of the line: fields may hold single spaces
		line := msg.line
		parts := strings.SplitN(line, " ", 4)
		if len(parts) != 4 || parts[3] == "" {
			bad("set-context takes KEY VALUE")
			return
		}
		key, value := parts[2], parts[3]
		if key == "" || len(key) > 40 || key[0] < 'a' || key[0] > 'z' ||
			strings.Trim(key, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
			bad(fmt.Sprintf("KEY %q is not a lowercase letter, then letters, digits and _ (at most 40)", key))
			return
		}
		if len(value) > rhunext.MaxContextValue {
			h.fail("limits", "set-context: a value over 256 bytes")
			rerr("toobig", "256 bytes")
			return
		}
		if !rhunext.PlainText(value) {
			bad("VALUE is not one line of plain UTF-8 text")
			return
		}
		h.contextKeys[key] = value != "-"
		if len(h.contextKeys) > 64 {
			h.fail("limits", "more than 64 context keys")
			rerr("toobig", "64 keys")
			return
		}
		ok()
	case "status-item":
		if len(args) != 2 || !validLocalName(args[0]) {
			bad("status-item takes ITEM LEN, ITEM 1 to 40 of a-z 0-9 _")
			return
		}
		b, good := body(args[1], 1<<20)
		if !good {
			return
		}
		if why := checkStatusBody(b); why != "" {
			bad(why)
			return
		}
		if !h.statusItems[args[0]] && len(h.statusItems)+h.m.statusItems >= 8 {
			h.fail("limits", "a ninth status item")
			rerr("toobig", "8 items")
			return
		}
		h.statusItems[args[0]] = true
		ok()
	case "status-remove":
		if len(args) != 1 || !validLocalName(args[0]) {
			bad("status-remove takes ITEM")
			return
		}
		delete(h.statusItems, args[0])
		ok()
	}
}

// checkStatusBody: ALIGN PRIORITY TEXT_LEN TOOLTIP_LEN EXTRA_LEN LF and the bytes; "" when good
func checkStatusBody(b []byte) string {
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return "the body has no header line"
	}
	f := strings.Split(string(b[:nl]), " ")
	if len(f) != 5 || (f[0] != "left" && f[0] != "right") {
		return fmt.Sprintf("header %q is not ALIGN PRIORITY TEXT_LEN TOOLTIP_LEN EXTRA_LEN", b[:nl])
	}
	prio, err := strconv.Atoi(f[1])
	if err != nil || prio < -1000 || prio > 1000 {
		return "PRIORITY is not -1000 to 1000"
	}
	var n [3]int
	for i := range n {
		if n[i], err = strconv.Atoi(f[2+i]); err != nil || n[i] < 0 {
			return "a length is not a number"
		}
	}
	rest := b[nl+1:]
	if n[0]+n[1]+n[2] != len(rest) {
		return "the three lengths do not use the body exactly"
	}
	text, tip, extra := rest[:n[0]], rest[n[0]:n[0]+n[1]], rest[n[0]+n[1]:]
	if len(text) > rhunext.MaxStatusText || !rhunext.PlainText(string(text)) {
		return "TEXT is not one line of at most 120 bytes"
	}
	if len(tip) > rhunext.MaxStatusTooltip {
		return "TOOLTIP is over 500 bytes"
	}
	for len(extra) > 0 {
		nl := bytes.IndexByte(extra, '\n')
		if nl < 0 {
			return "an extra record without a line end"
		}
		rf := strings.Split(string(extra[:nl]), " ")
		ln, err := strconv.Atoi(rf[len(rf)-1])
		if len(rf) < 2 || err != nil || ln < 0 || ln > len(extra)-nl-1 {
			return fmt.Sprintf("extra record %q: its length", extra[:nl])
		}
		data := extra[nl+1 : nl+1+ln]
		switch rf[0] {
		case "command":
			if len(rf) != 3 || !validLocalName(rf[1]) || ln > rhunext.MaxRefArgs {
				return "a command record is command NAME LEN, NAME 1 to 40 of a-z 0-9 _, at most 8192 bytes"
			}
		case "color":
			if len(rf) != 3 || ln != 0 || !map[string]bool{"fg": true, "muted": true, "accent": true, "error": true,
				"warning": true, "success": true}[rf[1]] {
				return "color NAME 0: fg, muted, accent, error, warning or success"
			}
		case "background":
			if len(rf) != 3 || ln != 0 || (rf[1] != "error" && rf[1] != "warning") {
				return "background NAME 0: error or warning"
			}
		case "when":
			if len(rf) != 2 || bytes.ContainsAny(data, "\r\n") {
				return "when LEN and one line"
			}
		}
		extra = extra[nl+1+ln:]
	}
	return ""
}

// checkCommands: with command-args agreed, a run as a key binding and as a command reference
// would send it (with bodies), and a call of each callable command
func (h *host) checkCommands() {
	for k := range h.m.cmdOptions {
		name, field, _ := strings.Cut(k, ".")
		known := false
		for _, c := range h.m.commands {
			known = known || c == name
		}
		if !known {
			h.fail("manifest", fmt.Sprintf("[command-options] %s: no command %s in [commands]", k, name))
		}
		if !map[string]bool{"icon": true, "when": true, "palette": true, "callable": true}[field] {
			h.warn("manifest", fmt.Sprintf("[command-options] %s: not NAME.icon, .when, .palette or .callable", k))
		}
	}
	if (h.m.statusItems > 0) && !h.features["status-items"] {
		h.note("[status] items show without status-items; runtime items need it")
	}
	if !h.features["command-args"] || len(h.m.commands) == 0 {
		for k, v := range h.m.cmdOptions {
			if strings.HasSuffix(k, ".callable") && v == "yes" {
				h.fail("command-args", k+" = yes without command-args agreed after hello: rhun refuses every call")
			}
		}
		return
	}
	c := h.m.name + "." + h.m.commands[0]
	for _, run := range []string{"0 run %s 1 key 4\nfast", "0 run %s 1 ref 15\nopaque\x00bytes\n:)"} {
		before := len(h.requests)
		h.activate(1)
		h.write(fmt.Sprintf(run, c))
		h.serve(700 * time.Millisecond)
		h.note("command %s with a body: %v", c, h.requests[before:])
		if h.cmdExited() {
			h.fail("command-args", c+": the program exited after a run with a body")
			return
		}
	}
	h.ok("takes runs with FROM and a body")
	for _, name := range h.m.commands {
		if h.m.cmdOptions[name+".callable"] != "yes" {
			continue
		}
		args := []byte("checker args")
		m, ok := h.askBody(fmt.Sprintf("call %s.%s 1 checker/caller", h.m.name, name), args, 10*time.Second)
		switch f := strings.Fields(m.line); {
		case !ok:
			h.fail("call", name+": no answer to a call within 10 s (rhun cancels it)")
		case len(f) > 0 && f[0] == "error":
			h.note("call %s answered %s", name, m.line)
			h.ok("answers a call of " + name)
		case len(m.body) > rhunext.MaxCallResult:
			h.fail("call", name+": a result over 1 MiB (rhun answers toobig)")
		default:
			h.ok("answers a call of " + name)
		}
	}
}
