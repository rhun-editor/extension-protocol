package rhunext

// Feature debug (protocol 1.1 "debug"): an extension that declares debuggers ([debugger.TYPE]
// sections, [needs] debug = yes) starts a session of one of its own debuggers with DebugStart,
// within 10 s of one of its commands, and hears debug-ended when it is over. Configurations travel
// as flat lists of typed leaves with dotted paths (DebugConfig): "args.0", "env.LOG_LEVEL".

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// FeatureDebug: debug-start, debug-stop, debug-ended (and debug-resolve).
const FeatureDebug = "debug"

// The kinds of a configuration leaf.
const (
	DebugString = 's'
	DebugNumber = 'n' // an integer, |v| < 2^53
	DebugBool   = 'b'
	DebugNull   = 'z'
)

// Limits of a configuration body.
const (
	MaxDebugLeaves    = 4096
	MaxDebugBody      = 1 << 20
	MaxDebugPath      = 256
	MaxDebugSegments  = 16
	MaxDebugValue     = 64 << 10
	MaxDebugStartName = 256
)

// DebugValue is one leaf's value: Kind picks S, N or B.
type DebugValue struct {
	Kind byte
	S    string
	N    int64
	B    bool
}

// DebugStr, DebugInt, DebugBoolean and DebugNil make values.
func DebugStr(s string) DebugValue   { return DebugValue{Kind: DebugString, S: s} }
func DebugInt(n int64) DebugValue    { return DebugValue{Kind: DebugNumber, N: n} }
func DebugBoolean(b bool) DebugValue { return DebugValue{Kind: DebugBool, B: b} }
func DebugNil() DebugValue           { return DebugValue{Kind: DebugNull} }

func (v DebugValue) text() string {
	switch v.Kind {
	case DebugNumber:
		return strconv.FormatInt(v.N, 10)
	case DebugBool:
		return strconv.FormatBool(v.B)
	case DebugNull:
		return ""
	}
	return v.S
}

// DebugEntry is one leaf: a dotted path and its value.
type DebugEntry struct {
	Path  string
	Value DebugValue
}

// DebugConfig is a configuration: the launch or attach arguments, with `type` (one of the
// extension's debugger types) and optionally `request` (launch or attach).
type DebugConfig []DebugEntry

func debugUnder(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+".")
}

// Get returns a leaf's value.
func (c DebugConfig) Get(path string) (DebugValue, bool) {
	for _, e := range c {
		if e.Path == path {
			return e.Value, true
		}
	}
	return DebugValue{}, false
}

// String returns a string leaf's text, or "".
func (c DebugConfig) String(path string) string {
	v, ok := c.Get(path)
	if !ok || v.Kind != DebugString {
		return ""
	}
	return v.S
}

// Set replaces a leaf (and anything under that path) or adds it.
func (c *DebugConfig) Set(path string, v DebugValue) {
	c.Remove(path)
	*c = append(*c, DebugEntry{Path: path, Value: v})
}

// Remove drops a leaf and everything under its path.
func (c *DebugConfig) Remove(path string) {
	out := (*c)[:0]
	for _, e := range *c {
		if !debugUnder(e.Path, path) {
			out = append(out, e)
		}
	}
	*c = out
}

// List returns the strings of an array (PATH.0, PATH.1, ...) in index order.
func (c DebugConfig) List(path string) []string {
	type item struct {
		i int
		s string
	}
	var items []item
	for _, e := range c {
		if !strings.HasPrefix(e.Path, path+".") {
			continue
		}
		rest := e.Path[len(path)+1:]
		if i, err := strconv.Atoi(rest); err == nil && i >= 0 {
			items = append(items, item{i, e.Value.text()})
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].i < items[b].i })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.s
	}
	return out
}

// SetList replaces an array with strings.
func (c *DebugConfig) SetList(path string, items []string) {
	c.Remove(path)
	for i, s := range items {
		*c = append(*c, DebugEntry{Path: path + "." + strconv.Itoa(i), Value: DebugStr(s)})
	}
}

func checkDebugLeaf(e DebugEntry) error {
	if e.Path == "" || len(e.Path) > MaxDebugPath || strings.Count(e.Path, ".") >= MaxDebugSegments {
		return fmt.Errorf("debug: bad path %q", e.Path)
	}
	switch e.Value.Kind {
	case DebugString, DebugBool, DebugNull:
	case DebugNumber:
		if e.Value.N >= 1<<53 || e.Value.N <= -(1<<53) {
			return fmt.Errorf("debug: %s is too large", e.Path)
		}
	default:
		return fmt.Errorf("debug: %s has no kind", e.Path)
	}
	if len(e.Value.text()) > MaxDebugValue {
		return fmt.Errorf("debug: %s is over 64 KB", e.Path)
	}
	return nil
}

