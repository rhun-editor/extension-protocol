package rhunext

// Protocol 1.1's AI features (v1.1/v1.1.md, "Agents, proposals and tools"): agent sessions an
// extension lists in rhun's agents panel (FeatureAgents, TranscriptProvider), edit proposals the
// user reviews (FeatureEditReview, ParseProposal), tools called through rhun (FeatureTools,
// ToolProvider), and prompts for an extension's sessions (FeatureAgentPrompts, Prompter).

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The AI features.
const (
	FeatureAgents       = "agents"
	FeatureEditReview   = "edit-review"
	FeatureTools        = "tools"
	FeatureAgentPrompts = "agent-prompts"
)

// Limits of the AI features.
const (
	MaxSessions = 1000
	MaxSID      = 128
	MaxMsgText  = 71680 // rhun cuts a message's text at 70 KB
	MaxMessages = 2000  // messages in one transcript answer
	MaxAIBody   = 4 << 20
)

// SessionState is what a session does, as its row shows it.
type SessionState string

const (
	SessionIdle    SessionState = "idle"
	SessionWorking SessionState = "working"
	SessionWaiting SessionState = "waiting"
	SessionError   SessionState = "error"
)

// Session is a row of the agents panel.
type Session struct {
	ID, Title, Model string
	// Cwd is the session's folder: the row shows when it is the project, a folder in it or one of
	// its worktrees; "" for every project.
	Cwd     string
	State   SessionState
	Updated int64 // Unix seconds of its last change
}

// NewSession is a session updated now and idle.
func NewSession(id, title string) Session {
	return Session{ID: id, Title: title, State: SessionIdle, Updated: time.Now().Unix()}
}

// SIDOK is whether a session id can travel: 1 to 128 bytes of visible ASCII.
func SIDOK(sid string) bool {
	if sid == "" || len(sid) > MaxSID {
		return false
	}
	for i := 0; i < len(sid); i++ {
		if sid[i] < 0x21 || sid[i] > 0x7e {
			return false
		}
	}
	return true
}

func absolutePath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		(len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\'))
}

// EncodeSessions is the body of agent-sessions.
func EncodeSessions(ss []Session) ([]byte, error) {
	if len(ss) > MaxSessions {
		return nil, fmt.Errorf("%d sessions: at most %d", len(ss), MaxSessions)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", len(ss))
	seen := map[string]bool{}
	for _, s := range ss {
		if !SIDOK(s.ID) {
			return nil, fmt.Errorf("session id %q: 1 to 128 visible ASCII bytes", s.ID)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("session id %s appears twice", s.ID)
		}
		seen[s.ID] = true
		if s.Cwd != "" && (!absolutePath(s.Cwd) || strings.ContainsAny(s.Cwd, "\x00\n")) {
			return nil, fmt.Errorf("session %s: the folder must be absolute", s.ID)
		}
		state := s.State
		if state == "" {
			state = SessionIdle
		}
		title, model := cut(s.Title, 4096), cut(s.Model, 4096)
		fmt.Fprintf(&b, "%d %s %d %d %d %d\n%s%s%s%s", s.Updated, state, len(s.ID), len(title), len(model),
			len(s.Cwd), s.ID, title, model, s.Cwd)
	}
	if b.Len() > 1<<20 {
		return nil, errors.New("the sessions are over 1 MB")
	}
	return []byte(b.String()), nil
}

// Role is who wrote a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"   // Name is the tool, Text a summary (a command, a path)
	RoleResult    Role = "result" // a tool's output, shown as an excerpt
	RoleError     Role = "error"
	RoleInfo      Role = "info"
)

// Message is a message of a session.
type Message struct {
	Role              Role
	Name, Text, Model string
}

// Transcript answers agent-transcript: how many messages there are, and some from FROM.
type Transcript struct {
	Total    int64
	Messages []Message
}

// TranscriptPage is all's messages from from on, as many as fit one answer.
func TranscriptPage(all []Message, from int64) Transcript {
	t := Transcript{Total: int64(len(all))}
	size := 32
	for i := from; i < int64(len(all)); i++ {
		m := all[i]
		n := 40 + len(cut(m.Name, 4096)) + len(cut(m.Text, MaxMsgText)) + len(cut(m.Model, 4096))
		if len(t.Messages) >= MaxMessages || size+n > MaxAIBody {
			break
		}
		size += n
		t.Messages = append(t.Messages, m)
	}
	return t
}

