package main

import (
	"fmt"
	"strconv"
)

type saveScope struct{ id, doc, version int64 }

func saveNumber(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func (h *host) saveApplyRequest(args []string, body []byte, reply func(string), rerr func(string, string), id int64) {
	if !h.features["save-actions"] {
		h.fail("features", "save-apply without negotiated save-actions")
		rerr("unknown", "save-actions was not agreed")
		return
	}
	if len(args) != 3 {
		rerr("syntax", "save-apply takes SAVE_REQUEST DOC VERSION LEN")
		return
	}
	request, ok1 := saveNumber(args[0])
	docID, ok2 := saveNumber(args[1])
	version, ok3 := saveNumber(args[2])
	if !ok1 || !ok2 || !ok3 || request < 1 || request > 2147483647 || docID < 1 {
		rerr("syntax", "invalid save-action identity")
		return
	}
	s := h.saveScope
	if s == nil || s.id != request || s.doc != docID {
		rerr("refused", "save invocation is not active for this document")
		return
	}
	d := h.docs[docID]
	if d == nil {
		rerr("nodoc", "document closed")
		return
	}
	if version != s.version || d.version != s.version {
		rerr("stale", fmt.Sprint(d.version))
		return
	}
	groups, code, why := h.checkWorkspaceEdit(body, d)
	if why != "" {
		rerr(code, why)
		return
	}
	h.apply(groups)
	s.version = d.version
	reply(fmt.Sprintf("ok %d %d", id, d.version))
}
