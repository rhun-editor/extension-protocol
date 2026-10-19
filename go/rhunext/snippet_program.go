package rhunext

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"unicode/utf8"
)

const MaxSnippetOptionBytes = 1 << 20

// SnippetChoice gives a primary field literal choices in display order. The first
// option must match its initial text. Empty and multiline values are supported.
type SnippetChoice struct {
	Group   int
	Options []string
}

// SnippetProgram combines prepared fields with self-contained native metadata.
// It does not execute a template or require the provider after insertion.
type SnippetProgram struct {
	Snippet PreparedSnippet
	Choices []SnippetChoice
	// InitialVariants lists one-based nonprimary fields whose initial text differs.
	// Requires both snippets and snippet-variants; later edits use ordinary mirrors.
	InitialVariants []int
	// Transforms run when leaving their field's group. Requires snippet-transforms.
	Transforms []SnippetFieldTransform
}

func (p SnippetProgram) extraSize() (int, error) {
	if len(p.Choices) > MaxSnippetGroups {
		return 0, fmt.Errorf("too many snippet choices")
	}
	rawTransforms, _, err := preflightSnippetTransforms(p.Transforms, p.Snippet.Fields)
	if err != nil {
		return 0, err
	}
	total := p.Snippet.extraSize()
	for _, index := range p.InitialVariants {
		total += len(fmt.Sprintf("variant %d 0\n", index))
	}
	seen := make(map[int]bool, len(p.Choices))
	for _, c := range p.Choices {
		if c.Group <= 0 || seen[c.Group] || len(c.Options) == 0 || len(c.Options) > MaxSnippetChoiceOptions {
			return 0, fmt.Errorf("invalid snippet choice group or count")
		}
		seen[c.Group] = true
		primary := -1
		for i, f := range p.Snippet.Fields {
			if f.Group == c.Group {
				primary = i
				break
			}
		}
		if primary < 0 {
			return 0, fmt.Errorf("snippet choice has no primary field")
		}
		f := p.Snippet.Fields[primary]
		if c.Options[0] != p.Snippet.Text[f.Start:f.End] {
			return 0, fmt.Errorf("snippet choice initial value does not match its primary")
		}
		size := 0
		for _, option := range c.Options {
			if len(option) > MaxSnippetOptionBytes || !utf8.ValidString(option) {
				return 0, fmt.Errorf("invalid snippet choice text")
			}
			n := len(option) + len(fmt.Sprintf("%d\n", len(option)))
			if n > MaxSnippetBody-size {
				return 0, fmt.Errorf("snippet choices exceed body limit")
			}
			size += n
		}
		n := size + len(fmt.Sprintf("choice %d %d %d\n", c.Group, len(c.Options), size))
		if n > MaxSnippetBody-total {
			return 0, fmt.Errorf("snippet choices exceed body limit")
		}
		total += n
	}
	if rawTransforms != 0 {
		encoded := base64.StdEncoding.EncodedLen(rawTransforms)
		added := encoded + len(fmt.Sprintf("transforms 0 %d\n", encoded))
		if added > MaxSnippetBody-total {
			return 0, fmt.Errorf("snippet transforms exceed body limit")
		}
		total += added
		if _, ok := snippetRecordSize(len(p.Snippet.Text), total); !ok {
			return 0, fmt.Errorf("snippet program exceeds body limit")
		}
		for _, b := range p.Transforms {
			if err := b.Transform.Validate(); err != nil {
				return 0, fmt.Errorf("snippet field %d transform: %w", b.Field, err)
			}
		}
	}
	return total, nil
}

// Validate checks graph, UTF-8, choice identity and encoded size before allocating
// the wire body. All metadata is accepted or rejected together.
func (p SnippetProgram) Validate() error {
	if len(p.InitialVariants) > MaxSnippetFields {
		return fmt.Errorf("too many initial variants")
	}
	variants := make(map[int]bool, len(p.InitialVariants))
	for _, index := range p.InitialVariants {
		if index < 1 || index > len(p.Snippet.Fields) || variants[index] {
			return fmt.Errorf("invalid initial variant index")
		}
		variants[index] = true
	}
	if err := p.Snippet.validate(variants); err != nil {
		return err
	}
	extra, err := p.extraSize()
	if err != nil {
		return err
	}
	if _, ok := snippetRecordSize(len(p.Snippet.Text), extra); !ok {
		return fmt.Errorf("snippet program exceeds body limit")
	}
	return nil
}

func (p SnippetProgram) extra() []byte {
	var out bytes.Buffer
	out.Write(p.Snippet.extra())
	for _, index := range p.InitialVariants {
		fmt.Fprintf(&out, "variant %d 0\n", index)
	}
	for _, c := range p.Choices {
		size := 0
		for _, option := range c.Options {
			size += len(option) + len(fmt.Sprintf("%d\n", len(option)))
		}
		fmt.Fprintf(&out, "choice %d %d %d\n", c.Group, len(c.Options), size)
		for _, option := range c.Options {
			fmt.Fprintf(&out, "%d\n", len(option))
			out.WriteString(option)
		}
	}
	if len(p.Transforms) != 0 {
		raw := encodeSnippetTransforms(p.Transforms, p.Snippet.Fields)
		encoded := base64.StdEncoding.EncodeToString(raw)
		fmt.Fprintf(&out, "transforms 0 %d\n", len(encoded))
		out.WriteString(encoded)
	}
	return out.Bytes()
}

// Encode returns the explicit request record, including choices.
func (p SnippetProgram) Encode() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	extra := p.extra()
	var out bytes.Buffer
	fmt.Fprintf(&out, "0 0 0 0 snippet 0 %d 0 %d\n", len(p.Snippet.Text), len(extra))
	out.WriteString(p.Snippet.Text)
	out.Write(extra)
	return out.Bytes(), nil
}

// WithSnippetProgram snapshots fields and choices. Invalid metadata is omitted
// as a whole, while prepared text still inserts, including empty text.
func (it CompletionItem) WithSnippetProgram(p SnippetProgram) CompletionItem {
	it.Insert, it.prepared = p.Snippet.Text, true
	it.fields, it.choices, it.initialVariants, it.transforms = nil, nil, nil, nil
	if p.Validate() == nil {
		it.transforms = append([]SnippetFieldTransform(nil), p.Transforms...)
		it.initialVariants = append([]int(nil), p.InitialVariants...)
		it.fields = append([]Field(nil), p.Snippet.Fields...)
		it.choices = make([]SnippetChoice, len(p.Choices))
		for i, c := range p.Choices {
			it.choices[i] = SnippetChoice{c.Group, append([]string(nil), c.Options...)}
		}
	}
	return it
}

func (p *SnippetProgram) detectInitialVariants() {
	primary := make(map[int]Field)
	for i, f := range p.Snippet.Fields {
		if f.Group == 0 {
			continue
		}
		if first, ok := primary[f.Group]; ok {
			if p.Snippet.Text[first.Start:first.End] != p.Snippet.Text[f.Start:f.End] {
				p.InitialVariants = append(p.InitialVariants, i+1)
			}
		} else {
			primary[f.Group] = f
		}
	}
}
