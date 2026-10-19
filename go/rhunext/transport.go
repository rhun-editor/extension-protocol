package rhunext

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"
)

const (
	maxPendingRequests   = 256
	maxOutstandingIDs    = 512
	maxQueuedWrites      = 64
	maxQueuedBytes       = 8 << 20
	maxOutboundBody      = 4 << 20
	maxOutboundLine      = 64 << 10
	physicalWriteTimeout = 2 * time.Second
)

var errWriteTimeout = errors.New("rhun: protocol write timed out")

type writeFrame struct {
	ctx   context.Context
	line  string
	body  []byte
	state atomic.Int32 // 0 queued, 1 started, 2 cancelled before send, 3 completed
	done  chan error
}

func (e *Ext) startWriter() {
	e.writerOnce.Do(func() {
		e.writeQueue = make(chan *writeFrame, maxQueuedWrites)
		e.writeChanged = make(chan struct{})
		go e.writeLoop()
	})
}

// A cancelled caller never abandons a partially written frame. The single writer finishes it,
// or closes the connection at the physical deadline. Custom RunOn streams should implement
// io.Closer to make physical shutdown interruptible; a non-closable writer can strand only this
// one worker, while cancellation and the connection deadline still release callers.
func (e *Ext) writeContext(ctx context.Context, line string, body []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(line) > maxOutboundLine || len(body) > maxOutboundBody {
		return false, &Error{Code: "toobig", Text: "outbound protocol frame"}
	}
	e.startWriter()
	frame := &writeFrame{ctx: ctx, line: line, done: make(chan error, 1)}
	size := len(line) + len(body)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		select {
		case <-e.done:
			return false, e.connectionError()
		default:
		}
		e.wmu.Lock()
		changed := e.writeChanged
		if len(e.writeQueue) < maxQueuedWrites && e.writeBytes+size <= maxQueuedBytes {
			frame.body = append([]byte(nil), body...)
			e.writeBytes += size
			e.writeQueue <- frame
			e.wmu.Unlock()
			break
		}
		e.wmu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-e.done:
			return false, e.connectionError()
		case <-changed:
		}
	}
	select {
	case err := <-frame.done:
		if err != nil {
			select {
			case <-e.done:
				err = e.connectionError()
			default:
			}
		}
		return frame.state.Load() != 2, err
	case <-ctx.Done():
		frame.state.CompareAndSwap(0, 2)
		return frame.state.Load() != 2, ctx.Err()
	case <-e.done:
		frame.state.CompareAndSwap(0, 2)
		return frame.state.Load() != 2, e.connectionError()
	}
}

func (e *Ext) writeLoop() {
	for {
		select {
		case <-e.done:
			return
		case frame := <-e.writeQueue:
			if frame.ctx.Err() != nil {
				frame.state.CompareAndSwap(0, 2)
			}
			var err error
			if frame.state.CompareAndSwap(0, 1) {
				timer := time.AfterFunc(physicalWriteTimeout, func() { e.closeConnection(errWriteTimeout) })
				err = writeAll(e.w, []byte(frame.line))
				if err == nil {
					err = writeAll(e.w, frame.body)
				}
				timer.Stop()
				frame.state.Store(3)
			} else {
				err = frame.ctx.Err()
				if err == nil {
					err = context.Canceled
				}
			}
			e.releaseWrite(len(frame.line) + len(frame.body))
			frame.done <- err
			if err != nil && frame.state.Load() == 3 {
				e.closeConnection(err)
				return
			}
		}
	}
}

func (e *Ext) releaseWrite(size int) {
	e.wmu.Lock()
	e.writeBytes -= size
	close(e.writeChanged)
	e.writeChanged = make(chan struct{})
	e.wmu.Unlock()
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (e *Ext) connectionError() error {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	if e.closed != nil {
		return e.closed
	}
	return io.EOF
}

func (e *Ext) closeConnection(err error) {
	e.stopOnce.Do(func() {
		e.failPending(err)
		e.cancelSources(true)
		close(e.done)
		if c, ok := e.w.(io.Closer); ok {
			c.Close()
		}
		if e.readCloser != nil {
			e.readCloser.Close()
		}
	})
}

// RequestContext sends one request and waits for its reply. Cancellation releases the caller;
// a reply body already on the wire is still consumed to preserve framing for later requests.
func (e *Ext) RequestContext(ctx context.Context, verb string, body []byte, args ...string) (Reply, error) {
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	e.pmu.Lock()
	if e.closed != nil {
		err := e.closed
		e.pmu.Unlock()
		return Reply{}, err
	}
	hasBody := verb == "text" || verb == "range" || verb == "ask" || verb == "snippet-context" || verb == "task-output" ||
		verb == "secret-get" || verb == "secret-keys"
	if len(e.pend) >= maxPendingRequests || len(e.outstanding) >= maxOutstandingIDs {
		e.pmu.Unlock()
		return Reply{}, &Error{Code: "busy", Text: "too many pending requests"}
	}
	var id int64
	for {
		id = e.nextID.Add(1)
		if id > 2147483647 {
			e.nextID.Store(0)
			continue
		}
		if !e.outstanding[id] && e.pend[id] == nil && !e.bodies[id] {
			break
		}
	}
	ch := make(chan result, 1)
	e.pend[id] = ch
	e.outstanding[id] = true
	if hasBody {
		e.bodies[id] = true
	}
	if verb == "snippet-context" {
		if e.replyBodyCaps == nil {
			e.replyBodyCaps = make(map[int64]int64)
		}
		e.replyBodyCaps[id] = MaxSnippetBody
	}
	if verb == "task-output" {
		if e.replyBodyCaps == nil {
			e.replyBodyCaps = make(map[int64]int64)
		}
		e.replyBodyCaps[id] = MaxTaskOutput
	}
	e.pmu.Unlock()
	line := itoa(id) + " " + verb
	for _, arg := range args {
		line += " " + arg
	}
	started, err := e.writeContext(ctx, line+"\n", body)
	if err != nil {
		e.cancelPending(id, ch, !started)
		return Reply{}, err
	}
	select {
	case res := <-ch:
		return res.reply, res.err
	case <-ctx.Done():
		e.cancelPending(id, ch, false)
		return Reply{}, ctx.Err()
	case <-e.done:
		e.cancelPending(id, ch, false)
		return Reply{}, e.connectionError()
	}
}

func (e *Ext) cancelPending(id int64, ch chan result, unsent bool) {
	e.pmu.Lock()
	if e.pend[id] == ch {
		delete(e.pend, id)
		if unsent {
			delete(e.bodies, id)
			delete(e.replyBodyCaps, id)
			delete(e.outstanding, id)
		}
	}
	e.pmu.Unlock()
}
