package rhunext

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type saveProbe struct {
	ready     chan *Ext
	requests  chan SaveActionsRequest
	runSave   func(*Ext, SaveActionsRequest) (WorkspaceEdit, error)
	readyGate chan struct{}
}

func (p *saveProbe) Ready(e *Ext, _ string) {
	p.ready <- e
	if p.readyGate != nil {
		<-p.readyGate
	}
}
func (p *saveProbe) Run(*Ext, string, Doc) {}
func (p *saveProbe) Event(*Ext, Event)     {}
func (p *saveProbe) SaveActions(e *Ext, req SaveActionsRequest) (WorkspaceEdit, error) {
	p.requests <- req
	if p.runSave != nil {
		return p.runSave(e, req)
	}
	return WorkspaceEdit{}, nil
}
func newSaveProbe() *saveProbe {
	return &saveProbe{ready: make(chan *Ext, 1), requests: make(chan SaveActionsRequest, 4)}
}
func startSaveHost(t *testing.T, p *saveProbe) (*host, *Ext) {
	s := startHost(t, []string{FeatureSaveActions}, p)
	s.expect("hello 1 save-actions")
	s.send("0 features save-actions\n0 ready /project\n")
	var ext *Ext
	select {
	case ext = <-p.ready:
	case <-time.After(time.Second):
		t.Fatal("no Ready")
	}
	return s, ext
}
func stopSaveHost(t *testing.T, s *host) {
	t.Helper()
	s.send("0 bye\n")
	select {
	case err := <-s.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not stop")
	}
}

func TestSourceKindAndSaveRequestValidation(t *testing.T) {
	for _, kind := range []string{"source", "source.organizeImports", "source.fixAll.eslint", "source.a_b-c.1", "source." + strings.Repeat("x", 121)} {
		if !validSourceKind(kind) {
			t.Fatal(kind)
		}
	}
	for _, kind := range []string{"", "quickfix", "source.", "source..x", "source.x.", "source.x y", "source.é", "Source.x", "source." + strings.Repeat("x", 122)} {
		if validSourceKind(kind) {
			t.Fatal(kind)
		}
	}
	for _, fields := range [][]string{{"0", "1", "source"}, {"1", "-1", "source"}, {"1", "1", "quickfix"}, {"1", "1", "source", "extra"}, {"1", "1"}} {
		if _, ok := parseSaveActions(24, fields, context.Background()); ok {
			t.Fatal(fields)
		}
	}
	req, ok := parseSaveActions(24, []string{"7", "9", "source.fixAll"}, context.Background())
	if !ok || req.ID != 24 || req.Doc != 7 || req.Version != 9 || req.Kind != "source.fixAll" || req.Context == nil {
		t.Fatalf("%+v", req)
	}
	if _, ok := parseSaveActions(2147483648, []string{"7", "9", "source"}, context.Background()); ok {
		t.Fatal("out-of-range request ID")
	}
}

func TestSaveActionsHandlerAndIntermediateCredit(t *testing.T) {
	p := newSaveProbe()
	credit := make(chan int64, 1)
	p.runSave = func(e *Ext, req SaveActionsRequest) (WorkspaceEdit, error) {
		v, err := e.SaveApplyContext(req.Context, req.ID, req.Doc, req.Version, WorkspaceEdit{})
		credit <- v
		return WorkspaceEdit{}, err
	}
	s, e := startSaveHost(t, p)
	s.send("24 save-actions 7 9 source.fixAll\n")
	req := <-p.requests
	s.expect("1 save-apply 24 7 9 2")
	body := make([]byte, 2)
	if _, err := io.ReadFull(s.br, body); err != nil || string(body) != "0\n" {
		t.Fatalf("%q %v", body, err)
	}
	s.send("ok 1 10\n")
	s.answer(24, "0\n")
	if got := <-credit; got != 10 {
		t.Fatal(got)
	}
	waitTest(t, func() bool { return req.Context.Err() != nil })
	e.sourceMu.Lock()
	remaining := len(e.sourceRequests)
	e.sourceMu.Unlock()
	if remaining != 0 {
		t.Fatal("completed source context retained")
	}
	stopSaveHost(t, s)
}

