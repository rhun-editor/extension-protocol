package rhunext

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConnection(t *testing.T, w io.Writer) *Ext {
	t.Helper()
	e := &Ext{w: w, pend: map[int64]chan result{}, bodies: map[int64]bool{}, outstanding: map[int64]bool{}, done: make(chan struct{})}
	t.Cleanup(func() { e.closeConnection(io.EOF) })
	return e
}

func waitTest(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
}

func pendingCount(e *Ext) int { e.pmu.Lock(); defer e.pmu.Unlock(); return len(e.pend) }
func bodyCount(e *Ext) int    { e.pmu.Lock(); defer e.pmu.Unlock(); return len(e.bodies) }

type wireWriter chan string

func (w wireWriter) Write(b []byte) (int, error) { w <- string(b); return len(b), nil }

func TestCancelledTextDrainsLateBodyBeforeFollowingReply(t *testing.T) {
	for _, body := range []string{"hello\n0 bye\n", "", strings.Repeat("x", 1<<20)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			wire := make(wireWriter, 8)
			e := testConnection(t, wire)
			ctx, cancel := context.WithCancel(context.Background())
			first := make(chan error, 1)
			go func() { _, _, err := e.TextContext(ctx, 1); first <- err }()
			if got := <-wire; got != "1 text 1\n" {
				t.Fatal(got)
			}
			cancel()
			if err := <-first; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if bodyCount(e) != 1 {
				t.Fatal("missing framing tombstone")
			}
			second := make(chan result, 1)
			go func() { r, err := e.Request("ping", nil); second <- result{r, err} }()
			if got := <-wire; got != "2 ping\n" {
				t.Fatal(got)
			}
			br := bufio.NewReader(strings.NewReader(fmt.Sprintf("ok 1 7 %d\n%sok 2 alive\n", len(body), body)))
			if _, err := e.readMessage(br); err != nil {
				t.Fatal(err)
			}
			if _, err := e.readMessage(br); err != nil {
				t.Fatal(err)
			}
			got := <-second
			if got.err != nil || len(got.reply.Args) != 1 || got.reply.Args[0] != "alive" {
				t.Fatalf("%+v", got)
			}
			if bodyCount(e) != 0 || pendingCount(e) != 0 {
				t.Fatal("pending state retained")
			}
		})
	}
}

func TestCancelledBodyErrorReleasesReservation(t *testing.T) {
	e := testConnection(t, io.Discard)
	e.bodies[1] = true
	if _, err := e.readMessage(bufio.NewReader(strings.NewReader("error 1 nodoc gone\n"))); err != nil {
		t.Fatal(err)
	}
	if bodyCount(e) != 0 {
		t.Fatal("error reply retained body reservation")
	}
}

func TestCancelledRequestDuringBodyReadDoesNotDeliverTwice(t *testing.T) {
	wire := make(wireWriter, 8)
	e := testConnection(t, wire)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := e.TextContext(ctx, 1); done <- err }()
	<-wire
	in, out := io.Pipe()
	t.Cleanup(func() { in.Close(); out.Close() })
	read := make(chan error, 1)
	go func() { _, err := e.readMessage(bufio.NewReader(in)); read <- err }()
	io.WriteString(out, "ok 1 0 8\n1234")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	io.WriteString(out, "5678")
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if bodyCount(e) != 0 || pendingCount(e) != 0 {
		t.Fatal("retained completed request")
	}
}

func TestPendingAndFramingReservationsBoundedBeforeSending(t *testing.T) {
	e := testConnection(t, io.Discard)
	for id := int64(1); id <= maxOutstandingIDs; id++ {
		e.bodies[id] = true
		e.outstanding[id] = true
	}
	_, err := e.RequestContext(context.Background(), "text", nil, "1")
	if re, ok := err.(*Error); !ok || re.Code != "busy" {
		t.Fatal(err)
	}
	if pendingCount(e) != 0 {
		t.Fatal("rejected request registered")
	}
	clear(e.bodies)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = e.RequestContext(ctx, "ping", nil)
	if re, ok := err.(*Error); !ok || re.Code != "busy" {
		t.Fatalf("cancelled non-body IDs exceeded bound: %v", err)
	}
	clear(e.outstanding)
	for id := int64(1); id <= maxPendingRequests; id++ {
		e.pend[id] = make(chan result, 1)
	}
	_, err = e.RequestContext(context.Background(), "ping", nil)
	if re, ok := err.(*Error); !ok || re.Code != "busy" {
		t.Fatal(err)
	}
}

