package rhunext

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

const (
	maxSnippetReplacementBytes   = MaxSnippetBody
	maxSnippetReplacementRecords = MaxSnippetTemplateNodes
	replacementMagic             = 0x46524e52
	replacementVersion           = 1
	replacementHeader            = 32
	replacementRecord            = 24
)
const (
	replacementLiteral uint32 = iota
	replacementCapture
	replacementUpper
	replacementLower
	replacementCapitalize
	replacementCamel
	replacementPascal
	replacementSnake
	replacementKebab
	replacementIf
	replacementElse
	replacementConditional
)

type snippetReplacementInstruction struct {
	op, index uint32
	a, b      string
}
type snippetReplacementProgram struct {
	code []snippetReplacementInstruction
}
type snippetReplacementProgramError struct {
	Offset  int
	Message string
}

func (e *snippetReplacementProgramError) Error() string {
	return fmt.Sprintf("replacement program byte %d: %s", e.Offset, e.Message)
}
func replacementError(at int, message string) error {
	return &snippetReplacementProgramError{at, message}
}
func replacementUses(op uint32) (bool, bool) {
	return op == replacementLiteral || op >= replacementIf, op == replacementConditional
}
func validateReplacementInstruction(in snippetReplacementInstruction, at int) error {
	if in.op > replacementConditional {
		return replacementError(at, "unknown opcode")
	}
	if in.index > 0x7fffffff || in.op == replacementLiteral && in.index != 0 {
		return replacementError(at+4, "invalid capture index")
	}
	a, b := replacementUses(in.op)
	if !a && in.a != "" {
		return replacementError(at+8, "unused text")
	}
	if !b && in.b != "" {
		return replacementError(at+16, "unused text")
	}
	if !utf8.ValidString(in.a) {
		return replacementError(at+8, "invalid UTF-8")
	}
	if !utf8.ValidString(in.b) {
		return replacementError(at+16, "invalid UTF-8")
	}
	return nil
}
func (p *snippetReplacementProgram) validate() (int, error) {
	if p == nil {
		return 0, replacementError(0, "nil program")
	}
	if len(p.code) > maxSnippetReplacementRecords {
		return 0, replacementError(12, "record limit exceeded")
	}
	size := uint64(replacementHeader) + uint64(len(p.code))*replacementRecord
	for i, in := range p.code {
		at := replacementHeader + i*replacementRecord
		if uint64(len(in.a)) > uint64(maxSnippetReplacementBytes)-size {
			return 0, replacementError(at+8, "program byte limit exceeded")
		}
		size += uint64(len(in.a))
		if uint64(len(in.b)) > uint64(maxSnippetReplacementBytes)-size {
			return 0, replacementError(at+16, "program byte limit exceeded")
		}
		size += uint64(len(in.b))
		if e := validateReplacementInstruction(in, at); e != nil {
			return 0, e
		}
	}

	return int(size), nil
}
func compileSnippetReplacement(parts []stFormat) (*snippetReplacementProgram, error) {
	if len(parts) > maxSnippetReplacementRecords {
		return nil, expansionError(parts[maxSnippetReplacementRecords].from, "replacement record limit exceeded")
	}
	p := &snippetReplacementProgram{code: make([]snippetReplacementInstruction, len(parts))}
	size := uint64(replacementHeader) + uint64(len(parts))*replacementRecord
	for i, part := range parts {
		fail := func(message string) (*snippetReplacementProgram, error) {
			return nil, expansionError(part.from, message)
		}
		if part.index < 0 || uint64(part.index) > 0x7fffffff {
			return fail("replacement capture index out of bounds")
		}
		in := snippetReplacementInstruction{index: uint32(part.index)}
		switch part.kind {
		case stFormatText:
			in.op = replacementLiteral
			in.a = part.text
		case stCapture:
			in.op = replacementCapture
		case stCase:
			switch part.text {
			case "upcase":
				in.op = replacementUpper
			case "downcase":
				in.op = replacementLower
			case "capitalize":
				in.op = replacementCapitalize
			case "camelcase":
				in.op = replacementCamel
			case "pascalcase":
				in.op = replacementPascal
			case "snakecase":
				in.op = replacementSnake
			case "kebabcase":
				in.op = replacementKebab
			default:
				return fail("unknown replacement case format")
			}
		case stIf:
			in.op = replacementIf
			in.a = part.text
		case stElse:
			in.op = replacementElse
			in.a = part.text
		case stConditional:
			in.op = replacementConditional
			in.a = part.text
			in.b = part.otherwise
		default:
			return fail("unknown replacement format")
		}
		if uint64(len(in.a)) > uint64(maxSnippetReplacementBytes)-size {
			return fail("replacement byte limit exceeded")
		}
		size += uint64(len(in.a))
		if uint64(len(in.b)) > uint64(maxSnippetReplacementBytes)-size {
			return fail("replacement byte limit exceeded")
		}
		size += uint64(len(in.b))
		if e := validateReplacementInstruction(in, replacementHeader+i*replacementRecord); e != nil {
			return fail(e.Error())
		}

		p.code[i] = in
	}
	return p, nil
}
func (p *snippetReplacementProgram) encode() ([]byte, error) {
	size, e := p.validate()
	if e != nil {
		return nil, e
	}
	out := make([]byte, size)
	put := func(at int, v uint32) { binary.LittleEndian.PutUint32(out[at:at+4], v) }
	blob := replacementHeader + len(p.code)*replacementRecord
	textSize := size - blob
	for i, v := range []uint32{replacementMagic, replacementVersion, rxProgramUnicode, uint32(len(p.code)), uint32(textSize), uint32(size), 0, 0} {
		put(i*4, v)
	}
	cursor := 0
	for i, in := range p.code {
		at := replacementHeader + i*replacementRecord
		put(at, in.op)
		put(at+4, in.index)
		a, b := replacementUses(in.op)
		if a {
			put(at+8, uint32(cursor))
			put(at+12, uint32(len(in.a)))
			copy(out[blob+cursor:], in.a)
			cursor += len(in.a)
		}
		if b {
			put(at+16, uint32(cursor))
			put(at+20, uint32(len(in.b)))
			copy(out[blob+cursor:], in.b)
			cursor += len(in.b)
		}
	}
	return out, nil
}
func decodeSnippetReplacement(data []byte) (*snippetReplacementProgram, error) {
	if len(data) < replacementHeader {
		return nil, replacementError(len(data), "truncated header")
	}
	if len(data) > maxSnippetReplacementBytes {
		return nil, replacementError(20, "program byte limit exceeded")
	}
	get := func(at int) uint32 { return binary.LittleEndian.Uint32(data[at : at+4]) }
	for _, field := range []struct {
		at    int
		value uint32
	}{{0, replacementMagic}, {4, replacementVersion}, {8, rxProgramUnicode}, {24, 0}, {28, 0}} {
		if get(field.at) != field.value {
			return nil, replacementError(field.at, "invalid header")
		}
	}
	count, textSize := uint64(get(12)), uint64(get(16))
	if count > maxSnippetReplacementRecords {
		return nil, replacementError(12, "record limit exceeded")
	}
	blob64 := uint64(replacementHeader) + count*replacementRecord
	size := blob64 + textSize
	if size > maxSnippetReplacementBytes || size != uint64(len(data)) || uint64(get(20)) != size {
		return nil, replacementError(20, "length mismatch")
	}
	blob := int(blob64)
	text := data[blob:]
	if !utf8.Valid(text) {
		for at := 0; at < len(text); {
			r, n := utf8.DecodeRune(text[at:])
			if r == utf8.RuneError && n == 1 {
				return nil, replacementError(blob+at, "invalid UTF-8")
			}
			at += n
		}
	}
	// A single copy owns every decoded string. Record validation never retains caller bytes.
	owned := string(text)
	p := &snippetReplacementProgram{code: make([]snippetReplacementInstruction, int(count))}
	cursor := uint64(0)
	boundary := func(n uint64) bool { return n == textSize || utf8.RuneStart(owned[int(n)]) }
	read := func(at int, used bool) (string, error) {
		offset, length := uint64(get(at)), uint64(get(at+4))
		if !used {
			if offset|length != 0 {
				return "", replacementError(at, "unused text span")
			}
			return "", nil
		}
		if offset != cursor {
			return "", replacementError(at, "noncanonical text offset")
		}
		if length > textSize-cursor {
			return "", replacementError(at+4, "text span out of bounds")
		}
		if !boundary(cursor) {
			return "", replacementError(at, "invalid text boundary")
		}
		end := cursor + length
		if !boundary(end) {
			return "", replacementError(at+4, "invalid text boundary")
		}
		value := owned[int(cursor):int(end)]
		cursor = end
		return value, nil
	}
	for i := range p.code {
		at := replacementHeader + i*replacementRecord
		in := snippetReplacementInstruction{op: get(at), index: get(at + 4)}
		if in.op > replacementConditional {
			return nil, replacementError(at, "unknown opcode")
		}
		if in.index > 0x7fffffff || in.op == replacementLiteral && in.index != 0 {
			return nil, replacementError(at+4, "invalid capture index")
		}
		a, b := replacementUses(in.op)
		var e error
		if in.a, e = read(at+8, a); e != nil {
			return nil, e
		}
		if in.b, e = read(at+16, b); e != nil {
			return nil, e
		}
		p.code[i] = in
	}
	if cursor != textSize {
		return nil, replacementError(blob+int(cursor), "unowned text")
	}
	return p, nil
}
