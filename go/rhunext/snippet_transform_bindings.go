package rhunext

import (
	"encoding/binary"
	"fmt"
)

// Preflight every length and field identity before any nested program scans.
func preflightSnippetTransforms(bindings []SnippetFieldTransform, fields []Field) (int, [MaxSnippetFields]int, error) {
	var order [MaxSnippetFields]int
	if len(bindings) == 0 {
		return 0, order, nil
	}
	if len(bindings) > MaxSnippetFields {
		return 0, order, fmt.Errorf("too many snippet transforms")
	}
	size := snippetTransformPayloadHeader + len(bindings)*snippetTransformPayloadRecord
	for i, b := range bindings {
		if b.Field < 1 || b.Field > MaxSnippetFields || b.Field > len(fields) || fields[b.Field-1].Group <= 0 || order[b.Field-1] != 0 {
			return 0, order, fmt.Errorf("invalid snippet transform field %d", b.Field)
		}
		order[b.Field-1] = i + 1
		r, f := len(b.Transform.regex), len(b.Transform.replacement)
		if err := snippetTransformPairSize(r, f); err != nil {
			return 0, order, err
		}
		if r > MaxSnippetBody-size {
			return 0, order, fmt.Errorf("snippet transform payload exceeds byte limit")
		}
		size += r
		if f > MaxSnippetBody-size {
			return 0, order, fmt.Errorf("snippet transform payload exceeds byte limit")
		}
		size += f
	}
	return size, order, nil
}

// The caller has validated all bindings and complete encoded body sizes.
func encodeSnippetTransforms(bindings []SnippetFieldTransform, fields []Field) []byte {
	size, order, _ := preflightSnippetTransforms(bindings, fields)
	if size == 0 {
		return nil
	}
	out := make([]byte, size)
	put := func(at int, v uint32) { binary.LittleEndian.PutUint32(out[at:at+4], v) }
	put(0, snippetTransformPayloadMagic)
	put(4, 1)
	put(8, rxProgramUnicode)
	put(12, uint32(len(bindings)))
	put(16, uint32(size))
	cursor := snippetTransformPayloadHeader + len(bindings)*snippetTransformPayloadRecord
	record := snippetTransformPayloadHeader
	for field, index := range order {
		if index == 0 {
			continue
		}
		b := bindings[index-1]
		put(record, uint32(field+1))
		put(record+4, uint32(cursor))
		put(record+8, uint32(len(b.Transform.regex)))
		copy(out[cursor:], b.Transform.regex)
		cursor += len(b.Transform.regex)
		put(record+12, uint32(cursor))
		put(record+16, uint32(len(b.Transform.replacement)))
		copy(out[cursor:], b.Transform.replacement)
		cursor += len(b.Transform.replacement)
		record += snippetTransformPayloadRecord
	}
	return out
}