func TestRequestIDWrapSkipsLiveAndCancelledIDs(t *testing.T) {
	e := testConnection(t, io.Discard)
	e.nextID.Store(2147483647)
	e.bodies[1] = true
	e.pend[2] = make(chan result, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.RequestContext(ctx, "ping", nil); done <- err }()
	waitTest(t, func() bool { e.pmu.Lock(); defer e.pmu.Unlock(); return e.pend[3] != nil })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type gatedWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	data    bytes.Buffer
}

func (w *gatedWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.started); <-w.release })
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.Write(b)
}

func TestCancelledQueuedFrameIsNeverSentAndReleasesBodyReservation(t *testing.T) {
	w := &gatedWriter{started: make(chan struct{}), release: make(chan struct{})}
	e := testConnection(t, w)
	first := make(chan error, 1)
	go func() { first <- e.write("hold\n", nil) }()
	<-w.started
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := e.TextContext(ctx, 1); done <- err }()
	waitTest(t, func() bool { return len(e.writeQueue) == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if bodyCount(e) != 0 || pendingCount(e) != 0 {
		t.Fatal("unsent frame retained state")
	}
	close(w.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	waitTest(t, func() bool { e.wmu.Lock(); defer e.wmu.Unlock(); return e.writeBytes == 0 })
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.data.String() != "hold\n" {
		t.Fatalf("sent cancelled frame: %q", w.data.String())
	}
}

func TestWriterAlreadyCancelledFrameDoesNotRetainUnsentBody(t *testing.T) {
	w := &gatedWriter{started: make(chan struct{}), release: make(chan struct{})}
	e := testConnection(t, w)
	first := make(chan error, 1)
	go func() { first <- e.write("hold\n", nil) }()
	<-w.started
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := e.TextContext(ctx, 1); done <- err }()
	// Pause at the writer's cancellation classification, before notifying its waiter.
	// The normal writer is held on the first frame, so this interleaving is deterministic.
	frame := <-e.writeQueue
	frame.state.Store(2)
	e.wmu.Lock()
	e.writeBytes -= len(frame.line) + len(frame.body)
	e.wmu.Unlock()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	retained := bodyCount(e)
	close(w.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if retained != 0 {
		t.Fatal("writer-cancelled unsent frame retained a body reservation")
	}
}

func TestCancelledNonBodyRequestIDCannotBeReusedByTextAfterWrap(t *testing.T) {
	wire := make(wireWriter, 8)
	e := testConnection(t, wire)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := e.RequestContext(ctx, "replace", nil, "1", "0", "1", "1", "1", "1", "0")
		first <- err
	}()
	if got := <-wire; !strings.HasPrefix(got, "1 replace ") {
		t.Fatal(got)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	e.nextID.Store(2147483647)
	second := make(chan result, 1)
	go func() { r, err := e.Request("text", nil, "1"); second <- result{r, err} }()
	query := <-wire
	if query != "2 text 1\n" {
		e.closeConnection(io.EOF)
		<-second
		t.Fatalf("reused an unanswered cancelled request ID: %q", query)
	}
	br := bufio.NewReader(strings.NewReader("ok 1 7\nok 2 9 4\ntext"))
	for range 2 {
		if _, err := e.readMessage(br); err != nil {
			t.Fatal(err)
		}
	}
	got := <-second
	if got.err != nil || string(got.reply.Body) != "text" || argInt(got.reply, 0) != 9 {
		t.Fatalf("%+v", got)
	}
}

type partialWriter struct{ bytes.Buffer }

func (w *partialWriter) Write(b []byte) (int, error) {
	if len(b) > 3 {
		b = b[:3]
	}
	return w.Buffer.Write(b)
}
func TestPartialWritesPreserveCompleteFrame(t *testing.T) {
	w := &partialWriter{}
	e := testConnection(t, w)
	if err := e.write("header 12\n", []byte("body payload")); err != nil {
		t.Fatal(err)
	}
	if w.String() != "header 12\nbody payload" {
		t.Fatal(w.String())
	}
}

func TestWriteByteLimitWaitsWithoutCopyingAnotherLargeFrame(t *testing.T) {
	w := &gatedWriter{started: make(chan struct{}), release: make(chan struct{})}
	e := testConnection(t, w)
	first := make(chan error, 1)
	body := make([]byte, maxOutboundBody)
	go func() { first <- e.write("large\n", body) }()
	<-w.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started, err := e.writeContext(ctx, "another\n", body)
	if started || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("started=%v err=%v", started, err)
	}
	e.wmu.Lock()
	reserved := e.writeBytes
	e.wmu.Unlock()
	if reserved != len("large\n")+len(body) || len(e.writeQueue) != 0 {
		t.Fatal("byte limit admitted another frame")
	}
	close(w.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestOversizeFrameReleasesUnsentRequestState(t *testing.T) {
	e := testConnection(t, io.Discard)
	for _, test := range []struct {
		line string
		body []byte
	}{
		{strings.Repeat("x", maxOutboundLine+1), nil},
		{"line\n", make([]byte, maxOutboundBody+1)},
	} {
		started, err := e.writeContext(context.Background(), test.line, test.body)
		if re, ok := err.(*Error); started || !ok || re.Code != "toobig" {
			t.Fatalf("started=%v err=%v", started, err)
		}
	}
	_, err := e.RequestContext(context.Background(), "text", make([]byte, maxOutboundBody+1), "1")
	if re, ok := err.(*Error); !ok || re.Code != "toobig" {
		t.Fatal(err)
	}
	if pendingCount(e) != 0 || bodyCount(e) != 0 {
		t.Fatal("unsent oversized request retained state")
	}
}

func TestBlockedClosableWriteFailsConnectionAndUnblocksCaller(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	e := testConnection(t, w)
	start := time.Now()
	err := e.write("never read\n", nil)
	if !errors.Is(err, errWriteTimeout) {
		t.Fatalf("write error: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("blocked beyond physical deadline")
	}
	select {
	case <-e.done:
	default:
		t.Fatal("connection remained open")
	}
}

func TestWriteAdmissionCancellationIsBoundedWithUnclosableWriter(t *testing.T) {
	w := &gatedWriter{started: make(chan struct{}), release: make(chan struct{})}
	e := testConnection(t, w)
	first := make(chan error, 1)
	go func() { first <- e.write("hold\n", nil) }()
	<-w.started
	done := make([]chan error, maxQueuedWrites)
	ctx, cancel := context.WithCancel(context.Background())
	for i := range done {
		done[i] = make(chan error, 1)
		go func(ch chan error) { _, err := e.writeContext(ctx, "queued\n", nil); ch <- err }(done[i])
	}
	waitTest(t, func() bool { return len(e.writeQueue) == maxQueuedWrites })
	blocked, cancelBlocked := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := e.writeContext(blocked, "beyond queue\n", nil); result <- err }()
	cancelBlocked()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cancel()
	for _, ch := range done {
		if err := <-ch; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	e.wmu.Lock()
	if e.writeBytes > maxQueuedBytes {
		t.Fatal("write bytes exceeded bound")
	}
	e.wmu.Unlock()
	close(w.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

type admissionContext struct {
	context.Context
	waiting       chan struct{}
	once          sync.Once
	checks        atomic.Int32
	cancelOnRetry context.CancelFunc
}

func (c *admissionContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}
func (c *admissionContext) Err() error {
	// Cancel immediately after this caller is woken to retry admission.
	if c.checks.Add(1) == 3 && c.cancelOnRetry != nil {
		c.cancelOnRetry()
	}
	return c.Context.Err()
}

func TestAdmissionCapacityWakeupSurvivesCancelledWaiter(t *testing.T) {
	w := &gatedWriter{started: make(chan struct{}), release: make(chan struct{})}
	e := testConnection(t, w)
	t.Cleanup(func() { close(w.release) })
	e.startWriter()
	// Model the instant the writer drains its last full allocation. Both admissions
	// must recheck capacity even if the first awakened caller cancels without writing.
	e.wmu.Lock()
	e.writeBytes = maxQueuedBytes
	e.wmu.Unlock()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &admissionContext{Context: parent, waiting: make(chan struct{}), cancelOnRetry: cancel}
	b := &admissionContext{Context: context.Background(), waiting: make(chan struct{})}
	failed := make(chan error, 1)
	go func() { _, err := e.writeContext(a, "cancelled\n", nil); failed <- err }()
	<-a.waiting
	go e.writeContext(b, "admitted\n", nil)
	<-b.waiting
	e.releaseWrite(maxQueuedBytes)
	cancelled, admitted := false, false
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	started := w.started
	for !cancelled || !admitted {
		select {
		case err := <-failed:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			cancelled = true
		case <-started:
			admitted = true
			started = nil
		case <-deadline.C:
			t.Fatalf("lost capacity wakeup: cancelled=%v admitted=%v", cancelled, admitted)
		}
	}
}