// Encode writes a configuration body: "N" LF, then each leaf "KIND PATH_LEN VALUE_LEN" LF + PATH
// + VALUE.
func (c DebugConfig) Encode() ([]byte, error) {
	if len(c) > MaxDebugLeaves {
		return nil, errors.New("debug: over 4096 leaves")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%d\n", len(c))
	for _, e := range c {
		if err := checkDebugLeaf(e); err != nil {
			return nil, err
		}
		v := e.Value.text()
		fmt.Fprintf(&b, "%c %d %d\n%s%s", e.Value.Kind, len(e.Path), len(v), e.Path, v)
	}
	if b.Len() > MaxDebugBody {
		return nil, errors.New("debug: the configuration is over 1 MB")
	}
	return b.Bytes(), nil
}

// DebugContext is a configuration body's context record: the active document, the cursor and the
// document's path, or Doc 0.
type DebugContext struct {
	Doc  Doc
	At   Pos
	Path string
}

// ParseDebugConfig reads a configuration body and its extra records (unknown ones are skipped).
func ParseDebugConfig(body []byte) (DebugConfig, DebugContext, error) {
	var ctx DebugContext
	bad := errors.New("debug: the configuration body does not parse")
	line := func() (string, bool) {
		nl := bytes.IndexByte(body, '\n')
		if nl < 0 {
			return "", false
		}
		s := string(body[:nl])
		body = body[nl+1:]
		return s, true
	}
	l, ok := line()
	if !ok {
		return nil, ctx, bad
	}
	n, err := strconv.Atoi(l)
	if err != nil || n < 0 || n > MaxDebugLeaves {
		return nil, ctx, bad
	}
	cfg := make(DebugConfig, 0, n)
	for i := 0; i < n; i++ {
		l, ok := line()
		if !ok {
			return nil, ctx, bad
		}
		f := strings.Split(l, " ")
		if len(f) != 3 || len(f[0]) != 1 {
			return nil, ctx, bad
		}
		pl, e1 := strconv.Atoi(f[1])
		vl, e2 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || pl < 1 || pl > MaxDebugPath || vl < 0 || vl > MaxDebugValue || pl+vl > len(body) {
			return nil, ctx, bad
		}
		path, val := string(body[:pl]), string(body[pl:pl+vl])
		body = body[pl+vl:]
		v := DebugValue{Kind: f[0][0]}
		switch v.Kind {
		case DebugString:
			v.S = val
		case DebugNumber:
			if v.N, err = strconv.ParseInt(val, 10, 64); err != nil || v.N >= 1<<53 || v.N <= -(1<<53) {
				return nil, ctx, bad
			}
		case DebugBool:
			if val != "true" && val != "false" {
				return nil, ctx, bad
			}
			v.B = val == "true"
		case DebugNull:
			if vl != 0 {
				return nil, ctx, bad
			}
		default:
			return nil, ctx, bad
		}
		cfg = append(cfg, DebugEntry{Path: path, Value: v})
	}
	for len(body) > 0 {
		l, ok := line()
		if !ok {
			break
		}
		f := strings.Split(l, " ")
		size, err := strconv.Atoi(f[len(f)-1])
		if err != nil || size < 0 || size > len(body) {
			return nil, ctx, bad
		}
		if f[0] == "context" && len(f) == 5 {
			doc, _ := strconv.ParseInt(f[1], 10, 64)
			ln, _ := strconv.Atoi(f[2])
			col, _ := strconv.Atoi(f[3])
			ctx = DebugContext{Doc: Doc(doc), At: Pos{ln, col}, Path: string(body[:size])}
		}
		body = body[size:]
	}
	return cfg, ctx, nil
}

// DebugStart starts a session of one of the extension's own debuggers (cfg's `type`) named name.
// It must come within 10 s of one of the extension's commands (one session per command); the
// user approves the extension's debugging once per project. It returns the session's id once the
// adapter started; errors: refused (no recent command, or a type not the extension's), cancelled
// (the user declined), busy (a dialog is open), toobig (8 sessions).
func (e *Ext) DebugStart(ctx context.Context, name string, cfg DebugConfig) (int64, error) {
	if !e.Has(FeatureDebug) {
		return 0, &Error{Code: "unknown", Text: "debug was not negotiated"}
	}
	if name == "" || len(name) > MaxDebugStartName || strings.ContainsAny(name, "\n") {
		return 0, &Error{Code: "syntax", Text: "a session name is 1 to 256 bytes on one line"}
	}
	b, err := cfg.Encode()
	if err != nil {
		return 0, &Error{Code: "toobig", Text: err.Error()}
	}
	body := append([]byte(name), b...)
	r, err := e.RequestContext(ctx, "debug-start", body, strconv.Itoa(len(name)), strconv.Itoa(len(body)))
	if err != nil {
		return 0, err
	}
	if len(r.Args) < 1 {
		return 0, &Error{Code: "syntax", Text: "debug-start answered without a session"}
	}
	return strconv.ParseInt(r.Args[0], 10, 64)
}

// DebugStop stops a session this extension started (as the toolbar's Stop: terminate, then
// disconnect, then the adapter is killed). It returns once the stop was asked; the session's
// debug-ended follows when it is over. One already over is fine; errors: refused (not a session
// this extension started).
func (e *Ext) DebugStop(ctx context.Context, session int64) error {
	if !e.Has(FeatureDebug) {
		return &Error{Code: "unknown", Text: "debug was not negotiated"}
	}
	if session <= 0 {
		return &Error{Code: "syntax", Text: "a session id is a positive number"}
	}
	_, err := e.RequestContext(ctx, "debug-stop", nil, strconv.FormatInt(session, 10))
	return err
}

// DebugEnded is the debug-ended event: a session this extension started is over. HasCode is
// false when the program's exit code is unknown.
type DebugEnded struct {
	Session int64
	Code    int
	HasCode bool
}

// ParseDebugEnded reads a debug-ended event (ok false for any other event).
func ParseDebugEnded(ev Event) (DebugEnded, bool) {
	if ev.Name != "debug-ended" || len(ev.Args) != 2 {
		return DebugEnded{}, false
	}
	s, err := strconv.ParseInt(ev.Args[0], 10, 64)
	if err != nil || s <= 0 {
		return DebugEnded{}, false
	}
	d := DebugEnded{Session: s}
	if ev.Args[1] != "-" {
		c, err := strconv.Atoi(ev.Args[1])
		if err != nil {
			return DebugEnded{}, false
		}
		d.Code, d.HasCode = c, true
	}
	return d, true
}

// DebugResolveRequest is debug-resolve: rhun shows a debugger's extension the configuration
// (variables replaced, types applied) before every launch of a debugger with resolve = yes.
// Context is cancelled when rhun withdraws the request (the user pressed Stop).
type DebugResolveRequest struct {
	ID      int64
	Type    string
	Request string // launch or attach
	NoDebug bool
	Config  DebugConfig
	Doc     Doc // the active document, 0 for none
	At      Pos
	Path    string
	Context context.Context
}

// DebugResolver answers debug-resolve: the complete resolved configuration, nil to launch it
// unchanged, ErrDebugCancelled to abandon the launch quietly (the user dismissed a question), or
// any other error to abandon it with the error's text. It may ask the user (Choose, Ask).
type DebugResolver interface {
	ResolveDebug(ext *Ext, req DebugResolveRequest) (DebugConfig, error)
}

// ErrDebugCancelled abandons a launch quietly.
var ErrDebugCancelled = errors.New("cancelled")

func parseDebugResolve(id int64, f []string, body []byte, ctx context.Context) (DebugResolveRequest, bool) {
	if len(f) != 6 || (f[3] != "launch" && f[3] != "attach") || (f[4] != "0" && f[4] != "1") {
		return DebugResolveRequest{}, false
	}
	cfg, dctx, err := ParseDebugConfig(body)
	if err != nil {
		return DebugResolveRequest{}, false
	}
	return DebugResolveRequest{ID: id, Type: f[2], Request: f[3], NoDebug: f[4] == "1", Config: cfg,
		Doc: dctx.Doc, At: dctx.At, Path: dctx.Path, Context: ctx}, true
}

func answerDebugResolve(e *Ext, r DebugResolver, req DebugResolveRequest) ([]byte, *Error) {
	cfg, err := r.ResolveDebug(e, req)
	switch {
	case errors.Is(err, ErrDebugCancelled) || req.Context.Err() != nil:
		return nil, &Error{Code: "cancelled", Text: "the launch was cancelled"}
	case err != nil:
		return nil, &Error{Code: "refused", Text: err.Error()}
	case cfg == nil:
		return nil, nil
	}
	b, err := cfg.Encode()
	if err != nil {
		return nil, &Error{Code: "toobig", Text: err.Error()}
	}
	return b, nil
}
