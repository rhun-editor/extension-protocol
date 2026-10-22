// git-history: the project's commits in a sidebar tree (protocol 1.1, features view-tree and
// git-data). rhun reads the history (git-log, at most 1000 commits) and the tree shows it 200 at a
// time: the "Load more" row asks for the next page (vmore). A commit's changed files are asked for
// when it is expanded (vkids: git show --name-status, run by the extension); a click on a file
// opens it.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

const page = 200

type history struct {
	mu   sync.Mutex
	root string
	view rhunext.View
	log  []rhunext.LogEntry // the history rhun gave, newest first
}

func gitOut(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	return cmd.Output()
}

// ago is a time as "N units ago".
func ago(t int64, now time.Time) string {
	d := now.Sub(time.Unix(t, 0))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
	return time.Unix(t, 0).Format("2006-01-02")
}

// commitNodes are the commits from skip on, a page of them as closed nodes; more when there are
// others after them.
func commitNodes(log []rhunext.LogEntry, skip int, now time.Time) ([]*rhunext.Node, bool) {
	if skip > len(log) {
		skip = len(log)
	}
	list := log[skip:]
	more := len(list) > page
	if more {
		list = list[:page]
	}
	nodes := make([]*rhunext.Node, 0, len(list))
	for _, c := range list {
		short := c.SHA
		if len(short) > 7 {
			short = short[:7]
		}
		nodes = append(nodes, rhunext.NewNode("c:"+c.SHA, c.Subject).WithKids(rhunext.KidsClosed).
			Desc(short+" "+c.Author+", "+ago(c.Time, now)).Icon("branch", "").Tip(c.SHA))
	}
	return nodes, more
}

// fileNodes are a commit's changed files (git show --name-status), opening the working copy.
func fileNodes(root, hash string) ([]*rhunext.Node, error) {
	out, err := gitOut(root, "show", "--name-status", "--format=", hash)
	if err != nil {
		return nil, err
	}
	var nodes []*rhunext.Node
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 2 {
			continue
		}
		status, path := f[0], f[len(f)-1]
		color := map[byte]rhunext.ViewColor{'A': "add", 'M': "mod", 'D': "del", 'R': "mod"}[status[0]]
		n := rhunext.NewNode("f:"+hash+":"+path, filepath.Base(path)).Desc(status+" "+filepath.Dir(path)).
			Icon("file:"+filepath.Base(path), "")
		if color != "" {
			n.Color(color)
		}
		if status[0] != 'D' {
			n.Open(filepath.Join(root, path), 1, 1)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (h *history) fill(ext *rhunext.Ext) {
	if h.view == 0 {
		return
	}
	p := (&rhunext.Patch{}).Title("History")
	log, err := ext.GitLog(context.Background(), "", 0, 0, 1000)
	if err != nil {
		p.State("error", "No Git history here: "+err.Error())
	} else {
		h.mu.Lock()
		h.log = log
		h.mu.Unlock()
		nodes, more := commitNodes(log, 0, time.Now())
		p.Kids(nil, nodes, more).State("ready", "")
	}
	if _, err := ext.PatchView(h.view, p); err != nil {
		ext.Logf("patch: %v", err)
	}
}

func (h *history) Ready(ext *rhunext.Ext, project string) { h.root = project }

func (h *history) Run(ext *rhunext.Ext, command string, doc rhunext.Doc) {
	if command == "git-history.refresh" {
		h.fill(ext)
	}
}

func (h *history) Event(ext *rhunext.Ext, ev rhunext.Event) {}

func (h *history) ViewOpen(ext *rhunext.Ext, open rhunext.ViewOpen) {
	if open.Name == "history" {
		h.view = open.View
		h.fill(ext)
	}
}

func (h *history) ViewEvent(ext *rhunext.Ext, view rhunext.View, ev rhunext.RetainedEvent) {}

// ViewChildren answers vkids: a commit's files.
func (h *history) ViewChildren(ext *rhunext.Ext, req rhunext.ChildrenRequest) (*rhunext.Patch, error) {
	id := string(req.Node)
	if !strings.HasPrefix(id, "c:") {
		return nil, fmt.Errorf("no children")
	}
	nodes, err := fileNodes(h.root, id[2:])
	if err != nil {
		return nil, err
	}
	return (&rhunext.Patch{}).Kids(req.Node, nodes, false), nil
}

// ViewMore answers vmore: the next page of commits after the ones rhun has.
func (h *history) ViewMore(ext *rhunext.Ext, req rhunext.ChildrenRequest) (*rhunext.Patch, error) {
	h.mu.Lock()
	log := h.log
	h.mu.Unlock()
	nodes, more := commitNodes(log, int(req.Have), time.Now())
	return (&rhunext.Patch{}).Add(nil, nodes, more), nil
}

func main() {
	if err := rhunext.RunWith([]string{rhunext.FeatureViewTree, rhunext.FeatureGitData}, &history{}); err != nil {
		os.Exit(1)
	}
}
