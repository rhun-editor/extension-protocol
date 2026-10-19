package rhunext

// Protocol 1.1: how commands are invoked and what they receive (feature command-args), command
// references inside bodies, running other commands (run-command) and being run by other
// extensions (call), context keys for when clauses (context-keys) and status bar items
// (status-items). See v1.1/v1.1.md.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The features of this file.
const (
	FeatureCommandArgs = "command-args"
	FeatureContextKeys = "context-keys"
	FeatureStatusItems = "status-items"
)

// Where an invocation came from (Invocation.From); "" when command-args was not agreed.
const (
	FromPalette = "palette" // the palette, a headless cmd, a menu listing all commands: no body
	FromKey     = "key"     // a key binding: the binding's ARGS
	FromMenu    = "menu"    // a menu item, a tab-strip or view button: TARGET LF the item's args
	FromRef     = "ref"     // a command reference (code lens, tree item, status item…): its ARGS
)

// Limits of command references and run-command.
const (
	MaxRefArgs       = 8192    // a command reference's ARGS
	MaxRunArgs       = 65536   // run-command's and call's ARGS
	MaxCallResult    = 1 << 20 // a call's result
	MaxContextValue  = 256
	MaxStatusText    = 120
	MaxStatusTooltip = 500
)

// Invocation is one invocation of one of the extension's commands: "0 run EXT.NAME DOC FROM LEN"
// and its body. Without command-args From is "" and Body nil.
type Invocation struct {
	Command string // the full name, EXT.NAME
	Doc     Doc    // the document it is about, or 0
	From    string // FromPalette, FromKey, FromMenu, FromRef, or ""
	Body    []byte
}

// Name is the command's local name, without "EXT.".
func (inv Invocation) Name() string {
	if i := strings.IndexByte(inv.Command, '.'); i >= 0 {
		return inv.Command[i+1:]
	}
	return inv.Command
}

// Target splits a menu invocation's body into its TARGET line ("file PATH", "tab doc DOC PATH",
// "item VIEW ID" …) and the item's args; ok is false for other invocations.
func (inv Invocation) Target() (target MenuTarget, args []byte, ok bool) {
	if inv.From != FromMenu {
		return MenuTarget{}, nil, false
	}
	line, rest := inv.Body, []byte(nil)
	if i := strings.IndexByte(string(inv.Body), '\n'); i >= 0 {
		line, rest = inv.Body[:i], inv.Body[i+1:]
	}
	return ParseMenuTarget(string(line)), rest, true
}

// MenuTarget is a menu's target line: Kind is its first word (editor, file, folder, root, tab,
// terminal, extension, view, item, commit, resource, group …) and Fields the words after it, the
// last one holding the rest of the line (a path may have spaces).
type MenuTarget struct {
	Kind   string
	Fields []string
	Line   string
}

// ParseMenuTarget reads a TARGET line. The number of fields depends on the kind: "tab doc DOC PATH"
// has three, "file PATH" one, "item VIEW ID" two.
func ParseMenuTarget(line string) MenuTarget {
	t := MenuTarget{Line: line}
	kind, rest, _ := strings.Cut(line, " ")
	t.Kind = kind
	n := 1
	switch kind {
	case "editor":
		n = 5
	case "tab":
		switch first, _, _ := strings.Cut(rest, " "); first {
		case "doc":
			n = 3
		case "image", "view":
			n = 2
		}
	case "item", "commit":
		n = 2
	case "resource":
		n = 2
	case "extension":
		n = 0
	}
	if rest != "" && n > 0 {
		t.Fields = strings.SplitN(rest, " ", n)
	}
	return t
}

