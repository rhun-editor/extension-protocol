package rhunext

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const FeatureTasks = "tasks"
const MaxTaskOutput = 256 << 10

// Task identifies one managed run for this extension runtime. Restart creates a new handle.
type Task int64

// TaskSpec runs one shell command in the current workspace after native command review.
// Workspace must equal the captured project path from Ready or a project event.
// The host checks that identity before review and its generation again on confirmation.
// The host chooses the platform task shell; Command is passed as one shell argument.
type TaskSpec struct{ Label, Command, Workspace string }

// Validate checks the same length, UTF-8 and visible-text rules required by the host.
func (s TaskSpec) Validate() error {
	if len(s.Workspace) == 0 || len(s.Workspace) > 4095 || !utf8.ValidString(s.Workspace) || strings.ContainsRune(s.Workspace, 0) {
		return &Error{Code: "syntax", Text: "invalid task workspace"}
	}
	if !taskPlainText(s.Label, 128) || !taskPlainText(s.Command, 4096) {
		return &Error{Code: "syntax", Text: "invalid task label or command"}
	}
	return nil
}
func taskPlainText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r >= 0x7f && r < 0xa0 || r >= 0x200b && r <= 0x200f || r >= 0x202a && r <= 0x202e || r == 0x2060 || r >= 0x2066 && r <= 0x2069 || r == 0xfeff {
			return false
		}
	}
	return true
}

var taskLaunchNonce atomic.Int64

func nextTaskNonce() (int64, error) {
	for {
		n := taskLaunchNonce.Load()
		if n == math.MaxInt64 {
			return 0, &Error{Code: "busy", Text: "task launch nonce exhausted"}
		}
		if taskLaunchNonce.CompareAndSwap(n, n+1) {
			return n + 1, nil
		}
	}
}
func (e *Ext) taskAgreed() error {
	if !e.Has(FeatureTasks) {
		return &Error{Code: "unknown", Text: "tasks was not negotiated"}
	}
	return nil
}
func taskHandleReply(r Reply) (Task, error) {
	if len(r.Args) != 1 || len(r.Body) != 0 {
		return 0, fmt.Errorf("invalid task handle reply")
	}
	n, ok := taskUnsigned(r.Args[0], math.MaxInt64)
	if !ok || n == 0 {
		return 0, fmt.Errorf("invalid task handle reply")
	}
	return Task(n), nil
}
func taskUnsigned(s string, max uint64) (uint64, bool) {
	if s == "" || len(s) > 20 || len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	for i := range s {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil && n <= max
}

func (e *Ext) RunTask(s TaskSpec) (Task, error) { return e.RunTaskContext(context.Background(), s) }
func (e *Ext) RunTaskContext(ctx context.Context, s TaskSpec) (Task, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := e.taskAgreed(); err != nil {
		return 0, err
	}
	if err := s.Validate(); err != nil {
		return 0, err
	}
	nonce, err := nextTaskNonce()
	if err != nil {
		return 0, err
	}
	body := []byte(s.Workspace + s.Label + s.Command)
	return e.taskPrompt(ctx, nonce, "task-run", body, strconv.FormatInt(nonce, 10), strconv.Itoa(len(s.Workspace)), strconv.Itoa(len(s.Label)), strconv.Itoa(len(body)))
}

// RestartTask reviews the command again and replaces the newest run in the task's family.
// A restart requested with an older retained handle still stops the current family member.
func (e *Ext) RestartTask(task Task) (Task, error) {
	return e.RestartTaskContext(context.Background(), task)
}
func (e *Ext) RestartTaskContext(ctx context.Context, task Task) (Task, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := e.taskAgreed(); err != nil {
		return 0, err
	}
	if task <= 0 {
		return 0, &Error{Code: "syntax", Text: "invalid task handle"}
	}
	nonce, err := nextTaskNonce()
	if err != nil {
		return 0, err
	}
	return e.taskPrompt(ctx, nonce, "task-restart", nil, strconv.FormatInt(nonce, 10), itoa(int64(task)))
}

// taskPrompt closes a canceled review and also stops a launch that won the confirmation race.
// Cancellation must not leave an unowned run after its handle reply was discarded.
func (e *Ext) taskPrompt(ctx context.Context, nonce int64, verb string, body []byte, args ...string) (Task, error) {
	r, err := e.RequestContext(ctx, verb, body, args...)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		ack, cleanupErr := e.RequestContext(cleanup, "task-cancel", nil, strconv.FormatInt(nonce, 10))
		cancel()
		if cleanupErr != nil || len(ack.Args) != 0 || len(ack.Body) != 0 {
			e.closeConnection(fmt.Errorf("task cancellation could not be acknowledged"))
		}
		return 0, err
	}
	if err != nil {
		return 0, err
	}
	task, err := taskHandleReply(r)
	if err != nil {
		// A malformed success may still refer to a launched run. Retiring the connection
		// gives the host an unambiguous owner-lifetime boundary for cleanup.
		e.closeConnection(err)
	}
	return task, err
}

