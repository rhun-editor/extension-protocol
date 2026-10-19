package rhunext

import "strings"

// Compiled helpers belong to one preparation, never to the shared parsed tree.
type snippetPreparedTransform struct {
	regex           *snippetRegexProgram
	replacement     *snippetReplacementProgram
	compiled        CompiledSnippetTransform
	initial         string
	initialResolved bool
}

// Translate a decoded regex byte through only the delimiter unescaping performed
// by the template parser. Other escape pairs retain both original bytes.
func snippetTransformSourceOffset(source string, t *stTransformValue, offset int) int {
	if strings.ContainsRune(t.options, 'v') {
		return min(t.regexFrom+offset, t.regexTo)
	}
	decoded := 0
	for at := t.regexFrom; at < t.regexTo; {
		if decoded == offset {
			return at
		}
		if source[at] == '\\' && at+1 < t.regexTo {
			if source[at+1] == '/' {
				decoded++
				at += 2
				continue
			}
			if decoded+1 == offset {
				return at + 1
			}
			decoded += 2
			at += 2
			continue
		}
		decoded++
		at++
	}
	return t.regexTo
}
func (e *snippetExpander) transformRegexError(t *stTransformValue, err error) error {
	diagnostic := err.(*snippetRegexError)
	at := t.optionsFrom + diagnostic.Offset
	if diagnostic.Part == "source" {
		at = snippetTransformSourceOffset(e.source, t, diagnostic.Offset)
	}
	return expansionError(at, "transform "+diagnostic.Part+": "+diagnostic.Message)
}
func (e *snippetExpander) compileSpend(n int, at int) error {
	if n < 0 || n > e.compileWork {
		return expansionError(at, "transform aggregate compile work limit exceeded")
	}
	e.compileWork -= n
	return nil
}
func (e *snippetExpander) compiledTransform(n *stNode) (*snippetPreparedTransform, error) {
	t := n.transform
	if cached := e.compiled[t]; cached != nil {
		return cached, nil
	}
	regexSource := t.regex
	if strings.ContainsRune(t.options, 'v') {
		// A delimiter slash is reserved inside v sets. Preserve its escape,
		// whose presence is significant to the class grammar.
		regexSource = e.source[t.regexFrom:t.regexTo]
	}
	if err := e.compileSpend(len(regexSource)+len(t.options), n.from); err != nil {
		return nil, err
	}
	ast, err := parseSnippetRegex(regexSource, t.options)
	if err != nil {
		return nil, e.transformRegexError(t, err)
	}
	work := min(e.compileWork, maxSnippetRegexCompileWork)
	available := work
	regex, err := compileSnippetRegexASTBudget(ast, &work)
	e.compileWork -= available - work
	if err != nil {
		return nil, e.transformRegexError(t, err)
	}
	replacement, err := compileSnippetReplacement(t.format)
	if err != nil {
		return nil, err
	} // format instructions already carry source offsets
	formatSize, err := replacement.validate()
	if err != nil {
		return nil, expansionError(n.from, "transform replacement: "+err.Error())
	}
	if err = e.compileSpend(formatSize, n.from); err != nil {
		return nil, err
	}
	regexSize, _ := rxProgramSize(uint64(len(regex.code)), uint64(len(regex.classes)), uint64(len(regex.ranges)), uint64(len(regex.repeats)))
	if regexSize > MaxSnippetBody-e.transformBytes || formatSize > MaxSnippetBody-e.transformBytes-regexSize {
		return nil, expansionError(n.from, "compiled transform cache exceeds byte limit")
	}
	rawRegex, err := regex.encode()
	if err != nil {
		return nil, expansionError(n.from, "transform regex: "+err.Error())
	}
	rawFormat, err := replacement.encode()
	if err != nil {
		return nil, expansionError(n.from, "transform replacement: "+err.Error())
	}
	result := &snippetPreparedTransform{regex: regex, replacement: replacement, compiled: CompiledSnippetTransform{string(rawRegex), string(rawFormat)}}
	if e.compiled == nil {
		e.compiled = make(map[*stTransformValue]*snippetPreparedTransform)
	}
	e.compiled[t] = result
	e.transformBytes += regexSize + formatSize
	return result, nil
}

func (e *snippetExpander) transformText(t *snippetPreparedTransform, input string, at int) (string, error) {
	text, err := executeSnippetTransform(t.regex, t.replacement, input, &e.matchBudget)
	if err != nil {
		return "", expansionError(at, "initial transform: "+err.Error())
	}
	return text, nil
}

// Raw context is constant within one preparation. Default evaluation and primary
// rendering share this result so wrapping a transform does not execute it twice.
func (e *snippetExpander) initialTransform(n *stNode) (string, error) {
	t, err := e.compiledTransform(n)
	if err != nil {
		return "", err
	}
	if t.initialResolved {
		return t.initial, nil
	}
	input, _ := e.variable(n.name)
	text, err := e.transformText(t, input, n.from)
	if err != nil {
		return "", err
	}
	if len(text) > MaxSnippetBody-e.initialBytes {
		return "", expansionError(n.from, "initial transform cache exceeds byte limit")
	}
	e.initialBytes += len(text)
	t.initial, t.initialResolved = text, true
	return text, nil
}
func (e *snippetExpander) transformSpend(n, at int) error {
	if n < 0 || n > e.matchBudget.remaining {
		return expansionError(at, "initial transform work limit exceeded")
	}
	e.matchBudget.remaining -= n
	return nil
}
func (e *snippetExpander) adjustSelectionInput(input, extra string, at int) (string, error) {
	if extra == "" {
		return input, nil
	}
	if err := e.transformSpend(len(input), at); err != nil {
		return "", err
	}
	total := len(input)
	for pos := 0; pos < len(input); pos++ {
		if input[pos] != '\r' && input[pos] != '\n' {
			continue
		}
		if len(extra) > MaxSnippetBody-total {
			return "", expansionError(at, "transformed selection input exceeds byte limit")
		}
		total += len(extra)
		if input[pos] == '\r' && pos+1 < len(input) && input[pos+1] == '\n' {
			pos++
		}
	}
	if err := e.transformSpend(total, at); err != nil {
		return "", err
	}
	var out strings.Builder
	out.Grow(total)
	start := 0
	for pos := 0; pos < len(input); pos++ {
		if input[pos] != '\r' && input[pos] != '\n' {
			continue
		}
		if input[pos] == '\r' && pos+1 < len(input) && input[pos+1] == '\n' {
			pos++
		}
		out.WriteString(input[start : pos+1])
		out.WriteString(extra)
		start = pos + 1
	}
	out.WriteString(input[start:])
	return out.String(), nil
}
