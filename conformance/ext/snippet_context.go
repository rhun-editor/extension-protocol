package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func (h *host) snippetContext(args []string) ([]byte, int64, string, string) {
	bad := func(code, why string) ([]byte, int64, string, string) { return nil, 0, code, why }
	if !h.features["snippets"] {
		return bad("unknown", "snippets was not agreed")
	}
	if len(args) != 6 {
		return bad("syntax", "snippet-context takes DOC VERSION L1 C1 L2 C2")
	}
	var n [6]int64
	for i, s := range args {
		v, ok := saveNumber(s)
		if !ok {
			return bad("syntax", "invalid context number")
		}
		n[i] = v
	}
	if n[0] <= 0 {
		return bad("nodoc", "document closed")
	}
	d := h.docs[n[0]]
	if d == nil {
		return bad("nodoc", "document closed")
	}
	if n[1] != d.version {
		return bad("stale", strconv.FormatInt(d.version, 10))
	}
	start, why := d.position(args[2], args[3])
	end := 0
	if why == "" {
		end, why = d.position(args[4], args[5])
	}
	if why != "" || end < start {
		return bad("badpos", "invalid context range")
	}
	lineStart := bytes.LastIndexByte(d.text[:start], '\n') + 1
	lineEnd := len(d.text)
	if end := bytes.IndexByte(d.text[start:], '\n'); end >= 0 {
		lineEnd = start + end
	}
	indentEnd := lineStart
	for indentEnd < start && (d.text[indentEnd] == ' ' || d.text[indentEnd] == '\t') {
		indentEnd++
	}
	tab, spaces := 4, 1
	for _, record := range h.options {
		f := strings.Fields(record)
		if len(f) != 3 {
			continue
		}
		optionDoc, valid := saveNumber(f[0])
		if !valid || optionDoc != d.id {
			continue
		}
		switch f[1] {
		case "tab_width":
			tab = 4
			if f[2] != "-" {
				tab, _ = strconv.Atoi(f[2])
			}
		case "insert_spaces":
			spaces = 1
			if f[2] == "no" {
				spaces = 0
			}
		}
	}
	comment := ""
	switch d.lang {
	case "Go", "Rust", "C", "C++", "JavaScript", "TypeScript", "Java", "JSON", "JSONC":
		comment = "//"
	case "Python", "Shell", "Ruby":
		comment = "#"
	}
	parts := [][]byte{d.text[start:end], d.text[lineStart:lineEnd], []byte(d.path), []byte(h.project), d.text[lineStart:indentEnd], []byte(comment)}
	crlf := 0
	if d.crlf {
		crlf = 1
	}
	head := fmt.Sprintf("context 1 %d %d %d %d %d", n[2], n[3], tab, spaces, crlf)
	total := 0
	for _, part := range parts {
		if len(part) > rhunext.MaxSnippetBody-total {
			return bad("toobig", "snippet context exceeds 4 MiB")
		}
		total += len(part)
		if !utf8.Valid(part) {
			return bad("refused", "snippet context is not UTF-8")
		}
		head += fmt.Sprintf(" %d", len(part))
	}
	head += "\n"
	if len(head) > rhunext.MaxSnippetBody-total {
		return bad("toobig", "snippet context exceeds 4 MiB")
	}
	out := make([]byte, 0, len(head)+total)
	out = append(out, head...)
	for _, part := range parts {
		out = append(out, part...)
	}
	return out, d.version, "", ""
}
