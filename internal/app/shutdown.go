package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/usecase/container"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/gordon/internal/usecase/proxy"
)

// waitForShutdown blocks on the event loop, handling server errors and
// Unix signals (reload, deploy, shutdown) until the context is cancelled.
func waitForShutdown(ctx context.Context, errChan <-chan error, reloadChan <-chan os.Signal, reload reloadTrigger, eventBus out.EventBus, log zerowrap.Logger) {
	worker := newSignalReloadWorker(ctx, reload)
	defer worker.Stop()
	for {
		select {
		case err := <-errChan:
			log.Error().Err(err).Msg("server error")
			return
		case <-reloadChan:
			log.Info().Msg("reload signal received (SIGUSR1)")
			worker.Request()
		case <-ctx.Done():
			log.Info().Msg("shutdown signal received")
			return
		}
	}
}

// appAdministration is the daemon-owned application lifecycle torn down
// during graceful shutdown: it refuses new background deploys, cancels
// in-flight executions on the daemon context, and joins them.
type appAdministration interface {
	Shutdown(context.Context) error
}

// quiesceAppAdministration cancels and joins daemon-owned app work on the
// bounded shutdown context. A non-nil error means the bound expired while an
// execution was still unwinding: the caller must stop tearing down the state
// and runtime that execution still uses.
func quiesceAppAdministration(ctx context.Context, appAdmin appAdministration, log zerowrap.Logger) error {
	if appAdmin == nil {
		return nil
	}
	if err := appAdmin.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("app administration did not quiesce before the shutdown deadline")
		return err
	}
	return nil
}

// gracefulShutdown drains ingress on its own deadline, then shuts down
// the container service and cleans up runtime files. It returns an error when
// app administration could not quiesce: in that case the remaining teardown
// is skipped and the error is propagated to the process entry point, which
// exits non-zero instead of closing state or runtime under an in-flight
// ExecuteDeploy.
func gracefulShutdown(registrySrvs []*http.Server, proxySrv, tlsSrv *http.Server, containerSvc *container.Service, proxySvc *proxy.Service, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, trafficManager *trafficadapter.Manager, monitor *appMonitor, appAdmin appAdministration, appState out.AppState, log zerowrap.Logger, drain time.Duration) error {
	log.Info().Msg("shutting down Gordon...")

	drainIngress(registrySrvs, proxySrv, tlsSrv, proxySvc, trafficManager, drain, log)

	// Stop periodic app recovery and wait for any in-flight
	// bounded pass, so no reconcile can race runtime, traffic, or state
	// dependency teardown (and no goroutine is left behind).
	monitor.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Phase 0.5: quiesce daemon-owned app administration. New background
	// deploys are refused and in-flight executions are cancelled on the
	// daemon context and joined here — before runtime or the app
	// state store is torn down, so no execution can write to closed state.
	// A timeout is fail-closed: the unfinished execution still owns the
	// state and runtime it uses, so teardown stops here and the error makes
	// the process exit non-zero rather than close resources underneath it.
	if err := quiesceAppAdministration(shutdownCtx, appAdmin, log); err != nil {
		return fmt.Errorf("app administration quiescence: %w", err)
	}

	// Stop PKI maintenance goroutines
	if pkiSvc != nil {
		pkiSvc.Stop()
	}

	// Stop public ACME TLS renewal loop
	if publicTLS != nil {
		if err := publicTLS.Stop(shutdownCtx); err != nil {
			log.Warn().Err(err).Msg("public TLS stop error")
		}
	}

	if err := containerSvc.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("error during container shutdown")
	}
	closeAppState(appState, log)

	cleanupInternalCredentials()
	log.Info().Msg("Gordon stopped")
	return nil
}

