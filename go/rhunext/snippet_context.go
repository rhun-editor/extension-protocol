package rhunext

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SnippetSnapshot is an owned read-only view of one insertion range. Position is
// its start, not the editor's cursor. All strings preserve literal UTF-8 bytes.
// Clipboard contents are never included.
type SnippetSnapshot struct {
	Version                                                         int64
	Position                                                        Pos
	TabWidth                                                        int
	InsertSpaces, CRLF                                              bool
	SelectedText, CurrentLine, Path, Workspace, Indent, LineComment string
}

// SnippetSnapshot reads insertion context at exactly version and span.
func (e *Ext) SnippetSnapshot(doc Doc, version int64, span Range) (SnippetSnapshot, error) {
	return e.SnippetSnapshotContext(context.Background(), doc, version, span)
}

// SnippetSnapshotContext supports cancellation, including draining a late reply.
func (e *Ext) SnippetSnapshotContext(ctx context.Context, doc Doc, version int64, span Range) (SnippetSnapshot, error) {
	if !e.Has(FeatureSnippets) {
		return SnippetSnapshot{}, &Error{Code: "unknown", Text: "snippets was not negotiated"}
	}
	if doc <= 0 || version < 0 || span.Start.Line <= 0 || span.Start.Col <= 0 || span.End.Line <= 0 || span.End.Col <= 0 ||
		span.End.Line < span.Start.Line || span.End.Line == span.Start.Line && span.End.Col < span.Start.Col {
		return SnippetSnapshot{}, &Error{Code: "syntax", Text: "invalid snippet target or range"}
	}
	r, err := e.RequestContext(ctx, "snippet-context", nil, itoa(int64(doc)), itoa(version),
		strconv.Itoa(span.Start.Line), strconv.Itoa(span.Start.Col), strconv.Itoa(span.End.Line), strconv.Itoa(span.End.Col))
	if err != nil {
		return SnippetSnapshot{}, err
	}
	return decodeSnippetSnapshot(r, version, span.Start)
}

func decodeSnippetSnapshot(r Reply, version int64, at Pos) (SnippetSnapshot, error) {
	bad := func() (SnippetSnapshot, error) { return SnippetSnapshot{}, fmt.Errorf("invalid snippet context reply") }
	if len(r.Args) != 2 || !saveDecimal(r.Args[0]) || !saveDecimal(r.Args[1]) || len(r.Body) > MaxSnippetBody {
		return bad()
	}
	v, err := strconv.ParseInt(r.Args[0], 10, 64)
	if err != nil || v != version {
		return bad()
	}
	n, err := strconv.ParseInt(r.Args[1], 10, 64)
	if err != nil || n != int64(len(r.Body)) {
		return bad()
	}
	end := bytes.IndexByte(r.Body, '\n')
	if end < 0 || end > 256 {
		return bad()
	}
	f := strings.Split(string(r.Body[:end]), " ")
	if len(f) != 13 || f[0] != "context" || f[1] != "1" {
		return bad()
	}
	var nums [11]int64
	for i := range nums {
		if !saveDecimal(f[i+2]) {
			return bad()
		}
		nums[i], err = strconv.ParseInt(f[i+2], 10, 64)
		if err != nil {
			return bad()
		}
	}
	if nums[0] != int64(at.Line) || nums[1] != int64(at.Col) || at.Line <= 0 || at.Col <= 0 ||
		nums[2] < 1 || nums[2] > 16 || nums[3] > 1 || nums[4] > 1 {
		return bad()
	}
	parts := r.Body[end+1:]
	var values [6]string
	for i := range values {
		length := nums[i+5]
		if length > int64(len(parts)) {
			return bad()
		}
		part := parts[:int(length)]
		if !utf8.Valid(part) {
			return bad()
		}
		values[i] = string(part)
		parts = parts[int(length):]
	}
	if len(parts) != 0 || strings.ContainsRune(values[1], '\n') || at.Col-1 > len(values[1]) ||
		at.Col-1 < len(values[1]) && !utf8.RuneStart(values[1][at.Col-1]) {
		return bad()
	}
	indent := 0
	for indent < at.Col-1 && (values[1][indent] == ' ' || values[1][indent] == '\t') {
		indent++
	}
	if values[4] != values[1][:indent] {
		return bad()
	}
	return SnippetSnapshot{Version: v, Position: at, TabWidth: int(nums[2]), InsertSpaces: nums[3] == 1, CRLF: nums[4] == 1,
		SelectedText: values[0], CurrentLine: values[1], Path: values[2], Workspace: values[3], Indent: values[4], LineComment: values[5]}, nil
}
