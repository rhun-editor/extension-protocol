package main

// Source control (protocol 1.1): the git-data requests an extension sends (checked against their
// grammar, [needs] git and the agreed feature, and answered from a small made-up repository), and
// a commit-message provider asked for a message (its answer checked as rhun checks it).

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

const checkerRoot = "/checker/repo"
const checkerSHA = "1111111111111111111111111111111111111111"

// gitRequest answers git-repo, git-status, git-blame, git-log, git-show and git-diff-open
func (h *host) gitRequest(id int64, verb string, args []string, msg message, reply func(string), rerr func(string, string)) {
	if !h.features["git-data"] {
		h.fail("git-data", verb+" without git-data agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	if h.m.needs["git"] != "read" {
		h.fail("needs", verb+" needs git = read in [needs]")
		rerr("needs", "git=read")
		return
	}
	bad := func(why string) {
		h.fail("request grammar", verb+": "+why)
		rerr("syntax", "cannot parse the line")
	}
	path := func(lenField string) (string, bool) {
		n, err := strconv.ParseInt(lenField, 10, 64)
		if err != nil || n < 0 || int64(len(msg.body)) != n {
			bad("LEN " + lenField + " does not match the body")
			return "", false
		}
		p := string(msg.body)
		if strings.ContainsAny(p, "\x00\r\n") || len(p) > 4095 {
			bad("a path with NUL, CR or LF, or over 4095 bytes")
			return "", false
		}
		return p, true
	}
	body := func(b string) { reply(fmt.Sprintf("ok %d %d\n%s", id, len(b), b)) }
	switch verb {
	case "git-repo":
		if len(args) != 0 {
			bad("git-repo takes no fields")
			return
		}
		body(fmt.Sprintf("%d 4 0 branch 0 0 clean\n%smain", len(checkerRoot), checkerRoot))
	case "git-status":
		if len(args) != 0 {
			bad("git-status takes no fields")
			return
		}
		body("1\n.M 5\na.txt")
	case "git-blame":
		if len(args) != 1 {
			bad("git-blame takes DOC")
			return
		}
		n, err := strconv.ParseInt(args[0], 10, 64)
		d := h.docs[n]
		if err != nil || d == nil {
			h.fail("document ids", fmt.Sprintf("git-blame names document %q, which was never opened to it", args[0]))
			rerr("nodoc", "no such document")
			return
		}
		lines := strings.Count(string(d.text), "\n") + 1
		body(fmt.Sprintf("1 1\n%s 1700000000 7 15 7\nCheckerchecker@rhun.appChecker%s 1 %d 1\n", checkerSHA, checkerSHA, lines))
	case "git-log":
		if len(args) != 4 {
			bad("git-log takes MAX L1 L2 LEN")
			return
		}
		max, e1 := strconv.Atoi(args[0])
		l1, e2 := strconv.Atoi(args[1])
		l2, e3 := strconv.Atoi(args[2])
		if e1 != nil || e2 != nil || e3 != nil || max < 1 || max > 1000 || l1 < 0 || l2 < l1 || (l1 == 0) != (l2 == 0) {
			bad("MAX is 1 to 1000; L1 L2 are 0 0 or 1 <= L1 <= L2")
			return
		}
		p, ok := path(args[3])
		if !ok {
			return
		}
		if p == "" && l1 != 0 {
			bad("lines need a path")
			return
		}
		body(fmt.Sprintf("1\n%s - 1700000000 7 7\nCheckerChecker", checkerSHA))
	case "git-show":
		if len(args) != 2 {
			bad("git-show takes REV LEN")
			return
		}
		if !rhunext.ValidRev(args[0]) {
			bad(fmt.Sprintf("%q is not a revision rhun passes to git", args[0]))
			return
		}
		if p, ok := path(args[1]); !ok || p == "" {
			if ok {
				bad("an empty path")
			}
			return
		}
		body("checker\n")
	case "git-diff-open":
		if len(args) != 3 {
			bad("git-diff-open takes REV1 REV2 LEN")
			return
		}
		if !rhunext.ValidRev(args[0]) || (args[1] != "-" && !rhunext.ValidRev(args[1])) {
			bad("a revision rhun does not pass to git")
			return
		}
		if p, ok := path(args[2]); !ok || p == "" {
			if ok {
				bad("an empty path")
			}
			return
		}
		reply(fmt.Sprintf("ok %d", id))
	}
	if !gitSeen[verb] {
		gitSeen[verb] = true
		h.ok("asks for Git data with " + verb)
	}
}

var gitSeen = map[string]bool{}

// askBody is ask for a request with a body (LEN is the line's last field)
func (h *host) askBody(line string, b []byte, wait time.Duration) (message, bool) {
	h.hostSeq++
	id := h.hostSeq
	h.hostReqs[id] = line
	h.write(fmt.Sprintf("%d %s %d\n%s", id, line, len(b), b))
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if m, ok := h.hostAnswers[id]; ok {
			delete(h.hostAnswers, id)
			return m, true
		}
		msg, err := h.readMessage(100 * time.Millisecond)
		if err == nil && msg.line != "" {
			h.handle(msg)
		}
		if h.cmdExited() {
			break
		}
	}
	delete(h.hostReqs, id)
	h.write(fmt.Sprintf("0 cancel %d\n", id))
	return message{}, false
}

