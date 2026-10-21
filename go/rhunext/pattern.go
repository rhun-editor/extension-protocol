package rhunext

// Problem patterns and matchers (protocol 1.1, "Terminals and tasks"): the reference rhun's own
// matcher follows step for step, so an extension can check a pattern before it publishes it and
// tests can see what a line captures. v1.1/patterns.txt holds the vectors every SDK and rhun pass.

import (
	"errors"
	"strings"
)

// The fields a pattern may hold, in the order captures are reported.
var PatternFields = []string{"file", "line", "col", "endline", "endcol", "severity", "code", "message"}

// BuiltinMatchers are the matchers a task names in `matcher NAME 0`, as their text.
var BuiltinMatchers = map[string]string{
	"go":      "columns bytes\n1 {file}:{line}:{col}: {message}\n1 vet: {file}:{line}:{col}: {message}\n1  {file}:{line}: {message}\n",
	"gcc":     "columns bytes\n1 {file}:{line}:{col}: {severity}: {message}\n1 {file}:{line}: {severity}: {message}\n",
	"rustc":   "1 {severity}[{code}]: {message}\n1 {severity}: {message}\n2  --> {file}:{line}:{col}\n",
	"tsc":     "1 {file}({line},{col}): {severity} {code}: {message}\n1 {file}:{line}:{col} - {severity} {code}: {message}\n",
	"msbuild": "1 {file}({line},{col}): {severity} {code}: {message}\n1 {file}({line},{col},{endline},{endcol}): {severity} {code}: {message}\n",
}

const (
	patMax  = 512
	patToks = 32
	stepMax = 10000
)

const (
	tokLit   = -1
	tokBlank = -2
	tokAny   = 8 // {_}: matched, not captured
)

type patToken struct {
	kind int // an index of PatternFields, tokAny, tokLit or tokBlank
	lit  string
}

// the severity words {severity} matches, in the order they are tried
var severityWords = []string{"fatal error", "error", "fatal", "warning", "warn", "info", "note", "hint", "help"}

func compilePattern(p string) ([]patToken, error) {
	if len(p) == 0 || len(p) > patMax {
		return nil, errors.New("a pattern is 1 to 512 bytes")
	}
	var toks []patToken
	seen := map[int]bool{}
	add := func(t patToken) error {
		if len(toks) >= patToks {
			return errors.New("more than 32 parts")
		}
		toks = append(toks, t)
		return nil
	}
	literal := func(c byte) error {
		if n := len(toks); n > 0 && toks[n-1].kind == tokLit {
			toks[n-1].lit += string(c)
			return nil
		}
		return add(patToken{kind: tokLit, lit: string(c)})
	}
	for i := 0; i < len(p); {
		c := p[i]
		switch {
		case c < 0x20 || c == 0x7f:
			return nil, errors.New("a pattern is one line of text")
		case c == ' ':
			for i < len(p) && p[i] == ' ' {
				i++
			}
			if err := add(patToken{kind: tokBlank}); err != nil {
				return nil, err
			}
		case c == '{':
			if i+1 >= len(p) {
				return nil, errors.New("{ without }")
			}
			if p[i+1] == '{' {
				if err := literal('{'); err != nil {
					return nil, err
				}
				i += 2
				continue
			}
			end := strings.IndexByte(p[i+1:], '}')
			if end < 0 {
				return nil, errors.New("{ without }")
			}
			name := p[i+1 : i+1+end]
			kind := -1
			if name == "_" {
				kind = tokAny
			}
			for k, f := range PatternFields {
				if f == name {
					kind = k
				}
			}
			if kind < 0 {
				return nil, errors.New("unknown field")
			}
			if kind != tokAny {
				if seen[kind] {
					return nil, errors.New("a field twice")
				}
				seen[kind] = true
			}
			if err := add(patToken{kind: kind}); err != nil {
				return nil, err
			}
			i += end + 2
		case c == '}':
			if i+1 >= len(p) || p[i+1] != '}' {
				return nil, errors.New("a } alone (write }})")
			}
			if err := literal('}'); err != nil {
				return nil, err
			}
			i += 2
		default:
			if err := literal(c); err != nil {
				return nil, err
			}
			i++
		}
	}
	return toks, nil
}

// CheckPattern says what is wrong with a pattern, as rhun would (nil when it is fine).
func CheckPattern(p string) error {
	_, err := compilePattern(p)
	return err
}

type patMatch struct {
	toks  []patToken
	line  string
	steps int
	caps  map[string]string
}

