package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	configusecase "github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	edgesnapshotusecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
	secretsusecase "github.com/bnema/gordon/internal/usecase/secrets"
)

// Kernel provides in-process service access for local CLI execution.
//
// It intentionally does not start HTTP servers or register signal handlers.
type Kernel struct {
	authEnabled     bool
	configSvc       in.ConfigService
	secretSvc       in.SecretService
	containerSvc    in.ContainerService
	backupSvc       in.BackupService
	volumeBackupSvc in.VolumeBackupService
	registrySvc     in.RegistryService
	healthSvc       in.HealthService
	logSvc          in.LogService
	volumeSvc       in.VolumeService
	publicTLSSvc    in.PublicTLSService
	cleanup         func()
	// Migration is initialized lazily because ordinary local CLI commands must
	// not create migration files or contact the runtime. The initializer is
	// composed around the already-running monolith runtime worker, never a
	// second Docker/Podman adapter owned by the CLI.
	migrationOnce sync.Once
	migrationSvc  *MigrationService
	migrationErr  error
	migrationInit func() (*MigrationService, error)
}

// NewKernel initializes local services without starting server listeners.
func NewKernel(configPath string) (*Kernel, error) {
	return newKernel(configPath, initLogger)
}

// NewKernelQuiet initializes local services without emitting console logs.
func NewKernelQuiet(configPath string) (*Kernel, error) {
	return newKernel(configPath, quietInitLogger)
}

type kernelLoggerInit func(cfg Config) (zerowrap.Logger, func(), error)

func newKernel(configPath string, initLog kernelLoggerInit) (*Kernel, error) {
	ctx := context.Background()
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return nil, err
	}
	if err := rejectSplitModeKernel(cfg); err != nil {
		return nil, err
	}

	log, cleanup, err := initLog(cfg)
	if err != nil {
		return nil, err
	}
	if cleanup == nil {
		cleanup = func() {}
	}

	ctx = zerowrap.WithCtx(ctx, log)

	// Prefer full service wiring so local CLI can execute the same operations
	// as remote mode without going through HTTP admin endpoints.
	// ACME Reconcile and renewal loop are only started from runServers,
	// so there are no side effects for read-only CLI commands.
	if svc, fullErr := createServicesWithOptions(ctx, v, cfg, log); fullErr == nil {
		// Wrap cleanup to stop public TLS service (with its renewal loop)
		// before the logger is cleaned up.
		wrappedCleanup := func() {
			if svc.publicTLSSvc != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := svc.publicTLSSvc.Stop(ctx); err != nil {
					log.Warn().Err(err).Msg("failed to stop public TLS service")
				}
			}
			cleanup()
		}

		kernel := &Kernel{
			authEnabled:     cfg.Auth.Enabled,
			configSvc:       svc.configSvc,
			secretSvc:       svc.secretSvc,
			containerSvc:    svc.containerSvc,
			backupSvc:       svc.backupSvc,
			volumeBackupSvc: svc.volumeBackupSvc,
			registrySvc:     svc.registrySvc,
			healthSvc:       svc.healthSvc,
			logSvc:          svc.logSvc,
			volumeSvc:       svc.volumeSvc,
			publicTLSSvc:    svc.publicTLSSvc,
			cleanup:         wrappedCleanup,
		}
		kernel.migrationInit = func() (*MigrationService, error) {
			return newMonolithMigrationService(configPath, cfg, svc)
		}
		return kernel, nil
	} else {
		log.Warn().Err(fullErr).Msg("local kernel running in minimal mode")
	}

	configSvc := configusecase.NewService(v, nil)
	if err := configSvc.Load(ctx); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	_, _, _, domainSecretStore, err := createDomainSecretStore(cfg, log)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to create local secret store: %w", err)
	}

	secretSvc := secretsusecase.NewService(domainSecretStore, log, nil)

	return &Kernel{
		authEnabled: cfg.Auth.Enabled,
		configSvc:   configSvc,
		secretSvc:   secretSvc,
		cleanup:     cleanup,
	}, nil
}

func quietInitLogger(Config) (zerowrap.Logger, func(), error) {
	return zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard}), func() {}, nil
}

func (k *Kernel) Close() error {
	if k == nil || k.cleanup == nil {
		return nil
	}
	k.cleanup()
	return nil
}

func (k *Kernel) Config() in.ConfigService { return k.configSvc }

func (k *Kernel) Secrets() in.SecretService { return k.secretSvc }

func (k *Kernel) Container() in.ContainerService { return k.containerSvc }

func (k *Kernel) Backup() in.BackupService { return k.backupSvc }

func (k *Kernel) VolumeBackup() in.VolumeBackupService { return k.volumeBackupSvc }

func (k *Kernel) Registry() in.RegistryService { return k.registrySvc }

func (k *Kernel) Health() in.HealthService { return k.healthSvc }

func (k *Kernel) Logs() in.LogService { return k.logSvc }

func (k *Kernel) Volumes() in.VolumeService { return k.volumeSvc }

func (k *Kernel) PublicTLS() in.PublicTLSService { return k.publicTLSSvc }

// Migration returns the migration facade owned by the existing monolith.
// During bootstrap the monolith remains the sole socket authority and sends
// lifecycle intents through its embedded runtime worker. This boundary keeps
// the local CLI from constructing a second raw runtime adapter.
func (k *Kernel) Migration() (*MigrationService, error) {
	if k == nil || k.migrationInit == nil {
		return nil, fmt.Errorf("local monolith migration is unavailable")
	}
	k.migrationOnce.Do(func() { k.migrationSvc, k.migrationErr = k.migrationInit() })
	return k.migrationSvc, k.migrationErr
}

