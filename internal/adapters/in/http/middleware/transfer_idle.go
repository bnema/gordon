package middleware

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// errTransferFinished is returned by writer calls made after the request
// finished (or was hijacked). The controller is never touched in that state.
var errTransferFinished = errors.New("transfer idle: request already finished")

// idleWriteChunk is the progress unit of response writes. Each chunk is handed
// to the connection separately and counts as progress once it has been fully
// accepted, so a client that keeps consuming data is never cut while a client
// that stops reading is cut after the idle timeout. The residual granularity is
// one chunk: a client must accept idleWriteChunk bytes within one idle period,
// which is a floor of idleWriteChunk/idle bytes per second (about 68 B/s for
// the 2 minute default).
const idleWriteChunk = 8 * 1024

// TransferIdleTimeout returns a middleware that replaces fixed per-request
// read/write timeouts with a request-scoped inactivity timeout. The server it
// wraps must not set ReadTimeout or WriteTimeout.
//
// A single timer covers the request. Progress renews it: a response chunk
// accepted by the connection, a flush, or request body bytes read. When idle
// elapses without progress, the middleware cancels the request context (so a
// ReverseProxy aborts its upstream request) and expires the connection's read
// and write deadlines, which unblocks pending I/O and makes the server drop the
// connection once the handler returns. The timer also covers a handler that is
// silent and not blocked in any I/O, which a plain write deadline cannot.
//
// The timer is armed when the request starts, so the wait for the first
// response byte is bounded too. It is stopped when the handler returns and when
// the connection is hijacked: upgraded streams (WebSocket) are exempt.
// Deadlines go through http.ResponseController, so HTTP/1.x and HTTP/2 both
// work; Flush, Hijack and Unwrap are preserved.
//
// net/http does work after the handler returns that can block on a stalled
// client: the final response flush and the drain of an unread request body.
// With no server Read/WriteTimeout those would hang forever, so finish arms
// one last bounded read and write deadline (idle from now) before the handler
// returns and leaves it in place. net/http clears the write deadline itself
// after the response is finished and replaces the read deadline with the
// keep-alive idle / header deadline before the next request, so nothing leaks
// into the next request. The timer never touches the controller after return.
//
// Limits: a handler that ignores the canceled context and never touches the
// connection again keeps its HTTP/1 connection until it returns. A handler that
// enables HTTP/1 full-duplex mode (ResponseController.EnableFullDuplex) is not
// covered for the unread-body drain: net/http clears the read deadline before
// that drain, so such a handler must consume or bound its request body itself.
// A non-positive idle disables the middleware.
func TransferIdleTimeout(idle time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if idle <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			hasBody := r.Body != nil && r.Body != http.NoBody
			state := newIdleState(idle, http.NewResponseController(w), cancel, hasBody)
			// finish marks the state done under the mutex before this
			// function returns, so nothing touches the controller afterwards.
			defer state.finish()

			req := r.WithContext(ctx)
			if hasBody {
				req.Body = &idleBody{ReadCloser: r.Body, state: state}
			}
			next.ServeHTTP(&idleResponseWriter{ResponseWriter: w, state: state}, req)
		})
	}
}

// idleState is the request-lifetime state shared by the writer, the body and
// the timer. The controller is only used while the state is live, under mu.
type idleState struct {
	idle   time.Duration
	rc     *http.ResponseController
	cancel context.CancelFunc

	hasBody bool // request has a body that net/http may still have to drain

	mu       sync.Mutex
	timer    *time.Timer
	last     time.Time
	done     bool // handler returned or connection hijacked
	expired  bool // the timer fired and expired the connection deadlines
	hijacked bool
}

func newIdleState(idle time.Duration, rc *http.ResponseController, cancel context.CancelFunc, hasBody bool) *idleState {
	s := &idleState{idle: idle, rc: rc, cancel: cancel, last: time.Now(), hasBody: hasBody}
	s.mu.Lock()
	s.timer = time.AfterFunc(idle, s.onTimer)
	s.mu.Unlock()
	return s
}

// progress records activity. It never touches the controller.
func (s *idleState) progress() {
	s.mu.Lock()
	if !s.done {
		s.last = time.Now()
	}
	s.mu.Unlock()
}