// Encode is the answer's body.
func (t Transcript) Encode() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %d\n", t.Total, len(t.Messages))
	for _, m := range t.Messages {
		name, text, model := cut(m.Name, 4096), cut(m.Text, MaxMsgText), cut(m.Model, 4096)
		fmt.Fprintf(&b, "%s %d %d %d\n%s%s%s", m.Role, len(name), len(text), len(model), name, text, model)
	}
	return []byte(b.String())
}

// aiReader reads the AI bodies.
type aiReader struct {
	b []byte
	p int
}

func (r *aiReader) word() (string, error) {
	s := r.p
	for r.p < len(r.b) && r.b[r.p] != ' ' && r.b[r.p] != '\n' {
		r.p++
	}
	w := string(r.b[s:r.p])
	if r.p < len(r.b) && r.b[r.p] == ' ' {
		r.p++
	}
	if w == "" {
		return "", errors.New("a field is missing")
	}
	return w, nil
}

func (r *aiReader) num() (int64, error) {
	w, err := r.word()
	if err != nil {
		return 0, err
	}
	if len(w) > 18 {
		return 0, fmt.Errorf("not a number: %s", w)
	}
	for i := 0; i < len(w); i++ {
		if w[i] < '0' || w[i] > '9' {
			return 0, fmt.Errorf("not a number: %s", w)
		}
	}
	return strconv.ParseInt(w, 10, 64)
}

func (r *aiReader) eol() error {
	if r.p < len(r.b) && r.b[r.p] == '\n' {
		r.p++
		return nil
	}
	return errors.New("a line feed is missing")
}

func (r *aiReader) take(n int64) (string, error) {
	if n < 0 || int64(len(r.b)-r.p) < n {
		return "", errors.New("the body is short")
	}
	s := string(r.b[r.p : r.p+int(n)])
	r.p += int(n)
	return s, nil
}

// ParseTranscript reads an answer back (tests, the checker).
func ParseTranscript(body []byte) (Transcript, error) {
	r := &aiReader{b: body}
	var t Transcript
	var err error
	if t.Total, err = r.num(); err != nil {
		return t, err
	}
	n, err := r.num()
	if err != nil {
		return t, err
	}
	if err := r.eol(); err != nil {
		return t, err
	}
	for i := int64(0); i < n; i++ {
		role, err := r.word()
		if err != nil {
			return t, err
		}
		switch Role(role) {
		case RoleUser, RoleAssistant, RoleTool, RoleResult, RoleError, RoleInfo:
		default:
			return t, fmt.Errorf("unknown role %s", role)
		}
		var l [3]int64
		for j := range l {
			if l[j], err = r.num(); err != nil {
				return t, err
			}
		}
		if err := r.eol(); err != nil {
			return t, err
		}
		var s [3]string
		for j := range s {
			if s[j], err = r.take(l[j]); err != nil {
				return t, err
			}
		}
		t.Messages = append(t.Messages, Message{Role: Role(role), Name: s[0], Text: s[1], Model: s[2]})
	}
	if r.p != len(body) {
		return t, errors.New("bytes after the messages")
	}
	return t, nil
}

// TranscriptProvider answers rhun's agent-transcript requests (FeatureAgents).
type TranscriptProvider interface {
	AgentTranscript(ext *Ext, sid string, from int64) (Transcript, error)
}

// AgentSessions replaces this extension's rows in the agents panel.
func (e *Ext) AgentSessions(ss []Session) error {
	body, err := EncodeSessions(ss)
	if err != nil {
		return &Error{Code: "toobig", Text: err.Error()}
	}
	_, err = e.Request("agent-sessions", body, strconv.Itoa(len(body)))
	return err
}

// AgentChanged says the session has total messages, the first changed one at first.
func (e *Ext) AgentChanged(sid string, total, first int64) error {
	_, err := e.Request("agent-changed", nil, sid, itoa(total), itoa(first))
	return err
}

