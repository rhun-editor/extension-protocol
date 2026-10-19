package rhunext

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTaskOutputTransportCapAndLateBody(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			e, wire := taskTestConnection(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := e.TaskOutputContext(ctx, 7); done <- err }()
			if line := <-wire; line != "1 task-output 7\n" {
				t.Fatal(line)
			}
			if cancelled {
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			if _, err := e.readMessage(bufio.NewReader(strings.NewReader("ok 1 0 262145 262145\n"))); err == nil || !strings.Contains(err.Error(), "length") {
				t.Fatal("oversized task output was not rejected before body read", err)
			}
			e.closeConnection(fmt.Errorf("invalid host framing"))
			if !cancelled {
				if err := <-done; err == nil {
					t.Fatal("invalid output succeeded")
				}
			}
		})
	}
	e, wire := taskTestConnection(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.TaskOutputContext(ctx, 7); done <- err }()
	<-wire
	cancel()
	<-done
	const body = "0 bye\nok 2 evil\n"
	br := bufio.NewReader(strings.NewReader(fmt.Sprintf("ok 1 20 %d %d\n%s0 active 7\n", 20+len(body), len(body), body)))
	if _, err := e.readMessage(br); err != nil {
		t.Fatal(err)
	}
	event, err := e.readMessage(br)
	if err != nil || event == nil || event.Name != "active" {
		t.Fatal(event, err)
	}
	if bodyCount(e) != 0 || pendingCount(e) != 0 || len(e.replyBodyCaps) != 0 {
		t.Fatal("late output retained framing state")
	}
}

func TestTaskReadyPreservesWorkspaceSpaces(t *testing.T) {
	e, _ := taskTestConnection(t)
	const workspace = "/workspace  日本語 😀 "
	ev, err := e.readMessage(bufio.NewReader(strings.NewReader("0 ready " + workspace + "\n")))
	if err != nil || ev == nil || ev.Name != "ready" || len(ev.Args) != 1 || ev.Args[0] != workspace {
		t.Fatal(ev, err)
	}
}
func TestTaskOutputRequestFraming(t *testing.T) {
	e, wire := taskTestConnection(t)
	done := make(chan error, 1)
	const body = "a\x00\xff\n"
	go func() {
		out, err := e.TaskOutput(7)
		if err == nil && (out.Offset != 10 || out.Total != 14 || string(out.Data) != body) {
			err = fmt.Errorf("unexpected task output %+v", out)
		}
		done <- err
	}()
	if got := <-wire; got != "1 task-output 7\n" {
		t.Fatal(got)
	}
	taskTestReply(t, e, "ok 1 10 14 4\n"+body)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
