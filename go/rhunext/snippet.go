package rhunext

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"unicode/utf8"
)

const FeatureSnippets = "snippets"
const FeatureSnippetVariants = "snippet-variants"

const (
	MaxSnippetFields = 512
	MaxSnippetGroups = 256
	MaxSnippetDepth  = 32
	MaxSnippetBody   = 4 << 20
)

// Field describes an occurrence in prepared UTF-8 text. The first occurrence of a
// positive Group is editable; its repetitions are linked mirrors. Group 0 is the
// final selection (empty for a caret). Parent is zero or the one-based index of an earlier primary field.
type Field struct {
	Group, Start, End, Parent int
}

// PreparedSnippet is expanded text with native editable fields. It contains no
// executable template syntax. Offsets are bytes into Text, on character boundaries.
type PreparedSnippet struct {
	Text   string
	Fields []Field
}

// Validate checks the complete snippet before it can change a document.
func (s PreparedSnippet) Validate() error { return s.validate(nil) }

func (s PreparedSnippet) validate(variants map[int]bool) error {
	if len(s.Text) > MaxSnippetBody || !utf8.ValidString(s.Text) || len(s.Fields) > MaxSnippetFields {
		return fmt.Errorf("snippet text or fields exceed their limits or contain invalid UTF-8")
	}
	primary := map[int]int{}
	groupIndex := map[int]int{}
	depth := make([]int, len(s.Fields))
	final := false
	boundary := func(n int) bool { return n >= 0 && n <= len(s.Text) && (n == len(s.Text) || utf8.RuneStart(s.Text[n])) }
	for i, f := range s.Fields {
		if f.Group < 0 || f.Group > 2147483647 || !boundary(f.Start) || !boundary(f.End) || f.Start > f.End || f.Parent < 0 || f.Parent > i {
			return fmt.Errorf("invalid snippet field %d", i+1)
		}
		depth[i] = 1
		if f.Parent != 0 {
			p := s.Fields[f.Parent-1]
			if p.Group == 0 || primary[p.Group] != f.Parent-1 || p.Group == f.Group || f.Start < p.Start || f.End > p.End {
				return fmt.Errorf("invalid snippet parent for field %d", i+1)
			}
			depth[i] = depth[f.Parent-1] + 1
		}
		if depth[i] > MaxSnippetDepth {
			return fmt.Errorf("snippet nesting exceeds its limit")
		}
		if f.Group == 0 {
			if variants[i+1] {
				return fmt.Errorf("final selection cannot be an initial variant")
			}
			if final {
				return fmt.Errorf("duplicate final snippet selection")
			}
			final = true
			continue
		}
		if first, ok := primary[f.Group]; ok {
			p := s.Fields[first]
			if (s.Text[p.Start:p.End] != s.Text[f.Start:f.End]) != variants[i+1] {
				return fmt.Errorf("snippet mirrors contain different text")
			}
		} else {
			if variants[i+1] {
				return fmt.Errorf("primary cannot be an initial variant")
			}
			if len(primary) == MaxSnippetGroups {
				return fmt.Errorf("too many snippet groups")
			}
			groupIndex[f.Group] = len(primary)
			primary[f.Group] = i
		}
	}
	ancestor := func(parent, child int) bool {
		for p := s.Fields[child].Parent; p != 0; p = s.Fields[p-1].Parent {
			if p-1 == parent {
				return true
			}
		}
		return false
	}
	for i, a := range s.Fields {
		for j := 0; j < i; j++ {
			b := s.Fields[j]
			// This also catches an empty field strictly inside a nonempty field.
			overlap := a.Start < b.End && b.Start < a.End
			if overlap && a.Group != 0 && b.Group != 0 && (a.Group == b.Group || !ancestor(i, j) && !ancestor(j, i)) {
				return fmt.Errorf("snippet fields overlap without ancestry")
			}
		}
	}
	// Record parents are acyclic by construction. Mirroring can introduce a cycle
	// between different groups, so also validate the propagation dependency graph.
	n := len(primary)
	edges := make([][]bool, n)
	indegree := make([]int, n)
	for i := range edges {
		edges[i] = make([]bool, n)
	}
	for _, f := range s.Fields {
		if f.Group == 0 {
			continue
		}
		from := groupIndex[f.Group]
		for p := f.Parent; p != 0; p = s.Fields[p-1].Parent {
			to := groupIndex[s.Fields[p-1].Group]
			if !edges[from][to] {
				edges[from][to] = true
				indegree[to]++
			}
		}
	}
	queue := make([]int, 0, n)
	for i, degree := range indegree {
		if degree == 0 {
			queue = append(queue, i)
		}
	}
	for i := 0; i < len(queue); i++ {
		for to, edge := range edges[queue[i]] {
			if edge {
				indegree[to]--
				if indegree[to] == 0 {
					queue = append(queue, to)
				}
			}
		}
	}
	if len(queue) != n {
		return fmt.Errorf("snippet mirroring has a dependency cycle")
	}
	if _, ok := snippetRecordSize(len(s.Text), s.extraSize()); !ok {
		return fmt.Errorf("snippet body exceeds its limit")
	}
	return nil
}