// drainIngress keeps registry backends available until frontends and tracked
// proxy requests drain. Both phases share one absolute connection deadline.
func drainIngress(registry []*http.Server, proxySrv, tlsSrv *http.Server, proxySvc *proxy.Service, manager *trafficadapter.Manager, timeout time.Duration, log zerowrap.Logger) {
	ctx, cancel := context.WithTimeout(zerowrap.WithCtx(context.Background(), log), timeout)
	defer cancel()
	started := time.Now()
	var wg sync.WaitGroup
	for _, srv := range []*http.Server{proxySrv, tlsSrv} {
		if srv != nil {
			wg.Go(func() { shutdownHTTPServers(ctx, log, srv) })
		}
	}
	if manager != nil {
		wg.Go(func() {
			if err := manager.Shutdown(ctx); err != nil {
				log.Warn().Err(err).Msg("traffic manager shutdown error")
			}
		})
	}
	if proxySvc != nil {
		wg.Go(func() {
			deadline, _ := ctx.Deadline()
			if !proxySvc.DrainRegistryInFlight(time.Until(deadline)) {
				log.Warn().Int64("in_flight", proxySvc.RegistryInFlight()).Msg("registry drain deadline reached; remaining transfers interrupted")
			}
		})
	}
	wg.Wait()
	log.Info().Dur("duration_ms", time.Since(started)).Msg("drained http frontends and traffic relays")
	started = time.Now()
	// No frontend can initiate another registry request now.
	for _, srv := range registry {
		if srv != nil {
			wg.Go(func() { shutdownHTTPServers(ctx, log, srv) })
		}
	}
	wg.Wait()
	log.Info().Dur("duration_ms", time.Since(started)).Msg("drained registry backends")
}

// shutdownHTTPServers gracefully stops each non-nil server on the shutdown
// context, logging per-server failures so one listener cannot mask another.
func shutdownHTTPServers(ctx context.Context, log zerowrap.Logger, servers ...*http.Server) {
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		if err := srv.Shutdown(ctx); err != nil {
			log.Warn().Err(err).Str("component", "http server").Int("servers", 1).Str("addr", srv.Addr).Msg("server drain deadline reached; force-closing remaining connections")
			_ = srv.Close()
		}
	}
}

func closeAppState(state out.AppState, log zerowrap.Logger) {
	if state == nil {
		return
	}
	if err := state.Close(); err != nil {
		log.Warn().Err(err).Msg("app state close error")
	}
}

// shutdownStartedServers gracefully shuts down listeners that bound before a
// partial startup failure so an error return does not leak them.
func shutdownStartedServers(servers []*http.Server, log zerowrap.Logger) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	for _, srv := range servers {
		if srv != nil {
			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Error().Err(err).Msg("failed to shut down server during startup cleanup")
			}
		}
	}
}

// cleanupStartupResources releases every resource a partial startup may have
// acquired once the local admin socket is serving: the admin socket first (no
// further mutations can begin), then the bound servers, then the traffic
// manager that startProxyServers may already have populated.
func cleanupStartupResources(localAdmin *localAdminServer, trafficManager *trafficadapter.Manager, log zerowrap.Logger, servers ...*http.Server) {
	if localAdmin != nil {
		localAdmin.Close()
	}
	shutdownStartedServers(servers, log)
	shutdownTrafficManagerForStartupCleanup(trafficManager, log)
}

func shutdownTrafficManagerForStartupCleanup(manager *trafficadapter.Manager, log zerowrap.Logger) {
	if manager == nil {
		return
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("failed to shut down traffic manager during startup cleanup")
	}
}

// signalReloadWorker bounds signal-triggered work to one running reload and one
// pending reload. Stop cancels the worker without joining a slow dependency.
type signalReloadWorker struct {
	pending chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
}

func newSignalReloadWorker(ctx context.Context, reload reloadTrigger) *signalReloadWorker {
	ctx, cancel := context.WithCancel(ctx)
	w := &signalReloadWorker{pending: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.pending:
				if ctx.Err() != nil {
					return
				}
				_ = reload.Trigger(ctx)
			}
		}
	}()
	return w
}

func (w *signalReloadWorker) Request() {
	select {
	case w.pending <- struct{}{}:
	default:
	}
}

func (w *signalReloadWorker) Stop() { w.cancel() }
