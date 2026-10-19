package rhunext

import (
	"context"
	"fmt"
	"strconv"
)

const FeatureWorkspaceWatch = "workspace_watch"

// WorkspaceWatch identifies one runtime's subscription to workspace invalidations.
// Notifications are coalesced. Both changed and rescan require reconciliation
// against disk; neither claims to list every intermediate filesystem operation.
type WorkspaceWatch int64

func (e *Ext) WatchWorkspace() (WorkspaceWatch, error) {
	return e.WatchWorkspaceContext(context.Background())
}

func (e *Ext) WatchWorkspaceContext(ctx context.Context) (WorkspaceWatch, error) {
	if !e.Has(FeatureWorkspaceWatch) {
		return 0, &Error{Code: "unknown", Text: "workspace_watch was not negotiated"}
	}
	r, err := e.RequestContext(ctx, "watch-workspace", nil)
	if err != nil {
		return 0, err
	}
	if len(r.Args) != 1 {
		return 0, fmt.Errorf("rhun: invalid workspace watch reply")
	}
	id, valid := parseWorkspaceWatchHandle(r.Args[0])
	if !valid {
		return 0, fmt.Errorf("rhun: invalid workspace watch handle")
	}
	return WorkspaceWatch(id), nil
}

func (e *Ext) UnwatchWorkspace(watch WorkspaceWatch) error {
	return e.UnwatchWorkspaceContext(context.Background(), watch)
}

func (e *Ext) UnwatchWorkspaceContext(ctx context.Context, watch WorkspaceWatch) error {
	if !e.Has(FeatureWorkspaceWatch) || watch <= 0 || watch > 2147483647 {
		return &Error{Code: "syntax", Text: "invalid workspace watch"}
	}
	_, err := e.RequestContext(ctx, "unwatch-workspace", nil, strconv.FormatInt(int64(watch), 10))
	return err
}

func ParseWorkspaceWatch(ev Event) (WorkspaceWatch, string, bool) {
	if ev.Name != "watch" || len(ev.Args) != 2 || (ev.Args[1] != "changed" && ev.Args[1] != "rescan") {
		return 0, "", false
	}
	id, valid := parseWorkspaceWatchHandle(ev.Args[0])
	if !valid {
		return 0, "", false
	}
	return WorkspaceWatch(id), ev.Args[1], true
}

func parseWorkspaceWatchHandle(raw string) (int64, bool) {
	if len(raw) == 0 || len(raw) > 10 {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, 32)
	return id, err == nil && id > 0
}
