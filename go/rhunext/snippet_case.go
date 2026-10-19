package rhunext

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Only malformed input has an original-input byte offset. Processing errors
// can arise in an intermediate string, so they deliberately have no offset.
type snippetCaseError struct {
	Offset  int
	Message string
}

func (e *snippetCaseError) Error() string {
	return fmt.Sprintf("snippet case byte %d: %s", e.Offset, e.Message)
}

type snippetCaseConverter struct {
	budget *snippetRegexMatchBudget
	err    error
}

func (c *snippetCaseConverter) fail(s string) {
	if c.err == nil {
		c.err = &snippetCaseError{-1, s}
	}
}
func (c *snippetCaseConverter) spend(n int) bool {
	if c.err != nil {
		return false
	}
	if n < 0 || n > c.budget.remaining {
		c.fail("case work limit exceeded")
		return false
	}
	c.budget.remaining -= n
	return true
}
func (c *snippetCaseConverter) decode(s string, i int) (rune, int) {
	if !c.spend(4) {
		return 0, 1
	}
	return utf8.DecodeRuneInString(s[i:])
}
func (c *snippetCaseConverter) property(r rune, key string) bool {
	if !c.spend(16) {
		return false
	}
	return snippetUnicodeContains(snippetUnicodeProperties[key], r)
}
func (c *snippetCaseConverter) space(r rune) bool {
	// ECMA whitespace includes BOM and excludes Unicode NEL.
	return c.property(r, "bin:White_Space") && r != 0x85 || r == 0xfeff
}
func snippetCaseDigit(r rune) bool                    { return r >= '0' && r <= '9' }
func (c *snippetCaseConverter) word(r rune) bool      { return c.property(r, "gc:L") || snippetCaseDigit(r) }
func (c *snippetCaseConverter) separator(r rune) bool { return c.space(r) || r == '_' || r == '-' }
func (c *snippetCaseConverter) append(b *strings.Builder, s string, cap int) {
	if c.err != nil {
		return
	}
	if len(s) > cap-b.Len() {
		c.fail("case output byte limit exceeded")
		return
	}
	if c.spend(len(s)) {
		b.WriteString(s)
	}
}
func (c *snippetCaseConverter) mapped(b *strings.Builder, r rune, original string, upper bool) {
	if !c.spend(16) {
		return
	}
	m := snippetUnicodeCase(r, upper)
	if m == "" {
		m = original
	}
	c.append(b, m, MaxSnippetBody)
}
func (c *snippetCaseConverter) lower(b *strings.Builder, s string) {
	prior := false
	for i := 0; i < len(s) && c.err == nil; {
		r, n := c.decode(s, i)
		// Case_Ignorable takes precedence over Cased, including their overlap.
		ignorable := c.property(r, "bin:Case_Ignorable")
		if r == 0x3a3 && prior {
			following := false
			for j := i + n; j < len(s) && c.err == nil; {
				q, k := c.decode(s, j)
				if !c.property(q, "bin:Case_Ignorable") {
					following = c.property(q, "bin:Cased")
					break
				}
				j += k
			}
			if !following {
				if c.spend(16) {
					c.append(b, "ς", MaxSnippetBody)
				}
			} else {
				c.mapped(b, r, s[i:i+n], false)
			}
		} else {
			c.mapped(b, r, s[i:i+n], false)
		}
		if !ignorable {
			prior = c.property(r, "bin:Cased")
		}
		i += n
	}
}
func (c *snippetCaseConverter) words(b *strings.Builder, s string, camel bool) {
	start := -1
	count := 0
	emit := func(end int) {
		r, n := c.decode(s, start)
		c.mapped(b, r, s[start:start+n], !camel || count != 0)
		c.append(b, s[start+n:end], MaxSnippetBody)
		count++
		start = -1
	}
	for i := 0; i < len(s) && c.err == nil; {
		r, n := c.decode(s, i)
		if c.word(r) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			emit(i)
		}
		i += n
	}
	if start >= 0 && c.err == nil {
		emit(len(s))
	}
	if count == 0 {
		c.append(b, s, MaxSnippetBody)
	}
}
func (c *snippetCaseConverter) snake(b *strings.Builder, s string) {
	var prep strings.Builder
	priorLower := false
	inSeparator := false
	for i := 0; i < len(s) && c.err == nil; {
		r, n := c.decode(s, i)
		sep := c.space(r) || r == '-'
		if sep {
			if !inSeparator {
				c.append(&prep, "_", 2*MaxSnippetBody)
			}
			priorLower = false
		} else {
			upper := c.property(r, "gc:Lu")
			if priorLower && upper {
				c.append(&prep, "_", 2*MaxSnippetBody)
			}
			c.append(&prep, s[i:i+n], 2*MaxSnippetBody)
			priorLower = c.property(r, "gc:Ll")
		}
		inSeparator = sep
		i += n
	}
	if c.err == nil {
		c.lower(b, prep.String())
	}
}

