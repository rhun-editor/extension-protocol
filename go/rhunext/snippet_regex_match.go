package rhunext

import (
	"fmt"
	"unicode/utf8"
)

const (
	maxSnippetRegexMatchWork = 1 << 24
	maxSnippetRegexBranches  = 4096
	maxSnippetRegexCalls     = 32
	maxSnippetRegexTrail     = 65536
)

type snippetRegexMatchBudget struct{ remaining int }
type snippetRegexMatchError struct {
	PC      int
	Message string
}

func (e *snippetRegexMatchError) Error() string {
	return fmt.Sprintf("regex match pc %d: %s", e.PC, e.Message)
}

type snippetRegexCapture struct {
	start, end int
	present    bool
}
type snippetRegexMatch struct {
	start, end int
	captures   []snippetRegexCapture
}
type snippetRegexTrailEntry struct{ key, old int }
type snippetRegexBranch struct{ pc, pos, trail int }
type snippetRegexCall struct {
	pc, pos, trail, branches int
	backward, negative       bool
}
type snippetRegexMatcher struct {
	p        *snippetRegexProgram
	input    string
	budget   *snippetRegexMatchBudget
	values   []int
	trail    []snippetRegexTrailEntry
	branches []snippetRegexBranch
	calls    []snippetRegexCall
	pc, pos  int
	backward bool
	flags    uint32
	err      error
}

func newSnippetRegexMatcher(p *snippetRegexProgram, input string, budget *snippetRegexMatchBudget) (*snippetRegexMatcher, error) {
	if budget == nil {
		budget = &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
	}
	m := &snippetRegexMatcher{p: p, input: input, budget: budget, pc: -1}
	if budget.remaining < 0 {
		m.fail("negative work budget")
		return nil, m.err
	}
	if p == nil {
		m.fail("nil program")
		return nil, m.err
	}
	m.flags = p.flags
	size, e := rxProgramSize(uint64(len(p.code)), uint64(len(p.classes)), uint64(len(p.ranges)), uint64(len(p.repeats)))
	if e != nil {
		return nil, e
	}
	if len(input) > MaxSnippetBody {
		m.fail("input byte limit exceeded")
		return nil, m.err
	}
	if !m.spend(size + len(input) + 1) {
		return nil, m.err
	}
	if e := p.validate(); e != nil {
		return nil, e
	}
	if !utf8.ValidString(input) {
		m.fail("input is not UTF-8")
		return nil, m.err
	}
	count := 2*(int(p.captures)+1) + 2*len(p.repeats)
	if !m.spend(count) {
		return nil, m.err
	}
	m.values = make([]int, count)
	for i := range m.values {
		m.values[i] = -1
	}
	return m, nil
}
func (m *snippetRegexMatcher) fail(message string) {
	if m.err == nil {
		m.err = &snippetRegexMatchError{m.pc, message}
	}
}
func (m *snippetRegexMatcher) spend(amount int) bool {
	if m.err != nil {
		return false
	}
	if amount < 0 || amount > m.budget.remaining {
		m.fail("match work limit exceeded")
		return false
	}
	m.budget.remaining -= amount
	return true
}
func (m *snippetRegexMatcher) boundary(pos int) bool {
	return pos >= 0 && pos <= len(m.input) && (pos == len(m.input) || utf8.RuneStart(m.input[pos]))
}
func (m *snippetRegexMatcher) write(key, value int) {
	if m.err != nil || m.values[key] == value {
		return
	}
	if !m.spend(1) {
		return
	}
	if len(m.trail) == maxSnippetRegexTrail {
		m.fail("trail limit exceeded")
		return
	}
	m.trail = append(m.trail, snippetRegexTrailEntry{key, m.values[key]})
	m.values[key] = value
}
func (m *snippetRegexMatcher) rewind(top int) {
	for len(m.trail) > top && m.err == nil {
		if !m.spend(1) {
			return
		}
		last := m.trail[len(m.trail)-1]
		m.trail = m.trail[:len(m.trail)-1]
		m.values[last.key] = last.old
	}
}
func (m *snippetRegexMatcher) pushBranch(pc int) {
	if !m.spend(1) {
		return
	}
	if len(m.branches) == maxSnippetRegexBranches {
		m.fail("branch limit exceeded")
		return
	}
	m.branches = append(m.branches, snippetRegexBranch{pc, m.pos, len(m.trail)})
}

