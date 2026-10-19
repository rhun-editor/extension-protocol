package rhunext

// Helpers for test adapters: the expected and actual values in a failure's text, as Go tests
// (got:/want:, "= GOT, want WANT"), testify (expected:/actual  :) and Rust's assert_eq!
// (left:/right:) print them.

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reGotWant  = regexp.MustCompile(`(?m)^\s*got:\s?(.*)$`)
	reWant     = regexp.MustCompile(`(?m)^\s*want:\s?(.*)$`)
	reInline   = regexp.MustCompile(`= (.+), want (.+)$`)
	reExpected = regexp.MustCompile(`(?m)^\s*expected\s*:\s?(.*)$`)
	reActual   = regexp.MustCompile(`(?m)^\s*actual\s*:\s?(.*)$`)
	reLeft     = regexp.MustCompile(`(?m)^\s*left:\s?(.*)$`)
	reRight    = regexp.MustCompile(`(?m)^\s*right:\s?(.*)$`)
)

// ExpectedActual finds the values a failure compared: Go's "got: X" / "want: Y" lines or
// "f(x) = GOT, want WANT", testify's "expected: X" / "actual  : Y" blocks, and Rust's "left: X" /
// "right: Y" (assert_eq!(actual, expected) by convention: left is actual). Quoted values are
// unquoted (Go strings, Rust Debug strings).
func ExpectedActual(text string) (expected, actual string, ok bool) {
	if g, w := reGotWant.FindStringSubmatch(text), reWant.FindStringSubmatch(text); g != nil && w != nil {
		return Unquote(w[1]), Unquote(g[1]), true
	}
	if e, a := reExpected.FindStringSubmatch(text), reActual.FindStringSubmatch(text); e != nil && a != nil {
		return Unquote(e[1]), Unquote(a[1]), true
	}
	if l, r := reLeft.FindStringSubmatch(text), reRight.FindStringSubmatch(text); l != nil && r != nil {
		return Unquote(r[1]), Unquote(l[1]), true
	}
	for _, line := range strings.Split(text, "\n") {
		if m := reInline.FindStringSubmatch(line); m != nil {
			return Unquote(m[2]), Unquote(m[1]), true
		}
	}
	return "", "", false
}

// Unquote returns a Go or Rust Debug string literal's value ("a\nb" with its escapes), a Go raw
// string's, or the text as it is when it is not one.
func Unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '`' && s[len(s)-1] == '`') {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		if s[0] == '"' {
			return rustUnescape(s[1 : len(s)-1])
		}
	}
	return s
}

// rustUnescape reads Rust's Debug escapes: \n \t \r \\ \" \' \0 and \u{XXXX}
func rustUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case 'u':
			if i+1 < len(s) && s[i+1] == '{' {
				end := strings.IndexByte(s[i:], '}')
				if end > 0 {
					if n, err := strconv.ParseUint(s[i+2:i+end], 16, 32); err == nil {
						b.WriteRune(rune(n))
						i += end
						continue
					}
				}
			}
			b.WriteString(`\u`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
