// rhun-ext-check plays rhun's side of extension protocol v1 (../../v1/v1.md) against an
// extension and reports what the extension gets wrong: the handshake, the grammar of every
// request, positions and versions against in-memory documents, bodies, how it copes with
// unknown events, error replies, fragmented and coalesced input, bye, end of file, and a burst
// of events. It also runs each manifest command once and shows what the extension did.
//
//	rhun-ext-check [-v] [-target linux-x86_64|macos-arm64|windows-x64] DIR
//
// DIR holds extension.ini and the program. Exit status 1 when a check fails.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var verbose = flag.Bool("v", false, "print the whole exchange")
var target = flag.String("target", "", "the run.* target to use (default: this machine)")

type manifest struct {
	name, owner, title, version string
	protocol                    int
	run                         map[string]string // target -> program
	runtime, script, args, on   string
	commands                    []string // NAME.key
	cmdValues                   map[string]string
	needs                       map[string]string
	subscribe                   map[string]string
}

func readManifest(dir string) (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "extension.ini"))
	if err != nil {
		return nil, err
	}
	m := &manifest{run: map[string]string{}, needs: map[string]string{}, subscribe: map[string]string{}}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch section {
		case "":
			switch {
			case k == "name":
				m.name = v
			case k == "owner":
				m.owner = v
			case k == "title":
				m.title = v
			case k == "version":
				m.version = v
			case k == "protocol":
				m.protocol, _ = strconv.Atoi(v)
			case strings.HasPrefix(k, "run."):
				m.run[k[4:]] = v
			case k == "runtime":
				m.runtime = v
			case k == "run":
				m.script = v
			case k == "args":
				m.args = v
			case k == "on":
				m.on = v
			}
		case "commands":
			m.commands = append(m.commands, k)
			if m.cmdValues == nil {
				m.cmdValues = map[string]string{}
			}
			m.cmdValues[k] = v
		case "needs":
			m.needs[k] = v
		case "subscribe":
			m.subscribe[k] = v
		}
	}
	return m, nil
}

// ---- the fake editor ----

type doc struct {
	id      int64
	version int64
	lang    string
	path    string
	text    []byte
	cur     int // byte offsets
	anchor  int
}

type host struct {
	dir, state, project string
	m                   *manifest
	docs                map[int64]*doc
	nextDoc             int64
	active              int64

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	wmu    sync.Mutex

	inflight map[int64]bool
	msgs     chan message
	requests []string // what the extension sent, for -v and the command report
	fails    []string
	oks      []string
	errOnce  int64 // reply error to this many requests (tests the extension copes)
	status   string
	toasts   []string
	hoverAt  string   // "DOC LINE COL" of the hover event we sent, "" when none
	rtcmds   []string // commands registered at runtime (EXT.NAME)
	options  []string // option requests, "DOC KEY VALUE"
	prompts  []string // choose/ask/term prompts shown
	cmds     []string // cmd names run
	hovers   []string // hover answers
	marks    int      // diagnostics lines in the last diagnostics request
	exited   chan error
}

// sub says whether the manifest subscribes to an event (any pattern but "no").
func (h *host) sub(name string) bool {
	v := h.m.subscribe[name]
	return v != "" && v != "no"
}

// edit is one record of an edits body (section 6a).
type edit struct {
	start, del int
	ins        []byte
}

func (h *host) ok(name string) { h.oks = append(h.oks, name); fmt.Println("ok  ", name) }
func (h *host) fail(name, why string) {
	h.fails = append(h.fails, name)
	fmt.Printf("FAIL %s: %s\n", name, why)
}
func (h *host) note(format string, a ...any) {
	if *verbose {
		fmt.Printf("     "+format+"\n", a...)
	}
}

func (h *host) newDoc(path, lang string, text []byte) *doc {
	h.nextDoc++
	d := &doc{id: h.nextDoc, lang: lang, path: path, text: text}
	h.docs[d.id] = d
	return d
}