// ProposalEvent is what became of a proposal's file (Index from 1), or with Done of the proposal.
type ProposalEvent struct {
	Proposal int64
	Index    int64
	State    string // accepted, rejected, stale, partial, or done
	Accepted int64  // partial: hunks accepted; done: files accepted or partial
	Hunks    int64  // partial: the file's hunks
	Rejected int64  // done: files rejected or stale
}

// ParseProposal reads a proposal event.
func ParseProposal(ev Event) (ProposalEvent, bool) {
	var p ProposalEvent
	if ev.Name != "proposal" || len(ev.Args) < 3 {
		return p, false
	}
	n := func(i int) (int64, bool) {
		if i >= len(ev.Args) {
			return 0, false
		}
		v, err := strconv.ParseInt(ev.Args[i], 10, 64)
		return v, err == nil
	}
	var ok bool
	if p.Proposal, ok = n(0); !ok {
		return p, false
	}
	if ev.Args[1] == "done" {
		p.State = "done"
		a, ok1 := n(2)
		r, ok2 := n(3)
		p.Accepted, p.Rejected = a, r
		return p, ok1 && ok2
	}
	if p.Index, ok = n(1); !ok {
		return p, false
	}
	p.State = ev.Args[2]
	switch p.State {
	case "accepted", "rejected", "stale":
		return p, true
	case "partial":
		a, ok1 := n(3)
		h, ok2 := n(4)
		p.Accepted, p.Hunks = a, h
		return p, ok1 && ok2
	}
	return p, false
}

// Propose sends an edit for the user to review: the proposal's id, at once (FeatureEditReview).
// The edit goes in 12g's grammar whatever was agreed: a hash as "-", without its Label and
// Confirm; file operations are refused (an *Error with code unknown).
func (e *Ext) Propose(label, sid string, edit WorkspaceEdit) (int64, error) {
	body, bad := proposalBody(label, sid, edit)
	if bad != nil {
		return 0, bad
	}
	r, err := e.Request("propose", body, strconv.Itoa(len(body)))
	if err != nil {
		return 0, err
	}
	return argInt(r, 0), nil
}

// proposalBody is a proposal's head and its edit in 12g's grammar (the Review tab reads 12g): the
// hashes, label and confirm are left out (a hashed file goes as "-"), and what is left is encoded
// as 12g's, so file operations are refused (unknown), as is what does not fit (toobig).
func proposalBody(label, sid string, edit WorkspaceEdit) ([]byte, *Error) {
	plain := WorkspaceEdit{Files: make([]FileEdit, len(edit.Files)), Ops: edit.Ops}
	for i, f := range edit.Files {
		f.Hashed, f.Hash = false, 0
		plain.Files[i] = f
	}
	eb, bad := plain.encodeWith(editCaps{fixed: "propose"})
	if bad != nil {
		return nil, bad
	}
	return append([]byte(fmt.Sprintf("%d %d\n%s%s", len(label), len(sid), label, sid)), eb...), nil
}

// Unpropose withdraws a proposal.
func (e *Ext) Unpropose(proposal int64) error {
	_, err := e.Request("unpropose", nil, itoa(proposal))
	return err
}

// ToolInfo is a tool as tools lists it: OWNER/NAME.tool or rhun.tool.
type ToolInfo struct {
	Name                      string
	Readonly                  bool
	Title, Description, Input string
}

// ParseTools reads a tools answer.
func ParseTools(body []byte) ([]ToolInfo, error) {
	r := &aiReader{b: body}
	n, err := r.num()
	if err != nil {
		return nil, err
	}
	if err := r.eol(); err != nil {
		return nil, err
	}
	var v []ToolInfo
	for i := int64(0); i < n; i++ {
		var t ToolInfo
		if t.Name, err = r.word(); err != nil {
			return nil, err
		}
		flags, err := r.word()
		if err != nil {
			return nil, err
		}
		t.Readonly = flags == "ro"
		var l [3]int64
		for j := range l {
			if l[j], err = r.num(); err != nil {
				return nil, err
			}
		}
		if err := r.eol(); err != nil {
			return nil, err
		}
		if t.Title, err = r.take(l[0]); err != nil {
			return nil, err
		}
		if t.Description, err = r.take(l[1]); err != nil {
			return nil, err
		}
		if t.Input, err = r.take(l[2]); err != nil {
			return nil, err
		}
		v = append(v, t)
	}
	return v, nil
}

