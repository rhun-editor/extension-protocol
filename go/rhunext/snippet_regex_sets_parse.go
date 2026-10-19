package rhunext

import (
	"strings"
	"unicode/utf8"
)

func (p *snippetRegexParser) setClass(start, depth int) (*snippetRegexNode, error) {
	if depth == maxSnippetRegexDepth {
		return nil, p.fail(start, "syntax depth exceeded")
	}
	negative := p.pos < len(p.s) && p.s[p.pos] == '^'
	if negative {
		p.pos++
	}
	n := snippetRegexNode{kind: rxSetUnion, from: start, negative: negative}
	operator := ""
	for p.pos < len(p.s) && p.s[p.pos] != ']' {
		child, err := p.setOperand(depth + 1)
		if err != nil {
			return nil, err
		}
		if operator != "" && child.kind == rxRange {
			return nil, p.fail(child.from, "class set operator requires a nested range")
		}
		n.children = append(n.children, child)
		if len(n.children) == 1 {
			n.mayStrings = child.mayStrings
		} else if operator == "&&" {
			n.mayStrings = n.mayStrings && child.mayStrings
		} else if operator == "" {
			n.mayStrings = n.mayStrings || child.mayStrings
		}
		if p.pos == len(p.s) || p.s[p.pos] == ']' {
			break
		}
		next := ""
		if strings.HasPrefix(p.s[p.pos:], "&&") || strings.HasPrefix(p.s[p.pos:], "--") {
			next = p.s[p.pos : p.pos+2]
		}
		if len(n.children) > 1 && next != operator {
			return nil, p.fail(p.pos, "mixed class set operators require nested brackets")
		}
		if next != "" {
			if child.kind == rxRange {
				return nil, p.fail(child.from, "class set operator requires a nested range")
			}
			operator = next
			p.pos += 2
			if p.pos == len(p.s) || p.s[p.pos] == ']' {
				return nil, p.fail(p.pos, "missing class set operand")
			}
		}
	}
	if p.pos == len(p.s) {
		return nil, p.fail(p.pos, "unclosed class")
	}
	p.pos++
	n.to = p.pos
	if operator == "&&" {
		n.kind = rxSetIntersection
	} else if operator == "--" {
		n.kind = rxSetSubtraction
	}
	if negative && n.mayStrings {
		return nil, p.fail(start, "cannot negate a class that may contain strings")
	}
	return p.node(n)
}

func (p *snippetRegexParser) setOperand(depth int) (*snippetRegexNode, error) {
	start := p.pos
	if p.s[p.pos] == '[' {
		p.pos++
		return p.setClass(start, depth)
	}
	if strings.HasPrefix(p.s[p.pos:], `\q{`) {
		return p.setStrings()
	}
	n, err := p.setCharacter(false)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.s) && p.s[p.pos] == '-' && !strings.HasPrefix(p.s[p.pos:], "--") {
		p.pos++
		if p.pos == len(p.s) || p.s[p.pos] == ']' {
			return nil, p.fail(p.pos, "missing class range endpoint")
		}
		end, err := p.setCharacter(false)
		if err != nil {
			return nil, err
		}
		if n.kind != rxLiteral || end.kind != rxLiteral || n.lo > end.lo {
			return nil, p.fail(start, "invalid class range")
		}
		n.kind, n.hi, n.to = rxRange, end.lo, p.pos
	}
	return p.node(n)
}

func (p *snippetRegexParser) setCharacter(inString bool) (snippetRegexNode, error) {
	start := p.pos
	if p.pos == len(p.s) {
		return snippetRegexNode{}, p.fail(start, "missing class character")
	}
	c := p.s[p.pos]
	if c == '\\' {
		p.pos++
		n, err := p.escape(start, true)
		if err != nil {
			return n, err
		}
		if inString && n.kind != rxLiteral {
			return n, p.fail(start, "string disjunction requires literal characters")
		}
		if n.kind == rxProperty {
			_, n.mayStrings = snippetEmojiProperties[n.text]
			if n.mayStrings && n.negative {
				return n, p.fail(start, "cannot complement a Unicode string property")
			}
		}
		return n, nil
	}
	if strings.ContainsRune("()[]{}/-\\|", rune(c)) || p.pos+1 < len(p.s) && p.s[p.pos+1] == c && strings.ContainsRune("&!#$%*+,.:;<=>?@^`~", rune(c)) {
		return snippetRegexNode{}, p.fail(start, "reserved class set punctuation")
	}
	r, size := utf8.DecodeRuneInString(p.s[p.pos:])
	p.pos += size
	return snippetRegexNode{kind: rxLiteral, from: start, to: p.pos, lo: r}, nil
}

func (p *snippetRegexParser) setStrings() (*snippetRegexNode, error) {
	start := p.pos
	p.pos += 3
	n := snippetRegexNode{kind: rxStrings, from: start}
	for {
		at := p.pos
		var word strings.Builder
		scalars := 0
		for p.pos < len(p.s) && p.s[p.pos] != '|' && p.s[p.pos] != '}' {
			char, err := p.setCharacter(true)
			if err != nil {
				return nil, err
			}
			if utf8.RuneLen(char.lo) > (256<<10)-p.stringBytes {
				return nil, p.fail(at, "string disjunction byte limit exceeded")
			}
			p.stringBytes += utf8.RuneLen(char.lo)
			word.WriteRune(char.lo)
			scalars++
		}
		if p.pos == len(p.s) {
			return nil, p.fail(p.pos, "unclosed string disjunction")
		}
		if p.stringCount == maxSnippetRegexNodes {
			return nil, p.fail(at, "string disjunction count limit exceeded")
		}
		p.stringCount++
		if _, err := p.node(snippetRegexNode{from: at}); err != nil {
			return nil, err
		}
		n.strings = append(n.strings, word.String())
		n.mayStrings = n.mayStrings || scalars != 1
		last := p.s[p.pos] == '}'
		p.pos++
		if last {
			break
		}
	}
	n.to = p.pos
	return p.node(n)
}
