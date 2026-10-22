package rhunext

import (
	"fmt"
	"strconv"
	"strings"
)

// QuickItem is a quick input's item or separator (Step.Items).
type QuickItem struct {
	ID, Label string
	Sep       bool
	extra     []byte
}

// NewQuickItem is an item; QuickSeparator a separator labelled so.
func NewQuickItem(id, label string) *QuickItem {
	return &QuickItem{ID: id, Label: viewLine(label, 1000)}
}
func QuickSeparator(label string) *QuickItem {
	return &QuickItem{Label: viewLine(label, 1000), Sep: true}
}

func (it *QuickItem) rec(head string, data []byte) *QuickItem {
	it.extra = append(it.extra, fmt.Sprintf("%s %d\n", head, len(data))...)
	it.extra = append(it.extra, data...)
	return it
}

// Desc, Detail, Icon, Picked, Always, Highlight and Button add the item's records.
func (it *QuickItem) Desc(s string) *QuickItem   { return it.rec("desc", []byte(viewLine(s, 1000))) }
func (it *QuickItem) Detail(s string) *QuickItem { return it.rec("detail", []byte(viewLine(s, 1000))) }
func (it *QuickItem) Icon(spec, color string) *QuickItem {
	return it.rec("icon "+spec+" "+color, nil)
}
func (it *QuickItem) Picked() *QuickItem { return it.rec("picked", nil) }
func (it *QuickItem) Always() *QuickItem { return it.rec("always", nil) }
func (it *QuickItem) Highlight(from, to int) *QuickItem {
	return it.rec(fmt.Sprintf("hl %d %d", from, to), nil)
}
func (it *QuickItem) Button(key, icon, tip string) *QuickItem {
	return it.rec("button "+key+" "+icon, []byte(viewLine(tip, 200)))
}

func (it *QuickItem) bytes() []byte {
	kind := "item"
	if it.Sep {
		kind = "sep"
	}
	out := []byte(fmt.Sprintf("%d %d %d %s\n", len(it.ID), len(it.Label), len(it.extra), kind))
	out = append(out, it.ID...)
	out = append(out, it.Label...)
	return append(out, it.extra...)
}

// QuickStep is a step body: only the records set are sent, so QuickSet changes only them.
type QuickStep struct{ out []byte }

func (s *QuickStep) rec(head string, data []byte) *QuickStep {
	s.out = append(s.out, fmt.Sprintf("%s %d\n", head, len(data))...)
	s.out = append(s.out, data...)
	return s
}

// NewQuickStep is an empty body; QuickPick and QuickInput start a pick list or an input.
func NewQuickStep() *QuickStep           { return &QuickStep{} }
func QuickPick(title string) *QuickStep  { return NewQuickStep().Title(title).rec("mode pick", nil) }
func QuickInput(title string) *QuickStep { return NewQuickStep().Title(title).rec("mode input", nil) }

func (s *QuickStep) Title(t string) *QuickStep { return s.rec("title", []byte(viewLine(t, 200))) }

// Step shows "N/M" (0, 0 hides it).
func (s *QuickStep) Step(n, of int) *QuickStep { return s.rec(fmt.Sprintf("step %d %d", n, of), nil) }
func (s *QuickStep) Placeholder(t string) *QuickStep {
	return s.rec("placeholder", []byte(viewLine(t, 200)))
}
func (s *QuickStep) Prompt(t string) *QuickStep { return s.rec("prompt", []byte(viewLine(t, 1000))) }

// Value sets the field's text at quick, after an accept, or when seq is its sequence (-1: "-").
func (s *QuickStep) Value(seq int64, text string) *QuickStep {
	q := "-"
	if seq >= 0 {
		q = strconv.FormatInt(seq, 10)
	}
	return s.rec("value "+q, []byte(viewLine(text, 65536)))
}

// Flags sets the flags in effect: multi back busy nofilter match-desc match-detail password.
func (s *QuickStep) Flags(flags ...string) *QuickStep {
	if len(flags) == 0 {
		return s.rec("flags -", nil)
	}
	return s.rec("flags "+strings.Join(flags, " "), nil)
}

