package app

import (
	"context"
	"fmt"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	configusecase "github.com/bnema/gordon/internal/usecase/config"
	secretsusecase "github.com/bnema/gordon/internal/usecase/secrets"
)

// Kernel provides in-process service access for local CLI execution.
//
// It intentionally does not start HTTP servers or register signal handlers.
type Kernel struct {
	authEnabled  bool
	configSvc    in.ConfigService
	secretSvc    in.SecretService
	containerSvc in.ContainerService
	backupSvc    in.BackupService
	registrySvc  in.RegistryService
	healthSvc    in.HealthService
	logSvc       in.LogService
	cleanup      func()
}

// NewKernel initializes local services without starting server listeners.
func NewKernel(configPath string) (*Kernel, error) {
	ctx := context.Background()
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return nil, err
	}

	log, cleanup, err := initLogger(cfg)
	if err != nil {
		return nil, err
	}
	if cleanup == nil {
		cleanup = func() {}
	}

	ctx = zerowrap.WithCtx(ctx, log)

	// Prefer full service wiring so local CLI can execute the same operations
	// as remote mode without going through HTTP admin endpoints.
	if svc, fullErr := createServices(ctx, v, cfg, log); fullErr == nil {
		return &Kernel{
			authEnabled:  cfg.Auth.Enabled,
			configSvc:    svc.configSvc,
			secretSvc:    svc.secretSvc,
			containerSvc: svc.containerSvc,
			backupSvc:    svc.backupSvc,
			registrySvc:  svc.registrySvc,
			healthSvc:    svc.healthSvc,
			logSvc:       svc.logSvc,
			cleanup:      cleanup,
		}, nil
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

	secretSvc := secretsusecase.NewService(domainSecretStore, log)

	return &Kernel{
		authEnabled: cfg.Auth.Enabled,
		configSvc:   configSvc,
		secretSvc:   secretSvc,
		cleanup:     cleanup,
	}, nil
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

func (k *Kernel) Registry() in.RegistryService { return k.registrySvc }

func (k *Kernel) Health() in.HealthService { return k.healthSvc }

func (k *Kernel) Logs() in.LogService { return k.logSvc }

func (k *Kernel) AuthEnabled() bool { return k != nil && k.authEnabled }
