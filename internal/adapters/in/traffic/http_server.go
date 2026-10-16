package traffic

import (
	"net/http"

	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/domain"
)

// httpOptions returns the effective HTTP server options of the current traffic
// snapshot, falling back to the defaults before the first graph is applied.
func (m *Manager) httpOptions() domain.HTTPOptions {
	graph := m.snapshot.Load()
	if graph == nil {
		return domain.DefaultHTTPOptions()
	}
	return graph.Options.HTTP.WithDefaults()
}

// newEntryPointHTTPServer builds an HTTP server for a traffic entrypoint.
// ReadTimeout and WriteTimeout stay zero: whole-request deadlines would cut
// long downloads and uploads that are still moving data. The handler is
// wrapped with the inactivity timeout instead, which renews the read and write
// deadlines as data flows.
func newEntryPointHTTPServer(handler http.Handler, options domain.HTTPOptions) *http.Server {
	return &http.Server{
		Handler:           middleware.TransferIdleTimeout(options.TransferIdleTimeout)(handler),
		ReadHeaderTimeout: options.ReadHeaderTimeout,
		IdleTimeout:       options.IdleTimeout,
	}
}
