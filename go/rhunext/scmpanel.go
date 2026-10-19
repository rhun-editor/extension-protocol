package rhunext

// Version control panels (protocol 1.1 feature scm): an extension registers a repository root
// and fills a sidebar section for it with a status line, an optional message box, buttons and
// groups of changed files. rhun colors those files in the explorer and the tab titles, and the
// extension's quick-diff bases (feature quickdiff) apply under its roots.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// FeatureScm: version control panels (ScmRegister, ScmUpdate, ScmInput, ScmInputSet).
const FeatureScm = "scm"

// Limits of an scm-update.
const (
	ScmTextMax       = 200     // the status line, the placeholder, labels
	ScmActionsMax    = 4       // buttons
	ScmItemActsMax   = 8       // actions of one group or resource
	ScmGroupsMax     = 64      // groups
	ScmResourcesMax  = 10000   // resources in all
	ScmInputMax      = 65536   // the message box's text
	ScmBodyMax       = 4 << 20 // an update's body
	ScmCommandArgMax = 8192    // a command reference's ARGS
	ScmTipMax        = 1000    // a resource's tooltip
)

// ScmAction is a command of the extension with an icon (a tree icon name such as "check" or
// "refresh", or "" for none), its ARGS (the body of "0 run EXT.NAME DOC ref LEN", with
// command-args agreed) and a label ("" shows the command's title).
type ScmAction struct {
	Icon    string
	Command string
	Args    []byte
	Label   string
}

// ScmResource is a changed file: its path relative to the root, a status letter (M, A, D, ?, …),
// a color (added, modified, deleted, conflict, untracked, ignored, or "" for none), the command
// its click runs (Click.Command "" opens the file; Click.Icon and Click.Label are not used), its
// menu's actions, and a tooltip (at most 1000 bytes).
type ScmResource struct {
	Path    string
	Code    byte
	Color   string
	Click   ScmAction
	Actions []ScmAction
	Tip     string
}

// ScmGroup is a titled group of resources (like Staged Changes) with actions in its menu.
type ScmGroup struct {
	ID        string
	Label     string
	Actions   []ScmAction
	Resources []ScmResource
}

// ScmPanel is what an scm-update shows: a status line, a message box when Placeholder is not
// empty, up to four buttons (the first also runs on Ctrl+Enter in the message box), and groups.
type ScmPanel struct {
	Status      string
	Placeholder string
	Actions     []ScmAction
	Groups      []ScmGroup
}

func scmText(what, s string, max int) error {
	if len(s) > max {
		return fmt.Errorf("%s: %d bytes (at most %d)", what, len(s), max)
	}
	if strings.ContainsAny(s, "\x00\r\n") {
		return fmt.Errorf("%s: NUL, CR or LF", what)
	}
	return nil
}

