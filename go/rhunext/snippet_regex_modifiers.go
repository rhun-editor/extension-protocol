package rhunext

func (p *snippetRegexParser) modifiers() (uint16, uint16, error) {
	var enable, disable uint16
	removing := false
	for p.pos < len(p.s) && p.s[p.pos] != ':' {
		ch := p.s[p.pos]
		if ch == '-' {
			if removing {
				return 0, 0, p.fail(p.pos, "duplicate modifier separator")
			}
			removing = true
			p.pos++
			continue
		}
		var bit uint16
		switch ch {
		case 'i':
			bit = rxIgnoreCase
		case 'm':
			bit = rxMultiline
		case 's':
			bit = rxDotAll
		default:
			return 0, 0, p.fail(p.pos, "unsupported local modifier")
		}
		if (enable|disable)&bit != 0 {
			return 0, 0, p.fail(p.pos, "duplicate local modifier")
		}
		if removing {
			disable |= bit
		} else {
			enable |= bit
		}
		p.pos++
	}
	if p.pos == len(p.s) {
		return 0, 0, p.fail(p.pos, "unclosed modifier prefix")
	}
	if enable|disable == 0 {
		return 0, 0, p.fail(p.pos, "empty local modifiers")
	}
	p.pos++
	return enable, disable, nil
}

func (c *snippetRegexCompiler) compileGroup(n *snippetRegexNode, backward bool) {
	flags := c.p.flags
	c.p.flags = (flags | uint32(n.enable)) &^ uint32(n.disable)
	defer func() { c.p.flags = flags }()
	c.compile(n.children[0], backward)
}

func (c *snippetRegexCompiler) compileReference(n *snippetRegexNode, group int) {
	if (c.p.flags^c.globalFlags)&uint32(rxIgnoreCase) == 0 {
		c.emit(n, rxReference, uint32(group), 0, 0)
	} else {
		c.emit(n, rxScopedReference, uint32(group), c.p.flags&uint32(rxIgnoreCase), 0)
	}
}

func (m *snippetRegexMatcher) scopedAssertion(kind, flags uint32) bool {
	previous := m.flags
	m.flags = previous&^uint32(rxIgnoreCase|rxMultiline) | flags
	defer func() { m.flags = previous }()
	return m.assertion(kind)
}

func (m *snippetRegexMatcher) scopedReference(group, flags uint32) bool {
	previous := m.flags
	m.flags = previous&^uint32(rxIgnoreCase) | flags
	defer func() { m.flags = previous }()
	return m.reference(group)
}