// live reports whether the request is still being served.
func (s *idleState) live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.done
}

// onTimer runs on timer expiry. Progress only stamps s.last, so the timer is
// re-armed for the remainder instead of being reset on every event; this also
// makes progress racing with expiry safe.
func (s *idleState) onTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	if remaining := s.idle - time.Since(s.last); remaining > 0 {
		s.timer.Reset(remaining)
		return
	}
	s.expired = true
	s.cancel()
	// Held under mu so finish() cannot complete while these run.
	now := time.Now()
	_ = s.rc.SetWriteDeadline(now)
	_ = s.rc.SetReadDeadline(now)
}

// finish ends the request lifetime. After it returns, no code path calls the
// controller. It is safe to call more than once. Unless the connection was
// hijacked or already expired, it first arms bounded deadlines so net/http's
// post-handler work (final flush, unread body drain) cannot block forever.
func (s *idleState) finish() {
	s.mu.Lock()
	wasDone := s.done
	s.done = true
	s.timer.Stop()
	if !wasDone && !s.expired && !s.hijacked {
		deadline := time.Now().Add(s.idle)
		_ = s.rc.SetWriteDeadline(deadline)
		if s.hasBody {
			_ = s.rc.SetReadDeadline(deadline)
		}
	}
	s.mu.Unlock()
	s.cancel()
}

// flush flushes through the controller unless the request finished. The mutex
// is not held during the flush: it may block on a slow client, and the timer
// callback needs the mutex to expire the deadlines that unblock it. net/http
// forbids using the writer after the handler returned or concurrently with its
// return, so the live check is a guard against misuse, not a synchronisation
// point for the flush itself.
func (s *idleState) flush() error {
	if !s.live() {
		return errTransferFinished
	}
	return s.rc.Flush()
}

// hijack hijacks the connection without holding the mutex: HTTP/1 Hijack may
// flush pending headers and block on a client that does not read, and the timer
// needs the mutex to expire the deadlines that unblock it. Enforcement stays
// active until the hijack succeeds; only then is the timer stopped. If the
// timer fired meanwhile, the deadlines it set on the connection are cleared so
// the upgraded stream is not cut.
func (s *idleState) hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !s.live() {
		return nil, nil, errTransferFinished
	}
	conn, rw, err := s.rc.Hijack()
	if err != nil {
		return conn, rw, err
	}
	s.mu.Lock()
	s.done = true
	s.hijacked = true
	s.timer.Stop()
	expired := s.expired
	s.mu.Unlock()
	if expired {
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, rw, nil
}

// idleResponseWriter reports response progress.
type idleResponseWriter struct {
	http.ResponseWriter
	state *idleState
}

// Write hands p to the connection in idleWriteChunk pieces and records progress
// after each piece.
func (w *idleResponseWriter) Write(p []byte) (int, error) {
	if !w.state.live() {
		return 0, errTransferFinished
	}
	if len(p) == 0 {
		return w.ResponseWriter.Write(p)
	}
	written := 0
	for written < len(p) {
		chunk := p[written:min(written+idleWriteChunk, len(p))]
		n, err := w.ResponseWriter.Write(chunk)
		written += n
		if n > 0 {
			w.state.progress()
		}
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// FlushError flushes and records progress. It lets http.ResponseController
// report flush failures.
func (w *idleResponseWriter) FlushError() error {
	err := w.state.flush()
	if err == nil {
		w.state.progress()
	}
	return err
}

// Flush implements http.Flusher.
func (w *idleResponseWriter) Flush() {
	_ = w.FlushError()
}

// Hijack implements http.Hijacker and stops the inactivity timer: upgraded
// streams (e.g. WebSocket) are exempt.
func (w *idleResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.state.hijack()
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *idleResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// idleBody reports request body progress. It only records timestamps, so it is
// safe to Read or Close after the handler returned (the HTTP/2 transport closes
// the outbound body asynchronously).
type idleBody struct {
	io.ReadCloser
	state *idleState

	closeOnce sync.Once
	closeErr  error
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.state.progress()
	}
	return n, err
}

// Close is idempotent.
func (b *idleBody) Close() error {
	b.closeOnce.Do(func() { b.closeErr = b.ReadCloser.Close() })
	return b.closeErr
}
