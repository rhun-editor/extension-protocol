package rhunext

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxSnippetTemplateNodes = 8192
	MaxSnippetChoiceOptions = 256
)

// SnippetSyntaxError identifies a byte in a template that cannot be parsed.
type SnippetSyntaxError struct {
	Offset  int
	Message string
}

func (e *SnippetSyntaxError) Error() string {
	return fmt.Sprintf("snippet byte %d: %s", e.Offset, e.Message)
}

// SnippetTemplate is an immutable, parsed template. Parsing checks syntax and
// resource bounds; it does not resolve variables, validate regexes, or insert text.
// A template can be shared by concurrent callers.
type SnippetTemplate struct {
	source string
	nodes  []stNode
}

// Source returns the original UTF-8 template, including its escapes.
func (t *SnippetTemplate) Source() string { return t.source }

type stKind uint8

const (
	stText stKind = iota
	stTabstop
	stPlaceholder
	stChoice
	stVariable
	stTransform
)

type stNode struct {
	kind       stKind
	from, to   int
	index      int
	name, text string
	hasDefault bool
	children   []stNode
	choices    []string
	transform  *stTransformValue
}

type stTransformValue struct {
	regexFrom, regexTo, optionsFrom int
	regex, options                  string
	format                          []stFormat
}

type stFormatKind uint8

const (
	stFormatText stFormatKind = iota
	stCapture
	stCase
	stIf
	stElse
	stConditional
)

type stFormat struct {
	kind            stFormatKind
	from, to, index int
	text, otherwise string
}

type stParser struct {
	s     string
	pos   int
	nodes int
}

// ParseSnippetTemplate parses tabstops, defaults, choices, variables and transform
// syntax. Regex source and flag letters remain opaque until compilation. Errors
// return no partial tree. Ordinary unknown dollar sequences remain literal text.
func ParseSnippetTemplate(source string) (*SnippetTemplate, error) {
	p := stParser{s: source}
	if len(source) > MaxSnippetBody {
		return nil, p.fail(MaxSnippetBody, "template exceeds byte limit")
	}
	if !utf8.ValidString(source) {
		for pos := 0; pos < len(source); {
			r, n := utf8.DecodeRuneInString(source[pos:])
			if r == utf8.RuneError && n == 1 {
				return nil, p.fail(pos, "invalid UTF-8")
			}
			pos += n
		}
	}
	nodes, err := p.sequence(false, 0)
	if err != nil {
		return nil, err
	}
	return &SnippetTemplate{source: source, nodes: nodes}, nil
}

func (p *stParser) fail(at int, message string) error {
	return &SnippetSyntaxError{Offset: at, Message: message}
}

func (p *stParser) node(at int) error {
	if p.nodes >= MaxSnippetTemplateNodes {
		return p.fail(at, "template exceeds node limit")
	}
	p.nodes++
	return nil
}

func (p *stParser) level(depth int) error {
	if depth > MaxSnippetDepth {
		return p.fail(p.pos, "template exceeds nesting limit")
	}
	return nil
}

func stDigit(c byte) bool { return c >= '0' && c <= '9' }
func stLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
func stNameStart(c byte) bool { return stLetter(c) || c == '_' }

func (p *stParser) sequence(close bool, depth int) ([]stNode, error) {
	var out []stNode
	var text strings.Builder
	start := p.pos
	flush := func() error {
		if text.Len() == 0 {
			return nil
		}
		if err := p.node(start); err != nil {
			return err
		}
		out = append(out, stNode{kind: stText, from: start, to: p.pos, text: text.String()})
		text.Reset()
		return nil
	}
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if close && c == '}' {
			if err := flush(); err != nil {
				return nil, err
			}
			p.pos++
			return out, nil
		}
		if c == '$' && p.pos+1 < len(p.s) {
			next := p.s[p.pos+1]
			if next == '{' || stDigit(next) || stNameStart(next) {
				if err := flush(); err != nil {
					return nil, err
				}
				node, err := p.dollar(depth)
				if err != nil {
					return nil, err
				}
				out = append(out, node)
				start = p.pos
				continue
			}
		}
		if text.Len() == 0 {
			start = p.pos
		}
		if c == '\\' && p.pos+1 < len(p.s) {
			next := p.s[p.pos+1]
			if next == '$' || next == '}' || next == '\\' {
				text.WriteByte(next)
				p.pos += 2
				continue
			}
		}
		text.WriteByte(c)
		p.pos++
	}
	if close {
		return nil, p.fail(p.pos, "unclosed default")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *stParser) number() (int, error) {
	start, value := p.pos, 0
	for p.pos < len(p.s) && stDigit(p.s[p.pos]) {
		digit := int(p.s[p.pos] - '0')
		if value > (2147483647-digit)/10 {
			return 0, p.fail(p.pos, "numeric identifier exceeds int31")
		}
		value = value*10 + digit
		p.pos++
	}
	if p.pos == start {
		return 0, p.fail(p.pos, "expected numeric identifier")
	}
	return value, nil
}

