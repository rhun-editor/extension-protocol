package rhunext

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func taskTestConnection(t *testing.T) (*Ext, wireWriter) {
	t.Helper()
	wire := make(wireWriter, 16)
	e := testConnection(t, wire)
	e.agreed = map[string]bool{FeatureTasks: true}
	return e, wire
}
func taskTestReply(t *testing.T, e *Ext, s string) {
	t.Helper()
	if _, err := e.readMessage(bufio.NewReader(strings.NewReader(s))); err != nil {
		t.Fatal(err)
	}
}
func TestTaskSpecValidation(t *testing.T) {
	for _, s := range []TaskSpec{{"Build", "go build ./...", ""}, {"日本語 😀", "printf 'hi; there'", ""}, {strings.Repeat("a", 128), strings.Repeat("x", 4096), ""}} {
		s.Workspace = "/p"
		if err := s.Validate(); err != nil {
			t.Fatal(s, err)
		}
	}
	for _, s := range []TaskSpec{{"", "x", ""}, {"x", "", ""}, {strings.Repeat("x", 129), "x", ""}, {"x", strings.Repeat("x", 4097), ""}, {"x\n", "x", ""}, {"x", "a\x00b", ""}, {"x", "a\t b", ""}, {"x", "\xff", ""}, {"x", "\xed\xa0\x80", ""}, {"x", "\xf4\x90\x80\x80", ""}, {"x", "x\u202ey", ""}, {"x", "x\u200cy", ""}, {"x", "x\u2060y", ""}, {"x", "x\ufeffy", ""}, {"x", "\x7f", ""}, {"x", "\u0080", ""}} {
		s.Workspace = "/p"
		if err := s.Validate(); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
func TestRunTaskWireAndHandle(t *testing.T) {
	e, wire := taskTestConnection(t)
	done := make(chan error, 1)
	go func() {
		id, err := e.RunTask(TaskSpec{Label: "Build 日本語", Command: "printf 'quoted value'", Workspace: "/p"})
		if err == nil && id != 73 {
			err = fmt.Errorf("id %d", id)
		}
		done <- err
	}()
	fields := strings.Fields(<-wire)
	if len(fields) != 6 || fields[0] != "1" || fields[1] != "task-run" || fields[3] != "2" || fields[4] != "15" || fields[5] != "38" {
		t.Fatal(fields)
	}
	if n, ok := taskUnsigned(fields[2], math.MaxInt64); !ok || n == 0 {
		t.Fatal(fields)
	}
	if body := <-wire; body != "/pBuild 日本語printf 'quoted value'" {
		t.Fatal(body)
	}
	taskTestReply(t, e, "ok 1 73\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestTaskGuardsDoNotWrite(t *testing.T) {
	e, wire := taskTestConnection(t)
	if _, err := e.RunTask(TaskSpec{Label: "", Command: "x", Workspace: "/p"}); err == nil {
		t.Fatal("empty label")
	}
	if _, err := e.RestartTask(0); err == nil {
		t.Fatal("zero restart")
	}
	if err := e.StopTask(-1); err == nil {
		t.Fatal("negative stop")
	}
	if _, err := e.TaskStatus(0); err == nil {
		t.Fatal("zero status")
	}
	if _, err := e.TaskOutput(0); err == nil {
		t.Fatal("zero output")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.RunTaskContext(ctx, TaskSpec{Label: "x", Command: "x", Workspace: "/p"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	e.agreed = nil
	if _, err := e.RunTask(TaskSpec{Label: "x", Command: "x", Workspace: "/p"}); err == nil {
		t.Fatal("not negotiated")
	}
	if len(wire) != 0 || pendingCount(e) != 0 {
		t.Fatal("invalid request changed transport")
	}
}
func TestTaskControlsAndStatus(t *testing.T) {
	e, wire := taskTestConnection(t)
	for _, tc := range []struct {
		verb string
		call func() error
	}{{"task-stop", func() error { return e.StopTask(7) }}, {"task-show", func() error { return e.ShowTaskOutput(7) }}} {
		done := make(chan error, 1)
		go func() { done <- tc.call() }()
		f := strings.Fields(<-wire)
		if len(f) != 3 || f[1] != tc.verb || f[2] != "7" {
			t.Fatal(f)
		}
		taskTestReply(t, e, "ok "+f[0]+"\n")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		s, err := e.TaskStatus(7)
		if err == nil && s != (TaskStatus{TaskExited, 3, 99}) {
			err = fmt.Errorf("%+v", s)
		}
		done <- err
	}()
	f := strings.Fields(<-wire)
	if len(f) != 3 || f[1] != "task-status" || f[2] != "7" {
		t.Fatal(f)
	}
	taskTestReply(t, e, "ok "+f[0]+" exited 3 99\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestTaskStatusAndOutputDecoders(t *testing.T) {
	for _, state := range []TaskState{TaskQueued, TaskRunning, TaskStopping, TaskExited, TaskFailed} {
		exit := "-1"
		if state == TaskExited {
			exit = "255"
		}
		s, err := decodeTaskStatus(Reply{Args: []string{string(state), exit, "18446744073709551615"}})
		if err != nil || s.Total != math.MaxUint64 || s.Terminal() != (state == TaskExited || state == TaskFailed) {
			t.Fatal(s, err)
		}
	}
	for _, args := range [][]string{{}, {"running", "0", "0"}, {"running", "-1", "+0"}, {"exited", "256", "0"}, {"exited", "-1", "0"}, {"failed", "0", "0"}, {"failed", "-4096", "0"}, {"failed", "-0", "0"}, {"failed", "-01", "0"}, {"running", "-1", "00"}, {"unknown", "-1", "0"}, {"running", "-1", "18446744073709551616"}} {
		if _, err := decodeTaskStatus(Reply{Args: args}); err == nil {
			t.Fatal(args)
		}
	}
	data := []byte{'a', 0, 255, '\n'}
	out, err := decodeTaskOutput(Reply{Args: []string{"10", "14", "4"}, Body: data})
	if err != nil || out.Offset != 10 || out.Total != 14 || !reflect.DeepEqual(out.Data, data) {
		t.Fatal(out, err)
	}
	if _, err := decodeTaskOutput(Reply{Args: []string{"18446744073709551611", "18446744073709551615", "4"}, Body: data}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Reply{{Args: []string{"0", "3", "4"}, Body: data}, {Args: []string{"15", "14", "4"}, Body: data}, {Args: []string{"0", "0", "0"}, Body: data}, {Args: []string{"0", "4", "+4"}, Body: data}, {Args: []string{"0", "4", "04"}, Body: data}, {Args: []string{"0", "262145", "262145"}, Body: make([]byte, MaxTaskOutput+1)}, {Args: []string{"0", "18446744073709551616", "4"}, Body: data}, {Args: []string{"0", "4", "4", "extra"}, Body: data}} {
		if _, err := decodeTaskOutput(r); err == nil {
			t.Fatal(r.Args)
		}
	}
}
func TestTaskCancelledPromptStopsCommittedNonce(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(strconv.FormatBool(restart), func(t *testing.T) {
			e, wire := taskTestConnection(t)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				var err error
				if restart {
					_, err = e.RestartTaskContext(ctx, 7)
				} else {
					_, err = e.RunTaskContext(ctx, TaskSpec{Label: "Build", Command: "echo done", Workspace: "/p"})
				}
				done <- err
			}()
			f := strings.Fields(<-wire)
			nonce := f[2]
			if !restart {
				<-wire
			}
			cancel()
			cleanup := strings.Fields(<-wire)
			if len(cleanup) != 3 || cleanup[1] != "task-cancel" || cleanup[2] != nonce {
				t.Fatal(cleanup)
			}
			// Confirmation may already have launched the task. Its late success does not
			// release cancellation until the nonce cleanup has also been acknowledged.
			taskTestReply(t, e, "ok "+f[0]+" 41\n")
			taskTestReply(t, e, "ok "+cleanup[0]+"\n")
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if pendingCount(e) != 0 || bodyCount(e) != 0 {
				t.Fatal("cancellation retained pending framing")
			}
		})
	}
}
func TestTaskCancellationCleanupFailureRetiresConnection(t *testing.T) {
	e, wire := taskTestConnection(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.RunTaskContext(ctx, TaskSpec{Label: "Build", Command: "echo hi", Workspace: "/p"})
		done <- err
	}()
	<-wire
	<-wire
	cancel()
	f := strings.Fields(<-wire)
	taskTestReply(t, e, "error "+f[0]+" unknown missing\n")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-e.done:
	default:
		t.Fatal("cleanup failure did not close connection")
	}
}
func TestTaskMalformedSuccessRetiresConnection(t *testing.T) {
	for _, success := range []string{"ok 1 0\n", "ok 1 +7\n", "ok 1 07\n", "ok 1 9223372036854775808\n", "ok 1 7 extra\n"} {
		e, wire := taskTestConnection(t)
		done := make(chan error, 1)
		go func() {
			_, err := e.RunTask(TaskSpec{Label: "Build", Command: "echo hi", Workspace: "/p"})
			done <- err
		}()
		<-wire
		<-wire
		taskTestReply(t, e, success)
		if err := <-done; err == nil {
			t.Fatal(success)
		}
		select {
		case <-e.done:
		default:
			t.Fatal("malformed success retained unknown launch ownership")
		}
	}
}

func TestTaskLaunchNonceExhaustionDoesNotWrap(t *testing.T) {
	previous := taskLaunchNonce.Swap(math.MaxInt64)
	defer taskLaunchNonce.Store(previous)
	e, wire := taskTestConnection(t)
	if _, err := e.RunTask(TaskSpec{Label: "Build", Command: "true", Workspace: "/p"}); err == nil {
		t.Fatal("nonce wrapped")
	}
	if len(wire) != 0 || taskLaunchNonce.Load() != math.MaxInt64 {
		t.Fatal("exhaustion changed wire or identity")
	}
}

func TestTaskWorkspaceValidation(t *testing.T) {
	for _, workspace := range []string{"", strings.Repeat("x", 4096), "/p\x00other", "/p\xff"} {
		if err := (TaskSpec{Label: "Build", Command: "true", Workspace: workspace}).Validate(); err == nil {
			t.Fatal("invalid workspace accepted")
		}
	}
}
