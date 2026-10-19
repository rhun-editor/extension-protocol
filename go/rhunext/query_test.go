package rhunext

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

type queryProbe struct {
	cancelled chan bool
}

func (q *queryProbe) Ready(ext *Ext, project string)       {}
func (q *queryProbe) Event(ext *Ext, ev Event)             {}
func (q *queryProbe) Run(ext *Ext, command string, doc Doc) {}

func (q *queryProbe) LocationsStream(ext *Ext, kind string, req PosRequest, out *Stream) []Location {
	if err := out.Progress(40, "Indexing\nall"); err != nil {
		return nil
	}
	if err := out.PartLocations([]Location{{Target: Target{Doc: req.Doc}, Range: rg(1, 1, 1, 2), Text: "a"}}); err != nil {
		q.cancelled <- true
		return nil
	}
	return []Location{{Target: Target{Doc: req.Doc}, Range: rg(2, 1, 2, 2), Text: "b"}}
}

func (q *queryProbe) Complete(ext *Ext, req Request) CompletionList {
	repl := rg(2, 5, 2, 9)
	return CompletionList{Items: []CompletionItem{{Range: rg(2, 5, 2, 7), Label: "print", Resolve: []byte("r1"),
		ResolveEdits: true, Replace: &repl, Commit: "..(\x01", Filter: "prt", Suffix: "(s)", Preselect: true,
		Deprecated: true, Adjust: true, AfterSignature: true}}}
}

func (q *queryProbe) ResolveCompletion(ext *Ext, req ResolveRequest) Resolution {
	return Resolution{Doc: "Docs " + string(req.Data), Detail: "fn print()", HasDetail: true,
		Edits: []TextEdit{{Range: rg(1, 1, 1, 1), Text: "use x;\n"}}}
}

func (q *queryProbe) Signatures(ext *Ext, req Request) *SignatureList {
	return &SignatureList{Signatures: []SignatureInfo{{Label: "f(a)", ActiveFrom: 2, ActiveTo: 3}, {Label: "f(a, b)", ActiveFrom: 5, ActiveTo: 6, Doc: "two"}, {Label: "f()", ActiveFrom: 9, ActiveTo: 12}}, Active: 1}
}

func TestQueryStreamPartsAndProgress(t *testing.T) {
	q := &queryProbe{cancelled: make(chan bool, 1)}
	s := startHost(t, []string{FeatureReferences, FeatureQueryStream}, q)
	s.expect("hello 1 references query-stream")
	s.send("0 features references query-stream\n0 ready -\n")
	s.send("7 references 4 2 1 1 invoked -\n")
	s.expect("1 query-progress 7 40 12")
	b := make([]byte, 12)
	io.ReadFull(s.br, b)
	if string(b) != "Indexing all" {
		t.Fatalf("progress text %q", b)
	}
	s.send("ok 1\n")
	part := list(locRec("1 1 1 2", "#4", "a"))
	s.expect(fmt.Sprintf("2 query-part 7 %d", len(part)))
	b = make([]byte, len(part))
	io.ReadFull(s.br, b)
	if string(b) != part {
		t.Fatalf("part %q", b)
	}
	s.send("ok 2\n")
	s.answer(7, list(locRec("2 1 2 2", "#4", "b")))
	// a part answered cancelled stops the handler
	s.send("8 definition 4 2 1 1 invoked -\n")
	s.expect("3 query-progress 8 40 12")
	io.ReadFull(s.br, b[:12])
	s.send("ok 3\n")
	s.line()
	io.ReadFull(s.br, make([]byte, len(part)))
	s.send("error 4 cancelled\n")
	<-q.cancelled
	s.answer(8, "0\n")
}

func TestStreamWithoutTheFeatureIsRefusedLocally(t *testing.T) {
	q := &queryProbe{cancelled: make(chan bool, 1)}
	s := startHost(t, []string{FeatureReferences}, q)
	s.expect("hello 1 references")
	s.send("0 features references\n0 ready -\n")
	s.send("7 references 4 2 1 1 invoked -\n")
	s.answer(7, "0\n") // Progress returned ErrNoStream: the handler gave up
}

func TestCompleteItemsRecordsAndResolve(t *testing.T) {
	q := &queryProbe{}
	s := startHost(t, []string{FeatureComplete, FeatureCompleteItems, FeatureSignatureList}, q)
	s.expect("hello 1 complete complete-items signature-list")
	s.send("0 features complete complete-items signature-list\n0 ready -\n")
	s.send("3 complete 4 2 2 7 invoked -\n")
	extra := "resolve 1 2\nr1replace 2 5 2 9 0\ncommit 2\n.(filter 3\nprtsuffix 3\n(s)preselect 0\n" +
		"deprecated 0\nadjust 0\nafter signature 0\n"
	item := fmt.Sprintf("2 5 2 7 text 5 5 0 %d\nprintprint%s", len(extra), extra)
	s.answer(3, "1 0\n"+item)
	s.send("4 complete-resolve 4 2 2\nr1")
	s.answer(4, "detail 10\nfn print()doc 7\nDocs r1edit 1 1 1 1 7\nuse x;\n")
	s.send("5 signatures 4 2 2 7 invoked -\n")
	s.answer(5, "3 1\n2 3 4 0\nf(a)5 6 7 3\nf(a, b)two-1 -1 3 0\nf()")
}

func TestProvidesNeedsTheFeature(t *testing.T) {
	type probe struct{ queryProbe }
	got := make(chan error, 2)
	h := &readyHook{fn: func(ext *Ext) {
		got <- ext.Provides("definition", "*.go")
		got <- ext.Withdraw("references")
	}}
	s := startHost(t, []string{FeatureProviderScope}, h)
	s.expect("hello 1 provider-scope")
	s.send("0 features provider-scope\n0 ready -\n")
	s.expect("1 provides definition *.go")
	s.send("ok 1\n")
	s.expect("2 provides references -")
	s.send("error 2 refused no\n")
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	if err := <-got; err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("withdraw: %v", err)
	}
	s2 := startHost(t, nil, &readyHook{fn: func(ext *Ext) { got <- ext.Provides("definition", "*.go") }})
	s2.expect("hello 1")
	s2.send("0 ready -\n")
	if err := <-got; err == nil {
		t.Fatal("provides without the feature was sent")
	}
}

type readyHook struct{ fn func(*Ext) }

func (r *readyHook) Ready(ext *Ext, project string)       { go r.fn(ext) }
func (r *readyHook) Event(ext *Ext, ev Event)             {}
func (r *readyHook) Run(ext *Ext, command string, doc Doc) {}
