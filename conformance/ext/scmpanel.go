package main

// Version control panels (protocol 1.1 feature scm): scm-register, scm-update (its body checked
// against rhun's grammar and limits), scm-input and scm-input-set, answered as rhun does.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// scmState is what the checker keeps of the extension's providers.
type scmState struct {
	roots   map[string]int64 // root -> handle
	inputs  map[int64]string // handle -> message box text
	updates int
}

func scmName(n string) bool {
	if n == "" || len(n) > 40 {
		return false
	}
	for _, c := range []byte(n) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func scmClean(b []byte) bool { return !bytes.ContainsAny(b, "\x00\r\n") }

// scmReader reads an scm-update body: header words up to LF, then raw bytes.
type scmReader struct {
	b   []byte
	pos int
}

func (r *scmReader) line() ([]string, error) {
	i := bytes.IndexByte(r.b[r.pos:], '\n')
	if i < 0 {
		return nil, fmt.Errorf("a record line without LF at byte %d", r.pos)
	}
	f := strings.Split(string(r.b[r.pos:r.pos+i]), " ")
	r.pos += i + 1
	for _, w := range f {
		if w == "" {
			return nil, fmt.Errorf("an empty field (two spaces) before byte %d", r.pos)
		}
	}
	return f, nil
}

func (r *scmReader) take(n string, max int) ([]byte, error) {
	v, err := strconv.Atoi(n)
	if err != nil || v < 0 || len(n) > 9 {
		return nil, fmt.Errorf("length %q", n)
	}
	if v > max {
		return nil, fmt.Errorf("a length of %d (at most %d)", v, max)
	}
	if r.pos+v > len(r.b) {
		return nil, fmt.Errorf("a text of %d bytes runs past the body", v)
	}
	out := r.b[r.pos : r.pos+v]
	r.pos += v
	return out, nil
}

func (r *scmReader) num(n string, max int) (int, error) {
	v, err := strconv.Atoi(n)
	if err != nil || v < 0 || len(n) > 9 || v > max {
		return 0, fmt.Errorf("count %q (0 to %d)", n, max)
	}
	return v, nil
}

// action reads "ICON NAME ARGS_LEN LABEL_LEN" LF <ARGS><LABEL> after the word action
func (r *scmReader) action(f []string, commands map[string]bool, unknown *[]string) error {
	if len(f) != 5 {
		return fmt.Errorf("action takes ICON NAME ARGS_LEN LABEL_LEN")
	}
	if !scmName(f[2]) {
		return fmt.Errorf("command name %q", f[2])
	}
	if !commands[f[2]] {
		*unknown = append(*unknown, f[2])
	}
	if _, err := r.take(f[3], 8192); err != nil {
		return fmt.Errorf("action %s ARGS: %v", f[2], err)
	}
	label, err := r.take(f[4], 200)
	if err != nil || !scmClean(label) {
		return fmt.Errorf("action %s label: %v", f[2], err)
	}
	return nil
}

var scmColors = map[string]bool{"added": true, "modified": true, "deleted": true, "conflict": true, "untracked": true, "ignored": true, "-": true}

// checkScmUpdate applies rhun's grammar and limits to an scm-update body; unknown lists commands
// the manifest does not have (rhun says "no longer available" when they are clicked)
func checkScmUpdate(body []byte, commands map[string]bool) (unknown []string, err error) {
	if len(body) > 4<<20 {
		return nil, fmt.Errorf("a body of %d bytes (at most 4 MB)", len(body))
	}
	r := &scmReader{b: body}
	f, err := r.line()
	if err != nil {
		return nil, err
	}
	if len(f) != 4 {
		return nil, fmt.Errorf("the head is STATUS_LEN INPUT_LEN NACT NGROUP")
	}
	for _, n := range f[:2] {
		t, err := r.take(n, 200)
		if err != nil || !scmClean(t) {
			return nil, fmt.Errorf("status or placeholder: %v", err)
		}
	}
	nact, err := r.num(f[2], 4)
	if err != nil {
		return nil, fmt.Errorf("buttons: %v", err)
	}
	ngroup, err := r.num(f[3], 64)
	if err != nil {
		return nil, fmt.Errorf("groups: %v", err)
	}
	for i := 0; i < nact; i++ {
		f, err := r.line()
		if err != nil {
			return nil, err
		}
		if f[0] != "action" {
			return nil, fmt.Errorf("button %d: %q instead of action", i+1, f[0])
		}
		if err := r.action(f, commands, &unknown); err != nil {
			return nil, err
		}
	}
	nres := 0
	for g := 0; g < ngroup; g++ {
		f, err := r.line()
		if err != nil {
			return nil, err
		}
		if f[0] != "group" || len(f) != 5 {
			return nil, fmt.Errorf("group %d: group ID LABEL_LEN NACT NRES", g+1)
		}
		if len(f[1]) > 64 {
			return nil, fmt.Errorf("group id over 64 bytes")
		}
		if t, err := r.take(f[2], 200); err != nil || !scmClean(t) {
			return nil, fmt.Errorf("group %s label: %v", f[1], err)
		}
		na, err := r.num(f[3], 8)
		if err != nil {
			return nil, fmt.Errorf("group %s actions: %v", f[1], err)
		}
		nr, err := r.num(f[4], 10000)
		if err != nil {
			return nil, fmt.Errorf("group %s resources: %v", f[1], err)
		}
		if nres += nr; nres > 10000 {
			return nil, fmt.Errorf("more than 10000 resources")
		}
		for i := 0; i < na; i++ {
			f, err := r.line()
			if err != nil {
				return nil, err
			}
			if f[0] != "action" {
				return nil, fmt.Errorf("group action: %q instead of action", f[0])
			}
			if err := r.action(f, commands, &unknown); err != nil {
				return nil, err
			}
		}
		for i := 0; i < nr; i++ {
			f, err := r.line()
			if err != nil {
				return nil, err
			}
			if f[0] != "res" || len(f) != 5 || len(f[1]) != 1 || f[1][0] < 0x21 || f[1][0] > 0x7e || !scmColors[f[2]] {
				return nil, fmt.Errorf("resource: res CODE COLOR PATH_LEN NREC (a printable letter, a known color)")
			}
			p, err := r.take(f[3], 4095)
			if err != nil || len(p) == 0 || !scmClean(p) || p[0] == '/' {
				return nil, fmt.Errorf("resource path: 1 to 4095 bytes, relative, no CR or LF")
			}
			nrec, err := r.num(f[4], 64)
			if err != nil {
				return nil, fmt.Errorf("resource %s records: %v", p, err)
			}
			acts, click := 0, false
			for k := 0; k < nrec; k++ {
				f, err := r.line()
				if err != nil {
					return nil, err
				}
				switch f[0] {
				case "command":
					if len(f) != 3 || !scmName(f[1]) {
						return nil, fmt.Errorf("resource %s: command NAME ARGS_LEN", p)
					}
					if click || acts > 0 {
						return nil, fmt.Errorf("resource %s: one command record, before its actions", p)
					}
					click = true
					if !commands[f[1]] {
						unknown = append(unknown, f[1])
					}
					if _, err := r.take(f[2], 8192); err != nil {
						return nil, fmt.Errorf("resource %s command ARGS: %v", p, err)
					}
				case "action":
					if acts++; acts > 8 {
						return nil, fmt.Errorf("resource %s: more than 8 actions", p)
					}
					if err := r.action(f, commands, &unknown); err != nil {
						return nil, err
					}
				case "tip":
					if len(f) != 2 {
						return nil, fmt.Errorf("resource %s: tip LEN", p)
					}
					if _, err := r.take(f[1], 1000); err != nil {
						return nil, fmt.Errorf("resource %s tip: %v", p, err)
					}
				default:
					if _, err := r.take(f[len(f)-1], len(body)); err != nil {
						return nil, fmt.Errorf("resource %s record %s: its last field is its length: %v", p, f[0], err)
					}
				}
			}
		}
	}
	if r.pos != len(body) {
		return nil, fmt.Errorf("%d bytes after the last record", len(body)-r.pos)
	}
	return unknown, nil
}

// scmRequest answers scm-register, scm-update, scm-input and scm-input-set
func (h *host) scmRequest(id int64, verb string, args []string, msg message, reply func(string), rerr func(string, string)) {
	if !h.features["scm"] {
		h.fail("scm", verb+" without scm agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	if h.scm.roots == nil {
		h.scm.roots, h.scm.inputs = map[string]int64{}, map[int64]string{}
	}
	bad := func(why string) {
		h.fail("request grammar", verb+": "+why)
		rerr("syntax", "cannot parse the line")
	}
	lenOK := func(f string) bool { return f == strconv.Itoa(len(msg.body)) }
	handle := func(f string) (int64, bool) {
		n, err := strconv.ParseInt(f, 10, 64)
		for _, v := range h.scm.roots {
			if err == nil && v == n {
				return n, true
			}
		}
		h.fail("scm", fmt.Sprintf("%s names provider %q, which scm-register never returned", verb, f))
		rerr("refused", "no such provider")
		return 0, false
	}
	switch verb {
	case "scm-register":
		if len(args) != 1 || !lenOK(args[0]) {
			bad("scm-register takes LEN and a body of that length")
			return
		}
		i := bytes.IndexByte(msg.body, '\n')
		f := strings.Fields(string(msg.body[:max(i, 0)]))
		if i < 0 || len(f) != 2 {
			bad("the body is ROOT_LEN LABEL_LEN LF <ROOT><LABEL>")
			return
		}
		rl, e1 := strconv.Atoi(f[0])
		ll, e2 := strconv.Atoi(f[1])
		rest := msg.body[i+1:]
		if e1 != nil || e2 != nil || rl < 1 || rl > 4095 || ll < 1 || ll > 200 || rl+ll != len(rest) || !scmClean(rest) {
			bad("ROOT 1 to 4095 bytes and LABEL 1 to 200, no CR or LF, filling the body")
			return
		}
		root := strings.TrimRight(string(rest[:rl]), "/")
		if root == "" {
			root = "/"
		}
		if !strings.HasPrefix(root, "/") && !(len(root) > 2 && root[1] == ':') {
			bad("ROOT must be an absolute path")
			return
		}
		if n, ok := h.scm.roots[root]; ok {
			reply(fmt.Sprintf("ok %d %d", id, n))
			return
		}
		if len(h.scm.roots) >= 4 {
			h.warn("scm", "a fifth scm-register: rhun allows 4 providers per extension")
			rerr("toobig", "4 providers per extension, 16 in all")
			return
		}
		n := int64(len(h.scm.roots) + 1)
		h.scm.roots[root] = n
		reply(fmt.Sprintf("ok %d %d", id, n))
		h.ok("registers a version control panel")
	case "scm-update":
		if len(args) != 2 || !lenOK(args[1]) {
			bad("scm-update takes SCM LEN and a body of that length")
			return
		}
		if _, ok := handle(args[0]); !ok {
			return
		}
		cmds := map[string]bool{}
		for _, c := range h.m.commands {
			cmds[c] = true
		}
		unknown, err := checkScmUpdate(msg.body, cmds)
		if err != nil {
			bad(err.Error())
			return
		}
		for _, c := range unknown {
			h.warn("scm", fmt.Sprintf("scm-update names command %q, which [commands] does not have (unless registered at runtime)", c))
		}
		reply(fmt.Sprintf("ok %d", id))
		if h.scm.updates++; h.scm.updates == 1 {
			h.ok("fills its version control panel with an scm-update rhun takes")
		}
	case "scm-input":
		if len(args) != 1 {
			bad("scm-input takes SCM")
			return
		}
		n, ok := handle(args[0])
		if !ok {
			return
		}
		t := h.scm.inputs[n]
		reply(fmt.Sprintf("ok %d %d\n%s", id, len(t), t))
	case "scm-input-set":
		if len(args) != 2 || !lenOK(args[1]) || len(msg.body) > 65536 {
			bad("scm-input-set takes SCM LEN and at most 65536 bytes")
			return
		}
		n, ok := handle(args[0])
		if !ok {
			return
		}
		h.scm.inputs[n] = string(msg.body)
		reply(fmt.Sprintf("ok %d", id))
	}
}
