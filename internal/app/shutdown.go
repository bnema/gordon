package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
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
	for {
		select {
		case err := <-errChan:
			log.Error().Err(err).Msg("server error")
			return
		case <-reloadChan:
			log.Info().Msg("reload signal received (SIGUSR1)")
			_ = reload.Trigger(ctx)
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

// gracefulShutdown stops HTTP servers with a 30s timeout, then shuts down
// the container service and cleans up runtime files. It returns an error when
// app administration could not quiesce: in that case the remaining teardown
// is skipped and the error is propagated to the process entry point, which
// exits non-zero instead of closing state or runtime under an in-flight
// ExecuteDeploy.
func gracefulShutdown(registrySrvs []*http.Server, proxySrv, tlsSrv *http.Server, containerSvc *container.Service, proxySvc *proxy.Service, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, trafficManager *trafficadapter.Manager, monitor *appMonitor, appAdmin appAdministration, appState out.AppState, log zerowrap.Logger) error {
	log.Info().Msg("shutting down Gordon...")

	// Phase 0: stop periodic app recovery first and wait for any in-flight
	// bounded pass, so no reconcile can race runtime, traffic, or state
	// teardown (and no goroutine is left behind).
	monitor.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Phase 0.5: quiesce daemon-owned app administration. New background
	// deploys are refused and in-flight executions are cancelled on the
	// daemon context and joined here — before traffic, runtime, or the app
	// state store is torn down, so no execution can write to closed state.
	// A timeout is fail-closed: the unfinished execution still owns the
	// state and runtime it uses, so teardown stops here and the error makes
	// the process exit non-zero rather than close resources underneath it.
	if err := quiesceAppAdministration(shutdownCtx, appAdmin, log); err != nil {
		return fmt.Errorf("app administration quiescence: %w", err)
	}

	// Phase 1: Stop ingress frontends (TLS, then proxy) — no new traffic accepted
	shutdownHTTPServers(shutdownCtx, log, tlsSrv, proxySrv)

	if trafficManager != nil {
		if err := trafficManager.Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Msg("traffic manager shutdown error")
		}
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

	// Phase 2: Drain in-flight registry push sessions before stopping the backend
	if proxySvc != nil {
		log.Info().Msg("draining in-flight registry requests...")
		if drained := proxySvc.DrainRegistryInFlight(25 * time.Second); !drained {
			log.Warn().Int64("in_flight", proxySvc.RegistryInFlight()).Msg("registry drain timed out; some in-flight pushes may be interrupted")
		}
	}

	// Phase 3: Stop the registry backends.
	shutdownHTTPServers(shutdownCtx, log, registrySrvs...)

	if err := containerSvc.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("error during container shutdown")
	}
	closeAppState(appState, log)

	cleanupInternalCredentials()
	log.Info().Msg("Gordon stopped")
	return nil
}

// shutdownHTTPServers gracefully stops each non-nil server on the shutdown
// context, logging per-server failures so one listener cannot mask another.
func shutdownHTTPServers(ctx context.Context, log zerowrap.Logger, servers ...*http.Server) {
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		if err := srv.Shutdown(ctx); err != nil {
			log.Warn().Err(err).Str("addr", srv.Addr).Msg("server shutdown error")
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