// Kebab follows the ordered word alternatives used by snippet formats. Runs
// of uppercase letters are consumed once, even if the run cannot form a word.
func (c *snippetCaseConverter) kebab(b *strings.Builder, s string) {
	hasWord := false
	for i := 0; i < len(s) && c.err == nil; {
		r, n := c.decode(s, i)
		if c.word(r) {
			hasWord = true
			break
		}
		i += n
	}
	if !hasWord {
		c.append(b, s, MaxSnippetBody)
		return
	}
	lo, hi := 0, len(s)
	for lo < hi && c.err == nil {
		r, n := c.decode(s, lo)
		if !c.space(r) {
			break
		}
		lo += n
	}
	for lo < hi && c.err == nil {
		if !c.spend(4) {
			break
		}
		r, n := utf8.DecodeLastRuneInString(s[lo:hi])
		if !c.space(r) {
			break
		}
		hi -= n
	}
	for lo < hi && s[lo] == '_' {
		if !c.spend(1) {
			break
		}
		lo++
	}
	for lo < hi && s[hi-1] == '_' {
		if !c.spend(1) {
			break
		}
		hi--
	}
	s = s[lo:hi]
	count := 0
	emit := func(a, z int) {
		if count != 0 {
			c.append(b, "-", MaxSnippetBody)
		}
		c.lower(b, s[a:z])
		count++
	}
	consume := func(i int, property string) int {
		for i < len(s) && c.err == nil {
			r, n := c.decode(s, i)
			ok := snippetCaseDigit(r)
			if property != "" {
				ok = c.property(r, property)
			}
			if !ok {
				break
			}
			i += n
		}
		return i
	}
	for i := 0; i < len(s) && c.err == nil; {
		r, n := c.decode(s, i)
		if c.property(r, "gc:Lu") {
			end := i + n
			last := i
			run := 1
			for end < len(s) && c.err == nil {
				q, k := c.decode(s, end)
				if !c.property(q, "gc:Lu") {
					break
				}
				last = end
				end += k
				run++
			}
			var q rune
			if end < len(s) {
				q, _ = c.decode(s, end)
			}
			if end < len(s) && c.property(q, "gc:Ll") {
				if run >= 2 {
					emit(i, last)
				}
				z := consume(end, "gc:Ll")
				z = consume(z, "")
				emit(last, z)
				i = z
			} else if end == len(s) || c.separator(q) {
				emit(i, end)
				i = end
			} else {
				i = end
			}
		} else if c.property(r, "gc:Ll") {
			z := consume(i+n, "gc:Ll")
			z = consume(z, "")
			emit(i, z)
			i = z
		} else if snippetCaseDigit(r) {
			z := consume(i+n, "")
			emit(i, z)
			i = z
		} else {
			i += n
		}
	}
	if count == 0 && c.err == nil {
		start := -1
		for i := 0; i < len(s) && c.err == nil; {
			r, n := c.decode(s, i)
			if c.separator(r) {
				if start >= 0 {
					emit(start, i)
					start = -1
				}
			} else if start < 0 {
				start = i
			}
			i += n
		}
		if start >= 0 {
			emit(start, len(s))
		}
	}
}
func snippetCaseConvert(input string, op uint32, budget *snippetRegexMatchBudget) (string, error) {
	if budget == nil {
		budget = &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
	}
	c := snippetCaseConverter{budget: budget}
	if op < replacementUpper || op > replacementKebab {
		c.fail("invalid case operation")
	} else if budget.remaining < 0 || budget.remaining > maxSnippetRegexMatchWork {
		c.fail("invalid case work budget")
	} else if len(input) > MaxSnippetBody {
		c.fail("input byte limit exceeded")
	}
	if c.err != nil {
		return "", c.err
	}
	if !c.spend(2*len(input) + 1) {
		return "", c.err
	}
	if !utf8.ValidString(input) {
		for i := 0; i < len(input); {
			r, n := utf8.DecodeRuneInString(input[i:])
			if r == utf8.RuneError && n == 1 {
				return "", &snippetCaseError{i, "input is not UTF-8"}
			}
			i += n
		}
	}
	var b strings.Builder
	switch op {
	case replacementUpper:
		for i := 0; i < len(input) && c.err == nil; {
			r, n := c.decode(input, i)
			c.mapped(&b, r, input[i:i+n], true)
			i += n
		}
	case replacementLower:
		c.lower(&b, input)
	case replacementCapitalize:
		if input != "" {
			r, n := c.decode(input, 0)
			c.mapped(&b, r, input[:n], true)
			c.append(&b, input[n:], MaxSnippetBody)
		}
	case replacementCamel, replacementPascal:
		c.words(&b, input, op == replacementCamel)
	case replacementSnake:
		c.snake(&b, input)
	case replacementKebab:
		c.kebab(&b, input)
	}
	if c.err != nil {
		return "", c.err
	}
	return b.String(), nil
}
