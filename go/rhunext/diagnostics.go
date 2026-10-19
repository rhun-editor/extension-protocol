package rhunext

// Protocol 1.1's file-diagnostics (v1.1/v1.1.md, "Feature: file-diagnostics"): diagnostics for any
// file, open or not, kept in rhun's Problems store, with ids that code actions get back, codes,
// sources, links, tags and related information.

import (
	"bytes"
	"fmt"
	"strings"
)

// DiagnosticTag marks a diagnostic: rhun draws unnecessary code faded and deprecated code struck
// through.
type DiagnosticTag string

const (
	TagUnnecessary DiagnosticTag = "unnecessary"
	TagDeprecated  DiagnosticTag = "deprecated"
)

// RelatedInfo is a place related to a diagnostic (another declaration, the first definition):
// Target is an absolute path or an open document, Range is there, Message one line.
type RelatedInfo struct {
	Target  Target
	Range   Range
	Message string
}

// The caps of file-diagnostics. FileDiagnostics cuts or leaves out what rhun would refuse:
// records past MaxFileDiagnostics or MaxFileDiagnosticsBody, a diagnostic without a message, ids
// that are not tokens or repeat one before, codes and sources past MaxDiagnosticCode, links that
// are not http(s) or past MaxDiagnosticHref, related places past MaxRelated.
const (
	MaxFileDiagnostics     = 2000
	MaxFileDiagnosticsBody = 4 << 20
	MaxDiagnosticMessage   = 4000
	MaxDiagnosticID        = 64
	MaxDiagnosticCode      = 100
	MaxDiagnosticHref      = 1000
	MaxRelated             = 16
)

// ValidDiagnosticID reports whether s is a diagnostic id rhun takes: 1 to 64 of [A-Za-z0-9_.:-].
func ValidDiagnosticID(s string) bool {
	if s == "" || len(s) > MaxDiagnosticID {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// validURL reports whether s is an http or https URL of at most max bytes, without white space or
// control characters.
func validURL(s string, max int) bool {
	l := strings.ToLower(s)
	rest := ""
	switch {
	case strings.HasPrefix(l, "http://"):
		rest = s[7:]
	case strings.HasPrefix(l, "https://"):
		rest = s[8:]
	default:
		return false
	}
	if rest == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] == 0x7f {
			return false
		}
	}
	return true
}

// absPath reports whether p is an absolute path: /…, a drive (C:\ or C:/) or \\server\share.
func absPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z' && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// validTarget reports whether t names a file rhun takes: an absolute path of at most MaxPath
// bytes on one line, or an open document.
func validTarget(t Target) bool {
	if t.Path == "" {
		return t.Doc > 0
	}
	return len(t.Path) <= MaxPath && absPath(t.Path) && !strings.ContainsAny(t.Path, "\n\r")
}

// markRange is a diagnostic's range as rhun takes it: positions of at least 1 1, the end not
// before the start.
func markRange(r Range) Range {
	fix := func(p Pos) Pos {
		return Pos{Line: max(p.Line, 1), Col: max(p.Col, 1)}
	}
	r.Start, r.End = fix(r.Start), fix(r.End)
	if r.End.Line < r.Start.Line || r.End.Line == r.Start.Line && r.End.Col < r.Start.Col {
		r.End = r.Start
	}
	return r
}