// checkScm: a commit-message provider answers with a message rhun takes, or an error; Git data
// and the provides keys agree with what hello said
func (h *host) checkScm() {
	provider := h.m.provides["commit_message"] == "yes"
	if h.features["commit-message"] && !provider {
		h.fail("provides", "commit-message agreed after hello but [provides] has no commit_message = yes: rhun never asks")
	}
	if provider && !h.features["commit-message"] {
		h.fail("provides", "[provides] commit_message = yes without saying commit-message after hello 1: rhun refuses it")
	}
	if h.features["git-data"] && h.m.needs["git"] != "read" {
		h.fail("needs", "git-data agreed after hello but [needs] has no git = read: every request is refused")
	}
	h.checkQuickDiff()
	if !h.features["commit-message"] || !provider {
		return
	}
	diff := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1,2 @@\n hello\n+world\n"
	m, ok := h.askBody("commit-message", []byte(fmt.Sprintf("4 %d\nmain%s", len(diff), diff)), 30*time.Second)
	switch f := strings.Fields(m.line); {
	case !ok:
		h.fail("commit-message", "no answer to a commit-message request within 30 s")
	case len(f) > 0 && f[0] == "error":
		h.warn("commit-message", "a commit-message request answered "+m.line+" (rhun shows the text to the user)")
	default:
		msg, err := rhunext.CheckCommitMessage(string(m.body))
		switch {
		case err != nil:
			h.fail("commit-message", "rhun refuses the message: "+err.Error())
		case !utf8.ValidString(msg):
			h.fail("commit-message", "the message is not UTF-8")
		default:
			h.ok("answers commit-message requests with a message rhun takes")
		}
	}
}

// quickdiffRefresh answers quickdiff-refresh LEN + a path or "-"
func (h *host) quickdiffRefresh(id int64, args []string, msg message, reply func(string), rerr func(string, string)) {
	if !h.features["quickdiff"] {
		h.fail("quickdiff", "quickdiff-refresh without quickdiff agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	if len(args) != 1 || args[0] != strconv.Itoa(len(msg.body)) || len(msg.body) == 0 ||
		strings.ContainsAny(string(msg.body), "\x00\r\n") {
		h.fail("request grammar", "quickdiff-refresh takes LEN and a path (or -) of that length")
		rerr("syntax", "cannot parse the line")
		return
	}
	reply(fmt.Sprintf("ok %d", id))
	h.ok("asks for fresh quick-diff bases")
}

// checkQuickDiff: a quick-diff provider answers with a base text (or none) for a file it matches
func (h *host) checkQuickDiff() {
	pats := h.m.provides["quickdiff"]
	if h.features["quickdiff"] && pats == "" {
		h.fail("provides", "quickdiff agreed after hello but [provides] has no quickdiff patterns: rhun never asks")
	}
	if pats != "" && !h.features["quickdiff"] {
		h.fail("provides", "[provides] quickdiff without saying quickdiff after hello 1: rhun never asks")
	}
	if !h.features["quickdiff"] || pats == "" {
		return
	}
	d := h.sampleFor(pats)
	h.serve(300 * time.Millisecond)
	m, ok := h.ask(fmt.Sprintf("quickdiff %d %d", d.id, d.version))
	switch f := strings.Fields(m.line); {
	case !ok:
		h.fail("quickdiff", "no answer to a quickdiff request within 3 s")
	case len(f) > 0 && f[0] == "error":
		h.warn("quickdiff", "a quickdiff request answered "+m.line+" (rhun keeps what it shows)")
	default:
		h.ok(fmt.Sprintf("answers quickdiff requests (a base of %d bytes)", len(m.body)))
	}
}
