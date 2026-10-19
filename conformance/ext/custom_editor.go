package main

// Custom editors (feature "custom-editors", v1.1.md): with [provides] editor and the feature
// agreed, the checker opens a file of a matching name in the editor (editor-open, with a view it
// counts as open), serves editor-read from the file and editor-dirty, and for an owned model asks
// editor-save part by part until MORE is 0, expecting the file's bytes back (nothing was edited);
// then editor-undo, and editor-closed, which the extension must survive.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// edState is the checker's open editor: its CDOC and the file it serves
type edState struct {
	cdoc  int64
	data  []byte
	reads int
	dirty int
}

var edCur *edState

func (h *host) edRequest(id int64, verb string, args []string, reply func(string), rerr func(string, string)) {
	if !h.features["custom-editors"] {
		h.fail("features", verb+" without custom-editors agreed after hello (hello 1 custom-editors)")
		rerr("unknown", "no such request")
		return
	}
	e := edCur
	num := func(s string) (int64, bool) {
		v, err := strconv.ParseInt(s, 10, 64)
		return v, err == nil && v >= 0
	}
	switch verb {
	case "editor-read":
		if len(args) != 3 {
			h.fail("editors", fmt.Sprintf("editor-read takes CDOC OFFSET MAX, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		cdoc, ok1 := num(args[0])
		off, ok2 := num(args[1])
		max, ok3 := num(args[2])
		if !ok1 || !ok2 || !ok3 {
			h.fail("editors", "editor-read: CDOC OFFSET MAX must be numbers")
			rerr("syntax", "cannot parse the line")
			return
		}
		if e == nil || cdoc != e.cdoc {
			rerr("stale", "no such editor")
			return
		}
		if max > 4<<20 {
			max = 4 << 20
		}
		start := min(off, int64(len(e.data)))
		end := min(start+max, int64(len(e.data)))
		e.reads++
		part := e.data[start:end]
		h.write(fmt.Sprintf("ok %d %d\n%s", id, len(part), part))
	case "editor-dirty":
		if len(args) != 2 || (args[1] != "0" && args[1] != "1") {
			h.fail("editors", fmt.Sprintf("editor-dirty takes CDOC 0|1, got %q", strings.Join(args, " ")))
			rerr("syntax", "cannot parse the line")
			return
		}
		if cdoc, _ := num(args[0]); e == nil || cdoc != e.cdoc {
			rerr("stale", "no such editor")
			return
		}
		e.dirty++
		reply(fmt.Sprintf("ok %d", id))
	}
}

// askEditor sends a host request (line already formatted after the id) and waits for its answer
func (h *host) askEditor(verb, rest string, body []byte) (message, bool) {
	h.hostSeq++
	id := h.hostSeq
	h.hostReqs[id] = verb
	bigReplies.Store(id, true)
	defer bigReplies.Delete(id)
	if body != nil {
		h.write(fmt.Sprintf("%d %s %s\n%s", id, verb, rest, body))
	} else {
		h.write(fmt.Sprintf("%d %s %s\n", id, verb, rest))
	}
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

func (h *host) checkEditors() {
	pat := h.m.provides["editor"]
	if pat == "" && !h.features["custom-editors"] {
		return
	}
	if pat == "" {
		h.fail("provides", "custom-editors agreed after hello but [provides] has no editor patterns: rhun never opens one")
		return
	}
	if !h.features["custom-editors"] {
		h.fail("provides", "[provides] editor without saying custom-editors after hello 1: rhun cannot open it")
		return
	}
	model := h.m.provides["editor.model"]
	if model == "" {
		model = "text"
	}
	if !h.features["views"] {
		h.fail("editors", "custom-editors without views: an editor draws its 12a view")
	}
	ext := strings.TrimPrefix(strings.Fields(pat)[0], "*")
	if !strings.HasPrefix(ext, ".") {
		ext = ".bin"
	}
	path := h.project + "/conformance-editor" + ext
	data := make([]byte, 5<<20+123) // more than one 4 MB read and part
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		h.warn("editors", "cannot write the test file: "+err.Error())
		return
	}
	defer os.Remove(path)
	e := &edState{cdoc: 9001, data: data}
	if model == "text" {
		// a text model edits rhun's document: it is opened first, with a smaller text
		data = []byte("first line\nsecond line\n")
		os.WriteFile(path, data, 0o644)
		d := h.newDoc(path, "", data)
		e = &edState{cdoc: d.id, data: data}
		h.write(fmt.Sprintf("0 opened %d %d - %s\n", d.id, d.version, d.path))
	}
	edCur = e
	h.nextView++
	view := h.nextView
	h.views[view] = true
	m, ok := h.askEditor("editor-open", fmt.Sprintf("%d %d %s file %d", e.cdoc, view, model, len(path)), []byte(path))
	switch {
	case !ok:
		h.fail("editors", "no answer to editor-open within 10 s")
		return
	case strings.HasPrefix(m.line, "error "):
		h.fail("editors", "editor-open answered "+m.line)
		return
	case strings.Fields(m.line)[len(strings.Fields(m.line))-1] != "0":
		h.fail("editors", "editor-open's answer is ok ID 0, got "+m.line)
	}
	h.ok(fmt.Sprintf("opens a file in its editor (%s model, %d editor-reads)", model, e.reads))
	if model == "owned" {
		var got []byte
		for part := 0; part < 300; part++ {
			m, ok := h.askEditor("editor-save", fmt.Sprintf("%d save %d", e.cdoc, part), nil)
			if !ok || strings.HasPrefix(m.line, "error ") {
				h.fail("editors", fmt.Sprintf("editor-save part %d: %s", part, m.line))
				return
			}
			nl := strings.IndexByte(string(m.body), '\n')
			if nl != 1 || (m.body[0] != '0' && m.body[0] != '1') {
				h.fail("editors", "an editor-save answer starts with a line 0 or 1 (MORE)")
				return
			}
			if len(m.body)-2 > 4<<20 {
				h.fail("editors", "an editor-save part over 4 MB")
				return
			}
			got = append(got, m.body[2:]...)
			if m.body[0] == '0' {
				break
			}
		}
		if string(got) != string(data) {
			h.fail("editors", fmt.Sprintf("the parts of an unedited file are not its bytes (%d bytes, want %d)", len(got), len(data)))
		} else {
			h.ok(fmt.Sprintf("saves in parts: the file's %d bytes back", len(got)))
		}
		if m, ok := h.askEditor("editor-undo", fmt.Sprint(e.cdoc), nil); !ok || !strings.HasPrefix(m.line, "ok ") {
			h.fail("editors", "editor-undo answered "+m.line+" (ok ID 0, even with nothing to undo)")
		} else {
			h.ok("answers editor-undo")
		}
	}
	h.write(fmt.Sprintf("0 editor-closed %d\n", e.cdoc))
	h.serve(500 * time.Millisecond)
	if h.cmdExited() {
		h.fail("editors", "the program exited when its editor closed")
		return
	}
	h.ok("survives editor-closed")
	edCur = nil
}
