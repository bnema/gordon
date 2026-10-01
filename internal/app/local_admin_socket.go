package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/adapters/localadmin"
)

// localAdminServer owns the daemon's owner-only admin Unix socket: one HTTP
// listener, one local-only authority handler, and the socket file it created.
type localAdminServer struct {
	srv  *http.Server
	path string
	info os.FileInfo
	log  zerowrap.Logger
	once sync.Once
}

// startLocalAdminServer binds the owner-only admin socket, composes the
// local-only authority over the admin handler, and serves it in the
// background. Any startup error aborts daemon startup; a later serve error is
// reported on errChan so the daemon fails rather than silently losing local
// administration.
func startLocalAdminServer(svc *services, errChan chan<- error, log zerowrap.Logger) (*localAdminServer, error) {
	if svc == nil || svc.adminHandler == nil {
		return nil, nil
	}

	dir, err := localadmin.EnsureRuntimeDir()
	if err != nil {
		return nil, fmt.Errorf("local admin socket directory: %w", err)
	}
	ln, info, err := localadmin.Listen(dir)
	if err != nil {
		return nil, fmt.Errorf("local admin socket: %w", err)
	}

	handler := middleware.Chain(
		middleware.PanicRecovery(log),
		middleware.SecurityHeaders,
	)(svc.adminHandler.LocalAuthority())

	// No WriteTimeout: app log streaming over the socket stays open for as long
	// as the client follows it.
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	s := &localAdminServer{
		srv:  srv,
		path: localadmin.SocketPath(dir),
		info: info,
		log:  log,
	}

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			wrapped := fmt.Errorf("local admin socket server: %w", serveErr)
			select {
			case errChan <- wrapped:
			default:
				log.Error().Err(serveErr).Msg("local admin socket server error")
			}
		}
	}()

	log.Info().Str("path", s.path).Msg("local admin socket listening")
	return s, nil
}

// Close drains and stops the socket server, then removes the socket file only
// when it is still the file this listener bound. It is safe to call twice.
func (s *localAdminServer) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(ctx); err != nil {
			s.log.Warn().Err(err).Msg("local admin socket shutdown error")
			if closeErr := s.srv.Close(); closeErr != nil {
				s.log.Warn().Err(closeErr).Msg("failed to force-close local admin socket")
			}
		}
		if err := localadmin.RemoveOwnedSocket(s.path, s.info); err != nil {
			s.log.Warn().Err(err).Str("path", s.path).Msg("failed to remove local admin socket")
			return
		}
		s.log.Info().Msg("local admin socket stopped")
	})
}
