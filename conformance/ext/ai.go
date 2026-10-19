package main

// Protocol 1.1's agents, proposals and tools (v1.1/v1.1.md, "Agents, proposals and tools"): the
// checker answers agent-sessions, agent-changed, propose, unpropose, tools, tool-call and
// agent-ask as rhun does, validating their grammar, and asks an agents extension for a transcript,
// a tools extension's tools for their output (and an agent-prompts one for a session).

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

// what the extension told the checker
var aiState = struct {
	sessions  []string // session ids, in its last list
	proposals int64
	denied    bool // the first call of a tool that changes something is denied, as a user may
}{}

// checkSessions validates an agent-sessions body: N, then rows and their strings.
func checkSessions(b []byte) ([]string, string) {
	r := bytes.NewReader(b)
	var n int
	if _, err := fmt.Fscanf(r, "%d\n", &n); err != nil {
		return nil, "the body does not start with N and a line feed"
	}
	if n < 0 || n > 1000 {
		return nil, fmt.Sprintf("%d rows: at most 1000", n)
	}
	if len(b) > 1<<20 {
		return nil, "over 1 MB"
	}
	var ids []string
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		var updated int64
		var state string
		var sl, tl, ml, cl int
		if _, err := fmt.Fscanf(r, "%d %s %d %d %d %d\n", &updated, &state, &sl, &tl, &ml, &cl); err != nil {
			return nil, fmt.Sprintf("row %d is not UPDATED STATE SID_LEN TITLE_LEN MODEL_LEN CWD_LEN", i+1)
		}
		switch state {
		case "idle", "working", "waiting", "error":
		default:
			return nil, fmt.Sprintf("row %d: state %q is not idle, working, waiting or error", i+1, state)
		}
		if tl > 4096 || ml > 4096 || cl > 4096 {
			return nil, fmt.Sprintf("row %d: a field over 4096 bytes", i+1)
		}
		take := func(k int) (string, bool) {
			s := make([]byte, k)
			if m, _ := r.Read(s); m != k && k > 0 {
				return "", false
			}
			return string(s), true
		}
		sid, ok1 := take(sl)
		_, ok2 := take(tl)
		_, ok3 := take(ml)
		cwd, ok4 := take(cl)
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return nil, fmt.Sprintf("row %d: the body ends inside its strings", i+1)
		}
		if !rhunext.SIDOK(sid) {
			return nil, fmt.Sprintf("row %d: session id %q is not 1 to 128 visible ASCII bytes", i+1, sid)
		}
		if seen[sid] {
			return nil, fmt.Sprintf("session id %s twice", sid)
		}
		seen[sid] = true
		if cwd != "" && !strings.HasPrefix(cwd, "/") && !(len(cwd) > 2 && cwd[1] == ':') {
			return nil, fmt.Sprintf("row %d: folder %q is not absolute", i+1, cwd)
		}
		ids = append(ids, sid)
	}
	if r.Len() != 0 {
		return nil, "bytes after the rows"
	}
	return ids, ""
}