func (s PreparedSnippet) extraSize() int {
	n := 0
	for _, f := range s.Fields {
		n += len(fmt.Sprintf("field %d %d %d %d 0\n", f.Group, f.Start, f.End, f.Parent))
	}
	return n
}

func snippetRecordSize(text, extra int) (int, bool) {
	if text > MaxSnippetBody || extra > MaxSnippetBody-text {
		return 0, false
	}
	n := text + extra + len(fmt.Sprintf("0 0 0 0 snippet 0 %d 0 %d\n", text, extra))
	return n, n <= MaxSnippetBody
}

func (s PreparedSnippet) extra() []byte {
	var out bytes.Buffer
	for _, f := range s.Fields {
		fmt.Fprintf(&out, "field %d %d %d %d 0\n", f.Group, f.Start, f.End, f.Parent)
	}
	return out.Bytes()
}

func (s PreparedSnippet) record() []byte {
	extra := s.extra()
	var out bytes.Buffer
	fmt.Fprintf(&out, "0 0 0 0 snippet 0 %d 0 %d\n", len(s.Text), len(extra))
	out.WriteString(s.Text)
	out.Write(extra)
	return out.Bytes()
}

// Encode returns a prepared-snippet record for the explicit snippet request.
func (s PreparedSnippet) Encode() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s.record(), nil
}

// WithSnippet inserts prepared text and fields when snippets was negotiated. On
// older hosts the text is still inserted. Invalid fields are omitted as a whole.
// Unlike a normal empty Insert, an empty prepared Text never falls back to Label.
func (it CompletionItem) WithSnippet(s PreparedSnippet) CompletionItem {
	return it.WithSnippetProgram(SnippetProgram{Snippet: s})
}

// InsertSnippet replaces a range and starts a native snippet session, returning
// the credited version. The host does not switch to an inactive target document.
func (e *Ext) InsertSnippet(doc Doc, version int64, span Range, s PreparedSnippet) (int64, error) {
	return e.InsertSnippetContext(context.Background(), doc, version, span, s)
}

func (e *Ext) InsertSnippetContext(ctx context.Context, doc Doc, version int64, span Range, s PreparedSnippet) (int64, error) {
	return e.InsertSnippetProgramContext(ctx, doc, version, span, SnippetProgram{Snippet: s})
}

// InsertSnippetProgram inserts prepared text, fields and literal choices.
func (e *Ext) InsertSnippetProgram(doc Doc, version int64, span Range, p SnippetProgram) (int64, error) {
	return e.InsertSnippetProgramContext(context.Background(), doc, version, span, p)
}

func (e *Ext) InsertSnippetProgramContext(ctx context.Context, doc Doc, version int64, span Range, p SnippetProgram) (int64, error) {
	if !e.Has(FeatureSnippets) || len(p.InitialVariants) != 0 && !e.Has(FeatureSnippetVariants) || len(p.Transforms) != 0 && !e.Has(FeatureSnippetTransforms) {
		return 0, &Error{Code: "unknown", Text: "snippets was not negotiated"}
	}
	if doc <= 0 || version < 0 || span.Start.Line <= 0 || span.Start.Col <= 0 || span.End.Line <= 0 || span.End.Col <= 0 ||
		span.End.Line < span.Start.Line || span.End.Line == span.Start.Line && span.End.Col < span.Start.Col {
		return 0, &Error{Code: "syntax", Text: "invalid snippet target or range"}
	}
	body, err := p.Encode()
	if err != nil {
		return 0, err
	}
	reply, err := e.RequestContext(ctx, "snippet", body, itoa(int64(doc)), itoa(version),
		strconv.Itoa(span.Start.Line), strconv.Itoa(span.Start.Col), strconv.Itoa(span.End.Line), strconv.Itoa(span.End.Col), strconv.Itoa(len(body)))
	if err != nil {
		return 0, err
	}
	if len(reply.Args) != 1 || !saveDecimal(reply.Args[0]) {
		return 0, fmt.Errorf("invalid snippet reply")
	}
	current, err := strconv.ParseInt(reply.Args[0], 10, 64)
	if err != nil || current < version {
		return 0, fmt.Errorf("invalid snippet version")
	}
	return current, nil
}
