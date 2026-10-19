package rhunext

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// readBody reads a request's body of exactly len(want) bytes and checks it.
func readBody(t *testing.T, s *host, want string) {
	t.Helper()
	b := make([]byte, len(want))
	if _, err := io.ReadFull(s.br, b); err != nil || string(b) != want {
		t.Fatalf("body %q %v, want %q", b, err, want)
	}
}

func TestParseGitRepoAndStatus(t *testing.T) {
	r, ok := ParseGitRepo([]byte("4 4 11 branch 1 2 dirty\n/tmpmainorigin/main"))
	if !ok || r.Root != "/tmp" || r.Branch != "main" || r.Upstream != "origin/main" || r.Head != "branch" ||
		r.Ahead != 1 || r.Behind != 2 || r.State != "dirty" {
		t.Fatalf("%+v %v", r, ok)
	}
	if _, ok := ParseGitRepo([]byte("4 4 11 branch 1 2 dirty\n/tmpmainorigin/mainx")); ok {
		t.Fatal("trailing bytes accepted")
	}
	if _, ok := ParseGitRepo([]byte("4 4 0 nope 0 0 clean\n/tmpmain")); ok {
		t.Fatal("bad head accepted")
	}
	st, ok := ParseGitStatus([]byte("2\n.M 5\na.txt?? 12\nnew file.txt"))
	if !ok || len(st) != 2 || st[0].X != '.' || st[0].Y != 'M' || st[1].Path != "new file.txt" {
		t.Fatalf("%+v %v", st, ok)
	}
	if _, ok := ParseGitStatus([]byte("2\n.M 5\na.txt")); ok {
		t.Fatal("short status accepted")
	}
}

func TestParseBlameAndLog(t *testing.T) {
	z, a := strings.Repeat("0", 40), strings.Repeat("a", 40)
	b, ok := ParseBlame([]byte("2 2\n" + a + " 100 3 7 5\nAdaada@x.yFirst" + z + " 200 3 3 4\nNotnotWork" +
		a + " 1 2 1\n" + z + " 3 3 3\n"))
	if !ok || b.Line(2).Summary != "First" || b.Line(4) != nil || !IsUncommitted(b.Runs[1].SHA) || b.Commits[0].Mail != "ada@x.y" {
		t.Fatalf("%+v %v", b, ok)
	}
	log, ok := ParseLog([]byte("2\n" + a + " " + z + " 10 3 4\nAdaMore" + z + " - 9 3 5\nAdaFirst"))
	if !ok || len(log) != 2 || log[0].Parents[0] != z || len(log[1].Parents) != 0 || log[1].Subject != "First" {
		t.Fatalf("%+v %v", log, ok)
	}
}

func TestChecks(t *testing.T) {
	if m, err := CheckCommitMessage("  Add x\n\nBody\n"); err != nil || m != "Add x\n\nBody" {
		t.Fatal(m, err)
	}
	if _, err := CheckCommitMessage("\x1b[31mred"); err == nil {
		t.Fatal("control character accepted")
	}
	if !ValidRev("HEAD~2") || !ValidRev("main@{1}") || ValidRev("-x") || ValidRev("a:b") {
		t.Fatal("revisions")
	}
}

type commitProbe struct {
	ready chan *Ext
	reqs  chan CommitMessageRequest
	wait  bool
}

func (p *commitProbe) Ready(e *Ext, _ string) { p.ready <- e }
func (p *commitProbe) Run(*Ext, string, Doc)  {}
func (p *commitProbe) Event(*Ext, Event)      {}
func (p *commitProbe) CommitMessage(e *Ext, req CommitMessageRequest) (string, error) {
	p.reqs <- req
	if p.wait {
		<-req.Context.Done()
		return "", errors.New("stopped")
	}
	if req.Branch == "fail" {
		return "", errors.New("no model")
	}
	return "  Add " + req.Diff + "\n", nil
}

func TestCommitMessageProvider(t *testing.T) {
	p := &commitProbe{ready: make(chan *Ext, 1), reqs: make(chan CommitMessageRequest, 4)}
	s := startHost(t, []string{FeatureCommitMessage, FeatureGitData}, p)
	s.expect("hello 1 commit-message git-data")
	s.send("0 features commit-message git-data\n0 ready /project\n")
	<-p.ready
	s.send("5 commit-message 9\n4 1\nmainx")
	s.answer(5, "Add x")
	s.send("6 commit-message 9\n4 1\nfailx")
	s.expect("error 6 refused no model")
	p.wait = true
	s.send("7 commit-message 9\n4 1\nmainy")
	select {
	case req := <-p.reqs:
		for req.ID != 7 {
			req = <-p.reqs
		}
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
	s.send("0 cancel 7\n")
	s.expect("error 7 cancelled generation was cancelled")
	s.send("0 bye\n")
	<-s.done
}

func TestGitDataRequests(t *testing.T) {
	p := &commitProbe{ready: make(chan *Ext, 1), reqs: make(chan CommitMessageRequest, 1)}
	s := startHost(t, []string{FeatureGitData}, p)
	s.expect("hello 1 git-data")
	s.send("0 features git-data\n0 ready /project\n")
	ext := <-p.ready
	done := make(chan error, 1)
	go func() {
		st, err := ext.GitStatus(context.Background())
		if err == nil && (len(st) != 1 || st[0].Path != "a b") {
			err = errors.New("status")
		}
		if err == nil {
			_, err = ext.GitLog(context.Background(), "a.txt", 2, 3, 10)
		}
		if err == nil {
			var b []byte
			b, err = ext.GitShow(context.Background(), "HEAD", "a.txt")
			if err == nil && string(b) != "hi\n" {
				err = errors.New("show")
			}
		}
		if err == nil {
			err = ext.GitDiffOpen(context.Background(), "HEAD~1", "", "a.txt")
		}
		done <- err
	}()
	s.expect("1 git-status")
	s.send("ok 1 10\n1\n.M 3\na b")
	select {
	case err := <-done:
		t.Fatalf("ended early: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	s.expect("2 git-log 10 2 3 5")
	readBody(t, s, "a.txt")
	s.send("ok 2 2\n0\n")
	s.expect("3 git-show HEAD 5")
	readBody(t, s, "a.txt")
	s.send("ok 3 3\nhi\n")
	s.expect("4 git-diff-open HEAD~1 - 5")
	readBody(t, s, "a.txt")
	s.send("ok 4\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
