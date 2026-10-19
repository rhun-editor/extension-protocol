package rhunext

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type snippetTransformIndentError struct {
	Offset  int
	Message string
}

func (e *snippetTransformIndentError) Error() string {
	return fmt.Sprintf("snippet transform indentation byte %d: %s", e.Offset, e.Message)
}

type snippetTransformIndenter struct {
	budget *snippetRegexMatchBudget
	out    strings.Builder
	err    error
}

func (x *snippetTransformIndenter) fail(message string) {
	if x.err == nil {
		x.err = &snippetTransformIndentError{-1, message}
	}
}
func (x *snippetTransformIndenter) spend(n int) bool {
	if x.err != nil {
		return false
	}
	if n < 0 || n > x.budget.remaining {
		x.fail("indentation work limit exceeded")
		return false
	}
	x.budget.remaining -= n
	return true
}
func (x *snippetTransformIndenter) append(s string) {
	if x.err != nil {
		return
	}
	if len(s) > MaxSnippetBody-x.out.Len() {
		x.fail("indentation output byte limit exceeded")
		return
	}
	if x.spend(len(s)) {
		x.out.WriteString(s)
	}
}
func (x *snippetTransformIndenter) fill(b byte, n int) {
	if x.err != nil {
		return
	}
	if n > MaxSnippetBody-x.out.Len() {
		x.fail("indentation output byte limit exceeded")
		return
	}
	if x.spend(n) {
		x.out.WriteString(strings.Repeat(string(b), n))
	}
}
func snippetIndentColumns(s string, start, width int) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\t' {
			start += width - start%width
		} else {
			start++
		}
	}
	return start
}

// Prefix is the insertion-line indentation captured before the snippet was
// inserted. Width and spaces are the current editor settings when leaving a field.
func normalizeSnippetTransform(input, prefix string, width int, spaces bool, budget *snippetRegexMatchBudget) (string, error) {
	if budget == nil {
		budget = &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
	}
	x := snippetTransformIndenter{budget: budget}
	if budget.remaining < 0 || budget.remaining > maxSnippetRegexMatchWork {
		x.fail("invalid indentation work budget")
	} else if width < 1 || width > 16 {
		x.fail("invalid indentation width")
	} else if len(input) > MaxSnippetBody || len(prefix) > MaxSnippetBody {
		x.fail("indentation input byte limit exceeded")
	}
	if x.err != nil {
		return "", x.err
	}
	if !x.spend(3*len(input) + len(prefix) + 1) {
		return "", x.err
	}
	if !utf8.ValidString(input) {
		for i := 0; i < len(input); {
			r, n := utf8.DecodeRuneInString(input[i:])
			if r == utf8.RuneError && n == 1 {
				return "", &snippetTransformIndentError{i, "input is not UTF-8"}
			}
			i += n
		}
	}
	for i := 0; i < len(prefix); i++ {
		if prefix[i] != ' ' && prefix[i] != '\t' {
			x.fail("invalid indentation prefix")
			return "", x.err
		}
	}
	first := true
	for start := 0; ; {
		end := len(input)
		if offset := strings.IndexAny(input[start:], "\r\n"); offset >= 0 {
			end = start + offset
		}
		leading := start
		if !first {
			if !x.spend(len(prefix)) {
				break
			}
			columns := snippetIndentColumns(prefix, 0, width)
			for leading < end && (input[leading] == ' ' || input[leading] == '\t') {
				if input[leading] == '\t' {
					columns += width - columns%width
				} else {
					columns++
				}
				leading++
			}
			if !spaces {
				x.fill('\t', columns/width)
				columns %= width
			}
			x.fill(' ', columns)
		}
		x.append(input[leading:end])
		if x.err != nil {
			break
		}
		if end == len(input) {
			return x.out.String(), nil
		}
		x.append("\n")
		if x.err != nil {
			break
		}
		start = end + 1
		if input[end] == '\r' && start < len(input) && input[start] == '\n' {
			start++
		}
		first = false
	}
	return "", x.err
}