func (h *host) start() error {
	prog, args := "", []string{}
	if h.m.runtime != "" {
		p, err := exec.LookPath(h.m.runtime)
		if err != nil {
			return fmt.Errorf("runtime %s not on PATH", h.m.runtime)
		}
		prog = p
		args = append(args, filepath.Join(h.dir, h.m.script))
	} else {
		rel, ok := h.m.run[*target]
		if !ok {
			return fmt.Errorf("no run.%s in the manifest", *target)
		}
		prog = filepath.Join(h.dir, rel)
	}
	if h.m.args != "" {
		args = append(args, strings.Fields(h.m.args)...)
	}
	cmd := exec.Command(prog, args...)
	cmd.Dir = h.dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + h.state, "TMPDIR=" + filepath.Join(h.state, "tmp"),
		"XDG_CACHE_HOME=" + filepath.Join(h.state, "cache"), "RHUN_VERSION=0.18.0", "RHUN_PROTOCOL=1",
		"RHUN_EXTENSION=" + h.m.owner + "/" + h.m.name, "RHUN_TARGET=" + *target, "RHUN_STATE=" + h.state,
		"RHUN_PROJECT=" + h.project,
	}
	for _, k := range []string{"LANG", "LC_ALL", "TZ"} {
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	os.MkdirAll(filepath.Join(h.state, "tmp"), 0o755)
	logf, _ := os.OpenFile(filepath.Join(h.state, "log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	cmd.Stderr = logf
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	h.cmd, h.stdin, h.stdout = cmd, in, bufio.NewReaderSize(out, 1<<20)
	h.inflight = map[int64]bool{}
	h.msgs = make(chan message, 1024)
	go h.reader()
	h.exited = make(chan error, 1)
	go func() { h.exited <- cmd.Wait() }()
	return nil
}

func (h *host) write(s string) error {
	h.wmu.Lock()
	defer h.wmu.Unlock()
	h.note("← %s", strings.TrimRight(s, "\n"))
	done := make(chan error, 1)
	go func() { _, err := io.WriteString(h.stdin, s); done <- err }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("the extension is not reading: a write blocked for 5 s")
	}
}

// message is one line from the extension, with its body when the line announces one.
type message struct {
	line string
	body []byte
	err  error
}

// bodyVerbs are the requests whose last field is a body length.
var bodyVerbs = map[string]bool{"replace": true, "insert": true, "toast": true, "status": true, "diagnostics": true, "hover": true, "choose": true, "ask": true, "term": true}

// reader is the one goroutine that reads the extension's output: a line, then its body when the
// verb has one, so nothing else ever touches the stream.
func (h *host) reader() {
	for {
		s, err := h.stdout.ReadString('\n')
		if err != nil && s == "" {
			h.msgs <- message{err: err}
			return
		}
		s = strings.TrimRight(s, "\r\n")
		m := message{line: s}
		f := strings.Split(s, " ")
		if len(f) >= 3 && bodyVerbs[f[1]] {
			if _, e := strconv.ParseInt(f[0], 10, 64); e == nil {
				n, e := strconv.ParseInt(f[len(f)-1], 10, 64)
				if e == nil && n >= 0 && n < 64<<20 {
					m.body = make([]byte, n)
					if _, e := io.ReadFull(h.stdout, m.body); e != nil {
						m.err = fmt.Errorf("the body of %q ended early: %w", s, e)
					}
				}
			}
		}
		h.msgs <- m
		if err != nil {
			h.msgs <- message{err: err}
			return
		}
	}
}

// readMessage returns the next message, or errTimeout.
func (h *host) readMessage(timeout time.Duration) (message, error) {
	select {
	case m := <-h.msgs:
		if m.err != nil && m.line == "" {
			return m, m.err
		}
		h.note("→ %s", m.line)
		return m, nil
	case <-time.After(timeout):
		return message{}, errTimeout
	}
}

var errTimeout = errors.New("timeout")

// position parses LINE COL against a document; ok false after the reason.
func (d *doc) position(ls, cs string) (int, string) {
	line, err1 := strconv.Atoi(ls)
	col, err2 := strconv.Atoi(cs)
	if err1 != nil || err2 != nil || line < 1 || col < 1 {
		return -1, "position fields must be positive integers"
	}
	start, n := 0, 1
	for n < line {
		i := bytes.IndexByte(d.text[start:], '\n')
		if i < 0 {
			return -1, fmt.Sprintf("line %d is past the last line", line)
		}
		start += i + 1
		n++
	}
	end := bytes.IndexByte(d.text[start:], '\n')
	if end < 0 {
		end = len(d.text) - start
	}
	if col-1 > end {
		return -1, fmt.Sprintf("column %d is past the end of line %d (%d bytes)", col, line, end)
	}
	pos := start + col - 1
	if pos < len(d.text) && col-1 < end && !utf8.RuneStart(d.text[pos]) {
		return -1, fmt.Sprintf("column %d of line %d is inside a multibyte character", col, line)
	}
	return pos, ""
}

// handle serves one request line from the extension; it returns false when the line was not a
// request (a reply or garbage), after recording the failure.
func (h *host) handle(msg message) {
	line := msg.line
	fields := strings.Split(line, " ")
	if len(fields) < 2 {
		h.fail("request grammar", fmt.Sprintf("too few fields: %q", line))
		return
	}
	if fields[0] == "ok" || fields[0] == "error" {
		h.fail("request grammar", fmt.Sprintf("a reply to a request rhun never sent: %q", line))
		return
	}
	id, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || id < 1 || id > 2147483647 {
		h.fail("request id", fmt.Sprintf("not an id in 1..2147483647: %q", fields[0]))
		return
	}
	if h.inflight[id] {
		h.fail("request id", fmt.Sprintf("id %d reused while in flight", id))
	}
	h.inflight[id] = true
	defer delete(h.inflight, id)
	verb := fields[1]
	args := fields[2:]
	h.requests = append(h.requests, verb)
	reply := func(s string) { h.write(s + "\n") }
	rerr := func(code, text string) { reply(fmt.Sprintf("error %d %s %s", id, code, text)) }
	if h.errOnce > 0 {
		h.errOnce--
		rerr("refused", "conformance: an error reply the extension must survive")
		return
	}
	need := func(level string) bool {
		have := h.m.needs["documents"]
		if level == "read" && (have == "read" || have == "write") {
			return true
		}
		if level == "write" && have == "write" {
			return true
		}
		h.fail("needs", fmt.Sprintf("%s needs documents=%s but the manifest has %q", verb, level, have))
		rerr("needs", "documents="+level)
		return false
	}
	getDoc := func(s string) *doc {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			h.fail("request grammar", fmt.Sprintf("%s: DOC %q is not a number", verb, s))
			rerr("syntax", "cannot parse the line")
			return nil
		}
		d := h.docs[n]
		if d == nil {
			h.fail("document ids", fmt.Sprintf("%s names document %d, which was never opened to it", verb, n))
			rerr("nodoc", "no such document")
		}
		return d
	}
	body := func(lenField string) ([]byte, bool) {
		n, err := strconv.ParseInt(lenField, 10, 64)
		if err != nil || n < 0 {
			h.fail("request grammar", fmt.Sprintf("%s: LEN %q is not a length", verb, lenField))
			return nil, false
		}
		if msg.err != nil || int64(len(msg.body)) != n {
			h.fail("bodies", fmt.Sprintf("%s announced %d bytes, the stream ended first", verb, n))
			return nil, false
		}
		return msg.body, true
	}
	switch verb {
	case "text":
		if len(args) != 1 || !need("read") {
			if len(args) != 1 {
				h.fail("request grammar", "text takes exactly DOC")
				rerr("syntax", "cannot parse the line")
			}
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		h.write(fmt.Sprintf("ok %d %d %d\n%s", id, d.version, len(d.text), d.text))
	case "range":
		if len(args) != 5 {
			h.fail("request grammar", "range takes DOC L1 C1 L2 C2")
			rerr("syntax", "cannot parse the line")
			return
		}
		if !need("read") {
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		a, why := d.position(args[1], args[2])
		if why == "" {
			var b int
			b, why = d.position(args[3], args[4])
			if why == "" && b < a {
				why = "the range ends before it starts"
			}
			if why == "" {
				h.write(fmt.Sprintf("ok %d %d %d\n%s", id, d.version, b-a, d.text[a:b]))
				return
			}
		}
		h.fail("positions", "range: "+why)
		rerr("badpos", why)
	case "replace":
		if len(args) != 7 {
			h.fail("request grammar", "replace takes DOC VERSION L1 C1 L2 C2 LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[6])
		if !ok {
			return
		}
		if !need("write") {
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		v, _ := strconv.ParseInt(args[1], 10, 64)
		if v != d.version {
			h.fail("versions", fmt.Sprintf("replace with version %d, the document is at %d (nothing changed it)", v, d.version))
			rerr("stale", strconv.FormatInt(d.version, 10))
			return
		}
		a, why := d.position(args[2], args[3])
		var e int
		if why == "" {
			e, why = d.position(args[4], args[5])
		}
		if why == "" && e < a {
			why = "the range ends before it starts"
		}
		if why != "" {
			h.fail("positions", "replace: "+why)
			rerr("badpos", why)
			return
		}
		d.text = append(append(append([]byte{}, d.text[:a]...), b...), d.text[e:]...)
		d.cur, d.anchor = a+len(b), a+len(b)
		recs := records(a, e-a, b)
		d.version += int64(len(recs))
		h.write(fmt.Sprintf("ok %d %d\n", id, d.version))
		h.afterEdit(d, recs...)
	case "insert":
		if len(args) != 3 {
			h.fail("request grammar", "insert takes DOC VERSION LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[2])
		if !ok {
			return
		}
		if !need("write") {
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		v, _ := strconv.ParseInt(args[1], 10, 64)
		if v != d.version {
			h.fail("versions", fmt.Sprintf("insert with version %d, the document is at %d", v, d.version))
			rerr("stale", strconv.FormatInt(d.version, 10))
			return
		}
		lo, hi := d.cur, d.anchor
		if lo > hi {
			lo, hi = hi, lo
		}
		d.text = append(append(append([]byte{}, d.text[:lo]...), b...), d.text[hi:]...)
		d.cur, d.anchor = lo+len(b), lo+len(b)
		recs := records(lo, hi-lo, b)
		d.version += int64(len(recs))
		h.write(fmt.Sprintf("ok %d %d\n", id, d.version))
		h.afterEdit(d, recs...)
	case "select":
		if len(args) != 5 {
			h.fail("request grammar", "select takes DOC L1 C1 L2 C2")
			rerr("syntax", "cannot parse the line")
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		a, why := d.position(args[1], args[2])
		var b int
		if why == "" {
			b, why = d.position(args[3], args[4])
		}
		if why != "" {
			h.fail("positions", "select: "+why)
			rerr("badpos", why)
			return
		}
		d.anchor, d.cur = a, b
		reply(fmt.Sprintf("ok %d", id))
	case "toast", "status":
		if len(args) != 1 {
			h.fail("request grammar", verb+" takes LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[0])
		if !ok {
			return
		}
		if verb == "toast" {
			h.toasts = append(h.toasts, string(b))
			if len(b) > 200 {
				h.fail("limits", fmt.Sprintf("a toast of %d bytes (rhun shows 200)", len(b)))
			}
		} else {
			h.status = string(b)
			if len(b) > 80 {
				h.fail("limits", fmt.Sprintf("a status text of %d bytes (rhun shows 80)", len(b)))
			}
		}
		reply(fmt.Sprintf("ok %d", id))
	case "open":
		if len(args) < 3 {
			h.fail("request grammar", "open takes LINE COL PATH")
			rerr("syntax", "cannot parse the line")
			return
		}
		path := strings.Join(args[2:], " ")
		if !filepath.IsAbs(path) {
			path = filepath.Join(h.project, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			rerr("nofile", "no such file")
			return
		}
		var d *doc
		for _, x := range h.docs {
			if x.path == path {
				d = x
			}
		}
		if d == nil {
			d = h.newDoc(path, langOf(path), data)
			h.write(fmt.Sprintf("0 opened %d %d %s %s\n", d.id, d.version, dash(d.lang), d.path))
		}
		h.active = d.id
		h.write(fmt.Sprintf("0 active %d\n", d.id))
		reply(fmt.Sprintf("ok %d %d", id, d.id))
	case "diagnostics":
		if len(args) != 4 {
			h.fail("request grammar", "diagnostics takes DOC VERSION N LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[3])
		if !ok {
			return
		}
		if !need("read") {
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		v, _ := strconv.ParseInt(args[1], 10, 64)
		if v != d.version {
			h.fail("versions", fmt.Sprintf("diagnostics with version %d, the document is at %d", v, d.version))
			rerr("stale", strconv.FormatInt(d.version, 10))
			return
		}
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 0 {
			h.fail("request grammar", fmt.Sprintf("diagnostics: N %q is not a count", args[2]))
			rerr("syntax", "cannot parse the line")
			return
		}
		if n > 2000 {
			h.fail("limits", fmt.Sprintf("%d diagnostics (rhun keeps 2000)", n))
			rerr("toobig", "2000")
			return
		}
		var lines []string
		if len(b) > 0 {
			lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		}
		if len(lines) != n {
			h.fail("bodies", fmt.Sprintf("diagnostics announced %d lines, the body has %d", n, len(lines)))
			rerr("syntax", "the body does not have N lines")
			return
		}
		for _, l := range lines {
			f := strings.SplitN(l, " ", 6)
			if len(f) < 6 {
				h.fail("request grammar", fmt.Sprintf("a diagnostics line is not L1 C1 L2 C2 SEVERITY MESSAGE: %q", l))
				rerr("syntax", "cannot parse a line of the body")
				return
			}
			a, why := d.position(f[0], f[1])
			var e int
			if why == "" {
				e, why = d.position(f[2], f[3])
			}
			if why == "" && e < a {
				why = "the range ends before it starts"
			}
			if why != "" {
				h.fail("positions", "diagnostics: "+why)
				rerr("badpos", why)
				return
			}
			switch f[4] {
			case "error", "warning", "info", "hint":
			default:
				h.fail("request grammar", fmt.Sprintf("diagnostics: severity %q", f[4]))
				rerr("syntax", "cannot parse a line of the body")
				return
			}
			if len(f[5]) > 200 {
				h.fail("limits", fmt.Sprintf("a diagnostic message of %d bytes (rhun shows 200)", len(f[5])))
			}
		}
		h.marks = n
		reply(fmt.Sprintf("ok %d", id))
	case "hover":
		if len(args) != 4 {
			h.fail("request grammar", "hover takes DOC LINE COL LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[3])
		if !ok {
			return
		}
		if !need("read") {
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		if _, why := d.position(args[1], args[2]); why != "" {
			h.fail("positions", "hover: "+why)
			rerr("badpos", why)
			return
		}
		if at := strings.Join(args[:3], " "); at != h.hoverAt {
			h.fail("hover", fmt.Sprintf("hover text for %s, the pointer rests at %q", at, h.hoverAt))
		}
		if len(b) > 4000 {
			h.fail("limits", fmt.Sprintf("a hover text of %d bytes (rhun shows 4000)", len(b)))
		}
		h.hovers = append(h.hovers, string(b))
		reply(fmt.Sprintf("ok %d", id))
	case "register":
		if len(args) < 3 {
			h.fail("request grammar", "register takes NAME KEYS TITLE")
			rerr("syntax", "cannot parse the line")
			return
		}
		name := args[0]
		if len(name) > 40 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
			h.fail("request grammar", fmt.Sprintf("register: NAME %q is not [a-z0-9_]{1,40}", name))
			rerr("syntax", "cannot parse the line")
			return
		}
		full := h.m.name + "." + name
		for _, c := range h.m.commands {
			if c == name {
				h.fail("verbs", fmt.Sprintf("register: %s is already a manifest command", full))
				rerr("refused", "the name is in use")
				return
			}
		}
		for _, c := range h.rtcmds {
			if c == full {
				h.fail("verbs", fmt.Sprintf("register: %s registered twice", full))
				rerr("refused", "the name is in use")
				return
			}
		}
		if len(h.rtcmds) >= 64 {
			h.fail("limits", "more than 64 runtime commands")
			rerr("toobig", "64 commands")
			return
		}
		if title := strings.Join(args[2:], " "); len(title) > 80 {
			h.fail("limits", fmt.Sprintf("a command title of %d bytes (rhun shows 80)", len(title)))
		}
		h.rtcmds = append(h.rtcmds, full)
		reply(fmt.Sprintf("ok %d", id))
	case "option":
		if len(args) != 3 {
			h.fail("request grammar", "option takes DOC KEY VALUE")
			rerr("syntax", "cannot parse the line")
			return
		}
		d := getDoc(args[0])
		if d == nil {
			return
		}
		ok := false
		switch args[1] {
		case "tab_width":
			n, err := strconv.Atoi(args[2])
			ok = args[2] == "-" || (err == nil && n >= 1 && n <= 16)
		case "insert_spaces", "trim_trailing_whitespace", "final_newline":
			ok = args[2] == "yes" || args[2] == "no" || args[2] == "-"
		}
		if !ok {
			h.fail("request grammar", fmt.Sprintf("option: %s %s is not a setting rhun knows", args[1], args[2]))
			rerr("syntax", "cannot parse the line")
			return
		}
		h.options = append(h.options, strings.Join(args, " "))
		reply(fmt.Sprintf("ok %d", id))
	case "choose":
		if len(args) != 1 {
			h.fail("request grammar", "choose takes LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[0])
		if !ok {
			return
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(lines) < 2 || len(b) == 0 {
			h.fail("request grammar", "choose: the body needs a prompt line and at least one item")
			rerr("syntax", "no items")
			return
		}
		if len(lines)-1 > 1000 {
			h.fail("limits", fmt.Sprintf("%d items (rhun shows 1000)", len(lines)-1))
			rerr("toobig", "1000 items")
			return
		}
		for _, l := range lines {
			if len(l) > 200 {
				h.fail("limits", fmt.Sprintf("a choose line of %d bytes (rhun shows 200)", len(l)))
			}
		}
		h.prompts = append(h.prompts, "choose: "+lines[0]+" "+fmt.Sprint(lines[1:]))
		// the user picks the last item
		reply(fmt.Sprintf("ok %d %d", id, len(lines)-1))
	case "ask":
		if len(args) != 1 {
			h.fail("request grammar", "ask takes LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[0])
		if !ok {
			return
		}
		if len(b) > 200 {
			h.fail("limits", fmt.Sprintf("an ask prompt of %d bytes (rhun shows 200)", len(b)))
		}
		h.prompts = append(h.prompts, "ask: "+string(b))
		answer := "conformance"
		h.write(fmt.Sprintf("ok %d %d\n%s", id, len(answer), answer))
	case "term":
		if len(args) != 1 {
			h.fail("request grammar", "term takes LEN")
			rerr("syntax", "cannot parse the line")
			return
		}
		b, ok := body(args[0])
		if !ok {
			return
		}
		if h.m.needs["terminal"] != "yes" {
			h.fail("needs", "term needs terminal=yes but the manifest does not say so")
			rerr("needs", "terminal=yes")
			return
		}
		if len(b) == 0 || len(b) > 4096 || strings.ContainsFunc(string(b), func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			h.fail("request grammar", "term: the body must be one line of 1 to 4096 bytes without control characters")
			rerr("syntax", "not one command line")
			return
		}
		h.prompts = append(h.prompts, "term: "+string(b))
		reply(fmt.Sprintf("ok %d", id)) // the user ran it
	case "cmd":
		if len(args) != 1 {
			h.fail("request grammar", "cmd takes NAME")
			rerr("syntax", "cannot parse the line")
			return
		}
		needsCap, listed := cmdAllowed[args[0]]
		if !listed {
			h.fail("verbs", fmt.Sprintf("cmd %s is not a command an extension may run (section 8a)", args[0]))
			rerr("refused", "not a command an extension may run")
			return
		}
		if needsCap == "write" && !need("write") {
			return
		}
		if needsCap == "terminal" && h.m.needs["terminal"] != "yes" {
			h.fail("needs", fmt.Sprintf("cmd %s needs terminal=yes but the manifest does not say so", args[0]))
			rerr("needs", "terminal=yes")
			return
		}
		h.cmds = append(h.cmds, args[0])
		reply(fmt.Sprintf("ok %d", id))
	case "cancel":
		reply(fmt.Sprintf("ok %d", id))
	default:
		h.fail("verbs", fmt.Sprintf("unknown request %q", verb))
		rerr("unknown", "no such request")
	}
}

// records are the edits records of replacing del bytes at start with ins: a delete record and
// an insert record, each only when it does something (that is how rhun counts versions too).
func records(start, del int, ins []byte) []edit {
	var out []edit
	if del > 0 {
		out = append(out, edit{start, del, nil})
	}
	if len(ins) > 0 {
		out = append(out, edit{start, 0, ins})
	}
	return out
}

// afterEdit reports an edit as rhun does at its next frame: edits (with the records), changed,
// and the cursor, when the manifest subscribes to them.
func (h *host) afterEdit(d *doc, recs ...edit) {
	if h.sub("edits") && len(recs) > 0 {
		var body []byte
		for _, r := range recs {
			body = append(body, fmt.Sprintf("%d %d %d\n", r.start, r.del, len(r.ins))...)
			body = append(body, r.ins...)
		}
		h.write(fmt.Sprintf("0 edits %d %d %d\n%s", d.id, d.version, len(body), body))
	}
	if h.sub("changed") {
		h.write(fmt.Sprintf("0 changed %d %d\n", d.id, d.version))
	}
	if h.sub("cursor") {
		h.sendCursor(d)
	}
}

// sendCursor reports the document's cursor, as rhun does after a move and when a document
// becomes active.
func (h *host) sendCursor(d *doc) {
	line, col := 1, 1
	for i := 0; i < d.cur && i < len(d.text); i++ {
		if d.text[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	h.write(fmt.Sprintf("0 cursor %d %d %d\n", d.id, line, col))
}

// activate makes a document active, with its cursor for subscribers, as rhun does.
func (h *host) activate(d int64) {
	h.active = d
	h.write(fmt.Sprintf("0 active %d\n", d))
	if h.sub("cursor") {
		h.sendCursor(h.docs[d])
	}
}

// cmdAllowed lists the commands of section 8a with the capability each needs ("" for none).
var cmdAllowed = func() map[string]string {
	m := map[string]string{}
	for _, n := range strings.Fields(`undo redo duplicate_line delete_line move_line_up move_line_down indent outdent
		toggle_comment newline_below newline_above`) {
		m[n] = "write"
	}
	for _, n := range strings.Fields(`toggle_terminal new_terminal clear_terminal`) {
		m[n] = "terminal"
	}
	for _, n := range strings.Fields(`select_all select_line select_next find replace find_next find_prev find_in_files
		goto_line quick_open command_palette next_tab prev_tab reveal_file focus_explorer toggle_sidebar toggle_agents
		toggle_word_wrap toggle_whitespace toggle_line_numbers zoom_in zoom_out zoom_reset`) {
		m[n] = ""
	}
	return m
}()

func langOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "Go"
	case ".json":
		return "JSON"
	case ".py":
		return "Python"
	case ".md":
		return "Markdown"
	}
	return ""
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return strings.ReplaceAll(s, " ", "_")
}

// serve reads the extension's lines until it has been quiet for `quiet`, handling requests.
func (h *host) serve(quiet time.Duration) {
	for {
		m, err := h.readMessage(quiet)
		if err != nil {
			return
		}
		if m.line == "" {
			continue
		}
		h.handle(m)
	}
}

func (h *host) catchUp() {
	ids := make([]int64, 0, len(h.docs))
	for id := range h.docs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		d := h.docs[id]
		h.write(fmt.Sprintf("0 opened %d %d %s %s\n", d.id, d.version, dash(d.lang), d.path))
	}
	h.write(fmt.Sprintf("0 active %d\n", h.active))
	if h.m.subscribe["cursor"] != "" && h.m.subscribe["cursor"] != "no" {
		h.write(fmt.Sprintf("0 cursor %d 1 1\n", h.active))
	}
}

// expectHello waits for the first line.
func (h *host) expectHello() bool {
	m, err := h.readMessage(5 * time.Second)
	if err != nil {
		h.fail("hello", "no line within 5 s: "+err.Error())
		return false
	}
	line := m.line
	if line != "hello 1" && !strings.HasPrefix(line, "hello 1 ") {
		h.fail("hello", fmt.Sprintf("the first line was %q, not \"hello 1\"", line))
		return false
	}
	h.ok("hello 1 as the first line")
	return true
}

func (h *host) stopAndWait(name string, how func()) bool {
	how()
	select {
	case <-h.exited:
		h.ok(name)
		return true
	case <-time.After(1500 * time.Millisecond):
		h.fail(name, "the program was still running 1.5 s later")
		h.cmd.Process.Kill()
		<-h.exited
		return false
	}
}

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: rhun-ext-check [-v] [-target T] DIR")
		os.Exit(2)
	}
	if *target == "" {
		switch runtime.GOOS + "/" + runtime.GOARCH {
		case "linux/amd64":
			*target = "linux-x86_64"
		case "darwin/arm64":
			*target = "macos-arm64"
		case "windows/amd64":
			*target = "windows-x64"
		}
	}
	dir, _ := filepath.Abs(flag.Arg(0))
	m, err := readManifest(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "manifest:", err)
		os.Exit(2)
	}
	h := &host{dir: dir, m: m, docs: map[int64]*doc{}}
	// the manifest itself
	switch {
	case m.name == "" || m.owner == "" || m.title == "" || m.version == "":
		h.fail("manifest", "name, owner, title and version are required")
	case m.name != filepath.Base(dir):
		h.fail("manifest", fmt.Sprintf("name %q is not the folder name %q", m.name, filepath.Base(dir)))
	default:
		h.ok("manifest has name, owner, title and version")
	}
	if m.protocol != 1 {
		h.fail("manifest", fmt.Sprintf("protocol = %d, this checker speaks 1", m.protocol))
	}
	if len(m.commands) == 0 {
		h.fail("manifest", "no [commands]: the extension can never be started on a command")
	}
	// the rules of v1/v1.json
	schemaFails := 0
	bad := func(why string) { schemaFails++; h.fail("manifest schema", why) }
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(m.name) {
		bad(fmt.Sprintf("name %q is not [a-z0-9][a-z0-9-]*", m.name))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`).MatchString(m.owner) {
		bad(fmt.Sprintf("owner %q is not [A-Za-z0-9][A-Za-z0-9-]*", m.owner))
	}
	if len(m.title) > 80 {
		bad(fmt.Sprintf("title is %d bytes (80 at most)", len(m.title)))
	}
	for t := range m.run {
		switch t {
		case "linux-x86_64", "macos-arm64", "windows-x64":
		default:
			bad(fmt.Sprintf("run.%s: not a target (linux-x86_64, macos-arm64, windows-x64)", t))
		}
	}
	if len(m.run) == 0 && (m.runtime == "" || m.script == "") {
		bad("no run.TARGET entry and no runtime with run")
	}
	for _, w := range strings.Fields(m.on) {
		if w != "startup" && w != "command" && w != "project" && !strings.HasPrefix(w, "lang:") && !strings.HasPrefix(w, "open:") {
			bad(fmt.Sprintf("on: %q is not startup, command, project, lang:LANG or open:PATTERN", w))
		}
	}
	for _, c := range m.commands {
		if !regexp.MustCompile(`^[a-z0-9_]{1,40}$`).MatchString(c) {
			bad(fmt.Sprintf("[commands] key %q is not [a-z0-9_]{1,40}", c))
		}
		if title, _, _ := strings.Cut(m.cmdValues[c], "|"); strings.TrimSpace(title) == "" {
			bad(fmt.Sprintf("[commands] %s has no title", c))
		}
	}
	for k := range m.subscribe {
		switch k {
		case "opened", "saved", "changed", "cursor", "selection", "edits", "hover":
		default:
			bad(fmt.Sprintf("[subscribe] %s is not an event with a subscription", k))
		}
	}
	for k, v := range m.needs {
		ok := false
		switch k {
		case "documents", "project":
			ok = v == "none" || v == "read" || v == "write"
		case "home":
			ok = v == "none" || v == "read"
		case "network", "terminal":
			ok = v == "no" || v == "yes"
		case "run":
			ok = true
		}
		if !ok {
			bad(fmt.Sprintf("[needs] %s = %s is not a value section 5 lists", k, v))
		}
	}
	if schemaFails == 0 {
		h.ok("manifest follows v1.json")
	}
	work, _ := os.MkdirTemp("", "rhun-ext-check-")
	defer os.RemoveAll(work)
	h.state = filepath.Join(work, "state")
	h.project = filepath.Join(work, "project")
	os.MkdirAll(h.project, 0o755)
	// the documents every command runs on: plain text, Go, JSON, and a two-byte character
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("hello world\nsecond line\n"), 0o644)
	os.WriteFile(filepath.Join(h.project, "main.go"), []byte("package main\n\nfunc  main( ) {\n}\n"), 0o644)
	os.WriteFile(filepath.Join(h.project, "data.json"), []byte("{\"a\":1,\"b\":[1,2]}"), 0o644)
	os.WriteFile(filepath.Join(h.project, "u.txt"), []byte("héllo\n"), 0o644)
	for _, name := range []string{"a.txt", "main.go", "data.json", "u.txt"} {
		data, _ := os.ReadFile(filepath.Join(h.project, name))
		h.newDoc(filepath.Join(h.project, name), langOf(name), data)
	}
	h.active = 1

	// 1. start, hello, ready and catch-up; the extension must not speak before ready
	if err := h.start(); err != nil {
		h.fail("start", err.Error())
		report(h)
	}
	if !h.expectHello() {
		report(h)
	}
	h.write("0 ready " + h.project + "\n")
	h.catchUp()
	h.serve(300 * time.Millisecond)
	if len(h.fails) == 0 {
		h.ok("ready and catch-up accepted")
	}

	// 2. each command once, on each document
	for _, c := range m.commands {
		for _, d := range []int64{1, 2, 3, 4} {
			before := len(h.requests)
			h.activate(d)
			h.write(fmt.Sprintf("0 run %s.%s %d\n", m.name, c, d))
			h.serve(700 * time.Millisecond)
			did := h.requests[before:]
			h.note("command %s on %s: %v", c, filepath.Base(h.docs[d].path), did)
		}
		if h.cmdExited() {
			h.fail("commands", c+": the program exited while handling the command")
			report(h)
		}
	}
	h.ok("every command handled on four documents without the program exiting")
	for _, c := range h.rtcmds {
		before := len(h.requests)
		h.write(fmt.Sprintf("0 run %s 1\n", c))
		h.serve(700 * time.Millisecond)
		h.note("runtime command %s: %v", c, h.requests[before:])
		if h.cmdExited() {
			h.fail("commands", c+": the program exited while handling the command")
			report(h)
		}
	}

	// 2b. the user types into main.go: the edits stream for subscribers; then the pointer rests
	if h.sub("edits") || h.sub("hover") {
		d := h.docs[2]
		h.activate(d.id)
		h.serve(200 * time.Millisecond)
		typed := []byte("\n// typed\n")
		at := len("package main\n")
		d.text = append(append(append([]byte{}, d.text[:at]...), typed...), d.text[at:]...)
		d.version++
		d.cur, d.anchor = at+len(typed), at+len(typed)
		before := len(h.requests)
		h.afterEdit(d, edit{at, 0, typed})
		h.serve(700 * time.Millisecond)
		if h.cmdExited() {
			h.fail("edits", "the program exited on an edits event")
			report(h)
		}
		h.note("after the edits event: %v", h.requests[before:])
		h.ok("takes the edits stream")
		if h.sub("hover") {
			h.hoverAt = fmt.Sprintf("%d 5 6", d.id) // on "main" of "func  main( )"
			h.write("0 hover " + h.hoverAt + "\n")
			h.serve(700 * time.Millisecond)
			if h.cmdExited() {
				h.fail("hover", "the program exited on a hover event")
				report(h)
			}
			h.note("hover answers: %q", h.hovers)
			h.hoverAt = ""
			h.ok("takes the hover event")
		}
	}

	// 3. an event it does not know, then an error reply it must survive
	h.write("0 frobnicate 1 2 three\n")
	h.errOnce = 1
	h.write(fmt.Sprintf("0 run %s.%s 1\n", m.name, m.commands[0]))
	h.serve(700 * time.Millisecond)
	if h.cmdExited() {
		h.fail("resilience", "the program exited after an unknown event or an error reply")
		report(h)
	}
	h.ok("survives an unknown event and an error reply")

	// 4. fragmented input: one byte at a time
	msg := fmt.Sprintf("0 run %s.%s 1\n", m.name, m.commands[0])
	before := len(h.requests)
	for i := range msg {
		h.write(msg[i : i+1])
		time.Sleep(2 * time.Millisecond)
	}
	h.serve(700 * time.Millisecond)
	frag := len(h.requests) - before
	// 5. coalesced input: several events in one write
	before = len(h.requests)
	h.write(fmt.Sprintf("0 cursor 1 1 2\n0 cursor 1 1 3\n0 active 1\n0 run %s.%s 1\n", m.name, m.commands[0]))
	h.serve(700 * time.Millisecond)
	coal := len(h.requests) - before
	if frag == coal {
		h.ok("reads fragmented and coalesced input alike")
	} else {
		h.fail("framing", fmt.Sprintf("the same command produced %d requests fragmented and %d coalesced", frag, coal))
	}

	// 6. a burst: 2000 events; the extension must keep reading
	burstErr := error(nil)
	for i := 0; i < 2000 && burstErr == nil; i++ {
		burstErr = h.write(fmt.Sprintf("0 cursor 1 1 %d\n", 1+i%10))
	}
	h.serve(500 * time.Millisecond)
	if burstErr != nil {
		why := burstErr.Error()
		if h.cmdExited() {
			why += " (the program exited: " + fmt.Sprint(h.cmd.ProcessState) + ")"
		}
		h.fail("keeps reading", why)
	} else if h.cmdExited() {
		h.fail("keeps reading", "the program exited during a burst of 2000 events")
	} else {
		h.ok("keeps reading through a burst of 2000 events")
	}

	// 7. bye: it leaves within a second
	h.stopAndWait("exits on bye", func() { h.write("0 bye\n") })

	// 8. restart, then end of file on its input (runtime commands do not survive a stop)
	h.rtcmds = nil
	if err := h.start(); err == nil {
		if h.expectHello() {
			h.write("0 ready " + h.project + "\n")
			h.catchUp()
			h.serve(200 * time.Millisecond)
			h.ok("restarts cleanly")
			h.stopAndWait("exits when its input ends", func() { h.stdin.Close() })
		}
	}
	report(h)
}

func (h *host) cmdExited() bool {
	select {
	case err := <-h.exited:
		h.exited <- err
		return true
	default:
		return false
	}
}

func report(h *host) {
	fmt.Printf("\n%d ok, %d failed", len(h.oks), len(h.fails))
	if h.status != "" {
		fmt.Printf("; last status %q", h.status)
	}
	if len(h.toasts) > 0 {
		fmt.Printf("; toasts: %q", h.toasts)
	}
	if h.marks > 0 {
		fmt.Printf("; %d diagnostics", h.marks)
	}
	if len(h.rtcmds) > 0 {
		fmt.Printf("; registered %v", h.rtcmds)
	}
	if len(h.options) > 0 {
		fmt.Printf("; options %q", h.options)
	}
	if len(h.prompts) > 0 {
		fmt.Printf("; prompts %q", h.prompts)
	}
	if len(h.cmds) > 0 {
		fmt.Printf("; cmd %v", h.cmds)
	}
	fmt.Println()
	if len(h.fails) > 0 {
		if data, err := os.ReadFile(filepath.Join(h.state, "log")); err == nil && len(data) > 0 {
			lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
			if len(lines) > 20 {
				lines = lines[len(lines)-20:]
			}
			fmt.Println("the extension's log ends with:")
			for _, l := range lines {
				fmt.Println("    " + l)
			}
		}
		os.Exit(1)
	}
	os.Exit(0)
}

var _ = context.Background
