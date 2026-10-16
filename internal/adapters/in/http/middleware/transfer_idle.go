package middleware

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// TransferIdleTimeout returns a middleware that replaces fixed per-request
// read/write timeouts with an inactivity timeout. The server it wraps must not
// set ReadTimeout or WriteTimeout.
//
//   - The write deadline is set to now+idle when the request starts and renewed
//     before every Write and Flush. A response is cut when no byte is written
//     for idle (the next Write fails because the deadline has expired, or the
//     pending Write fails when the client stops reading).
//   - The read deadline is renewed before every request body Read, so a slow
//     but steady upload is never cut, while a stalled one is.
//
// Deadlines are applied with http.ResponseController, so HTTP/1.x and HTTP/2
// are both supported. Flush, Hijack (protocol upgrades) and Unwrap keep
// working. A non-positive idle disables the middleware.
func TransferIdleTimeout(idle time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if idle <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := http.NewResponseController(w)
			iw := &idleResponseWriter{ResponseWriter: w, rc: rc, idle: idle}
			iw.renew()
			if r.Body != nil && r.Body != http.NoBody {
				ib := &idleBody{ReadCloser: r.Body, rc: rc, idle: idle, writer: iw}
				ib.renew()
				r.Body = ib
			}
			next.ServeHTTP(iw, r)
		})
	}
}

// idleResponseWriter renews the write deadline on every Write/Flush.
type idleResponseWriter struct {
	http.ResponseWriter
	rc   *http.ResponseController
	idle time.Duration

	mu       sync.Mutex
	deadline time.Time
}

// renew extends the write deadline unless it already expired. An expired
// deadline is left in place so that the write fails: the response was silent
// for longer than the idle timeout.
func (w *idleResponseWriter) renew() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if !w.deadline.IsZero() && now.After(w.deadline) {
		return
	}
	next := now.Add(w.idle)
	if err := w.rc.SetWriteDeadline(next); err == nil {
		w.deadline = next
	}
}

// extend pushes the write deadline forward unconditionally. It is called when
// the request body makes progress: a client that is still uploading has not
// left the response idle, so the wait for the first response byte starts from
// the last upload activity.
func (w *idleResponseWriter) extend() {
	w.mu.Lock()
	defer w.mu.Unlock()
	next := time.Now().Add(w.idle)
	if err := w.rc.SetWriteDeadline(next); err == nil {
		w.deadline = next
	}
}

// maxIdleWriteChunk bounds how much is handed to the connection per deadline
// renewal, so one very large Write to a slow but steady client is not cut as if
// it were stalled.
const maxIdleWriteChunk = 64 * 1024

// Write renews the write deadline before each chunk, then writes it.
func (w *idleResponseWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		w.renew()
		return w.ResponseWriter.Write(p)
	}
	written := 0
	for written < len(p) {
		chunk := p[written:min(written+maxIdleWriteChunk, len(p))]
		w.renew()
		n, err := w.ResponseWriter.Write(chunk)
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// FlushError renews the write deadline, then flushes. It lets
// http.ResponseController report flush failures.
func (w *idleResponseWriter) FlushError() error {
	w.renew()
	return w.rc.Flush()
}

// Flush implements http.Flusher.
func (w *idleResponseWriter) Flush() {
	_ = w.FlushError()
}

// Hijack implements http.Hijacker. The server clears connection deadlines when
// a connection is hijacked, so upgraded streams (e.g. WebSocket) are not
// subject to the inactivity timeout.
func (w *idleResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.rc.Hijack()
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *idleResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// idleBody renews the read deadline before every request body Read.
type idleBody struct {
	io.ReadCloser
	rc     *http.ResponseController
	idle   time.Duration
	writer *idleResponseWriter
}

func (b *idleBody) renew() {
	_ = b.rc.SetReadDeadline(time.Now().Add(b.idle))
}

// clear removes the read deadline. It must not stay armed once the body is
// fully read: net/http then watches the connection for client disconnects and
// treats a read timeout as an abort.
func (b *idleBody) clear() {
	_ = b.rc.SetReadDeadline(time.Time{})
}

// Read renews the read deadline, reads, and clears the deadline at EOF.
func (b *idleBody) Read(p []byte) (int, error) {
	b.renew()
	n, err := b.ReadCloser.Read(p)
	if n > 0 || errors.Is(err, io.EOF) {
		b.writer.extend()
	}
	if errors.Is(err, io.EOF) {
		b.clear()
	}
	return n, err
}

// Close drains within a fresh deadline, then clears it.
func (b *idleBody) Close() error {
	b.renew()
	err := b.ReadCloser.Close()
	b.clear()
	return err
}