// Path is the target's path: file, folder and root targets, "tab doc DOC PATH" (not "-"),
// "tab image PATH", "commit SHA PATH", "resource GROUP PATH"; "" otherwise.
func (t MenuTarget) Path() string {
	switch {
	case (t.Kind == "file" || t.Kind == "folder" || t.Kind == "root") && len(t.Fields) == 1:
		return t.Fields[0]
	case t.Kind == "tab" && len(t.Fields) == 3 && t.Fields[0] == "doc" && t.Fields[2] != "-":
		return t.Fields[2]
	case t.Kind == "tab" && len(t.Fields) == 2 && t.Fields[0] == "image":
		return t.Fields[1]
	case (t.Kind == "commit" || t.Kind == "resource") && len(t.Fields) == 2:
		return t.Fields[1]
	}
	return ""
}

// Invoker receives every invocation of the extension's commands with where it came from and its
// body. A Handler that implements it gets Invoke instead of Run.
type Invoker interface {
	Invoke(ext *Ext, inv Invocation)
}

// EncodeCommandRef is the record "command NAME LEN" LF ARGS: a reference to one of the extension's
// own commands (local NAME) inside a body (code lens, tree items, status items …). When the user
// activates it, rhun runs the command with FromRef and the ARGS as the body. (A code action's
// command is a *CommandRef, which encodes the same record.)
func EncodeCommandRef(name string, args []byte) ([]byte, error) {
	return AppendCommandRef(nil, name, args)
}

// AppendCommandRef appends EncodeCommandRef's record to dst.
func AppendCommandRef(dst []byte, name string, args []byte) ([]byte, error) {
	if !validCommandName(name) {
		return dst, &Error{Code: "syntax", Text: "a command name is 1 to 40 of a-z 0-9 _"}
	}
	if len(args) > MaxRefArgs {
		return dst, &Error{Code: "toobig", Text: "a command reference's args are at most 8192 bytes"}
	}
	dst = append(dst, "command "+name+" "+itoa(int64(len(args)))+"\n"...)
	return append(dst, args...), nil
}

// RunCommand runs a rhun built-in ("goto_line", "rhun.find") or another extension's callable
// command ("OWNER/EXT.NAME", "EXT.NAME") with ARGS, and returns its result (always empty for a
// built-in). Feature command-args.
func (e *Ext) RunCommand(name string, args []byte) ([]byte, error) {
	return e.RunCommandContext(context.Background(), name, args)
}

// RunCommandContext is RunCommand with cancellation.
func (e *Ext) RunCommandContext(ctx context.Context, name string, args []byte) ([]byte, error) {
	if !e.Has(FeatureCommandArgs) {
		return nil, &Error{Code: "unknown", Text: "command-args was not agreed"}
	}
	if name == "" || len(name) > 200 || strings.ContainsAny(name, " \r\n") {
		return nil, &Error{Code: "syntax", Text: "not a command name"}
	}
	if len(args) > MaxRunArgs {
		return nil, &Error{Code: "toobig", Text: "run-command args are at most 65536 bytes"}
	}
	r, err := e.RequestContext(ctx, "run-command", args, name, itoa(int64(len(args))))
	if err != nil {
		return nil, err
	}
	return r.Body, nil
}

// CallRequest is another extension running one of this extension's commands: "ID call EXT.NAME DOC
// CALLER LEN" and ARGS. Context ends when rhun cancels it (the caller's 10 seconds are up, the
// caller stopped) or the connection ends.
type CallRequest struct {
	ID      int64
	Command string // the full name, EXT.NAME
	Doc     Doc    // the active document, or 0
	Caller  string // OWNER/NAME of the extension that called
	Args    []byte
	Context context.Context
}

// Name is the command's local name.
func (c CallRequest) Name() string { return Invocation{Command: c.Command}.Name() }

// Callee answers calls of its commands declared callable ([command-options] NAME.callable = yes).
// The result goes back to the caller (at most 1 MiB); an *Error goes back with its code and text,
// any other error as refused.
type Callee interface {
	Call(ext *Ext, c CallRequest) ([]byte, error)
}