func (m *patMatch) from(tok, pos int) bool {
	m.steps++
	if m.steps > stepMax {
		return false
	}
	if tok == len(m.toks) {
		return pos == len(m.line)
	}
	t := m.toks[tok]
	switch t.kind {
	case tokLit:
		if !strings.HasPrefix(m.line[pos:], t.lit) {
			return false
		}
		return m.from(tok+1, pos+len(t.lit))
	case tokBlank:
		for q := pos; q < len(m.line) && (m.line[q] == ' ' || m.line[q] == '\t'); {
			q++
			if m.from(tok+1, q) {
				return true
			}
			if m.steps > stepMax {
				return false
			}
		}
		return false
	case 5: // severity
		for _, w := range severityWords {
			if pos+len(w) > len(m.line) || !strings.EqualFold(m.line[pos:pos+len(w)], w) {
				continue
			}
			m.caps["severity"] = m.line[pos : pos+len(w)]
			if m.from(tok+1, pos+len(w)) {
				return true
			}
		}
		return false
	case 1, 2, 3, 4: // numbers: 1 to 9 digits
		for n := 1; n <= 9 && pos+n <= len(m.line); n++ {
			if c := m.line[pos+n-1]; c < '0' || c > '9' {
				return false
			}
			m.caps[PatternFields[t.kind]] = m.line[pos : pos+n]
			if m.from(tok+1, pos+n) {
				return true
			}
			if m.steps > stepMax {
				return false
			}
		}
		return false
	}
	// {file}, {code}, {message}: 1 or more bytes; {_}: 0 or more; the fewest first
	n := 1
	if t.kind == tokAny {
		n = 0
	}
	if t.kind == 0 && (pos >= len(m.line) || m.line[pos] == ' ' || m.line[pos] == '\t') {
		return false
	}
	for ; pos+n <= len(m.line); n++ {
		if t.kind == 0 && n >= 3 && m.line[pos+n-3:pos+n] == "://" {
			return false
		}
		if t.kind != tokAny {
			m.caps[PatternFields[t.kind]] = m.line[pos : pos+n]
		}
		if m.from(tok+1, pos+n) {
			return true
		}
		if m.steps > stepMax {
			return false
		}
	}
	return false
}

// MatchPattern matches a whole line against a pattern as rhun does: the fields it captured, or
// false when the line does not match (or the pattern is not valid; see CheckPattern).
func MatchPattern(p, line string) (map[string]string, bool) {
	toks, err := compilePattern(p)
	if err != nil {
		return nil, false
	}
	m := &patMatch{toks: toks, line: line, caps: map[string]string{}}
	if !m.from(0, 0) {
		return nil, false
	}
	// a capture left over from a branch that failed is not the line's
	out := map[string]string{}
	for _, t := range toks {
		if t.kind >= 0 && t.kind < tokAny {
			out[PatternFields[t.kind]] = m.caps[PatternFields[t.kind]]
		}
	}
	return out, true
}

// CheckMatcher says what is wrong with an inline matcher's text, as rhun would.
func CheckMatcher(text string) error {
	if len(text) > 4096 {
		return errors.New("a matcher is at most 4096 bytes")
	}
	steps := 0
	var alts [4]int
	for _, raw := range strings.Split(text, "\n") {
		line := strings.Trim(raw, " \t\r")
		if line == "" {
			continue
		}
		word, rest, has := line, "", false
		if i := strings.IndexAny(line, " \t"); i >= 0 {
			word, rest, has = line[:i], line[i+1:], true
		}
		bad := errors.New("a matcher line is severity S, columns chars|bytes, N PATTERN or loop")
		switch {
		case word == "loop":
			if has {
				return bad
			}
		case !has:
			return bad
		case word == "severity":
			if rest != "error" && rest != "warning" && rest != "info" && rest != "hint" {
				return bad
			}
		case word == "columns":
			if rest != "bytes" && rest != "chars" {
				return bad
			}
		case len(word) == 1 && word[0] >= '1' && word[0] <= '4':
			n := int(word[0] - '0')
			if n != steps && n != steps+1 {
				return errors.New("steps go 1, 2, 3, 4 in order")
			}
			steps = n
			if alts[n-1] >= 8 {
				return errors.New("more than 8 patterns in a step")
			}
			alts[n-1]++
			if err := CheckPattern(rest); err != nil {
				return err
			}
		default:
			return bad
		}
	}
	if steps == 0 {
		return errors.New("a matcher needs a step 1 pattern")
	}
	return nil
}
