// Package proxy implements the HTTP adapter for the reverse proxy.
package proxy

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
)

// Handler wraps the proxy service for HTTP.
// The actual proxying logic is in the usecase layer (ProxyService implements http.Handler).
type Handler struct {
	proxySvc in.ProxyService
	log      zerowrap.Logger
	port     int
}

// NewHandler creates a new proxy HTTP handler.
func NewHandler(proxySvc in.ProxyService, port int, log zerowrap.Logger) *Handler {
	return &Handler{
		proxySvc: proxySvc,
		port:     port,
		log:      log,
	}
}

// ServeHTTP implements http.Handler by delegating to the proxy service.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.proxySvc.ServeHTTP(w, r)
}

// Start starts the proxy HTTP server.
func (h *Handler) Start(ctx context.Context, handler http.Handler) error {
	addr := ":" + strconv.Itoa(h.port)

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,   // Timeout for reading entire request
		WriteTimeout:      5 * time.Minute,   // Timeout for writing response
		IdleTimeout:       120 * time.Second, // Timeout for idle keep-alive connections
		MaxHeaderBytes:    1 << 20,           // 1MB max header size
	}

	h.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "http").
		Str("address", addr).
		Msg("proxy server starting")

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	// Wait for context cancellation or server error
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		h.log.Info().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "http").
			Msg("proxy server shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	}
}
