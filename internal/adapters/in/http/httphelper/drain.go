package httphelper

import (
	"context"
	"net"
	"net/http"
	"sync"

	"github.com/bnema/zerowrap"
)

// HTTPDrain owns connection admission and accounting for one HTTP server.
// StateNew sockets have no admitted request and must not consume the drain
// budget (net/http otherwise gives them a five-second first-request grace).
type HTTPDrain struct {
	mu       sync.Mutex
	conns    map[net.Conn]http.ConnState
	stopping bool
}

// TrackHTTPDrain installs connection accounting before Serve starts. The
// returned drain belongs to the server owner and preserves existing hooks.
func TrackHTTPDrain(server *http.Server) *HTTPDrain {
	d := &HTTPDrain{conns: make(map[net.Conn]http.ConnState)}
	previous := server.ConnState
	server.ConnState = func(conn net.Conn, state http.ConnState) {
		d.mu.Lock()
		if state == http.StateClosed || state == http.StateHijacked {
			delete(d.conns, conn)
		} else {
			d.conns[conn] = state
		}
		closeNew := d.stopping && state == http.StateNew
		d.mu.Unlock()
		if closeNew {
			_ = conn.Close()
		}
		if previous != nil {
			previous(conn, state)
		}
	}
	server.RegisterOnShutdown(d.stopAdmission)
	return d
}

// Shutdown closes unadmitted connections immediately, then lets net/http drain
// active requests and close keep-alives. The caller owns the absolute deadline.
func (d *HTTPDrain) Shutdown(ctx context.Context, server *http.Server, component string) {
	if d != nil {
		d.stopAdmission()
	}
	if err := server.Shutdown(ctx); err != nil {
		count := 0
		if d != nil {
			d.mu.Lock()
			count = len(d.conns)
			d.mu.Unlock()
		}
		log := zerowrap.FromCtx(ctx)
		log.Warn().Err(err).Str("component", component).Int("connections", count).Msg("HTTP drain deadline reached; force-closing connections")
		_ = server.Close()
	}
}

func (d *HTTPDrain) stopAdmission() {
	d.mu.Lock()
	d.stopping = true
	var pending []net.Conn
	for conn, state := range d.conns {
		if state == http.StateNew {
			pending = append(pending, conn)
		}
	}
	d.mu.Unlock()
	for _, conn := range pending {
		_ = conn.Close()
	}
}
