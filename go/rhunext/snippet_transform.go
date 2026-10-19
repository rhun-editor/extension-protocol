package rhunext

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type snippetTransformError struct {
	Stage  string
	Record int
	Cause  error
}

func (e *snippetTransformError) Error() string {
	return fmt.Sprintf("snippet transform %s record %d: %v", e.Stage, e.Record, e.Cause)
}
func (e *snippetTransformError) Unwrap() error { return e.Cause }

type snippetTransformExecutor struct {
	budget *snippetRegexMatchBudget
	format *snippetReplacementProgram
	input  string
	out    strings.Builder
	err    error
}

func (x *snippetTransformExecutor) fail(stage string, record int, cause error) {
	if x.err == nil {
		x.err = &snippetTransformError{stage, record, cause}
	}
}
func (x *snippetTransformExecutor) spend(n int, stage string, record int) bool {
	if x.err != nil {
		return false
	}
	if n < 0 || n > x.budget.remaining {
		x.fail(stage, record, errors.New("transform work limit exceeded"))
		return false
	}
	x.budget.remaining -= n
	return true
}
func (x *snippetTransformExecutor) append(s string, record int) {
	if x.err != nil {
		return
	}
	if len(s) > MaxSnippetBody-x.out.Len() {
		x.fail("output", record, errors.New("transform output byte limit exceeded"))
		return
	}
	if x.spend(len(s), "output", record) {
		x.out.WriteString(s)
	}
}
func (x *snippetTransformExecutor) validateFormat() bool {
	p := x.format
	if len(p.code) > maxSnippetReplacementRecords {
		x.fail("format", -1, replacementError(12, "record limit exceeded"))
		return false
	}
	base := replacementHeader + len(p.code)*replacementRecord
	if !x.spend(base, "format", -1) {
		return false
	}
	size := base
	// This metadata-only pass is prepaid by the record bytes. Reserve all text
	// together so native encoded formats can use the same two work reservations.
	for i, in := range p.code {
		at := replacementHeader + i*replacementRecord
		if len(in.a) > maxSnippetReplacementBytes-size {
			x.fail("format", i, replacementError(at+8, "program byte limit exceeded"))
			return false
		}
		size += len(in.a)
		if len(in.b) > maxSnippetReplacementBytes-size {
			x.fail("format", i, replacementError(at+16, "program byte limit exceeded"))
			return false
		}
		size += len(in.b)
	}
	if !x.spend(size-base, "format", -1) {
		return false
	}
	fallback := false
	for i, in := range p.code {
		if e := validateReplacementInstruction(in, replacementHeader+i*replacementRecord); e != nil {
			x.fail("format", i, e)
			return false
		}
		fallback = fallback || in.op == replacementElse && in.a != "" || in.op == replacementConditional && in.b != ""
	}
	return fallback
}
func (x *snippetTransformExecutor) render(match *snippetRegexMatch) {
	for i, in := range x.format.code {
		if !x.spend(1, "format", i) {
			return
		}
		capture := ""
		if match != nil && int(in.index) < len(match.captures) {
			c := match.captures[int(in.index)]
			if c.present {
				capture = x.input[c.start:c.end]
			}
		}
		switch in.op {
		case replacementLiteral:
			x.append(in.a, i)
		case replacementCapture:
			x.append(capture, i)
		case replacementUpper, replacementLower, replacementCapitalize, replacementCamel, replacementPascal, replacementSnake, replacementKebab:
			text, e := snippetCaseConvert(capture, in.op, x.budget)
			if e != nil {
				x.fail("case", i, e)
				return
			}
			x.append(text, i)
		case replacementIf:
			if capture != "" {
				x.append(in.a, i)
			} else {
				x.append("", i)
			}
		case replacementElse:
			if capture != "" {
				x.append(capture, i)
			} else {
				x.append(in.a, i)
			}
		case replacementConditional:
			if capture != "" {
				x.append(in.a, i)
			} else {
				x.append(in.b, i)
			}
		}
		if x.err != nil {
			return
		}
	}
}

// executeSnippetTransform is a private reference for native deferred transforms.
// Programs are immutable; one sequential batch owns the shared work budget.
func executeSnippetTransform(regex *snippetRegexProgram, format *snippetReplacementProgram, input string, budget *snippetRegexMatchBudget) (string, error) {
	if budget == nil {
		budget = &snippetRegexMatchBudget{maxSnippetRegexMatchWork}
	}
	x := snippetTransformExecutor{budget: budget, format: format, input: input}
	if budget.remaining < 0 || budget.remaining > maxSnippetRegexMatchWork {
		x.fail("arguments", -1, errors.New("invalid transform work budget"))
	} else if regex == nil || format == nil {
		x.fail("arguments", -1, errors.New("nil transform program"))
	}
	if x.err != nil {
		return "", x.err
	}
	fallback := x.validateFormat()
	if x.err != nil {
		return "", x.err
	}
	matcher, e := newSnippetRegexMatcher(regex, input, budget)
	if e != nil {
		x.fail("match", -1, e)
		return "", x.err
	}
	cursor, next := 0, 0
	found := false
	for {
		match, e := matcher.find(next)
		if e != nil {
			x.fail("match", -1, e)
			break
		}
		if match == nil {
			break
		}
		found = true
		x.append(input[cursor:match.start], -1)
		x.render(match)
		if x.err != nil {
			break
		}
		cursor = match.end
		if regex.flags&uint32(rxGlobal) == 0 {
			break
		}
		next = match.end
		if match.start == match.end {
			if next == len(input) {
				break
			}
			if !x.spend(4, "match", -1) {
				break
			}
			_, n := utf8.DecodeRuneInString(input[next:])
			next += n
		}
	}
	if x.err != nil {
		return "", x.err
	}
	if !found && fallback {
		x.render(nil)
	} else {
		x.append(input[cursor:], -1)
	}
	if x.err != nil {
		return "", x.err
	}
	return x.out.String(), nil
}