// aiRequest answers the AI requests; false for other verbs.
func (h *host) aiRequest(id int64, verb string, args []string, b []byte, bodyOK bool, reply func(string), rerr func(string, string)) bool {
	feature := map[string]string{"agent-sessions": "agents", "agent-changed": "agents", "propose": "edit-review",
		"unpropose": "edit-review", "tools": "tools", "tool-call": "tools", "agent-ask": "agent-prompts"}[verb]
	if feature == "" {
		return false
	}
	if !h.features[feature] {
		h.fail(feature, verb+" without the "+feature+" feature agreed: rhun answers unknown")
		rerr("unknown", "no such request")
		return true
	}
	switch verb {
	case "agent-sessions":
		if !bodyOK {
			return true
		}
		if h.m.provides["agents"] != "yes" {
			h.fail("agents", "agent-sessions without [provides] agents = yes: rhun refuses it")
			rerr("refused", "the manifest does not say [provides] agents = yes")
			return true
		}
		ids, why := checkSessions(b)
		if why != "" {
			h.fail("agents", "agent-sessions: "+why)
			rerr("syntax", "the sessions cannot be read")
			return true
		}
		aiState.sessions = ids
		h.ok(fmt.Sprintf("lists %d agent sessions rhun can read", len(ids)))
		reply(fmt.Sprintf("ok %d", id))
	case "agent-changed":
		if len(args) != 3 {
			h.fail("agents", "agent-changed takes SID TOTAL FIRST")
			rerr("syntax", "cannot parse the line")
			return true
		}
		known := false
		for _, s := range aiState.sessions {
			known = known || s == args[0]
		}
		if !known {
			h.fail("agents", "agent-changed names session "+args[0]+", which its list does not have")
			rerr("refused", "no such session")
			return true
		}
		reply(fmt.Sprintf("ok %d", id))
	case "propose":
		if !bodyOK {
			return true
		}
		have := h.m.needs["documents"]
		if have != "read" && have != "write" {
			h.fail("needs", "propose needs documents=read")
			rerr("needs", "documents=read")
			return true
		}
		var ll, sl int
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 || func() bool { _, err := fmt.Sscanf(string(b[:nl]), "%d %d", &ll, &sl); return err != nil }() ||
			len(b) < nl+1+ll+sl {
			h.fail("edit-review", "propose: the head is not LABEL_LEN SID_LEN, a line feed, the label and the session id")
			rerr("syntax", "the proposal cannot be read")
			return true
		}
		if sid := string(b[nl+1+ll : nl+1+ll+sl]); sid != "" && !rhunext.SIDOK(sid) {
			h.fail("edit-review", "propose: session id "+sid+" is not 1 to 128 visible bytes")
		}
		if aiState.proposals >= 8 {
			h.warn("edit-review", "more than 8 proposals waiting: rhun answers toobig")
		}
		aiState.proposals++
		p := aiState.proposals
		reply(fmt.Sprintf("ok %d %d", id, p))
		// the user decides at once: everything accepted
		h.write(fmt.Sprintf("0 proposal %d 1 accepted\n0 proposal %d done 1 0\n", p, p))
		h.ok("proposes an edit with a head rhun reads (decided as accepted)")
	case "unpropose":
		if len(args) != 1 {
			rerr("syntax", "cannot parse the line")
			return true
		}
		reply(fmt.Sprintf("ok %d", id))
	case "tools":
		var buf bytes.Buffer
		tools := []rhunext.ToolInfo{
			{Name: "rhun.read", Readonly: true, Title: "Read", Description: "a document's text", Input: "path PATH or doc DOC"},
			{Name: "rhun.propose", Title: "Propose an edit", Description: "the Review tab", Input: "edit LEN"},
		}
		for _, t := range h.m.tools {
			tools = append(tools, rhunext.ToolInfo{Name: h.m.owner + "/" + h.m.name + "." + t, Title: t})
		}
		fmt.Fprintf(&buf, "%d\n", len(tools))
		for _, t := range tools {
			flags := "rw"
			if t.Readonly {
				flags = "ro"
			}
			fmt.Fprintf(&buf, "%s %s %d %d %d\n%s%s%s", t.Name, flags, len(t.Title), len(t.Description), len(t.Input),
				t.Title, t.Description, t.Input)
		}
		h.write(fmt.Sprintf("ok %d %d\n", id, buf.Len()))
		h.write(buf.String())
	case "tool-call":
		if !bodyOK || len(args) != 2 {
			if len(args) != 2 {
				rerr("syntax", "tool-call takes TOOL LEN")
			}
			return true
		}
		switch {
		case args[0] == "rhun.read":
			out := "hello world\n"
			h.write(fmt.Sprintf("ok %d %d\n%s", id, len(out), out))
		case args[0] == "rhun.propose" && !aiState.denied:
			aiState.denied = true
			rerr("refused", "denied by the user")
			h.ok("a tool call the user denies is answered refused")
		case strings.HasPrefix(args[0], "rhun."):
			h.write(fmt.Sprintf("ok %d 2\nok", id))
		default:
			rerr("refused", "no such tool")
		}
	case "agent-ask":
		if !bodyOK || len(args) != 2 {
			if len(args) != 2 {
				rerr("syntax", "agent-ask takes SID LEN")
			}
			return true
		}
		if !bytes.Contains(b, []byte("\n")) {
			h.fail("agent-prompts", "agent-ask: a prompt line and at least one option line")
			rerr("syntax", "a prompt and options")
			return true
		}
		reply(fmt.Sprintf("ok %d 1", id))
	}
	return true
}