func scmName(name string) bool {
	if name == "" || len(name) > 40 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func scmWord(what, w string) error {
	if w == "" || len(w) > 64 || strings.ContainsAny(w, " \t\r\n\x00") {
		return fmt.Errorf("%s %q: 1 to 64 bytes without spaces", what, w)
	}
	return nil
}

func (a ScmAction) encode(b *strings.Builder, record string) error {
	if !scmName(a.Command) {
		return fmt.Errorf("command %q: 1 to 40 of a-z 0-9 _", a.Command)
	}
	if len(a.Args) > ScmCommandArgMax {
		return fmt.Errorf("command %s: ARGS over %d bytes", a.Command, ScmCommandArgMax)
	}
	if err := scmText("label", a.Label, ScmTextMax); err != nil {
		return err
	}
	icon := a.Icon
	if icon == "" {
		icon = "-"
	} else if err := scmWord("icon", icon); err != nil {
		return err
	}
	fmt.Fprintf(b, "%s %s %s %d %d\n", record, icon, a.Command, len(a.Args), len(a.Label))
	b.Write(a.Args)
	b.WriteString(a.Label)
	return nil
}

var scmColors = map[string]bool{"added": true, "modified": true, "deleted": true, "conflict": true, "untracked": true, "ignored": true}

// Encode is the scm-update body, checked against rhun's limits.
func (p ScmPanel) Encode() ([]byte, error) {
	if err := scmText("status", p.Status, ScmTextMax); err != nil {
		return nil, err
	}
	if err := scmText("placeholder", p.Placeholder, ScmTextMax); err != nil {
		return nil, err
	}
	if len(p.Actions) > ScmActionsMax {
		return nil, fmt.Errorf("%d buttons (at most %d)", len(p.Actions), ScmActionsMax)
	}
	if len(p.Groups) > ScmGroupsMax {
		return nil, fmt.Errorf("%d groups (at most %d)", len(p.Groups), ScmGroupsMax)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %d %d %d\n%s%s", len(p.Status), len(p.Placeholder), len(p.Actions), len(p.Groups), p.Status, p.Placeholder)
	for _, a := range p.Actions {
		if err := a.encode(&b, "action"); err != nil {
			return nil, err
		}
	}
	nres := 0
	for _, g := range p.Groups {
		if err := scmWord("group id", g.ID); err != nil {
			return nil, err
		}
		if err := scmText("group label", g.Label, ScmTextMax); err != nil {
			return nil, err
		}
		if len(g.Actions) > ScmItemActsMax {
			return nil, fmt.Errorf("group %s: %d actions (at most %d)", g.ID, len(g.Actions), ScmItemActsMax)
		}
		fmt.Fprintf(&b, "group %s %d %d %d\n%s", g.ID, len(g.Label), len(g.Actions), len(g.Resources), g.Label)
		for _, a := range g.Actions {
			if err := a.encode(&b, "action"); err != nil {
				return nil, err
			}
		}
		for _, r := range g.Resources {
			if nres++; nres > ScmResourcesMax {
				return nil, fmt.Errorf("more than %d resources", ScmResourcesMax)
			}
			if r.Path == "" || len(r.Path) > 4095 || strings.ContainsAny(r.Path, "\x00\r\n") {
				return nil, fmt.Errorf("resource path %q", r.Path)
			}
			if r.Code < 0x21 || r.Code > 0x7e {
				return nil, fmt.Errorf("resource %s: status letter %q", r.Path, r.Code)
			}
			color := r.Color
			if color == "" {
				color = "-"
			} else if !scmColors[color] {
				return nil, fmt.Errorf("resource %s: color %q", r.Path, color)
			}
			if len(r.Actions) > ScmItemActsMax {
				return nil, fmt.Errorf("resource %s: %d actions (at most %d)", r.Path, len(r.Actions), ScmItemActsMax)
			}
			nrec := len(r.Actions)
			if r.Click.Command != "" {
				nrec++
			}
			if len(r.Tip) > ScmTipMax {
				return nil, fmt.Errorf("resource %s: a tooltip of %d bytes (at most %d)", r.Path, len(r.Tip), ScmTipMax)
			}
			if r.Tip != "" {
				nrec++
			}
			fmt.Fprintf(&b, "res %c %s %d %d\n%s", r.Code, color, len(r.Path), nrec, r.Path)
			if r.Click.Command != "" {
				if !scmName(r.Click.Command) || len(r.Click.Args) > ScmCommandArgMax {
					return nil, fmt.Errorf("resource %s: click command %q", r.Path, r.Click.Command)
				}
				fmt.Fprintf(&b, "command %s %d\n", r.Click.Command, len(r.Click.Args))
				b.Write(r.Click.Args)
			}
			for _, a := range r.Actions {
				if err := a.encode(&b, "action"); err != nil {
					return nil, err
				}
			}
			if r.Tip != "" {
				fmt.Fprintf(&b, "tip %d\n%s", len(r.Tip), r.Tip)
			}
		}
	}
	if b.Len() > ScmBodyMax {
		return nil, fmt.Errorf("an update of %d bytes (at most %d)", b.Len(), ScmBodyMax)
	}
	return []byte(b.String()), nil
}

func (e *Ext) scmFeature() error {
	if !e.Has(FeatureScm) {
		return &Error{Code: "unknown", Text: "scm was not negotiated"}
	}
	return nil
}

// ScmRegister adds a sidebar section titled label for the repository at root (an absolute
// path) and returns its handle; the same root again returns the same handle. At most 4 per
// extension and 16 in all.
func (e *Ext) ScmRegister(ctx context.Context, root, label string) (int64, error) {
	if err := e.scmFeature(); err != nil {
		return 0, err
	}
	if root == "" || label == "" {
		return 0, &Error{Code: "syntax", Text: "a root and a label"}
	}
	if err := scmText("label", label, ScmTextMax); err != nil {
		return 0, &Error{Code: "syntax", Text: err.Error()}
	}
	body := strconv.Itoa(len(root)) + " " + strconv.Itoa(len(label)) + "\n" + root + label
	r, err := e.RequestContext(ctx, "scm-register", []byte(body), strconv.Itoa(len(body)))
	if err != nil {
		return 0, err
	}
	if len(r.Args) < 1 {
		return 0, invalidReply("scm-register")
	}
	h, perr := strconv.ParseInt(r.Args[0], 10, 64)
	if perr != nil || h <= 0 {
		return 0, invalidReply("scm-register")
	}
	return h, nil
}

// ScmUpdate replaces what the provider's section shows.
func (e *Ext) ScmUpdate(ctx context.Context, scm int64, p ScmPanel) error {
	if err := e.scmFeature(); err != nil {
		return err
	}
	body, err := p.Encode()
	if err != nil {
		return &Error{Code: "syntax", Text: err.Error()}
	}
	_, err = e.RequestContext(ctx, "scm-update", body, strconv.FormatInt(scm, 10), strconv.Itoa(len(body)))
	return err
}

// ScmInput is the text of the provider's message box.
func (e *Ext) ScmInput(ctx context.Context, scm int64) (string, error) {
	if err := e.scmFeature(); err != nil {
		return "", err
	}
	r, err := e.RequestContext(ctx, "scm-input", nil, strconv.FormatInt(scm, 10))
	if err != nil {
		return "", err
	}
	return string(r.Body), nil
}

// ScmInputSet puts text in the provider's message box ("" clears it).
func (e *Ext) ScmInputSet(ctx context.Context, scm int64, text string) error {
	if err := e.scmFeature(); err != nil {
		return err
	}
	if len(text) > ScmInputMax {
		return &Error{Code: "toobig", Text: "at most 65536 bytes"}
	}
	_, err := e.RequestContext(ctx, "scm-input-set", []byte(text), strconv.FormatInt(scm, 10), strconv.Itoa(len(text)))
	return err
}

// Panel buttons and clicks are command references: with FeatureCommandArgs their ARGS arrive as
// an Invocation's Body (invoke.go).
