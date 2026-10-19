package rhunext

// Source control (protocol 1.1): Git data through rhun (feature git-data, [needs] git = read) and
// commit-message providers (feature commit-message, CommitMessageProvider). rhun runs git itself,
// so the trust pause holds and the extension needs no run = git.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	FeatureGitData       = "git-data"
	FeatureCommitMessage = "commit-message"
)

// GitRepo is the git-repo reply.
type GitRepo struct {
	Root     string // the work tree's absolute path
	Branch   string // the branch, or the commit's first 7 hex digits when detached
	Upstream string // like origin/main; "" without one
	Head     string // "branch", "detached" or "unborn"
	Ahead    int64
	Behind   int64
	State    string // "clean", "dirty", "merging" or "rebasing"
}

// GitStatusEntry is one git-status entry: git's two status columns ('.' for an unchanged side)
// and the path relative to the work tree (an untracked folder ends in /).
type GitStatusEntry struct {
	X, Y byte
	Path string
}

// BlameCommit is a commit of a blame; Time is the author time in seconds since 1970.
type BlameCommit struct {
	SHA     string
	Time    int64
	Author  string
	Mail    string
	Summary string
}

// BlameRun says lines L1 to L2 (one-based, in the document) come from commit SHA, where they
// started at line Orig. Lines not committed carry the zero SHA (IsUncommitted).
type BlameRun struct {
	SHA          string
	L1, L2, Orig int
}

// Blame is the git-blame reply: the blame of a document's current text, unsaved edits included.
type Blame struct {
	Commits []BlameCommit
	Runs    []BlameRun
}

// Line is the commit that wrote a line (one-based), or nil.
func (b Blame) Line(line int) *BlameCommit {
	for _, r := range b.Runs {
		if r.L1 <= line && line <= r.L2 {
			for i := range b.Commits {
				if b.Commits[i].SHA == r.SHA {
					return &b.Commits[i]
				}
			}
		}
	}
	return nil
}