// checkAI asks what rhun asks: a transcript of a listed session, each manifest tool once, and a
// prompt for a new session
func (h *host) checkAI() {
	if h.features["agents"] && len(aiState.sessions) == 0 {
		h.warn("agents", "agents agreed, but no agent-sessions came: the panel shows none of its sessions")
	}
	if h.features["agents"] && len(aiState.sessions) > 0 {
		m, ok := h.ask("agent-transcript " + aiState.sessions[0] + " 0")
		switch {
		case !ok:
			h.fail("agents", "no answer to agent-transcript within 3 s")
		case strings.HasPrefix(m.line, "error "):
			h.warn("agents", "agent-transcript of a listed session answered "+m.line)
		default:
			t, err := rhunext.ParseTranscript(m.body)
			switch {
			case err != nil:
				h.fail("agents", "the transcript cannot be read: "+err.Error())
			case int64(len(t.Messages)) > t.Total:
				h.fail("agents", "the transcript has more messages than its TOTAL")
			case len(t.Messages) > 2000:
				h.fail("agents", "more than 2000 messages in one answer")
			default:
				h.ok(fmt.Sprintf("answers agent-transcript (%d of %d messages)", len(t.Messages), t.Total))
			}
		}
	}
	if h.features["tools"] {
		for _, t := range h.m.tools {
			input := "test"
			m, ok := h.askBody(fmt.Sprintf("tool %s rhun-tests/checker %d", t, len(input)), []byte(input))
			switch {
			case !ok:
				h.fail("tools", "no answer to a call of tool "+t+" within 3 s")
			case strings.HasPrefix(m.line, "error "):
				if f := strings.Fields(m.line); len(f) < 3 {
					h.fail("tools", "tool "+t+": "+m.line+" is not error ID CODE TEXT")
				} else {
					h.ok("tool " + t + " answers an error rhun passes on: " + m.line)
				}
			default:
				h.ok(fmt.Sprintf("tool %s answers (%d bytes)", t, len(m.body)))
			}
		}
	} else if len(h.m.tools) > 0 {
		h.fail("tools", "[tools] without the tools feature in hello: rhun never calls them")
	}
	if h.features["agent-prompts"] {
		text := "hello"
		m, ok := h.askBody(fmt.Sprintf("agent-prompt - %d", len(text)), []byte(text))
		switch {
		case !ok:
			h.fail("agent-prompts", "no answer to agent-prompt within 3 s")
		case strings.HasPrefix(m.line, "error "):
			h.warn("agent-prompts", "a prompt for a new session answered "+m.line)
		case !rhunext.SIDOK(string(m.body)):
			h.fail("agent-prompts", fmt.Sprintf("the answer %q is not a session id", m.body))
		default:
			h.ok("starts a session for a prompt: " + string(m.body))
			h.write("0 agent-stop " + string(m.body) + "\n")
		}
	}
	_ = strconv.Itoa
}

// askBody is ask for a request with a body: the line, then exactly the body's bytes
func (h *host) askBody(line string, body []byte) (message, bool) {
	h.hostSeq++
	id := h.hostSeq
	h.hostReqs[id] = line
	h.write(fmt.Sprintf("%d %s\n%s", id, line, body))
	deadline := time.Now().Add(3 * time.Second)
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
