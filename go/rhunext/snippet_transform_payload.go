package rhunext

import (
	"encoding/binary"
	"fmt"
)

const (
	snippetTransformPayloadMagic  = 0x58544e53
	snippetTransformPayloadHeader = 32
	snippetTransformPayloadRecord = 24
)

type snippetTransformBinding struct {
	field  int
	regex  *snippetRegexProgram
	format *snippetReplacementProgram
}
type snippetTransformPayload struct{ bindings []snippetTransformBinding }
type snippetTransformPayloadError struct {
	Offset  int
	Message string
	Cause   error
}

func (e *snippetTransformPayloadError) Error() string {
	return fmt.Sprintf("snippet transforms byte %d: %s", e.Offset, e.Message)
}
func (e *snippetTransformPayloadError) Unwrap() error { return e.Cause }
func transformPayloadError(at int, message string) error {
	return &snippetTransformPayloadError{Offset: at, Message: message}
}
func transformPayloadNestedError(at int, message string, cause error) error {
	return &snippetTransformPayloadError{at, message + ": " + cause.Error(), cause}
}
func transformPayloadFormatSize(p *snippetReplacementProgram) (int, error) {
	if p == nil {
		return 0, replacementError(0, "nil program")
	}
	if len(p.code) > maxSnippetReplacementRecords {
		return 0, replacementError(12, "record limit exceeded")
	}
	size := replacementHeader + len(p.code)*replacementRecord
	for i, in := range p.code {
		at := replacementHeader + i*replacementRecord
		if len(in.a) > maxSnippetReplacementBytes-size {
			return 0, replacementError(at+8, "program byte limit exceeded")
		}
		size += len(in.a)
		if len(in.b) > maxSnippetReplacementBytes-size {
			return 0, replacementError(at+16, "program byte limit exceeded")
		}
		size += len(in.b)
	}
	return size, nil
}
func (p *snippetTransformPayload) encode() ([]byte, error) {
	if p == nil {
		return nil, transformPayloadError(0, "nil payload")
	}
	if len(p.bindings) > MaxSnippetFields {
		return nil, transformPayloadError(12, "binding limit exceeded")
	}
	base := snippetTransformPayloadHeader + len(p.bindings)*snippetTransformPayloadRecord
	size := base
	previous := 0
	// Preflight every nested length before validators scan text or encoders allocate.
	for i, b := range p.bindings {
		at := snippetTransformPayloadHeader + i*snippetTransformPayloadRecord
		if b.field <= previous || b.field > MaxSnippetFields {
			return nil, transformPayloadError(at, "invalid field order")
		}
		previous = b.field
		if b.regex == nil {
			return nil, transformPayloadError(at+4, "nil regex program")
		}
		r, e := rxProgramSize(uint64(len(b.regex.code)), uint64(len(b.regex.classes)), uint64(len(b.regex.ranges)), uint64(len(b.regex.repeats)))
		if e != nil {
			return nil, transformPayloadNestedError(at+8, "regex size", e)
		}
		f, e := transformPayloadFormatSize(b.format)
		if e != nil {
			return nil, transformPayloadNestedError(at+16, "replacement size", e)
		}
		if r > MaxSnippetBody-size {
			return nil, transformPayloadError(at+8, "payload byte limit exceeded")
		}
		size += r
		if f > MaxSnippetBody-size {
			return nil, transformPayloadError(at+16, "payload byte limit exceeded")
		}
		size += f
	}
	out := make([]byte, size)
	put := func(at int, v uint32) { binary.LittleEndian.PutUint32(out[at:at+4], v) }
	put(0, snippetTransformPayloadMagic)
	put(4, 1)
	put(8, rxProgramUnicode)
	put(12, uint32(len(p.bindings)))
	put(16, uint32(size))
	cursor := base
	for i, b := range p.bindings {
		at := snippetTransformPayloadHeader + i*snippetTransformPayloadRecord
		r, e := b.regex.encode()
		if e != nil {
			var offset int
			if detail, ok := e.(*snippetRegexProgramError); ok {
				offset = detail.Offset
			}
			return nil, transformPayloadNestedError(cursor+offset, "regex program", e)
		}
		f, e := b.format.encode()
		if e != nil {
			var offset int
			if detail, ok := e.(*snippetReplacementProgramError); ok {
				offset = detail.Offset
			}
			return nil, transformPayloadNestedError(cursor+len(r)+offset, "replacement program", e)
		}
		put(at, uint32(b.field))
		put(at+4, uint32(cursor))
		put(at+8, uint32(len(r)))
		copy(out[cursor:], r)
		cursor += len(r)
		put(at+12, uint32(cursor))
		put(at+16, uint32(len(f)))
		copy(out[cursor:], f)
		cursor += len(f)
	}
	return out, nil
}
func decodeSnippetTransformPayload(data []byte) (*snippetTransformPayload, error) {
	if len(data) < snippetTransformPayloadHeader {
		return nil, transformPayloadError(len(data), "short header")
	}
	if len(data) > MaxSnippetBody {
		return nil, transformPayloadError(16, "payload byte limit exceeded")
	}
	get := func(at int) uint32 { return binary.LittleEndian.Uint32(data[at : at+4]) }
	for _, v := range []struct {
		at    int
		value uint32
	}{{0, snippetTransformPayloadMagic}, {4, 1}, {8, rxProgramUnicode}, {20, 0}, {24, 0}, {28, 0}} {
		if get(v.at) != v.value {
			return nil, transformPayloadError(v.at, "invalid header")
		}
	}
	countWord := get(12)
	if countWord > MaxSnippetFields {
		return nil, transformPayloadError(12, "binding limit exceeded")
	}
	count := int(countWord)
	base := snippetTransformPayloadHeader + count*snippetTransformPayloadRecord
	if get(16) != uint32(len(data)) || base > len(data) {
		return nil, transformPayloadError(16, "invalid payload length")
	}
	p := &snippetTransformPayload{bindings: make([]snippetTransformBinding, 0, count)}
	cursor, previous := base, 0
	for i := 0; i < count; i++ {
		at := snippetTransformPayloadHeader + i*snippetTransformPayloadRecord
		field := int(get(at))
		if field <= previous || field > MaxSnippetFields {
			return nil, transformPayloadError(at, "invalid field order")
		}
		if get(at+20) != 0 {
			return nil, transformPayloadError(at+20, "reserved record word")
		}
		if uint64(get(at+4)) != uint64(cursor) {
			return nil, transformPayloadError(at+4, "noncanonical regex offset")
		}
		rlen := uint64(get(at + 8))
		if rlen > uint64(len(data)-cursor) {
			return nil, transformPayloadError(at+8, "regex length exceeds payload")
		}
		roff := cursor
		cursor += int(rlen)
		if uint64(get(at+12)) != uint64(cursor) {
			return nil, transformPayloadError(at+12, "noncanonical replacement offset")
		}
		flen := uint64(get(at + 16))
		if flen > uint64(len(data)-cursor) {
			return nil, transformPayloadError(at+16, "replacement length exceeds payload")
		}
		foff := cursor
		cursor += int(flen)
		r, e := decodeSnippetRegexProgram(data[roff:foff])
		if e != nil {
			offset := 0
			if detail, ok := e.(*snippetRegexProgramError); ok {
				offset = detail.Offset
			}
			return nil, transformPayloadNestedError(roff+offset, "regex program", e)
		}
		f, e := decodeSnippetReplacement(data[foff:cursor])
		if e != nil {
			offset := 0
			if detail, ok := e.(*snippetReplacementProgramError); ok {
				offset = detail.Offset
			}
			return nil, transformPayloadNestedError(foff+offset, "replacement program", e)
		}
		p.bindings = append(p.bindings, snippetTransformBinding{field, r, f})
		previous = field
	}
	if cursor != len(data) {
		return nil, transformPayloadError(16, "unused payload tail")
	}
	return p, nil
}