// IsUncommitted reports whether a SHA is the zero SHA of lines not committed.
func IsUncommitted(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

// LogEntry is a commit of git-log, newest first; Parents is empty for a root commit.
type LogEntry struct {
	SHA     string
	Parents []string
	Time    int64
	Author  string
	Subject string
}

// CommitMessageRequest asks for a commit message for the branch and the bounded diff (the staged
// changes, else what Commit All would take). Context ends when rhun sends "0 cancel ID".
type CommitMessageRequest struct {
	ID      int64
	Branch  string
	Diff    string
	Context context.Context
}

// CommitMessageProvider answers commit-message requests: the message, or why not (shown to the
// user). rhun trims it and refuses one over 8192 bytes or with control characters
// (CheckCommitMessage).
type CommitMessageProvider interface {
	CommitMessage(ext *Ext, req CommitMessageRequest) (string, error)
}

// CheckCommitMessage applies rhun's checks: trimmed of spaces, tabs and line breaks, 1 to 8192
// bytes, no control characters but tab, CR and LF.
func CheckCommitMessage(m string) (string, error) {
	m = strings.Trim(m, " \t\r\n")
	if m == "" || len(m) > 8192 {
		return "", fmt.Errorf("a commit message of %d bytes (1 to 8192)", len(m))
	}
	for i := 0; i < len(m); i++ {
		if c := m[i]; c == 0x7f || c < 0x20 && c != '\t' && c != '\r' && c != '\n' {
			return "", fmt.Errorf("a control character in the commit message")
		}
	}
	return m, nil
}

// IsGitChanged reports whether an event is "0 git changed": ask again.
func IsGitChanged(ev Event) bool {
	return ev.Name == "git" && len(ev.Args) == 1 && ev.Args[0] == "changed"
}

// ValidRev reports whether rhun hands a revision to git: 1 to 200 bytes of letters, digits and
// . _ / ~ ^ @ { } + -, not starting with -.
func ValidRev(rev string) bool {
	if rev == "" || len(rev) > 200 || rev[0] == '-' {
		return false
	}
	for i := 0; i < len(rev); i++ {
		c := rev[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._/~^@{}+-", c) >= 0) {
			return false
		}
	}
	return true
}

func (e *Ext) gitData() error {
	if !e.Has(FeatureGitData) {
		return &Error{Code: "unknown", Text: "git-data was not negotiated"}
	}
	return nil
}

func invalidReply(what string) error { return fmt.Errorf("rhun: invalid %s reply", what) }

// GitRepo is the project's repository (git-repo).
func (e *Ext) GitRepo(ctx context.Context) (GitRepo, error) {
	if err := e.gitData(); err != nil {
		return GitRepo{}, err
	}
	r, err := e.RequestContext(ctx, "git-repo", nil)
	if err != nil {
		return GitRepo{}, err
	}
	repo, ok := ParseGitRepo(r.Body)
	if !ok {
		return GitRepo{}, invalidReply("git-repo")
	}
	return repo, nil
}

// GitStatus is the status (git-status), in git's order.
func (e *Ext) GitStatus(ctx context.Context) ([]GitStatusEntry, error) {
	if err := e.gitData(); err != nil {
		return nil, err
	}
	r, err := e.RequestContext(ctx, "git-status", nil)
	if err != nil {
		return nil, err
	}
	st, ok := ParseGitStatus(r.Body)
	if !ok {
		return nil, invalidReply("git-status")
	}
	return st, nil
}

// GitBlame is the blame of an open document's current text (git-blame).
func (e *Ext) GitBlame(ctx context.Context, doc Doc) (Blame, error) {
	if err := e.gitData(); err != nil {
		return Blame{}, err
	}
	r, err := e.RequestContext(ctx, "git-blame", nil, strconv.FormatInt(int64(doc), 10))
	if err != nil {
		return Blame{}, err
	}
	b, ok := ParseBlame(r.Body)
	if !ok {
		return Blame{}, invalidReply("git-blame")
	}
	return b, nil
}

// GitLog lists at most max (1 to 1000) commits: of HEAD (path ""), of a file (l1 = l2 = 0,
// following renames), or of its lines l1 to l2 as they are at HEAD.
func (e *Ext) GitLog(ctx context.Context, path string, l1, l2, max int) ([]LogEntry, error) {
	if err := e.gitData(); err != nil {
		return nil, err
	}
	r, err := e.RequestContext(ctx, "git-log", []byte(path), strconv.Itoa(max), strconv.Itoa(l1), strconv.Itoa(l2), strconv.Itoa(len(path)))
	if err != nil {
		return nil, err
	}
	log, ok := ParseLog(r.Body)
	if !ok {
		return nil, invalidReply("git-log")
	}
	return log, nil
}

// GitShow is a file's bytes at a revision (git-show), at most 4 MiB.
func (e *Ext) GitShow(ctx context.Context, rev, path string) ([]byte, error) {
	if err := e.gitData(); err != nil {
		return nil, err
	}
	if !ValidRev(rev) {
		return nil, &Error{Code: "syntax", Text: "invalid revision"}
	}
	r, err := e.RequestContext(ctx, "git-show", []byte(path), rev, strconv.Itoa(len(path)))
	if err != nil {
		return nil, err
	}
	return r.Body, nil
}

// GitDiffOpen opens rhun's diff view of a file from rev1 to rev2 ("" or "-": the work tree).
func (e *Ext) GitDiffOpen(ctx context.Context, rev1, rev2, path string) error {
	if err := e.gitData(); err != nil {
		return err
	}
	if rev2 == "" {
		rev2 = "-"
	}
	if !ValidRev(rev1) || rev2 != "-" && !ValidRev(rev2) {
		return &Error{Code: "syntax", Text: "invalid revision"}
	}
	_, err := e.RequestContext(ctx, "git-diff-open", []byte(path), rev1, rev2, strconv.Itoa(len(path)))
	return err
}

// framed reads length-framed bodies: a header line of space-separated words, then raw bytes.
type framed struct {
	b   []byte
	pos int
	bad bool
}

func (f *framed) word() string {
	if f.pos < len(f.b) && f.b[f.pos] == ' ' {
		f.pos++
	}
	s := f.pos
	for f.pos < len(f.b) && f.b[f.pos] != ' ' && f.b[f.pos] != '\n' {
		f.pos++
	}
	if f.pos == s {
		f.bad = true
	}
	return string(f.b[s:f.pos])
}

func (f *framed) num() int64 {
	w := f.word()
	if w == "" || len(w) > 18 {
		f.bad = true
		return 0
	}
	n, err := strconv.ParseInt(w, 10, 64)
	if err != nil || n < 0 || w[0] == '+' {
		f.bad = true
	}
	return n
}

func (f *framed) eol() {
	if f.pos < len(f.b) && f.b[f.pos] == '\n' {
		f.pos++
	} else {
		f.bad = true
	}
}

func (f *framed) bytes(n int64) string {
	if f.bad || n < 0 || n > int64(len(f.b)-f.pos) {
		f.bad = true
		return ""
	}
	s := string(f.b[f.pos : f.pos+int(n)])
	f.pos += int(n)
	return s
}

func (f *framed) ok() bool { return !f.bad && f.pos == len(f.b) }

// ParseGitRepo decodes a git-repo body.
func ParseGitRepo(body []byte) (GitRepo, bool) {
	f := &framed{b: body}
	rl, bl, ul := f.num(), f.num(), f.num()
	r := GitRepo{Head: f.word(), Ahead: f.num(), Behind: f.num(), State: f.word()}
	f.eol()
	r.Root, r.Branch, r.Upstream = f.bytes(rl), f.bytes(bl), f.bytes(ul)
	switch r.Head {
	case "branch", "detached", "unborn":
	default:
		return GitRepo{}, false
	}
	switch r.State {
	case "clean", "dirty", "merging", "rebasing":
	default:
		return GitRepo{}, false
	}
	return r, f.ok()
}

// ParseGitStatus decodes a git-status body.
func ParseGitStatus(body []byte) ([]GitStatusEntry, bool) {
	f := &framed{b: body}
	n := f.num()
	f.eol()
	var out []GitStatusEntry
	for i := int64(0); i < n && !f.bad; i++ {
		xy := f.word()
		l := f.num()
		f.eol()
		if len(xy) != 2 {
			return nil, false
		}
		out = append(out, GitStatusEntry{X: xy[0], Y: xy[1], Path: f.bytes(l)})
	}
	return out, f.ok()
}

// ParseBlame decodes a git-blame body.
func ParseBlame(body []byte) (Blame, bool) {
	f := &framed{b: body}
	nc, nr := f.num(), f.num()
	f.eol()
	var b Blame
	for i := int64(0); i < nc && !f.bad; i++ {
		c := BlameCommit{SHA: f.word(), Time: f.num()}
		al, ml, sl := f.num(), f.num(), f.num()
		f.eol()
		c.Author, c.Mail, c.Summary = f.bytes(al), f.bytes(ml), f.bytes(sl)
		b.Commits = append(b.Commits, c)
	}
	for i := int64(0); i < nr && !f.bad; i++ {
		r := BlameRun{SHA: f.word(), L1: int(f.num()), L2: int(f.num()), Orig: int(f.num())}
		f.eol()
		b.Runs = append(b.Runs, r)
	}
	return b, f.ok()
}

// ParseLog decodes a git-log body.
func ParseLog(body []byte) ([]LogEntry, bool) {
	f := &framed{b: body}
	n := f.num()
	f.eol()
	var out []LogEntry
	for i := int64(0); i < n && !f.bad; i++ {
		e := LogEntry{SHA: f.word()}
		if p := f.word(); p != "-" {
			e.Parents = strings.Split(p, ",")
		}
		e.Time = f.num()
		al, sl := f.num(), f.num()
		f.eol()
		e.Author, e.Subject = f.bytes(al), f.bytes(sl)
		out = append(out, e)
	}
	return out, f.ok()
}

// ParseCommitMessageRequest decodes a commit-message body: BRANCH_LEN DIFF_LEN LF <BRANCH><DIFF>.
func ParseCommitMessageRequest(id int64, body []byte, ctx context.Context) (CommitMessageRequest, bool) {
	f := &framed{b: body}
	bl, dl := f.num(), f.num()
	f.eol()
	r := CommitMessageRequest{ID: id, Branch: f.bytes(bl), Diff: f.bytes(dl), Context: ctx}
	return r, f.ok()
}