func parseCall(id int64, f []string, args []byte, ctx context.Context) (CallRequest, bool) {
	// f: EXT.NAME DOC CALLER LEN
	if len(f) != 4 || !strings.Contains(f[0], ".") || f[2] == "" {
		return CallRequest{}, false
	}
	doc, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || doc < 0 {
		return CallRequest{}, false
	}
	return CallRequest{ID: id, Command: f[0], Doc: Doc(doc), Caller: f[2], Args: args, Context: ctx}, true
}

// answerCall runs the Callee for a call request: the reply's body, or the error to send.
func (e *Ext) answerCall(h Handler, id int64, f []string, args []byte, ctx context.Context) ([]byte, *Error, bool) {
	c, is := h.(Callee)
	if !is || !e.Has(FeatureCommandArgs) {
		return nil, nil, false
	}
	call, ok := parseCall(id, f, args, ctx)
	if !ok {
		return nil, nil, false
	}
	if ctx.Err() != nil {
		return nil, &Error{Code: "cancelled", Text: "the call was cancelled"}, true
	}
	res, err := c.Call(e, call)
	switch {
	case ctx.Err() != nil:
		return nil, &Error{Code: "cancelled", Text: "the call was cancelled"}, true
	case err != nil:
		var re *Error
		if errors.As(err, &re) && validErrorCode(re.Code) {
			return nil, re, true
		}
		return nil, &Error{Code: "refused", Text: err.Error()}, true
	case len(res) > MaxCallResult:
		return nil, &Error{Code: "toobig", Text: "a call's result is at most 1048576 bytes"}, true
	}
	return res, nil, true
}

// validErrorCode is a code a reply may carry: a word of a-z.
func validErrorCode(code string) bool {
	return code != "" && len(code) <= 40 && strings.Trim(code, "abcdefghijklmnopqrstuvwxyz") == ""
}

// ---- context keys

// validContextKey is a key an extension sets: a lowercase letter, then ASCII letters, digits and
// underscores, at most 40 bytes.
func validContextKey(key string) bool {
	if key == "" || len(key) > 40 || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// PlainText reports whether s shows as it is on one line: valid UTF-8 without control characters
// (C0, DEL, C1), bidirectional controls or zero-width characters, as rhun checks context values
// and status item texts.
func PlainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			return false
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return false
		case r >= 0x200b && r <= 0x200f, r == 0x2060, r == 0xfeff:
			return false
		}
	}
	return true
}

// SetContext sets one of the extension's context keys (its when clauses see it as
// OWNER/NAME.KEY): VALUE one line of at most 256 bytes. Feature context-keys.
func (e *Ext) SetContext(key, value string) error {
	if value == "-" {
		return &Error{Code: "syntax", Text: `"-" removes a key: use RemoveContext`}
	}
	return e.setContext(key, value)
}

// RemoveContext removes the value set at run time: the manifest's [context] default applies again.
func (e *Ext) RemoveContext(key string) error { return e.setContext(key, "-") }

func (e *Ext) setContext(key, value string) error {
	if !e.Has(FeatureContextKeys) {
		return &Error{Code: "unknown", Text: "context-keys was not agreed"}
	}
	if !validContextKey(key) {
		return &Error{Code: "syntax", Text: "a context key is a lowercase letter, then letters, digits and _, at most 40 bytes"}
	}
	if len(value) > MaxContextValue {
		return &Error{Code: "toobig", Text: "a context value is at most 256 bytes"}
	}
	if value == "" || !PlainText(value) || value[0] == ' ' {
		return &Error{Code: "syntax", Text: "a context value is one line of text"}
	}
	_, err := e.Request("set-context", nil, key, value)
	return err
}

// ---- status items