func (p *stParser) dollar(depth int) (stNode, error) {
	node := stNode{from: p.pos}
	if err := p.node(p.pos); err != nil {
		return node, err
	}
	p.pos++ // dollar
	braced := p.s[p.pos] == '{'
	if braced {
		p.pos++
		depth++
		if err := p.level(depth); err != nil {
			return node, err
		}
	}
	if p.pos >= len(p.s) {
		return node, p.fail(p.pos, "missing identifier")
	}
	if stDigit(p.s[p.pos]) {
		index, err := p.number()
		if err != nil {
			return node, err
		}
		node.kind, node.index = stTabstop, index
	} else if stNameStart(p.s[p.pos]) {
		start := p.pos
		for p.pos < len(p.s) && (stNameStart(p.s[p.pos]) || stDigit(p.s[p.pos])) {
			p.pos++
		}
		node.kind, node.name = stVariable, p.s[start:p.pos]
	} else {
		return node, p.fail(p.pos, "invalid identifier")
	}
	if braced {
		if p.pos == len(p.s) {
			return node, p.fail(p.pos, "unclosed construct")
		}
		switch p.s[p.pos] {
		case '}':
			p.pos++
		case ':':
			p.pos++
			node.hasDefault = true
			if node.kind == stTabstop {
				node.kind = stPlaceholder
			}
			children, err := p.sequence(true, depth)
			if err != nil {
				return node, err
			}
			node.children = children
		case '|':
			if node.kind != stTabstop {
				return node, p.fail(p.pos, "choices require a numeric field")
			}
			p.pos++
			options, err := p.choice()
			if err != nil {
				return node, err
			}
			node.kind, node.choices = stChoice, options
		case '/':
			p.pos++
			transform, err := p.transform(depth)
			if err != nil {
				return node, err
			}
			node.kind, node.transform = stTransform, transform
		default:
			return node, p.fail(p.pos, "expected closing brace, default, choice or transform")
		}
	}
	node.to = p.pos
	return node, nil
}

func (p *stParser) choice() ([]string, error) {
	var options []string
	var text strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == ',' || c == '|' {
			if len(options) >= MaxSnippetChoiceOptions {
				return nil, p.fail(p.pos, "choice exceeds option limit")
			}
			options = append(options, text.String())
			text.Reset()
			p.pos++
			if c == ',' {
				continue
			}
			if p.pos >= len(p.s) || p.s[p.pos] != '}' {
				return nil, p.fail(p.pos, "expected closing choice brace")
			}
			p.pos++
			return options, nil
		}
		if c == '\\' && p.pos+1 < len(p.s) {
			next := p.s[p.pos+1]
			if next == ',' || next == '|' || next == '\\' {
				text.WriteByte(next)
				p.pos += 2
				continue
			}
		}
		text.WriteByte(c)
		p.pos++
	}
	return nil, p.fail(p.pos, "unclosed choice")
}

func (p *stParser) transform(depth int) (*stTransformValue, error) {
	regexFrom, regexTo := p.pos, p.pos
	var regex strings.Builder
	closed := false
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '/' {
			regexTo = p.pos
			p.pos++
			closed = true
			break
		}
		if c == '\\' && p.pos+1 < len(p.s) {
			if p.s[p.pos+1] == '/' {
				regex.WriteByte('/')
			} else {
				regex.WriteString(p.s[p.pos : p.pos+2])
			}
			p.pos += 2
			continue
		}
		regex.WriteByte(c)
		p.pos++
	}
	if !closed {
		return nil, p.fail(p.pos, "unclosed regex")
	}
	format, err := p.format(depth)
	if err != nil {
		return nil, err
	}
	start := p.pos
	for p.pos < len(p.s) && stLetter(p.s[p.pos]) {
		p.pos++
	}
	options := p.s[start:p.pos]
	if p.pos >= len(p.s) || p.s[p.pos] != '}' {
		return nil, p.fail(p.pos, "expected transform closing brace")
	}
	p.pos++
	return &stTransformValue{regexFrom: regexFrom, regexTo: regexTo, optionsFrom: start, regex: regex.String(), options: options, format: format}, nil
}