// Msg is a message about the text of sequence seq: info, warning or error (refuses Enter).
func (s *QuickStep) Msg(seq int64, severity, text string) *QuickStep {
	return s.rec(fmt.Sprintf("msg %d %s", seq, severity), []byte(viewLine(text, 1000)))
}

// QuickButton is a title button.
type QuickButton struct{ Key, Icon, Tip string }

func (s *QuickStep) Buttons(bs ...QuickButton) *QuickStep {
	var data []byte
	for _, b := range bs {
		t := viewLine(b.Tip, 200)
		data = append(data, fmt.Sprintf("%s %s %d\n", b.Key, b.Icon, len(t))...)
		data = append(data, t...)
	}
	return s.rec(fmt.Sprintf("buttons %d", len(bs)), data)
}

// Items replaces the items (checks and the active item stay by id).
func (s *QuickStep) Items(items ...*QuickItem) *QuickStep {
	var data []byte
	for _, it := range items {
		data = append(data, it.bytes()...)
	}
	return s.rec(fmt.Sprintf("items %d", len(items)), data)
}

// Active makes an item the active one.
func (s *QuickStep) Active(id string) *QuickStep { return s.rec("active", []byte(id)) }

// Bytes is the step body.
func (s *QuickStep) Bytes() []byte { return s.out }

// Quick opens a quick input (feature quick-input); its Q.
func (e *Ext) Quick(s *QuickStep) (int64, error) {
	body := s.Bytes()
	r, err := e.Request("quick", body, itoa(int64(len(body))))
	if err != nil {
		return 0, err
	}
	return argInt(r, 0), nil
}

// QuickSet changes what the step's records name, atomically.
func (e *Ext) QuickSet(q int64, s *QuickStep) error {
	body := s.Bytes()
	_, err := e.Request("quick-set", body, itoa(q), itoa(int64(len(body))))
	return err
}

// QuickClose closes it without a hide event.
func (e *Ext) QuickClose(q int64) error {
	_, err := e.Request("quick-close", nil, itoa(q))
	return err
}

// QuickEvent is what the user did: Kind value, active, accept, back, button or hide; Seq the
// field's sequence; Text the field (value, accept); IDs the accepted ids, or the active item's or
// the button's item (empty: a title button); Key a button's key.
type QuickEvent struct {
	Q    int64
	Kind string
	Seq  int64
	Text string
	IDs  []string
	Key  string
}

// QuickListener receives "0 quick Q ..." events.
type QuickListener interface {
	QuickEvent(ext *Ext, ev QuickEvent)
}

func quickEventHasBody(what string) bool {
	switch what {
	case "value", "active", "accept", "button":
		return true
	}
	return false
}

func parseQuickEvent(ev Event) (QuickEvent, bool) {
	if len(ev.Args) < 2 {
		return QuickEvent{}, false
	}
	q, err := strconv.ParseInt(ev.Args[0], 10, 64)
	if err != nil {
		return QuickEvent{}, false
	}
	num := func(i int) int64 {
		if i < len(ev.Args) {
			v, _ := strconv.ParseInt(ev.Args[i], 10, 64)
			return v
		}
		return 0
	}
	r := QuickEvent{Q: q, Kind: ev.Args[1], Seq: num(2)}
	switch r.Kind {
	case "value":
		r.Text = string(ev.Body)
	case "active":
		r.IDs = []string{string(ev.Body)}
	case "accept":
		n := int(num(3))
		parts := strings.SplitN(string(ev.Body), "\n", n+1)
		if len(parts) < n {
			return QuickEvent{}, false
		}
		r.IDs = parts[:n]
		if len(parts) > n {
			r.Text = parts[n]
		}
	case "button":
		if len(ev.Args) > 3 {
			r.Key = ev.Args[3]
		}
		r.IDs = []string{string(ev.Body)}
	case "back", "hide":
	default:
		return QuickEvent{}, false
	}
	return r, true
}
