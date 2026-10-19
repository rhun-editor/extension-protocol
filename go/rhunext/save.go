package rhunext

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const FeatureSaveActions = "save-actions"
const maxSourceRequests = 128

// SaveActionsRequest asks for one source kind before a document is saved. Intermediate
// command edits use SaveApplyContext with this ID and Context. The host grants that request
// authority only while this invocation is active.
type SaveActionsRequest struct {
	ID      int64
	Doc     Doc
	Version int64
	Kind    string
	Context context.Context
}

// SaveActionProvider runs source actions before saving. Return an empty edit when edits were
// submitted through SaveApplyContext. Cancellation is delivered directly by the IO reader,
// even while the handler is blocked. Handler methods retain their existing serial ordering.
type SaveActionProvider interface {
	SaveActions(ext *Ext, req SaveActionsRequest) (WorkspaceEdit, error)
}

func validSourceKind(kind string) bool {
	if len(kind) > 128 {
		return false
	}
	if kind == "source" {
		return true
	}
	if !strings.HasPrefix(kind, "source.") {
		return false
	}
	segment := false
	for _, c := range kind[len("source."):] {
		if c == '.' {
			if !segment {
				return false
			}
			segment = false
		} else if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			segment = true
		} else {
			return false
		}
	}
	return segment
}

func parseSaveActions(id int64, fields []string, ctx context.Context) (SaveActionsRequest, bool) {
	if len(fields) != 3 || id <= 0 || id > 2147483647 {
		return SaveActionsRequest{}, false
	}
	if !saveDecimal(fields[0]) || !saveDecimal(fields[1]) {
		return SaveActionsRequest{}, false
	}
	doc, version, ok := parseDocVersion(fields)
	if !ok || doc <= 0 || version < 0 || !validSourceKind(fields[2]) {
		return SaveActionsRequest{}, false
	}
	return SaveActionsRequest{ID: id, Doc: doc, Version: version, Kind: fields[2], Context: ctx}, true
}

func saveDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type sourceRequest struct {
	id     int64
	ctx    context.Context
	cancel context.CancelFunc
}

func (e *Ext) beginSource(id int64) (*sourceRequest, error) {
	e.sourceMu.Lock()
	defer e.sourceMu.Unlock()
	e.pmu.Lock()
	closed := e.closed
	e.pmu.Unlock()
	if closed != nil {
		return nil, closed
	}
	if len(e.sourceRequests) >= maxSourceRequests {
		return nil, fmt.Errorf("too many save-action requests")
	}
	if e.sourceRequests[id] != nil {
		return nil, fmt.Errorf("duplicate save-action request ID")
	}
	if e.sourceRequests == nil {
		e.sourceRequests = make(map[int64]*sourceRequest)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &sourceRequest{id: id, ctx: ctx, cancel: cancel}
	e.sourceRequests[id] = s
	return s, nil
}

func (e *Ext) cancelSource(id int64) {
	e.sourceMu.Lock()
	s := e.sourceRequests[id]
	e.sourceMu.Unlock()
	if s != nil {
		s.cancel()
	}
}

func (e *Ext) finishSource(s *sourceRequest) {
	e.sourceMu.Lock()
	if e.sourceRequests[s.id] == s {
		delete(e.sourceRequests, s.id)
	}
	e.sourceMu.Unlock()
	s.cancel()
}

func (e *Ext) sourceFor(id int64) *sourceRequest {
	e.sourceMu.Lock()
	defer e.sourceMu.Unlock()
	return e.sourceRequests[id]
}

func (e *Ext) cancelSources(clearRegistry bool) {
	e.sourceMu.Lock()
	all := make([]*sourceRequest, 0, len(e.sourceRequests))
	for _, s := range e.sourceRequests {
		all = append(all, s)
	}
	if clearRegistry {
		e.sourceRequests = nil
	}
	e.sourceMu.Unlock()
	for _, s := range all {
		s.cancel()
	}
}

// SaveApply submits an intermediate source action edit and returns its credited native version.
func (e *Ext) SaveApply(requestID int64, doc Doc, version int64, edit WorkspaceEdit) (int64, error) {
	return e.SaveApplyContext(context.Background(), requestID, doc, version, edit)
}

// SaveApplyContext is SaveApply with cancellation. The host also rechecks the active save
// request, document, destination and version, so late or foreign edits cannot be credited.
func (e *Ext) SaveApplyContext(ctx context.Context, requestID int64, doc Doc, version int64, edit WorkspaceEdit) (int64, error) {
	if !e.Has(FeatureSaveActions) {
		return 0, &Error{Code: "unknown", Text: "save-actions was not negotiated"}
	}
	if requestID <= 0 || requestID > 2147483647 || doc <= 0 || version < 0 {
		return 0, &Error{Code: "syntax", Text: "invalid save-action identity"}
	}
	body, bad := edit.encodeWith(editCaps{}) // 12g's grammar: one open document
	if bad != nil {
		return 0, encodeErr(bad)
	}
	reply, err := e.RequestContext(ctx, "save-apply", body, itoa(requestID), itoa(int64(doc)), itoa(version), itoa(int64(len(body))))
	if err != nil {
		return 0, err
	}
	if len(reply.Args) != 1 || !saveDecimal(reply.Args[0]) {
		return 0, fmt.Errorf("invalid save-apply reply")
	}
	current, err := strconv.ParseInt(reply.Args[0], 10, 64)
	if err != nil || current < version {
		return 0, fmt.Errorf("invalid save-apply version")
	}
	return current, nil
}
