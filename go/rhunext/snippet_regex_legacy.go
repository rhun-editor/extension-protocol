package rhunext

import "strings"

func (p *snippetRegexParser) legacy() bool { return p.flags&(rxUnicode|rxUnicodeSets) == 0 }

// This lexical census is used only for Annex B escape ambiguity. Real parsing
// still validates every group and class. Saturation avoids numeric overflow.
func (p *snippetRegexParser) legacyCensus() {
	if p.legacyScanned {
		return
	}
	p.legacyScanned = true
	inClass := false
	for at := 0; at < len(p.s); at++ {
		switch p.s[at] {
		case '\\':
			at++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			if inClass {
				continue
			}
			named := strings.HasPrefix(p.s[at:], "(?<") && at+3 < len(p.s) && p.s[at+3] != '=' && p.s[at+3] != '!'
			if named || at+1 == len(p.s) || p.s[at+1] != '?' {
				p.legacyCaptures = min(p.legacyCaptures+1, maxSnippetRegexCaptures+1)
			}
			p.legacyNamed = p.legacyNamed || named
		}
	}
}

func (p *snippetRegexParser) bracedQuantifier(at int) bool {
	if at >= len(p.s) || p.s[at] != '{' {
		return false
	}
	at++
	start := at
	for at < len(p.s) && stDigit(p.s[at]) {
		at++
	}
	if at == start {
		return false
	}
	if at < len(p.s) && p.s[at] == ',' {
		at++
		for at < len(p.s) && stDigit(p.s[at]) {
			at++
		}
	}
	return at < len(p.s) && p.s[at] == '}'
}

func (p *snippetRegexParser) fixedHex(at, count int) bool {
	if count > len(p.s)-at {
		return false
	}
	for _, b := range []byte(p.s[at : at+count]) {
		if rxHex(b) < 0 {
			return false
		}
	}
	return true
}

func (p *snippetRegexParser) bracedHex(at int) bool {
	if at == len(p.s) || p.s[at] != '{' {
		return false
	}
	start := at + 1
	for at = start; at < len(p.s) && rxHex(p.s[at]) >= 0; at++ {
	}
	return at > start && at < len(p.s) && p.s[at] == '}'
}

func (p *snippetRegexParser) legacyProperty(at int) bool {
	if at == len(p.s) || p.s[at] != '{' {
		return false
	}
	start := at + 1
	for at = start; at < len(p.s) && p.s[at] != '}'; at++ {
		if at-start == maxSnippetRegexToken {
			return false
		}
	}
	if at == len(p.s) {
		return false
	}
	_, valid := snippetUnicodeProperty(p.s[start:at])
	return valid
}

// Called at the first digit, before any trailing quantifier has been parsed.
func (p *snippetRegexParser) legacyDecimal(start int, inClass bool) snippetRegexNode {
	n := snippetRegexNode{kind: rxLiteral, from: start}
	at := p.pos
	if !inClass && p.s[at] != '0' {
		p.legacyCensus()
		group, end := 0, at
		for end < len(p.s) && stDigit(p.s[end]) {
			group = min(maxSnippetRegexCaptures+1, group*10+int(p.s[end]-'0'))
			end++
		}
		if group <= p.legacyCaptures {
			n.kind, n.group, p.pos = rxBackref, group, end
			n.to = p.pos
			return n
		}
	}
	first := p.s[p.pos]
	if first > '7' {
		n.lo = rune(first)
		p.pos++
	} else {
		limit := 2
		if first <= '3' {
			limit = 3
		}
		for p.pos < len(p.s) && p.pos-at < limit && p.s[p.pos] >= '0' && p.s[p.pos] <= '7' {
			n.lo = n.lo*8 + rune(p.s[p.pos]-'0')
			p.pos++
		}
	}
	n.to = p.pos
	return n
}