// Failure exhausts the current assertion's alternatives before unwinding its call.
func (m *snippetRegexMatcher) backtrack() bool {
	for m.err == nil {
		base := 0
		if len(m.calls) > 0 {
			base = m.calls[len(m.calls)-1].branches
		}
		if len(m.branches) > base {
			if !m.spend(1) {
				return false
			}
			b := m.branches[len(m.branches)-1]
			m.branches = m.branches[:len(m.branches)-1]
			m.rewind(b.trail)
			m.pc = b.pc
			m.pos = b.pos
			return m.err == nil
		}
		if len(m.calls) == 0 {
			return false
		}
		if !m.spend(1) {
			return false
		}
		call := m.calls[len(m.calls)-1]
		m.calls = m.calls[:len(m.calls)-1]
		m.rewind(call.trail)
		m.pos = call.pos
		m.backward = call.backward
		m.pc = call.pc
		if call.negative {
			return m.err == nil
		}
	}
	return false
}
func (m *snippetRegexMatcher) scalar(pos int, backward bool) (rune, int, bool) {
	if backward {
		if pos == 0 {
			return 0, pos, false
		}
		if !m.spend(4) {
			return 0, pos, false
		}
		r, size := utf8.DecodeLastRuneInString(m.input[:pos])
		return r, pos - size, true
	}
	if pos == len(m.input) {
		return 0, pos, false
	}
	if !m.spend(4) {
		return 0, pos, false
	}
	r, size := utf8.DecodeRuneInString(m.input[pos:])
	return r, pos + size, true
}
func (m *snippetRegexMatcher) canonical(cp rune) rune {
	if m.flags&uint32(rxIgnoreCase) != 0 {
		if !m.spend(16) {
			return cp
		}
		return snippetRegexCanonical(cp, m.flags)
	}
	return cp
}
func rxLineTerminator(cp rune) bool { return cp == '\n' || cp == '\r' || cp == 0x2028 || cp == 0x2029 }
func (m *snippetRegexMatcher) word(pos int, backward bool) bool {
	cp, _, ok := m.scalar(pos, backward)
	if !ok {
		return false
	}
	cp = m.canonical(cp)
	return cp >= 'a' && cp <= 'z' || cp >= 'A' && cp <= 'Z' || cp >= '0' && cp <= '9' || cp == '_'
}
func (m *snippetRegexMatcher) assertion(kind uint32) bool {
	switch kind {
	case 1:
		if m.pos == 0 {
			return true
		}
		if m.flags&uint32(rxMultiline) != 0 {
			cp, _, ok := m.scalar(m.pos, true)
			return ok && rxLineTerminator(cp)
		}
	case 2:
		if m.pos == len(m.input) {
			return true
		}
		if m.flags&uint32(rxMultiline) != 0 {
			cp, _, ok := m.scalar(m.pos, false)
			return ok && rxLineTerminator(cp)
		}
	case 3, 4:
		a := m.word(m.pos, true)
		b := m.word(m.pos, false)
		return (a != b) == (kind == 3)
	}
	return false
}
func (m *snippetRegexMatcher) class(id uint32, cp rune) bool {
	d := m.p.classes[id]
	a := m.p.ranges[d.start : d.start+d.count]
	lo, hi := 0, len(a)
	for lo < hi {
		if !m.spend(1) {
			return false
		}
		mid := lo + (hi-lo)/2
		if a[mid].hi < cp {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo < len(a) && a[lo].lo <= cp
}
func (m *snippetRegexMatcher) capture(group uint32) (int, int, bool) {
	start, end := m.values[2*group], m.values[2*group+1]
	if start == -1 || end == -1 {
		return 0, 0, false
	}
	if !m.boundary(start) || !m.boundary(end) || start > end {
		m.fail("invalid capture range")
		return 0, 0, false
	}
	return start, end, true
}
func (m *snippetRegexMatcher) reference(group uint32) bool {
	start, end, defined := m.capture(group)
	if !defined {
		return m.err == nil
	}
	at, pos := start, m.pos
	if m.backward {
		at = end
	}
	for at != end && !m.backward || at != start && m.backward {
		if !m.spend(1) {
			return false
		}
		cp, next, ok := m.scalar(at, m.backward)
		if !ok {
			m.fail("invalid captured scalar")
			return false
		}
		other, after, ok := m.scalar(pos, m.backward)
		if !ok {
			return false
		}
		if m.canonical(cp) != m.canonical(other) {
			return false
		}
		if m.err != nil {
			return false
		}
		at, pos = next, after
	}
	m.pos = pos
	return true
}
func (m *snippetRegexMatcher) repeatState(id uint32) (int, int, int) {
	key := 2*(int(m.p.captures)+1) + 2*int(id)
	count, previous := m.values[key], m.values[key+1]
	if count < 0 || count > 0x7fffffff || !m.boundary(previous) {
		m.fail("uninitialized or invalid repeat state")
	}
	return key, count, previous
}
func (m *snippetRegexMatcher) result(start int) *snippetRegexMatch {
	if !m.spend(int(m.p.captures) + 1) {
		return nil
	}
	captures := make([]snippetRegexCapture, int(m.p.captures)+1)
	captures[0] = snippetRegexCapture{start, m.pos, true}
	for group := 1; group < len(captures); group++ {
		a, b := m.values[2*group], m.values[2*group+1]
		if a == -1 && b == -1 {
			continue
		}
		if a == -1 || b == -1 || !m.boundary(a) || !m.boundary(b) || a > b {
			m.fail("incomplete or invalid capture")
			return nil
		}
		captures[group] = snippetRegexCapture{a, b, true}
	}
	return &snippetRegexMatch{start, m.pos, captures}
}
func (m *snippetRegexMatcher) find(start int) (*snippetRegexMatch, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.pc = -1
	if !m.boundary(start) {
		m.fail("start is not a scalar boundary")
		return nil, m.err
	}
	m.rewind(0)
	m.branches = m.branches[:0]
	m.calls = m.calls[:0]
	for candidate := start; m.err == nil; {
		m.pc = int(m.p.entry)
		m.pos = candidate
		m.backward = false
		for m.err == nil {
			if !m.spend(1) {
				break
			}
			in := m.p.code[m.pc]
			matched := true
			switch in.op {
			case rxMatch:
				if len(m.calls) == 0 {
					result := m.result(candidate)
					return result, m.err
				}
				if !m.spend(1) {
					break
				}
				call := m.calls[len(m.calls)-1]
				m.calls = m.calls[:len(m.calls)-1]
				// Successful assertions are atomic. Keep positive captures; restore negative state.
				m.branches = m.branches[:call.branches]
				m.pos = call.pos
				m.backward = call.backward
				m.pc = call.pc
				if call.negative {
					m.rewind(call.trail)
					matched = false
				}
			case rxChar, rxDot, rxSet:
				cp, next, ok := m.scalar(m.pos, m.backward)
				matched = ok
				if ok {
					switch in.op {
					case rxChar:
						matched = m.canonical(cp) == rune(in.x)
					case rxDot:
						matched = m.p.flags&uint32(rxDotAll) != 0 || !rxLineTerminator(cp)
					case rxSet:
						matched = m.class(in.x, cp)
					}
				}
				if matched {
					m.pos = next
					m.pc++
				}
			case rxAssertion, rxScopedAssertion:
				if in.op == rxAssertion {
					matched = m.assertion(in.x)
				} else {
					matched = m.scopedAssertion(in.x, in.y)
				}
				if matched {
					m.pc++
				}
			case rxSave:
				m.write(int(in.x), m.pos)
				m.pc++
			case rxClear:
				for i := int(in.x); i < int(in.x+in.y) && m.err == nil; i++ {
					if !m.spend(1) {
						break
					}
					m.write(i, -1)
				}
				m.pc++
			case rxSplit:
				m.pushBranch(int(in.y))
				m.pc = int(in.x)
			case rxJump:
				m.pc = int(in.x)
			case rxReference, rxScopedReference:
				if in.op == rxReference {
					matched = m.reference(in.x)
				} else {
					matched = m.scopedReference(in.x, in.y)
				}
				if matched {
					m.pc++
				}
			case rxRepeatStart:
				key := 2*(int(m.p.captures)+1) + 2*int(in.x)
				m.write(key, 0)
				m.write(key+1, m.pos)
				m.pc++
			case rxRepeatTest:
				key, count, _ := m.repeatState(in.x)
				r := m.p.repeats[in.x]
				if uint32(count) >= r.max {
					m.pc = int(in.z)
				} else {
					m.write(key+1, m.pos)
					if uint32(count) < r.min {
						m.pc = int(in.y)
					} else if r.flags&1 == 0 {
						m.pushBranch(int(in.z))
						m.pc = int(in.y)
					} else {
						m.pushBranch(int(in.y))
						m.pc = int(in.z)
					}
				}
			case rxRepeatNext:
				key, count, previous := m.repeatState(in.x)
				r := m.p.repeats[in.x]
				if m.pos == previous && uint32(count) >= r.min {
					matched = false
				} else if count == 0x7fffffff {
					m.fail("repeat count overflow")
				} else {
					m.write(key, count+1)
					m.pc = int(in.y)
				}
			case rxLookCall:
				if !m.spend(1) {
					break
				}
				if len(m.calls) == maxSnippetRegexCalls {
					m.fail("look call limit exceeded")
					break
				}
				m.calls = append(m.calls, snippetRegexCall{int(in.y), m.pos, len(m.trail), len(m.branches), m.backward, in.z&1 != 0})
				m.backward = in.z&2 != 0
				m.pc = int(in.x)
			}
			if !matched && !m.backtrack() {
				break
			}
		}
		if m.err != nil {
			break
		}
		m.rewind(0)
		m.branches = m.branches[:0]
		m.calls = m.calls[:0]
		if m.p.flags&uint32(rxSticky) != 0 || candidate == len(m.input) {
			return nil, m.err
		}
		if !m.spend(1) {
			break
		}
		_, next, ok := m.scalar(candidate, false)
		if !ok {
			break
		}
		candidate = next
	}
	return nil, m.err
}
