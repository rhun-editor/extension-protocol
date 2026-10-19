package rhunext

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func snapshotReply(parts [6]string) Reply {
	body := "context 1 2 3 8 0 1"
	for _, s := range parts {
		body += fmt.Sprintf(" %d", len(s))
	}
	body += "\n" + strings.Join(parts[:], "")
	return Reply{Args: []string{"9", strconv.Itoa(len(body))}, Body: []byte(body)}
}

func TestSnippetSnapshotStrictDecoder(t *testing.T) {
	parts := [6]string{"😀\n\x00", "\t xy😀", "/p/a.go", "/p", "\t ", "//"}
	r := snapshotReply(parts)
	want := SnippetSnapshot{Version: 9, Position: Pos{2, 3}, TabWidth: 8, CRLF: true, SelectedText: parts[0], CurrentLine: parts[1], Path: parts[2], Workspace: parts[3], Indent: parts[4], LineComment: parts[5]}
	got, err := decodeSnippetSnapshot(r, 9, Pos{2, 3})
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	for name, change := range map[string]func(*Reply){
		"args missing":     func(r *Reply) { r.Args = r.Args[:1] },
		"args extra":       func(r *Reply) { r.Args = append(r.Args, "extra") },
		"version sign":     func(r *Reply) { r.Args[0] = "+9" },
		"version mismatch": func(r *Reply) { r.Args[0] = "10" },
		"outer length":     func(r *Reply) { r.Args[1] = "0" },
		"length overflow":  func(r *Reply) { r.Args[1] = "9223372036854775808" },
		"trailing bytes":   func(r *Reply) { r.Body = append(r.Body, 'x') },
		"truncated":        func(r *Reply) { r.Body = r.Body[:len(r.Body)-1] },
		"double spaces":    func(r *Reply) { r.Body = []byte(strings.Replace(string(r.Body), "1 2", "1  2", 1)) },
		"signed inner":     func(r *Reply) { r.Body = []byte(strings.Replace(string(r.Body), " 8 ", " +8 ", 1)) },
		"inner overflow":   func(r *Reply) { r.Body = []byte(strings.Replace(string(r.Body), " 8 ", " 9223372036854775808 ", 1)) },
		"tab width":        func(r *Reply) { r.Body = []byte(strings.Replace(string(r.Body), " 8 ", " 17 ", 1)) },
		"spaces bool":      func(r *Reply) { r.Body = []byte(strings.Replace(string(r.Body), "8 0 1", "8 2 1", 1)) },
		"crlf bool":        func(r *Reply) { r.Body = []byte(strings.Replace(string(r.Body), "8 0 1", "8 0 2", 1)) },
		"wrong position":   func(r *Reply) { r.Body = []byte(strings.Replace(string(r.Body), "1 2 3", "1 2 4", 1)) },
		"bad utf8":         func(r *Reply) { p := parts; p[0] = "\xff"; *r = snapshotReply(p) },
		"line newline":     func(r *Reply) { p := parts; p[1] = "\t \nx"; *r = snapshotReply(p) },
		"short line":       func(r *Reply) { p := parts; p[1] = ""; *r = snapshotReply(p) },
		"midcodepoint":     func(r *Reply) { p := parts; p[1] = "😀"; p[4] = ""; *r = snapshotReply(p) },
		"bad indent":       func(r *Reply) { p := parts; p[4] = "\t"; *r = snapshotReply(p) },
		"header limit":     func(r *Reply) { r.Body = []byte(strings.Repeat("0", 257) + "\n") },
	} {
		t.Run(name, func(t *testing.T) {
			r := snapshotReply(parts)
			change(&r)
			// Keep outer LEN valid to exercise inner decoder except deliberate outer cases.
			if name != "outer length" && name != "length overflow" {
				r.Args[len(r.Args)-1] = strconv.Itoa(len(r.Body))
			}
			got, err := decodeSnippetSnapshot(r, 9, Pos{2, 3})
			if err == nil || got != (SnippetSnapshot{}) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestSnippetSnapshotRequestAndGuards(t *testing.T) {
	wire := make(wireWriter, 8)
	e := testConnection(t, wire)
	e.agreed = map[string]bool{FeatureSnippets: true}
	span := Range{Pos{2, 3}, Pos{3, 1}}
	done := make(chan error, 1)
	go func() {
		s, err := e.SnippetSnapshot(7, 9, span)
		if err == nil && s.SelectedText != "hello\n" {
			err = fmt.Errorf("%+v", s)
		}
		done <- err
	}()
	if got := <-wire; got != "1 snippet-context 7 9 2 3 3 1\n" {
		t.Fatal(got)
	}
	r := snapshotReply([6]string{"hello\n", "  hello", "/p/a", "/p", "  ", "#"})
	if _, err := e.readMessage(bufio.NewReader(strings.NewReader(fmt.Sprintf("ok 1 9 %d\n%s", len(r.Body), r.Body)))); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		doc Doc
		v   int64
		r   Range
	}{{0, 9, span}, {7, -1, span}, {7, 9, Range{}}, {7, 9, Range{Pos{2, 3}, Pos{2, 2}}}} {
		if _, err := e.SnippetSnapshot(test.doc, test.v, test.r); err == nil {
			t.Fatal(test)
		}
	}
	e.agreed = nil
	if _, err := e.SnippetSnapshot(7, 9, span); err == nil {
		t.Fatal("not negotiated")
	}
	if len(wire) != 0 || pendingCount(e) != 0 || e.nextID.Load() != 1 || len(e.replyBodyCaps) != 0 {
		t.Fatal("invalid request changed transport")
	}
}

func TestSnippetSnapshotCancellationAndTransportCap(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			wire := make(wireWriter, 8)
			e := testConnection(t, wire)
			e.agreed = map[string]bool{FeatureSnippets: true}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := e.SnippetSnapshotContext(ctx, 7, 9, Range{Pos{1, 1}, Pos{1, 1}}); done <- err }()
			<-wire
			if cancelled {
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			// No body is sent: an oversized advertised length must fail immediately.
			if _, err := e.readMessage(bufio.NewReader(strings.NewReader("ok 1 9 4194305\n"))); err == nil {
				t.Fatal("transport cap ignored")
			}
			e.closeConnection(fmt.Errorf("invalid host framing"))
			if !cancelled {
				if err := <-done; err == nil {
					t.Fatal("bad reply succeeded")
				}
			}
			if len(e.replyBodyCaps) != 0 {
				t.Fatal("retained caps")
			}
		})
	}
	wire := make(wireWriter, 8)
	e := testConnection(t, wire)
	e.agreed = map[string]bool{FeatureSnippets: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.SnippetSnapshotContext(ctx, 7, 9, Range{Pos{1, 1}, Pos{1, 1}}); done <- err }()
	<-wire
	cancel()
	<-done
	const body = "0 bye\nok 2 wrong\n"
	br := bufio.NewReader(strings.NewReader(fmt.Sprintf("ok 1 9 %d\n%s0 active 7\n", len(body), body)))
	if _, err := e.readMessage(br); err != nil {
		t.Fatal(err)
	}
	ev, err := e.readMessage(br)
	if err != nil || ev == nil || ev.Name != "active" || !reflect.DeepEqual(ev.Args, []string{"7"}) {
		t.Fatal(ev, err)
	}
	if bodyCount(e) != 0 || len(e.replyBodyCaps) != 0 {
		t.Fatal("late reply retained state")
	}
}