// Tools lists the tools rhun offers (FeatureTools).
func (e *Ext) Tools() ([]ToolInfo, error) {
	r, err := e.Request("tools", nil)
	if err != nil {
		return nil, err
	}
	return ParseTools(r.Body)
}

// CallTool calls a tool and waits for its output; a tool that changes anything asks the user
// first ("refused denied by the user").
func (e *Ext) CallTool(tool string, input []byte) ([]byte, error) {
	r, err := e.Request("tool-call", input, tool, strconv.Itoa(len(input)))
	if err != nil {
		return nil, err
	}
	return r.Body, nil
}

// ToolInput builds a built-in tool's input: KEY VALUE lines, and KEY LEN and the bytes for text and
// edit.
type ToolInput struct{ b []byte }

// Set adds "key value" (one line).
func (t *ToolInput) Set(key, value string) *ToolInput {
	t.b = append(t.b, key+" "+oneLine(value)+"\n"...)
	return t
}

// Bytes adds "key LEN" and the bytes.
func (t *ToolInput) Bytes(key string, value []byte) *ToolInput {
	t.b = append(t.b, fmt.Sprintf("%s %d\n", key, len(value))...)
	t.b = append(t.b, value...)
	return t
}

// Encode is the input.
func (t *ToolInput) Encode() []byte { return t.b }

// ToolCall is a call of one of the extension's [tools]: its own name, the caller (OWNER/NAME) and
// the input, opaque to rhun.
type ToolCall struct {
	Name, Caller string
	Input        []byte
}

// ToolProvider answers calls of the extension's tools (FeatureTools). An *Error keeps its code;
// any other error is refused.
type ToolProvider interface {
	Tool(ext *Ext, call ToolCall) ([]byte, error)
}

// Prompter takes prompts for the extension's sessions (FeatureAgentPrompts): sid "" starts a new
// session; the answer is the session's id. AgentStop is the user's Stop.
type Prompter interface {
	AgentPrompt(ext *Ext, sid, text string) (string, error)
	AgentStop(ext *Ext, sid string)
}

// AgentAsk shows a card in a session's conversation: the option the user picked, from 0.
func (e *Ext) AgentAsk(sid, prompt string, options []string) (int, error) {
	body := oneLine(prompt)
	for _, o := range options {
		body += "\n" + oneLine(o)
	}
	r, err := e.Request("agent-ask", []byte(body), sid, strconv.Itoa(len(body)))
	if err != nil {
		return 0, err
	}
	return int(argInt(r, 0)) - 1, nil
}

func aiError(err error) *Error {
	var re *Error
	if errors.As(err, &re) {
		return re
	}
	return &Error{Code: "refused", Text: err.Error()}
}

// answerAI answers rhun's AI requests; known is false for other verbs.
func (e *Ext) answerAI(h Handler, f []string, body []byte) ([]byte, *Error, bool) {
	switch f[1] {
	case "agent-transcript":
		p, is := h.(TranscriptProvider)
		if !is || !e.Has(FeatureAgents) || len(f) != 4 {
			return nil, nil, false
		}
		from, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return nil, nil, false
		}
		t, err := p.AgentTranscript(e, f[2], from)
		if err != nil {
			return nil, aiError(err), true
		}
		return t.Encode(), nil, true
	case "tool":
		p, is := h.(ToolProvider)
		if !is || !e.Has(FeatureTools) || len(f) != 5 {
			return nil, nil, false
		}
		out, err := p.Tool(e, ToolCall{Name: f[2], Caller: f[3], Input: body})
		if err != nil {
			return nil, aiError(err), true
		}
		return out, nil, true
	case "agent-prompt":
		p, is := h.(Prompter)
		if !is || !e.Has(FeatureAgentPrompts) || len(f) != 4 {
			return nil, nil, false
		}
		sid := f[2]
		if sid == "-" {
			sid = ""
		}
		id, err := p.AgentPrompt(e, sid, string(body))
		if err != nil {
			return nil, aiError(err), true
		}
		return []byte(id), nil, true
	}
	return nil, nil, false
}

// ParseAgentStop reads the agent-stop event: the session.
func ParseAgentStop(ev Event) (string, bool) {
	if ev.Name != "agent-stop" || len(ev.Args) != 1 {
		return "", false
	}
	return ev.Args[0], true
}
