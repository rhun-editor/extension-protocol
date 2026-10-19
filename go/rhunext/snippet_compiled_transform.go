package rhunext

import (
	"fmt"
	"unicode/utf8"
)

const FeatureSnippetTransforms = "snippet-transforms"

// SnippetTransformCompileError identifies a byte in source, options, format, or
// an imported regex/replacement program. Part program and Offset 0 denote a
// combined size error. Cause preserves the underlying diagnostic.
type SnippetTransformCompileError struct {
	Part    string
	Offset  int
	Message string
	Cause   error
}

func (e *SnippetTransformCompileError) Error() string {
	return fmt.Sprintf("snippet transform %s byte %d: %s", e.Part, e.Offset, e.Message)
}
func (e *SnippetTransformCompileError) Unwrap() error { return e.Cause }

// CompiledSnippetTransform owns immutable portable regex and replacement programs.
// Its zero value is invalid. Copies may be used concurrently.
type CompiledSnippetTransform struct{ regex, replacement string }

func transformCompileError(part string, offset int, message string, cause error) error {
	return &SnippetTransformCompileError{part, offset, message, cause}
}
func snippetTransformPairSize(regex, replacement int) error {
	if regex > maxSnippetRegexProgramBytes {
		return transformCompileError("regex", 40, "program exceeds byte limit", nil)
	}
	if replacement > MaxSnippetBody {
		return transformCompileError("replacement", 20, "program exceeds byte limit", nil)
	}
	if replacement > MaxSnippetBody-snippetTransformPayloadHeader-snippetTransformPayloadRecord-regex {
		return transformCompileError("program", 0, "programs cannot fit one transform binding", nil)
	}
	return nil
}
func snippetTransformPairValidate(regex, replacement []byte) error {
	if err := snippetTransformPairSize(len(regex), len(replacement)); err != nil {
		return err
	}
	if _, err := decodeSnippetRegexProgram(regex); err != nil {
		return transformCompileError("regex", err.(*snippetRegexProgramError).Offset, err.Error(), err)
	}
	if _, err := decodeSnippetReplacement(replacement); err != nil {
		return transformCompileError("replacement", err.(*snippetReplacementProgramError).Offset, err.Error(), err)
	}
	return nil
}

// NewCompiledSnippetTransform imports validated RNRX and RNRF v1 programs.
// It checks all lengths before scanning either program and owns copies of both.
func NewCompiledSnippetTransform(regex, replacement []byte) (CompiledSnippetTransform, error) {
	if err := snippetTransformPairValidate(regex, replacement); err != nil {
		return CompiledSnippetTransform{}, err
	}
	return CompiledSnippetTransform{string(regex), string(replacement)}, nil
}

// Programs returns independent copies of the portable regex and replacement bytes.
func (t CompiledSnippetTransform) Programs() (regex, replacement []byte) {
	return []byte(t.regex), []byte(t.replacement)
}

// Validate checks the complete program pair, including its combined byte limit.
func (t CompiledSnippetTransform) Validate() error {
	if err := snippetTransformPairSize(len(t.regex), len(t.replacement)); err != nil {
		return err
	}
	return snippetTransformPairValidate([]byte(t.regex), []byte(t.replacement))
}

// CompileSnippetTransform compiles a regex source and replacement format without
// ambient state. Format uses snippet capture/case/conditional syntax and escapes;
// a literal slash must be escaped. Options use the supported scalar regex grammar.
func CompileSnippetTransform(source, format, options string) (CompiledSnippetTransform, error) {
	if len(format) > MaxSnippetBody {
		return CompiledSnippetTransform{}, transformCompileError("format", MaxSnippetBody, "format exceeds byte limit", nil)
	}
	if !utf8.ValidString(format) {
		for at := 0; at < len(format); {
			r, n := utf8.DecodeRuneInString(format[at:])
			if r == utf8.RuneError && n == 1 {
				return CompiledSnippetTransform{}, transformCompileError("format", at, "invalid UTF-8", nil)
			}
			at += n
		}
	}
	regex, err := compileSnippetRegex(source, options)
	if err != nil {
		e := err.(*snippetRegexError)
		return CompiledSnippetTransform{}, transformCompileError(e.Part, e.Offset, e.Message, err)
	}
	parser := stParser{s: format + "/"}
	parts, err := parser.format(0)
	if err != nil {
		e := err.(*SnippetSyntaxError)
		at := e.Offset
		if at > len(format) {
			at = len(format)
		}
		return CompiledSnippetTransform{}, transformCompileError("format", at, e.Message, err)
	}
	if parser.pos != len(parser.s) {
		return CompiledSnippetTransform{}, transformCompileError("format", parser.pos-1, "unescaped format delimiter", nil)
	}
	replacement, err := compileSnippetReplacement(parts)
	if err != nil {
		e := err.(*SnippetExpansionError)
		at := e.Offset
		if at > len(format) {
			at = len(format)
		}
		return CompiledSnippetTransform{}, transformCompileError("format", at, e.Message, err)
	}
	r, err := regex.encode()
	if err != nil {
		return CompiledSnippetTransform{}, err
	}
	f, err := replacement.encode()
	if err != nil {
		return CompiledSnippetTransform{}, err
	}
	if err = snippetTransformPairSize(len(r), len(f)); err != nil {
		return CompiledSnippetTransform{}, err
	}
	return CompiledSnippetTransform{string(r), string(f)}, nil
}

// SnippetFieldTransform binds a compiled transform to a one-based field occurrence.
// Prepared snippet validation checks field existence and positive group ownership.
type SnippetFieldTransform struct {
	Field     int
	Transform CompiledSnippetTransform
}

// SnippetTransformPayloadError reports an absolute byte in a canonical payload.
type SnippetTransformPayloadError = snippetTransformPayloadError

// DecodeSnippetTransforms imports owned field bindings from canonical SNTX bytes.
// It does not check field existence or ownership without a prepared snippet.
func DecodeSnippetTransforms(data []byte) ([]SnippetFieldTransform, error) {
	p, err := decodeSnippetTransformPayload(data)
	if err != nil {
		return nil, err
	}
	out := make([]SnippetFieldTransform, len(p.bindings))
	for i, b := range p.bindings {
		r, _ := b.regex.encode()
		f, _ := b.format.encode()
		out[i] = SnippetFieldTransform{b.field, CompiledSnippetTransform{string(r), string(f)}}
	}
	return out, nil
}