// diagnosticExtra is a diagnostic's EXTRA records (only the valid ones); ids is the set's ids so
// far, and the diagnostic's own is added.
func diagnosticExtra(d Diagnostic, ids map[string]bool) []byte {
	var b bytes.Buffer
	if ValidDiagnosticID(d.ID) && !ids[d.ID] {
		ids[d.ID] = true
		fmt.Fprintf(&b, "id %d\n%s", len(d.ID), d.ID)
	}
	if c := cut(oneLine(d.Code), MaxDiagnosticCode); c != "" {
		fmt.Fprintf(&b, "code %d\n%s", len(c), c)
	}
	if s := cut(oneLine(d.Source), MaxDiagnosticCode); s != "" {
		fmt.Fprintf(&b, "source %d\n%s", len(s), s)
	}
	if validURL(d.Href, MaxDiagnosticHref) {
		fmt.Fprintf(&b, "href %d\n%s", len(d.Href), d.Href)
	}
	var tagged [2]bool
	for _, t := range d.Tags {
		switch {
		case t == TagUnnecessary && !tagged[0]:
			tagged[0] = true
			b.WriteString("tag unnecessary 0\n")
		case t == TagDeprecated && !tagged[1]:
			tagged[1] = true
			b.WriteString("tag deprecated 0\n")
		}
	}
	related := 0
	for _, r := range d.Related {
		if related == MaxRelated {
			break
		}
		if !validTarget(r.Target) {
			continue
		}
		p, m := r.Target.String(), cut(oneLine(r.Message), MaxDiagnosticMessage)
		fmt.Fprintf(&b, "related %s %d %d\n%s%s", rangeFields(markRange(r.Range)), len(p), len(m), p, m)
		related++
	}
	return b.Bytes()
}

// EncodeFileDiagnostics is the records of a file-diagnostics request and how many there are.
// Diagnostics without a message or with an unknown severity are left out ("" is SevError), as
// those past MaxFileDiagnostics or MaxFileDiagnosticsBody; messages are cut at
// MaxDiagnosticMessage bytes, at a character, and keep their line feeds; ranges get positions of
// at least 1 1 and an end not before their start.
func EncodeFileDiagnostics(ds []Diagnostic) ([]byte, int) {
	var recs bytes.Buffer
	ids := map[string]bool{}
	n := 0
	for _, d := range ds {
		sev := d.Severity
		if sev == "" {
			sev = SevError
		}
		if sev != SevError && sev != SevWarning && sev != SevInfo && sev != SevHint {
			continue
		}
		msg := cut(d.Message, MaxDiagnosticMessage)
		if msg == "" {
			continue
		}
		extra := diagnosticExtra(d, ids)
		head := fmt.Sprintf("%s %s %d %d\n", rangeFields(markRange(d.Range)), sev, len(msg), len(extra))
		if n == MaxFileDiagnostics || recs.Len()+len(head)+len(msg)+len(extra)+MaxPath > MaxFileDiagnosticsBody {
			break
		}
		recs.WriteString(head)
		recs.WriteString(msg)
		recs.Write(extra)
		n++
	}
	return recs.Bytes(), n
}

// FileDiagnostics replaces this extension's diagnostics for a file in rhun's Problems store
// (feature file-diagnostics; needs documents = read): Target{Doc: d} for an open document at its
// current version, Target{Path: p} for any file by its absolute path (version is not sent: pass
// OnDisk). The file need not be open: its diagnostics show when it opens. No diagnostics clears
// them. The records are EncodeFileDiagnostics'. rhun refusing the set is an *Error (stale with
// the document's current version as Text, needs, nodoc, nofile for an untitled document, syntax,
// toobig), and then nothing changed.
func (e *Ext) FileDiagnostics(target Target, version int64, ds []Diagnostic) error {
	if !e.Has(FeatureFileDiagnostics) {
		return &Error{Code: "unknown", Text: "file-diagnostics was not agreed"}
	}
	if !validTarget(target) {
		return &Error{Code: "syntax", Text: "a target is an absolute path or an open document"}
	}
	v := "-"
	if target.Path == "" {
		if version < 0 {
			return &Error{Code: "syntax", Text: "an open document's diagnostics need its version"}
		}
		v = itoa(version)
	}
	recs, n := EncodeFileDiagnostics(ds)
	t := target.String()
	body := append([]byte(t), recs...)
	_, err := e.Request("file-diagnostics", body, v, itoa(int64(n)), itoa(int64(len(t))), itoa(int64(len(body))))
	return err
}

// ResetDiagnostics removes every set of diagnostics this extension gave with FileDiagnostics
// (feature file-diagnostics).
func (e *Ext) ResetDiagnostics() error {
	if !e.Has(FeatureFileDiagnostics) {
		return &Error{Code: "unknown", Text: "file-diagnostics was not agreed"}
	}
	_, err := e.Request("file-diagnostics-reset", nil)
	return err
}
