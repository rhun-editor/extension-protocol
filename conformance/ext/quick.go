package main

// Quick input (protocol 1.1, feature quick-input): the checker reads quick, quick-set and
// quick-close against the step body's grammar and the limits, answers them as rhun does (one
// session at a time: busy; a Q that is gone: noview), and plays a session: the field's value, an
// accept, then hide.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

var quickFlags = map[string]bool{"multi": true, "back": true, "busy": true, "nofilter": true, "match-desc": true,
	"match-detail": true, "password": true, "-": true}

// checkQuickBody reads a step body: "" or why not, and whether it is past a limit
func checkQuickBody(body []byte) (string, bool) {
	if len(body) > 2<<20 {
		return "", true
	}
	for len(body) > 0 {
		f, data, rest, why := readRecord(body)
		if why != "" {
			return "a record: " + why, false
		}
		body = rest
		switch f[0] {
		case "title", "placeholder", "prompt":
			if len(f) != 2 || bytes.ContainsAny(data, "\n") {
				return f[0] + " LEN + one line", false
			}
		case "step":
			if len(f) != 4 || len(data) != 0 {
				return "step N M 0", false
			}
		case "value":
			if len(f) != 3 || bytes.ContainsAny(data, "\n") {
				return "value SEQ LEN + one line", false
			}
		case "mode":
			if len(f) != 3 || (f[1] != "pick" && f[1] != "input") {
				return "mode pick|input 0", false
			}
		case "flags":
			for _, w := range f[1 : len(f)-1] {
				if !quickFlags[w] {
					return fmt.Sprintf("flags: %q is not multi, back, busy, nofilter, match-desc, match-detail, password or -", w), false
				}
			}
		case "msg":
			if len(f) != 4 || !noteSeverities[f[2]] {
				return "msg SEQ info|warning|error LEN", false
			}
		case "buttons":
			n, err := strconv.Atoi(f[1])
			if len(f) != 3 || err != nil || n < 0 {
				return "buttons N LEN", false
			}
			if n > 8 {
				return "", true
			}
			for i := 0; i < n; i++ {
				bf, _, r, why := readRecord(data)
				if why != "" || len(bf) != 3 {
					return "a title button is KEY ICON TIP_LEN, LF, the tooltip", false
				}
				data = r
			}
		case "items":
			n, err := strconv.Atoi(f[1])
			if len(f) != 3 || err != nil || n < 0 {
				return "items N LEN", false
			}
			if n > 10000 {
				return "", true
			}
			for i := 0; i < n; i++ {
				nl := bytes.IndexByte(data, '\n')
				if nl < 0 {
					return "an item is ID_LEN LABEL_LEN EXTRA_LEN KIND, LF, the id, label and records", false
				}
				h := strings.Split(string(data[:nl]), " ")
				if len(h) != 4 || (h[3] != "item" && h[3] != "sep") {
					return fmt.Sprintf("item %q: ID_LEN LABEL_LEN EXTRA_LEN item|sep", string(data[:nl])), false
				}
				il, e1 := strconv.Atoi(h[0])
				ll, e2 := strconv.Atoi(h[1])
				xl, e3 := strconv.Atoi(h[2])
				rest := data[nl+1:]
				if e1 != nil || e2 != nil || e3 != nil || il < 0 || ll < 0 || xl < 0 || il > 1000 || ll > 1000 || il+ll+xl > len(rest) {
					return "an item's lengths do not fit", false
				}
				x := rest[il+ll : il+ll+xl]
				buttons := 0
				for len(x) > 0 {
					rf, _, r, why := readRecord(x)
					if why != "" {
						return "an item's record: " + why, false
					}
					if rf[0] == "button" {
						buttons++
					}
					x = r
				}
				if buttons > 4 {
					return "", true
				}
				data = rest[il+ll+xl:]
			}
		}
	}
	return "", false
}

// quickRequest answers quick, quick-set and quick-close
func (h *host) quickRequest(id int64, verb string, args []string, body []byte, reply func(string), rerr func(string, string)) {
	if !h.features["quick-input"] {
		h.fail("features", verb+" without quick-input agreed after hello (hello 1 quick-input)")
		rerr("unknown", "no such request")
		return
	}
	check := func() bool {
		why, big := checkQuickBody(body)
		if big {
			rerr("toobig", "10000 items, 2 MB, 8 buttons, 4 item buttons")
			return false
		}
		if why != "" {
			h.fail("quick-input", fmt.Sprintf("%s: %s", verb, why))
			rerr("syntax", "cannot parse the line")
			return false
		}
		return true
	}
	switch verb {
	case "quick":
		if len(args) != 1 {
			h.fail("quick-input", "quick LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		if h.quickQ != 0 {
			rerr("busy", "a prompt is open")
			return
		}
		if !check() {
			return
		}
		h.quickSeq++
		h.quickQ = h.quickSeq
		reply(fmt.Sprintf("ok %d %d", id, h.quickQ))
		q := h.quickQ
		h.write(fmt.Sprintf("0 quick %d value 1 1\nx", q))
		h.write(fmt.Sprintf("0 quick %d accept 1 0 1\nx", q))
		h.write(fmt.Sprintf("0 quick %d hide\n", q))
		h.quickQ = 0
	case "quick-set":
		q, err := strconv.ParseInt(firstArg(args), 10, 64)
		if len(args) != 2 || err != nil {
			h.fail("quick-input", "quick-set Q LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		if !check() {
			return
		}
		if q != h.quickQ || q == 0 {
			rerr("noview", "no such quick input")
			return
		}
		reply(fmt.Sprintf("ok %d", id))
	case "quick-close":
		q, err := strconv.ParseInt(firstArg(args), 10, 64)
		if len(args) != 1 || err != nil {
			h.fail("quick-input", "quick-close Q")
			rerr("syntax", "cannot parse the line")
			return
		}
		if q != h.quickQ || q == 0 {
			rerr("noview", "no such quick input")
			return
		}
		h.quickQ = 0
		reply(fmt.Sprintf("ok %d", id))
	}
}