func (k *Kernel) AuthEnabled() bool { return k != nil && k.authEnabled }

// monolithMigrationRuntime is a deliberately narrow transitional authority.
// It is backed by the monolith's existing worker/lifecycle manager; it does
// not expose the runtime adapter or socket to the CLI or migration service.
type monolithMigrationRuntime struct {
	worker        *container.RuntimeWorker
	probe         out.RuntimeEnvironmentProbe
	listenerProbe out.RuntimePublicListenerProbe
}

func (r *monolithMigrationRuntime) SelfUpdateRuntime(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	return r.worker.SelfUpdate(ctx, command)
}

func (r *monolithMigrationRuntime) ProbeRuntimeEnvironment(ctx context.Context) (out.RuntimeEnvironment, error) {
	if r == nil || r.probe == nil {
		return out.RuntimeEnvironment{}, fmt.Errorf("runtime environment probe unavailable")
	}
	return r.probe.ProbeRuntimeEnvironment(ctx)
}

func (r *monolithMigrationRuntime) ProbePublicListeners(ctx context.Context, ports []int) ([]bool, error) {
	if r == nil || r.listenerProbe == nil {
		return nil, fmt.Errorf("runtime listener ownership probe unavailable")
	}
	return r.listenerProbe.ProbePublicListeners(ctx, ports)
}

func (r *monolithMigrationRuntime) SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	if r == nil || r.worker == nil {
		return nil, fmt.Errorf("runtime state unavailable")
	}
	snapshot, err := r.worker.Snapshot(ctx, 1, "monolith-migration", "gordon-monolith")
	if err != nil {
		return nil, err
	}
	updates := make(chan domain.RuntimeActualStateSnapshot, 1)
	updates <- snapshot
	close(updates)
	return updates, nil
}

func newMonolithMigrationService(configPath string, cfg Config, svc *services) (*MigrationService, error) {
	if svc == nil || svc.runtime == nil || svc.containerSvc == nil {
		return nil, fmt.Errorf("monolith runtime migration is unavailable")
	}
	// The worker is the existing monolith socket authority. It is retained only
	// until the split runtime channel handoff; no local CLI capability reaches
	// svc.runtime directly.
	policy := runtimeRolePolicy(cfg, nil)
	// Migration component networks use a fixed, allowlisted prefix even when
	// the legacy monolith did not enable application network isolation.
	policy.ManagedNetworkPrefix = "gordon-internal"
	worker := container.NewRuntimeWorkerWithPolicy(svc.containerSvc, policy).
		WithComponentLifecycleManager(container.NewRuntimeComponentLifecycleManager(svc.runtime, policy))
	bridge := &monolithMigrationRuntime{worker: worker, probe: svc.runtime, listenerProbe: svc.runtime}
	store, err := NewMigrationCheckpointStore(migrationCheckpointPath(cfg.Server.DataDir))
	if err != nil {
		return nil, fmt.Errorf("create monolith migration checkpoint store: %w", err)
	}
	preflight := newControlMigrationPreflight(configPath, cfg, bridge, bridge)
	v, _, err := initConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load migration routing configuration: %w", err)
	}
	migration, err := NewMigrationService(preflight, store, MigrationEnvOptions{
		Config:                cfg,
		Environment:           componentEnvironmentFromEnviron(os.Environ()),
		RuntimeSocket:         svc.runtimeDetection.SocketPath,
		RuntimeName:           svc.runtimeDetection.RuntimeName,
		RuntimeSocketRequired: true,
		Directory:             filepath.Join(resolveDataDir(cfg.Server.DataDir), "migration", "env"),
		ExternalRoutes:        v.Get("external_routes"),
	})
	if err != nil {
		return nil, err
	}
	launcher, err := NewRuntimeComponentLauncherWithHandoff(bridge, newRuntimeHandoffDialer(cfg.Runtime))
	if err != nil {
		return nil, err
	}
	orchestrator, err := NewMigrationOrchestrator(preflight, store, launcher)
	if err != nil {
		return nil, err
	}
	orchestrator.WithRuntimeSnapshotAppNetworks(bridge)
	// Bootstrap uses the embedded runtime worker as the sole socket authority,
	// but the same concrete fail-closed switcher is installed as split control.
	// Until the candidate edge reports authenticated applied state and probes
	// are available, it cannot activate the replacement listener.
	checks, checkErr := newMigrationTrafficChecks(bridge, bridge, store, edgesnapshotusecase.NewAppliedStateTrackerAny(), cfg)
	if checkErr != nil {
		return nil, fmt.Errorf("create monolith migration traffic checks: %w", checkErr)
	}
	// The launcher now forwards to the proven replacement runtime, so its
	// cutover handler survives stopping this monolith/CLI container.
	switcher, switchErr := NewTrafficSwitch(launcher, checks)
	if switchErr != nil {
		return nil, fmt.Errorf("create monolith migration traffic switch: %w", switchErr)
	}
	orchestrator.WithTrafficSwitcher(switcher)
	return migration.WithMigrationOrchestrator(orchestrator).WithMigrationCandidateImage(os.Getenv("GORDON_MIGRATION_IMAGE")), nil
}