// StatusItem is a status bar item (feature status-items).
type StatusItem struct {
	Name       string // 1 to 40 of a-z 0-9 _
	Align      string // "left" or "right" ("" is right)
	Priority   int    // -1000 to 1000, higher further left on both sides
	Text       string // one line, at most 120 bytes; "$(NAME)" is an icon (a built-in or ext:NAME)
	Tooltip    string // at most 500 bytes, may hold line breaks
	Command    string // one of the extension's own commands (local name), run with FromRef; "" none
	Args       []byte // the command's ARGS
	Color      string // fg, muted (""), accent, error, warning, success
	Background string // "", error or warning
	When       string // a when clause, "" always
}

var statusColors = map[string]bool{"fg": true, "muted": true, "accent": true, "error": true, "warning": true, "success": true}

// Encode is the status-item body: ALIGN PRIORITY TEXT_LEN TOOLTIP_LEN EXTRA_LEN LF and the bytes.
func (it StatusItem) Encode() ([]byte, error) {
	align := it.Align
	if align == "" {
		align = "right"
	}
	switch {
	case !validCommandName(it.Name):
		return nil, &Error{Code: "syntax", Text: "a status item's name is 1 to 40 of a-z 0-9 _"}
	case align != "left" && align != "right":
		return nil, &Error{Code: "syntax", Text: "align is left or right"}
	case it.Priority < -1000 || it.Priority > 1000:
		return nil, &Error{Code: "syntax", Text: "priority is -1000 to 1000"}
	case len(it.Text) > MaxStatusText || !PlainText(it.Text):
		return nil, &Error{Code: "syntax", Text: "text is one line of at most 120 bytes"}
	case len(it.Tooltip) > MaxStatusTooltip || !utf8.ValidString(it.Tooltip):
		return nil, &Error{Code: "syntax", Text: "a tooltip is at most 500 bytes of UTF-8"}
	case it.Color != "" && !statusColors[it.Color]:
		return nil, &Error{Code: "syntax", Text: "color is fg, muted, accent, error, warning or success"}
	case it.Background != "" && it.Background != "error" && it.Background != "warning":
		return nil, &Error{Code: "syntax", Text: "background is error or warning"}
	case len(it.When) > 512 || strings.ContainsAny(it.When, "\r\n"):
		return nil, &Error{Code: "syntax", Text: "a when clause is one line of at most 512 bytes"}
	}
	var extra []byte
	if it.Command != "" {
		var err error
		if extra, err = AppendCommandRef(extra, it.Command, it.Args); err != nil {
			return nil, err
		}
	}
	if it.Color != "" {
		extra = append(extra, "color "+it.Color+" 0\n"...)
	}
	if it.Background != "" {
		extra = append(extra, "background "+it.Background+" 0\n"...)
	}
	if it.When != "" {
		extra = append(extra, "when "+itoa(int64(len(it.When)))+"\n"+it.When...)
	}
	body := fmt.Appendf(nil, "%s %d %d %d %d\n", align, it.Priority, len(it.Text), len(it.Tooltip), len(extra))
	body = append(body, it.Text...)
	body = append(body, it.Tooltip...)
	return append(body, extra...), nil
}

// SetStatusItem adds or replaces a status bar item; an extension has at most 8 (manifest items
// included).
func (e *Ext) SetStatusItem(it StatusItem) error {
	if !e.Has(FeatureStatusItems) {
		return &Error{Code: "unknown", Text: "status-items was not agreed"}
	}
	body, err := it.Encode()
	if err != nil {
		return err
	}
	_, err = e.Request("status-item", body, it.Name, itoa(int64(len(body))))
	return err
}

// RemoveStatusItem removes a status item (a manifest item is hidden until the program stops).
func (e *Ext) RemoveStatusItem(name string) error {
	if !e.Has(FeatureStatusItems) {
		return &Error{Code: "unknown", Text: "status-items was not agreed"}
	}
	if !validCommandName(name) {
		return &Error{Code: "syntax", Text: "a status item's name is 1 to 40 of a-z 0-9 _"}
	}
	_, err := e.Request("status-remove", nil, name)
	return err
}
