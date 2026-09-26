package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	configusecase "github.com/bnema/gordon/internal/usecase/config"
)

// Kernel provides in-process service access for local CLI execution.
//
// It intentionally does not start HTTP servers or register signal handlers.
type Kernel struct {
	authEnabled     bool
	configSvc       in.ConfigService
	containerSvc    in.ContainerService
	backupSvc       in.BackupService
	volumeBackupSvc in.VolumeBackupService
	registrySvc     in.RegistryService
	healthSvc       in.HealthService
	logSvc          in.LogService
	volumeSvc       in.VolumeService
	publicTLSSvc    in.PublicTLSService
	appSvc          in.AppService
	// appAdmin is the daemon-owned app lifecycle (when the full wiring is
	// available). Close cancels and joins its in-flight background deploy
	// executions before any other kernel resource is torn down.
	appAdmin appAdministration
	log      zerowrap.Logger
	cleanup  func()
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
			containerSvc:    svc.containerSvc,
			backupSvc:       svc.backupSvc,
			volumeBackupSvc: svc.volumeBackupSvc,
			registrySvc:     svc.registrySvc,
			healthSvc:       svc.healthSvc,
			logSvc:          svc.logSvc,
			volumeSvc:       svc.volumeSvc,
			publicTLSSvc:    svc.publicTLSSvc,
			appSvc:          svc.appSvc,
			log:             log,
			cleanup:         wrappedCleanup,
		}
		// A nil *AppServiceImpl must not be stored in the interface: the
		// interface would be non-nil and Close would call it.
		if svc.appSvcImpl != nil {
			kernel.appAdmin = svc.appSvcImpl
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

	return &Kernel{
		authEnabled: cfg.Auth.Enabled,
		configSvc:   configSvc,
		log:         log,
		cleanup:     cleanup,
	}, nil
}

func quietInitLogger(Config) (zerowrap.Logger, func(), error) {
	return zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard}), func() {}, nil
}

// Close tears the kernel down. It first cancels and joins daemon-owned app
// administration on a bounded context, so a background deploy execution is
// never torn down mid-flight alongside the state and runtime it uses. When
// that quiescence times out the remaining cleanup is skipped and the error is
// returned: an unfinished execution may still be writing to state.
func (k *Kernel) Close() error {
	if k == nil {
		return nil
	}
	if k.appAdmin != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := quiesceAppAdministration(ctx, k.appAdmin, k.log)
		cancel()
		if err != nil {
			return err
		}
	}
	if k.cleanup != nil {
		k.cleanup()
	}
	return nil
}

func (k *Kernel) Config() in.ConfigService { return k.configSvc }

func (k *Kernel) Container() in.ContainerService { return k.containerSvc }

func (k *Kernel) Backup() in.BackupService { return k.backupSvc }

func (k *Kernel) VolumeBackup() in.VolumeBackupService { return k.volumeBackupSvc }

func (k *Kernel) Registry() in.RegistryService { return k.registrySvc }

func (k *Kernel) Health() in.HealthService { return k.healthSvc }

func (k *Kernel) Logs() in.LogService { return k.logSvc }

func (k *Kernel) Volumes() in.VolumeService { return k.volumeSvc }

func (k *Kernel) PublicTLS() in.PublicTLSService { return k.publicTLSSvc }

func (k *Kernel) Apps() in.AppService { return k.appSvc }

func (k *Kernel) AuthEnabled() bool { return k != nil && k.authEnabled }
