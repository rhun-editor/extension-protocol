package rhunext

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxSnippetRegexBytes    = 65536
	maxSnippetRegexNodes    = 4096
	maxSnippetRegexCaptures = 64
	maxSnippetRegexDepth    = 32
	maxSnippetRegexToken    = 64
)

type snippetRegexKind uint8

const (
	rxEmpty snippetRegexKind = iota
	rxLiteral
	rxSequence
	rxAlternative
	rxAny
	rxAssert
	rxCapture
	rxGroup
	rxLook
	rxRepeat
	rxClass
	rxRange
	rxShorthand
	rxProperty
	rxBackref
	rxSetUnion
	rxSetIntersection
	rxSetSubtraction
	rxStrings
)
const (
	rxGlobal uint16 = 1 << iota
	rxIgnoreCase
	rxMultiline
	rxDotAll
	rxUnicode
	rxSticky
	rxIndices
	rxUnicodeSets
)

type snippetRegexNode struct {
	kind                     snippetRegexKind
	from, to                 int
	lo, hi                   rune
	children                 []*snippetRegexNode
	group, min, max          int
	text                     string
	lazy, negative, backward bool
	mayStrings               bool
	strings                  []string
	aliasGroups              []int
	enable, disable          uint16
}
type snippetRegex struct {
	source   string
	flags    uint16
	root     *snippetRegexNode
	captures int
	names    map[string]int
}
type snippetRegexError struct {
	Part    string
	Offset  int
	Message string
}

func (e *snippetRegexError) Error() string {
	return fmt.Sprintf("regex %s byte %d: %s", e.Part, e.Offset, e.Message)
}

type snippetRegexParser struct {
	s                    string
	pos, nodes, captures int
	names                map[string]int
	flags                uint16
	stringBytes          int
	stringCount          int
	duplicateNames       bool
	aliases              map[string][]int
	legacyScanned        bool
	legacyCaptures       int
	legacyNamed          bool
}