func TestSourceCancellationReachesBlockedHandlerAndDoesNotAffectNextRequest(t *testing.T) {
	p := newSaveProbe()
	p.runSave = func(_ *Ext, req SaveActionsRequest) (WorkspaceEdit, error) {
		if req.ID == 24 {
			<-req.Context.Done()
			return WorkspaceEdit{}, req.Context.Err()
		}
		return WorkspaceEdit{}, nil
	}
	s, _ := startSaveHost(t, p)
	s.send("24 save-actions 7 9 source.fixAll\n")
	first := <-p.requests
	s.send("0 cancel 24\n25 save-actions 7 9 source.organizeImports\n")
	s.expect("error 24 cancelled save was cancelled")
	s.answer(25, "0\n")
	second := <-p.requests
	if first.Context.Err() == nil || second.ID != 25 {
		t.Fatal("wrong cancellation/request identity")
	}
	stopSaveHost(t, s)
}

func TestQueuedCancelledSourceRequestNeverCallsProvider(t *testing.T) {
	p := newSaveProbe()
	p.readyGate = make(chan struct{})
	s, e := startSaveHost(t, p)
	s.send("24 save-actions 7 9 source.fixAll\n0 cancel 24\n")
	waitTest(t, func() bool { s := e.sourceFor(24); return s != nil && s.ctx.Err() != nil })
	close(p.readyGate)
	s.expect("error 24 cancelled save was cancelled")
	select {
	case <-p.requests:
		t.Fatal("called cancelled queued provider")
	default:
	}
	stopSaveHost(t, s)
}

func TestByeCancelsRunningSourceBeforeHandlerProcessesByeEvent(t *testing.T) {
	p := newSaveProbe()
	p.runSave = func(_ *Ext, req SaveActionsRequest) (WorkspaceEdit, error) {
		<-req.Context.Done()
		return WorkspaceEdit{}, req.Context.Err()
	}
	s, _ := startSaveHost(t, p)
	s.send("24 save-actions 7 9 source.fixAll\n")
	<-p.requests
	s.send("0 bye\n")
	s.expect("error 24 cancelled save was cancelled")
	select {
	case err := <-s.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler remained blocked on Bye")
	}
}

func TestSaveApplyNegotiationAndIdentityGuardsSendNothing(t *testing.T) {
	e := testConnection(t, io.Discard)
	if _, err := e.SaveApply(24, 7, 9, WorkspaceEdit{}); err == nil {
		t.Fatal("unnegotiated save-apply")
	}
	e.agreed = map[string]bool{FeatureSaveActions: true}
	for _, ids := range [][3]int64{{0, 7, 9}, {2147483648, 7, 9}, {24, 0, 9}, {24, 7, -1}} {
		_, err := e.SaveApply(ids[0], Doc(ids[1]), ids[2], WorkspaceEdit{})
		if re, ok := err.(*Error); !ok || re.Code != "syntax" {
			t.Fatal(err)
		}
	}
	if pendingCount(e) != 0 || e.nextID.Load() != 0 {
		t.Fatal("invalid request sent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.SaveApplyContext(ctx, 24, 7, 9, WorkspaceEdit{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSourceContextLimitIncludesCancelledQueuedRequests(t *testing.T) {
	e := testConnection(t, io.Discard)
	for id := int64(1); id <= maxSourceRequests; id++ {
		if _, err := e.beginSource(id); err != nil {
			t.Fatal(err)
		}
		e.cancelSource(id)
	}
	if _, err := e.beginSource(129); err == nil {
		t.Fatal("unbounded queued source contexts")
	}
	old := e.sourceFor(1)
	e.finishSource(old)
	current, err := e.beginSource(1)
	if err != nil {
		t.Fatal(err)
	}
	e.finishSource(old)
	if e.sourceFor(1) != current {
		t.Fatal("old completion deleted a reused request ID")
	}
}