func (e *Ext) StopTask(task Task) error { return e.StopTaskContext(context.Background(), task) }
func (e *Ext) StopTaskContext(ctx context.Context, task Task) error {
	return e.taskEmpty(ctx, "task-stop", task)
}
func (e *Ext) ShowTaskOutput(task Task) error {
	return e.ShowTaskOutputContext(context.Background(), task)
}
func (e *Ext) ShowTaskOutputContext(ctx context.Context, task Task) error {
	return e.taskEmpty(ctx, "task-show", task)
}
func (e *Ext) taskEmpty(ctx context.Context, verb string, task Task) error {
	if err := e.taskAgreed(); err != nil {
		return err
	}
	if task <= 0 {
		return &Error{Code: "syntax", Text: "invalid task handle"}
	}
	r, err := e.RequestContext(ctx, verb, nil, itoa(int64(task)))
	if err != nil {
		return err
	}
	if len(r.Args) != 0 || len(r.Body) != 0 {
		return fmt.Errorf("invalid %s reply", verb)
	}
	return nil
}

type TaskState string

const (
	TaskQueued   TaskState = "queued"
	TaskRunning  TaskState = "running"
	TaskStopping TaskState = "stopping"
	TaskExited   TaskState = "exited"
	TaskFailed   TaskState = "failed"
)

// TaskStatus becomes terminal only when the supervised lifetime is over and output is drained.
// Failed output reads also finalize after lifetime confirmation, with a negative Exit.
type TaskStatus struct {
	State TaskState
	Exit  int
	Total uint64
}

func (s TaskStatus) Terminal() bool { return s.State == TaskExited || s.State == TaskFailed }
func (e *Ext) TaskStatus(task Task) (TaskStatus, error) {
	return e.TaskStatusContext(context.Background(), task)
}
func (e *Ext) TaskStatusContext(ctx context.Context, task Task) (TaskStatus, error) {
	if err := e.taskAgreed(); err != nil {
		return TaskStatus{}, err
	}
	if task <= 0 {
		return TaskStatus{}, &Error{Code: "syntax", Text: "invalid task handle"}
	}
	r, err := e.RequestContext(ctx, "task-status", nil, itoa(int64(task)))
	if err != nil {
		return TaskStatus{}, err
	}
	return decodeTaskStatus(r)
}
func decodeTaskStatus(r Reply) (TaskStatus, error) {
	bad := func() (TaskStatus, error) { return TaskStatus{}, fmt.Errorf("invalid task status reply") }
	if len(r.Args) != 3 || len(r.Body) != 0 {
		return bad()
	}
	s := TaskStatus{State: TaskState(r.Args[0])}
	total, ok := taskUnsigned(r.Args[2], math.MaxUint64)
	if !ok {
		return bad()
	}
	s.Total = total
	value := r.Args[1]
	if value == "" || len(value) > 5 || value[0] == '+' || value == "-0" {
		return bad()
	}
	digits := value
	if digits[0] == '-' {
		digits = digits[1:]
	}
	if _, ok := taskUnsigned(digits, 4095); !ok {
		return bad()
	}
	exit, err := strconv.Atoi(value)
	if err != nil {
		return bad()
	}
	s.Exit = exit
	switch s.State {
	case TaskQueued, TaskRunning, TaskStopping:
		if exit != -1 {
			return bad()
		}
	case TaskExited:
		if exit < 0 || exit > 255 {
			return bad()
		}
	case TaskFailed:
		if exit >= 0 || exit < -4095 {
			return bad()
		}
	default:
		return bad()
	}
	return s, nil
}

// TaskOutput preserves child bytes, including NUL and invalid UTF-8. Offset>0 means truncation.
type TaskOutput struct {
	Offset, Total uint64
	Data          []byte
}

func (e *Ext) TaskOutput(task Task) (TaskOutput, error) {
	return e.TaskOutputContext(context.Background(), task)
}
func (e *Ext) TaskOutputContext(ctx context.Context, task Task) (TaskOutput, error) {
	if err := e.taskAgreed(); err != nil {
		return TaskOutput{}, err
	}
	if task <= 0 {
		return TaskOutput{}, &Error{Code: "syntax", Text: "invalid task handle"}
	}
	r, err := e.RequestContext(ctx, "task-output", nil, itoa(int64(task)))
	if err != nil {
		return TaskOutput{}, err
	}
	return decodeTaskOutput(r)
}
func decodeTaskOutput(r Reply) (TaskOutput, error) {
	bad := func() (TaskOutput, error) { return TaskOutput{}, fmt.Errorf("invalid task output reply") }
	if len(r.Args) != 3 || len(r.Body) > MaxTaskOutput {
		return bad()
	}
	offset, a := taskUnsigned(r.Args[0], math.MaxUint64)
	total, b := taskUnsigned(r.Args[1], math.MaxUint64)
	length, c := taskUnsigned(r.Args[2], MaxTaskOutput)
	if !a || !b || !c || length != uint64(len(r.Body)) || total < offset || total-offset != length {
		return bad()
	}
	return TaskOutput{Offset: offset, Total: total, Data: r.Body}, nil
}
