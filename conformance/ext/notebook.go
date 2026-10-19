package main

// Notebooks (feature "notebooks", v1.1.md): with [provides] notebook and the feature agreed, the
// checker opens a new notebook (nb-open with no bytes), saves what came back (nb-save) and opens
// the saved bytes again, expecting the same cells; validates every notebook and output body; runs
// a code cell when notebook.kernel is not "no" and checks the nb-state, nb-output, nb-stream and
// nb-kernel-label requests that follow name that run; and closes the notebook during a run, which
// the extension must survive.

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

// bigReplies are the ids of rhun's requests whose answers may carry 64 MB (nb-open, nb-save): the
// reader goroutine looks them up
var bigReplies sync.Map

// nbState is the checker's notebook: its id, its cells' ids and their latest runs
type nbState struct {
	id      int64
	cells   []int64
	runs    map[int64]int64 // cell -> latest run
	states  map[int64]string
	outputs int
	streams int
	labels  int
	closed  bool
}

var nbCur *nbState

func (h *host) nbRequest(id int64, verb string, args []string, body []byte, reply func(string), rerr func(string, string)) {
	if !h.features["notebooks"] {
		h.fail("features", verb+" without notebooks agreed after hello (hello 1 notebooks)")
		rerr("unknown", "no such request")
		return
	}
	nb := nbCur
	num := func(s string) (int64, bool) {
		v, err := strconv.ParseInt(s, 10, 64)
		return v, err == nil && v >= 0
	}
	if verb == "nb-kernel-label" {
		if len(args) != 3 {
			h.fail("notebooks", fmt.Sprintf("nb-kernel-label takes NB STATE LEN, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		switch args[1] {
		case "idle", "busy", "starting", "dead", "none":
		default:
			h.fail("notebooks", fmt.Sprintf("nb-kernel-label state %q is not idle busy starting dead none", args[1]))
			rerr("syntax", "cannot parse the line")
			return
		}
		if n, ok := num(args[0]); !ok || nb == nil || n != nb.id {
			rerr("stale", "no such notebook")
			return
		}
		if len(body) > 200 || strings.ContainsAny(string(body), "\n\r") {
			h.warn("notebooks", "a kernel label over 200 bytes or with line breaks: rhun cuts it to one line")
		}
		nb.labels++
		reply(fmt.Sprintf("ok %d", id))
		return
	}
	want := map[string]int{"nb-state": 5, "nb-output": 6, "nb-stream": 5}[verb]
	if len(args) != want {
		h.fail("notebooks", fmt.Sprintf("%s takes %d fields, got %q", verb, want, strings.Join(args, " ")))
		rerr("syntax", "cannot parse the line")
		return
	}
	nbID, ok1 := num(args[0])
	cell, ok2 := num(args[1])
	run, ok3 := num(args[2])
	if !ok1 || !ok2 || !ok3 {
		h.fail("notebooks", verb+": NB CELL RUN must be numbers")
		rerr("syntax", "cannot parse the line")
		return
	}
	if nb == nil || nb.closed || nbID != nb.id || nb.runs[cell] != run || run == 0 {
		rerr("stale", "the run was superseded")
		return
	}
	switch verb {
	case "nb-state":
		switch args[3] {
		case "queued", "running", "done", "failed", "cancelled":
		default:
			h.fail("notebooks", fmt.Sprintf("nb-state %q is not queued running done failed cancelled", args[3]))
			rerr("syntax", "cannot parse the line")
			return
		}
		if args[4] != "-" {
			if _, ok := num(args[4]); !ok {
				h.fail("notebooks", "nb-state's COUNT is a number or -")
				rerr("syntax", "cannot parse the line")
				return
			}
		}
		nb.states[cell] = args[3]
	case "nb-output":
		switch args[3] {
		case "append", "replace":
			if _, err := rhunext.ParseOutput(body); err != nil {
				h.fail("notebooks", "nb-output's body: "+err.Error())
				rerr("syntax", "cannot parse the body")
				return
			}
		case "clear", "clear-wait":
			if len(body) != 0 {
				h.fail("notebooks", args[3]+" carries no body (LEN 0)")
			}
		default:
			h.fail("notebooks", fmt.Sprintf("nb-output op %q is not append replace clear clear-wait", args[3]))
			rerr("syntax", "cannot parse the line")
			return
		}
		nb.outputs++
	case "nb-stream":
		if args[3] != "stdout" && args[3] != "stderr" {
			h.fail("notebooks", fmt.Sprintf("nb-stream name %q is not stdout or stderr", args[3]))
			rerr("syntax", "cannot parse the line")
			return
		}
		if len(body) > rhunext.NbStreamMax {
			h.fail("notebooks", "an nb-stream over 64 KB: rhun stops the extension")
		}
		nb.streams++
	}
	reply(fmt.Sprintf("ok %d", id))
}

// askNotebook sends nb-open or nb-save with its body and waits for the answer
func (h *host) askNotebook(verb string, nb int64, body []byte) (message, bool) {
	h.hostSeq++
	id := h.hostSeq
	h.hostReqs[id] = verb
	bigReplies.Store(id, true)
	defer bigReplies.Delete(id)
	h.write(fmt.Sprintf("%d %s %d %d\n%s", id, verb, nb, len(body), body))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m, ok := h.hostAnswers[id]; ok {
			delete(h.hostAnswers, id)
			return m, true
		}
		msg, err := h.readMessage(100 * time.Millisecond)
		if err == nil && msg.line != "" {
			h.handle(msg)
		}
	}
	delete(h.hostReqs, id)
	h.write(fmt.Sprintf("0 cancel %d\n", id))
	return message{}, false
}

func (h *host) checkNotebooks() {
	pat := h.m.provides["notebook"]
	if pat == "" && !h.features["notebooks"] {
		return
	}
	if pat == "" {
		h.fail("provides", "notebooks agreed after hello but [provides] has no notebook patterns: rhun never opens one")
		return
	}
	if !h.features["notebooks"] {
		h.fail("provides", "[provides] notebook without saying notebooks after hello 1: rhun cannot ask")
		return
	}
	nb := &nbState{id: 1, runs: map[int64]int64{}, states: map[int64]string{}}
	nbCur = nb
	path := h.project + "/conformance-new." + strings.TrimPrefix(strings.Fields(pat)[0], "*.")
	open := func(data []byte) (*rhunext.Notebook, bool) {
		m, ok := h.askNotebook("nb-open", nb.id, []byte(fmt.Sprintf("%d %d\n%s%s", len(path), len(data), path, data)))
		switch {
		case !ok:
			h.fail("notebooks", "no answer to nb-open within 10 s")
		case strings.HasPrefix(m.line, "error "):
			h.fail("notebooks", "nb-open answered "+m.line)
		default:
			n, err := rhunext.ParseNotebook(m.body)
			if err != nil {
				h.fail("notebooks", "nb-open's notebook body: "+err.Error())
				return nil, false
			}
			return n, true
		}
		return nil, false
	}
	n, ok := open(nil)
	if !ok {
		return
	}
	h.ok(fmt.Sprintf("opens a new notebook (%d cells)", len(n.Cells)))
	if len(n.Cells) == 0 {
		n.Cells = append(n.Cells, rhunext.Cell{Kind: rhunext.CellCode, Lang: "-", Count: -1})
	}
	n.Cells[0].Source = []byte("1 + 1")
	enc := n.Encode()
	m, ok := h.askNotebook("nb-save", nb.id, []byte(fmt.Sprintf("%d\n%s%s", len(path), path, enc)))
	switch {
	case !ok:
		h.fail("notebooks", "no answer to nb-save within 10 s")
		return
	case strings.HasPrefix(m.line, "error "):
		h.fail("notebooks", "nb-save answered "+m.line)
		return
	}
	back, ok := open(m.body)
	if !ok {
		return
	}
	same := len(back.Cells) == len(n.Cells)
	for i := 0; same && i < len(n.Cells); i++ {
		same = back.Cells[i].Kind == n.Cells[i].Kind && string(back.Cells[i].Source) == string(n.Cells[i].Source)
	}
	if !same {
		h.fail("notebooks", "the bytes nb-save gave open as other cells (kinds or sources differ)")
	} else {
		h.ok("saves a notebook and opens what it saved as the same cells")
	}
	if strings.TrimSpace(h.m.provides["notebook.kernel"]) == "no" {
		return
	}
	// a run, then another one cut short by the notebook closing
	cell, lang := int64(7), back.Cells[0].Lang
	if lang == "" {
		lang = "-"
	}
	nb.cells = []int64{cell}
	nb.runs[cell] = 1
	src := "1 + 1"
	h.write(fmt.Sprintf("0 nb-run %d %d 1 %s %d\n%s", nb.id, cell, lang, len(src), src))
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := nb.states[cell]
		if st == "done" || st == "failed" || st == "cancelled" {
			break
		}
		if msg, err := h.readMessage(100 * time.Millisecond); err == nil && msg.line != "" {
			h.handle(msg)
		}
	}
	switch st := nb.states[cell]; st {
	case "done", "failed", "cancelled":
		h.ok(fmt.Sprintf("runs a cell: %s (%d outputs, %d streams)", st, nb.outputs, nb.streams))
	case "":
		h.warn("notebooks", "no nb-state for a run within 15 s (a kernel may take longer to start)")
	default:
		h.warn("notebooks", "the run did not finish within 15 s: "+st)
	}
	nb.runs[cell] = 2
	h.write(fmt.Sprintf("0 nb-run %d %d 2 %s %d\n%s0 nb-closed %d\n", nb.id, cell, lang, len(src), src, nb.id))
	nb.closed = true
	h.serve(700 * time.Millisecond)
	if h.cmdExited() {
		h.fail("notebooks", "the program exited when its notebook closed during a run")
		return
	}
	h.ok("survives nb-closed during a run")
	nbCur = nil
}