func (p *snippetRegexParser) fail(at int, message string) error {
	return &snippetRegexError{"source", at, message}
}
func (p *snippetRegexParser) node(n snippetRegexNode) (*snippetRegexNode, error) {
	if p.nodes == maxSnippetRegexNodes {
		return nil, p.fail(n.from, "node limit exceeded")
	}
	p.nodes++
	return &n, nil
}
func parseSnippetRegex(source, options string) (*snippetRegex, error) {
	if len(source) > maxSnippetRegexBytes {
		return nil, &snippetRegexError{"source", maxSnippetRegexBytes, "source limit exceeded"}
	}
	if !utf8.ValidString(source) {
		for i := 0; i < len(source); {
			r, n := utf8.DecodeRuneInString(source[i:])
			if r == utf8.RuneError && n == 1 {
				return nil, &snippetRegexError{"source", i, "invalid UTF-8"}
			}
			i += n
		}
	}
	flags := uint16(0)
	for i := 0; i < len(options); i++ {
		var bit uint16
		switch options[i] {
		case 'g':
			bit = rxGlobal
		case 'i':
			bit = rxIgnoreCase
		case 'm':
			bit = rxMultiline
		case 's':
			bit = rxDotAll
		case 'u':
			bit = rxUnicode
		case 'y':
			bit = rxSticky
		case 'd':
			bit = rxIndices
		case 'v':
			bit = rxUnicodeSets
		default:
			return nil, &snippetRegexError{"options", i, "unsupported flag"}
		}
		if flags&bit != 0 {
			return nil, &snippetRegexError{"options", i, "duplicate flag"}
		}
		if bit == rxUnicode && flags&rxUnicodeSets != 0 || bit == rxUnicodeSets && flags&rxUnicode != 0 {
			return nil, &snippetRegexError{"options", i, "u and v flags are mutually exclusive"}
		}
		flags |= bit
	}
	p := snippetRegexParser{s: source, flags: flags, names: map[string]int{}}
	root, err := p.alternative(0)
	if err != nil {
		return nil, err
	}
	if p.pos != len(source) {
		return nil, p.fail(p.pos, "unmatched closing group")
	}
	if p.duplicateNames {
		if err = p.resolveDuplicateNames(root); err != nil {
			return nil, err
		}
	}
	if err = p.resolve(root, 0); err != nil {
		return nil, err
	}
	return &snippetRegex{source, flags, root, p.captures, p.names}, nil
}
func (p *snippetRegexParser) alternative(depth int) (*snippetRegexNode, error) {
	start := p.pos
	first, err := p.sequence(depth)
	if err != nil {
		return nil, err
	}
	if p.pos == len(p.s) || p.s[p.pos] != '|' {
		return first, nil
	}
	children := []*snippetRegexNode{first}
	for p.pos < len(p.s) && p.s[p.pos] == '|' {
		p.pos++
		child, err := p.sequence(depth)
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	return p.node(snippetRegexNode{kind: rxAlternative, from: start, to: p.pos, children: children})
}
func (p *snippetRegexParser) sequence(depth int) (*snippetRegexNode, error) {
	start := p.pos
	var children []*snippetRegexNode
	for p.pos < len(p.s) && p.s[p.pos] != '|' && p.s[p.pos] != ')' {
		atom, err := p.atom(depth)
		if err != nil {
			return nil, err
		}
		if p.pos < len(p.s) && strings.ContainsRune("*+?{", rune(p.s[p.pos])) &&
			(p.s[p.pos] != '{' || !p.legacy() || p.bracedQuantifier(p.pos)) {
			if atom.kind == rxAssert || atom.kind == rxLook && (!p.legacy() || atom.backward) {
				return nil, p.fail(p.pos, "assertion cannot be quantified")
			}
			at := p.pos
			min, max := 0, -1
			switch p.s[p.pos] {
			case '*':
				p.pos++
			case '+':
				min = 1
				p.pos++
			case '?':
				max = 1
				p.pos++
			case '{':
				p.pos++
				min, err = p.number()
				if err != nil {
					return nil, err
				}
				max = min
				if p.pos < len(p.s) && p.s[p.pos] == ',' {
					p.pos++
					max = -1
					if p.pos < len(p.s) && stDigit(p.s[p.pos]) {
						max, err = p.number()
						if err != nil {
							return nil, err
						}
					}
				}
				if p.pos == len(p.s) || p.s[p.pos] != '}' {
					return nil, p.fail(p.pos, "unclosed quantifier")
				}
				p.pos++
				if max >= 0 && max < min {
					return nil, p.fail(at, "reversed quantifier bounds")
				}
			}
			lazy := false
			if p.pos < len(p.s) && p.s[p.pos] == '?' {
				lazy = true
				p.pos++
			}
			atom, err = p.node(snippetRegexNode{kind: rxRepeat, from: atom.from, to: p.pos, children: []*snippetRegexNode{atom}, min: min, max: max, lazy: lazy})
			if err != nil {
				return nil, err
			}
		}
		children = append(children, atom)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	kind := rxSequence
	if len(children) == 0 {
		kind = rxEmpty
	}
	return p.node(snippetRegexNode{kind: kind, from: start, to: p.pos, children: children})
}
func (p *snippetRegexParser) atom(depth int) (*snippetRegexNode, error) {
	start := p.pos
	c := p.s[p.pos]
	p.pos++
	n := snippetRegexNode{from: start, kind: rxLiteral}
	switch c {
	case '(':
		if depth == maxSnippetRegexDepth {
			return nil, p.fail(start, "syntax depth exceeded")
		}
		n.kind = rxCapture
		if p.pos < len(p.s) && p.s[p.pos] == '?' {
			p.pos++
			if p.pos == len(p.s) {
				return nil, p.fail(p.pos, "unclosed group")
			}
			switch p.s[p.pos] {
			case ':':
				n.kind = rxGroup
				p.pos++
			case 'i', 'm', 's', '-':
				enable, disable, err := p.modifiers()
				if err != nil {
					return nil, err
				}
				n.kind, n.enable, n.disable = rxGroup, enable, disable
			case '=', '!':
				n.kind = rxLook
				n.negative = p.s[p.pos] == '!'
				p.pos++
			case '<':
				p.pos++
				if p.pos < len(p.s) && (p.s[p.pos] == '=' || p.s[p.pos] == '!') {
					n.kind = rxLook
					n.backward = true
					n.negative = p.s[p.pos] == '!'
					p.pos++
				} else {
					name, err := p.name()
					if err != nil {
						return nil, err
					}
					n.text = name
					if p.pos == len(p.s) || p.s[p.pos] != '>' {
						return nil, p.fail(p.pos, "unclosed capture name")
					}
					p.pos++
				}
			default:
				return nil, p.fail(p.pos, "unsupported group prefix")
			}
		}
		if n.kind == rxCapture {
			if p.captures == maxSnippetRegexCaptures {
				return nil, p.fail(start, "capture limit exceeded")
			}
			p.captures++
			n.group = p.captures
			if n.text != "" {
				if _, exists := p.names[n.text]; exists {
					p.duplicateNames = true
				} else {
					p.names[n.text] = n.group
				}
			}
		}
		child, err := p.alternative(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.pos == len(p.s) || p.s[p.pos] != ')' {
			return nil, p.fail(p.pos, "unclosed group")
		}
		p.pos++
		n.children = []*snippetRegexNode{child}
	case '[':
		if p.flags&rxUnicodeSets != 0 {
			return p.setClass(start, depth)
		}
		if depth == maxSnippetRegexDepth {
			return nil, p.fail(start, "syntax depth exceeded")
		}
		n.kind = rxClass
		if p.pos < len(p.s) && p.s[p.pos] == '^' {
			n.negative = true
			p.pos++
		}
		for p.pos < len(p.s) && p.s[p.pos] != ']' {
			term, err := p.classAtom()
			if err != nil {
				return nil, err
			}
			if p.pos < len(p.s) && p.s[p.pos] == '-' && p.pos+1 < len(p.s) && p.s[p.pos+1] != ']' {
				dash := p.pos
				p.pos++
				end, err := p.classAtom()
				if err != nil {
					return nil, err
				}
				if term.kind != rxLiteral || end.kind != rxLiteral {
					if !p.legacy() {
						return nil, p.fail(term.from, "invalid class range")
					}
					for _, part := range []snippetRegexNode{term, {kind: rxLiteral, from: dash, to: dash + 1, lo: '-'}, end} {
						child, err := p.node(part)
						if err != nil {
							return nil, err
						}
						n.children = append(n.children, child)
					}
					continue
				}
				if term.lo > end.lo {
					return nil, p.fail(term.from, "invalid class range")
				}
				term.kind = rxRange
				term.hi = end.lo
				term.to = end.to
			}
			child, err := p.node(term)
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, child)
		}
		if p.pos == len(p.s) {
			return nil, p.fail(p.pos, "unclosed class")
		}
		p.pos++
	case '\\':
		var err error
		n, err = p.escape(start, false)
		if err != nil {
			return nil, err
		}
	case '.':
		n.kind = rxAny
	case '^':
		n.kind = rxAssert
		n.text = "start"
	case '$':
		n.kind = rxAssert
		n.text = "end"
	case '*', '+', '?':
		return nil, p.fail(start, "unexpected regex punctuation")
	case '{':
		if !p.legacy() || p.bracedQuantifier(start) {
			return nil, p.fail(start, "unexpected regex punctuation")
		}
		n.lo = rune(c)
	case '}', ']':
		if !p.legacy() {
			return nil, p.fail(start, "unexpected regex punctuation")
		}
		n.lo = rune(c)
	default:
		n.lo, _ = utf8.DecodeRuneInString(p.s[start:])
		p.pos = start + utf8.RuneLen(n.lo)
	}
	n.to = p.pos
	return p.node(n)
}
func (p *snippetRegexParser) classAtom() (snippetRegexNode, error) {
	start := p.pos
	if p.s[p.pos] == '\\' {
		p.pos++
		return p.escape(start, true)
	}
	r, size := utf8.DecodeRuneInString(p.s[p.pos:])
	p.pos += size
	return snippetRegexNode{kind: rxLiteral, from: start, to: p.pos, lo: r}, nil
}
func (p *snippetRegexParser) number() (int, error) {
	start, value := p.pos, 0
	for p.pos < len(p.s) && stDigit(p.s[p.pos]) {
		digit := int(p.s[p.pos] - '0')
		if value > (2147483647-digit)/10 {
			return 0, p.fail(p.pos, "number exceeds int31")
		}
		value = value*10 + digit
		p.pos++
	}
	if start == p.pos {
		return 0, p.fail(start, "expected decimal number")
	}
	return value, nil
}
func (p *snippetRegexParser) name() (string, error) {
	var name strings.Builder
	for p.pos < len(p.s) && p.s[p.pos] != '>' {
		at := p.pos
		var cp rune
		if p.s[p.pos] == '\\' {
			if p.pos+1 == len(p.s) || p.s[p.pos+1] != 'u' {
				return "", p.fail(at, "invalid capture name escape")
			}
			p.pos++
			char, err := p.escapeMode(at, true, false)
			if err != nil {
				return "", err
			}
			cp = char.lo
		} else {
			var size int
			cp, size = utf8.DecodeRuneInString(p.s[p.pos:])
			p.pos += size
		}
		property := "bin:ID_Continue"
		if name.Len() == 0 {
			property = "bin:ID_Start"
		}
		valid := cp == '$' || cp == '_' || snippetUnicodeContains(snippetUnicodeProperties[property], cp)
		if name.Len() != 0 && (cp == 0x200c || cp == 0x200d) {
			valid = true
		}
		if !valid {
			return "", p.fail(at, "invalid capture name")
		}
		if utf8.RuneLen(cp) > maxSnippetRegexToken-name.Len() {
			return "", p.fail(at, "capture name limit exceeded")
		}
		name.WriteRune(cp)
	}
	if name.Len() == 0 {
		return "", p.fail(p.pos, "invalid capture name")
	}
	return name.String(), nil
}
func rxHex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}
func (p *snippetRegexParser) hex(count int) (rune, error) {
	var value rune
	for i := 0; i < count; i++ {
		if p.pos == len(p.s) || rxHex(p.s[p.pos]) < 0 {
			return 0, p.fail(p.pos, "expected hex digit")
		}
		value = value*16 + rune(rxHex(p.s[p.pos]))
		p.pos++
	}
	return value, nil
}
func (p *snippetRegexParser) escape(start int, inClass bool) (snippetRegexNode, error) {
	return p.escapeMode(start, inClass, p.legacy())
}
func (p *snippetRegexParser) escapeMode(start int, inClass, legacy bool) (snippetRegexNode, error) {
	n := snippetRegexNode{kind: rxLiteral, from: start}
	if p.pos == len(p.s) {
		return n, p.fail(p.pos, "trailing escape")
	}
	c := p.s[p.pos]
	p.pos++
	switch c {
	case 'n':
		n.lo = '\n'
	case 'r':
		n.lo = '\r'
	case 't':
		n.lo = '\t'
	case 'f':
		n.lo = '\f'
	case 'v':
		n.lo = '\v'
	case 'b', 'B':
		if inClass {
			if c == 'B' {
				if !legacy {
					return n, p.fail(start, "boundary escape inside class")
				}
				n.lo = 'B'
				break
			}
			n.lo = '\b'
		} else {
			n.kind = rxAssert
			n.text = "word"
			n.negative = c == 'B'
		}
	case 'd', 'D', 'w', 'W', 's', 'S':
		n.kind = rxShorthand
		n.negative = c < 'a'
		n.text = strings.ToLower(string(c))
	case 'p', 'P':
		if legacy && !p.legacyProperty(p.pos) {
			n.lo = rune(c)
			break
		}
		n.kind = rxProperty
		n.negative = c == 'P'
		if p.pos == len(p.s) || p.s[p.pos] != '{' {
			return n, p.fail(p.pos, "expected property brace")
		}
		p.pos++
		at := p.pos
		equals := -1
		for p.pos < len(p.s) && p.s[p.pos] != '}' {
			b := p.s[p.pos]
			if p.pos-at == maxSnippetRegexToken {
				return n, p.fail(p.pos, "property token limit exceeded")
			}
			if b == '=' {
				if equals >= 0 || p.pos == at {
					return n, p.fail(p.pos, "invalid property expression")
				}
				equals = p.pos
			} else if (!stLetter(b) && !stDigit(b) && b != '_') || stDigit(b) && (p.pos == at || p.pos == equals+1) {
				return n, p.fail(p.pos, "invalid property token")
			}
			p.pos++
		}
		if p.pos == len(p.s) || p.pos == at || equals == p.pos-1 {
			return n, p.fail(p.pos, "unclosed or empty property")
		}
		n.text = p.s[at:p.pos]
		p.pos++
	case 'k':
		if legacy {
			p.legacyCensus()
			if !p.legacyNamed {
				n.lo = 'k'
				break
			}
		}
		if inClass {
			return n, p.fail(start, "backreference inside class")
		}
		n.kind = rxBackref
		if p.pos == len(p.s) || p.s[p.pos] != '<' {
			return n, p.fail(p.pos, "expected reference name")
		}
		p.pos++
		name, err := p.name()
		if err != nil {
			return n, err
		}
		n.text = name
		if p.pos == len(p.s) || p.s[p.pos] != '>' {
			return n, p.fail(p.pos, "unclosed reference name")
		}
		p.pos++
	case '0':
		if legacy {
			p.pos--
			return p.legacyDecimal(start, inClass), nil
		}
		if p.pos < len(p.s) && stDigit(p.s[p.pos]) {
			return n, p.fail(start, "legacy octal escape is unsupported")
		}
		n.lo = 0
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if legacy {
			p.pos--
			return p.legacyDecimal(start, inClass), nil
		}
		if inClass {
			return n, p.fail(start, "backreference inside class")
		}
		p.pos--
		group, err := p.number()
		if err != nil {
			return n, err
		}
		n.kind = rxBackref
		n.group = group
	case 'c':
		if p.pos == len(p.s) || !(stLetter(p.s[p.pos]) || legacy && inClass && (stDigit(p.s[p.pos]) || p.s[p.pos] == '_')) {
			if legacy {
				n.lo = '\\'
				p.pos--
				break
			}
			return n, p.fail(p.pos, "invalid control escape")
		}
		n.lo = rune(p.s[p.pos] & 31)
		p.pos++
	case 'x':
		if legacy && !p.fixedHex(p.pos, 2) {
			n.lo = 'x'
			break
		}
		v, err := p.hex(2)
		if err != nil {
			return n, err
		}
		n.lo = v
	case 'u':
		if legacy {
			valid := p.fixedHex(p.pos, 4)
			if p.pos < len(p.s) && p.s[p.pos] == '{' {
				valid = p.bracedHex(p.pos)
			}
			if !valid {
				n.lo = 'u'
				break
			}
		}
		var value rune
		var err error
		if p.pos < len(p.s) && p.s[p.pos] == '{' {
			p.pos++
			at := p.pos
			for p.pos < len(p.s) && p.s[p.pos] != '}' {
				digit := rxHex(p.s[p.pos])
				if digit < 0 {
					return n, p.fail(p.pos, "invalid scalar escape")
				}
				if value > (utf8.MaxRune-rune(digit))/16 {
					return n, p.fail(start, "invalid Unicode scalar")
				}
				value = value*16 + rune(digit)
				p.pos++
			}
			if p.pos == len(p.s) || p.pos == at {
				return n, p.fail(p.pos, "unclosed or empty scalar escape")
			}
			p.pos++
		} else {
			value, err = p.hex(4)
			if err != nil {
				return n, err
			}
			if value >= 0xd800 && value <= 0xdbff {
				if !strings.HasPrefix(p.s[p.pos:], "\\u") {
					return n, p.fail(start, "unpaired high surrogate")
				}
				p.pos += 2
				low, err := p.hex(4)
				if err != nil {
					return n, err
				}
				if low < 0xdc00 || low > 0xdfff {
					return n, p.fail(start, "unpaired high surrogate")
				}
				value = 0x10000 + (value-0xd800)*0x400 + (low - 0xdc00)
			}
		}
		if value > utf8.MaxRune || value >= 0xd800 && value <= 0xdfff {
			return n, p.fail(start, "invalid Unicode scalar")
		}
		n.lo = value
	default:
		allowed := strings.ContainsRune("^$\\.*+?()[]{}|/-", rune(c))
		if !inClass && p.flags&rxUnicodeSets != 0 && c == '-' {
			allowed = false
		}
		if inClass && p.flags&rxUnicodeSets != 0 {
			allowed = strings.ContainsRune("^$\\.*+?()[]{}|/-&!#%,:;<=>@`~", rune(c))
		}
		if !allowed && !legacy {
			return n, p.fail(start, "unsupported identity escape")
		}
		n.lo = rune(c)
		if legacy {
			var size int
			n.lo, size = utf8.DecodeRuneInString(p.s[p.pos-1:])
			p.pos += size - 1
		}
	}
	n.to = p.pos
	return n, nil
}
func (p *snippetRegexParser) resolve(n *snippetRegexNode, depth int) error {
	switch n.kind {
	case rxGroup, rxCapture, rxLook, rxRepeat, rxClass, rxSetUnion, rxSetIntersection, rxSetSubtraction:
		depth++
	}
	if depth > maxSnippetRegexDepth {
		return p.fail(n.from, "syntax depth exceeded")
	}
	if n.kind == rxBackref {
		if n.text != "" {
			group, ok := p.names[n.text]
			if !ok {
				return p.fail(n.from, "unknown capture name")
			}
			n.group = group
			if groups := p.aliases[n.text]; len(groups) > 1 {
				n.aliasGroups = append([]int(nil), groups...)
			}
		} else if n.group > p.captures {
			return p.fail(n.from, "unknown capture number")
		}
	}
	for _, child := range n.children {
		if err := p.resolve(child, depth); err != nil {
			return err
		}
	}
	return nil
}