func (p *stParser) format(depth int) ([]stFormat, error) {
	var out []stFormat
	var text strings.Builder
	start := p.pos
	flush := func() error {
		if text.Len() == 0 {
			return nil
		}
		if err := p.node(start); err != nil {
			return err
		}
		out = append(out, stFormat{kind: stFormatText, from: start, to: p.pos, text: text.String()})
		text.Reset()
		return nil
	}
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '/' {
			if err := flush(); err != nil {
				return nil, err
			}
			p.pos++
			return out, nil
		}
		if c == '$' && p.pos+1 < len(p.s) && (p.s[p.pos+1] == '{' || stDigit(p.s[p.pos+1])) {
			if err := flush(); err != nil {
				return nil, err
			}
			part, err := p.capture(depth)
			if err != nil {
				return nil, err
			}
			out = append(out, part)
			start = p.pos
			continue
		}
		if text.Len() == 0 {
			start = p.pos
		}
		if c == '\\' && p.pos+1 < len(p.s) {
			next := p.s[p.pos+1]
			if next == '/' || next == '$' || next == '}' || next == '\\' {
				text.WriteByte(next)
				p.pos += 2
				continue
			}
		}
		text.WriteByte(c)
		p.pos++
	}
	return nil, p.fail(p.pos, "unclosed transform format")
}

func (p *stParser) capture(depth int) (stFormat, error) {
	part := stFormat{kind: stCapture, from: p.pos}
	if err := p.node(p.pos); err != nil {
		return part, err
	}
	p.pos++
	braced := p.s[p.pos] == '{'
	if braced {
		p.pos++
		if err := p.level(depth + 1); err != nil {
			return part, err
		}
	}
	index, err := p.number()
	if err != nil {
		return part, err
	}
	part.index = index
	if !braced {
		part.to = p.pos
		return part, nil
	}
	if p.pos >= len(p.s) {
		return part, p.fail(p.pos, "unclosed capture format")
	}
	if p.s[p.pos] == '}' {
		p.pos++
		part.to = p.pos
		return part, nil
	}
	if p.s[p.pos] != ':' {
		return part, p.fail(p.pos, "expected capture format separator")
	}
	p.pos++
	if p.pos >= len(p.s) {
		return part, p.fail(p.pos, "unclosed capture format")
	}
	switch p.s[p.pos] {
	case '/':
		p.pos++
		start := p.pos
		for p.pos < len(p.s) && stLetter(p.s[p.pos]) {
			p.pos++
		}
		name := p.s[start:p.pos]
		switch name {
		case "upcase", "downcase", "capitalize", "camelcase", "pascalcase", "snakecase", "kebabcase":
		default:
			return part, p.fail(start, "unknown case format")
		}
		part.kind, part.text = stCase, name
		if p.pos >= len(p.s) || p.s[p.pos] != '}' {
			return part, p.fail(p.pos, "expected case format closing brace")
		}
		p.pos++
	case '?':
		p.pos++
		part.kind = stConditional
		part.text, err = p.branch(':')
		if err == nil {
			part.otherwise, err = p.branch('}')
		}
	case '+':
		p.pos++
		part.kind = stIf
		part.text, err = p.branch('}')
	case '-':
		p.pos++
		part.kind = stElse
		part.text, err = p.branch('}')
	default:
		part.kind = stElse
		part.text, err = p.branch('}')
	}
	if err != nil {
		return part, err
	}
	part.to = p.pos
	return part, nil
}

func (p *stParser) branch(terminator byte) (string, error) {
	var text strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == terminator {
			p.pos++
			return text.String(), nil
		}
		if terminator == ':' && c == '}' {
			return "", p.fail(p.pos, "conditional format needs an else separator")
		}
		if c == '\\' && p.pos+1 < len(p.s) {
			next := p.s[p.pos+1]
			if next == '/' || next == '$' || next == '}' || next == '\\' || next == ':' {
				text.WriteByte(next)
				p.pos += 2
				continue
			}
		}
		text.WriteByte(c)
		p.pos++
	}
	return "", p.fail(p.pos, "unclosed capture branch")
}
