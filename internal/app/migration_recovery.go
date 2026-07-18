package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	edgesnapshotusecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// OpenMigrationCheckpointStore opens only the durable migration checkpoint. It
// intentionally creates neither a container client nor a monolith service, so
// a fresh CLI status read remains available after the old runtime has stopped.
func OpenMigrationCheckpointStore(configPath string) (*MigrationCheckpointStore, error) {
	_, cfg, err := initConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load migration checkpoint configuration: %w", err)
	}
	store, err := NewMigrationCheckpointStore(migrationCheckpointPath(cfg.Server.DataDir))
	if err != nil {
		return nil, fmt.Errorf("open migration checkpoint: %w", err)
	}
	return store, nil
}

// NewPostHandoffMigrationRecovery constructs the deliberately narrow recovery
// plane used only after the old runtime has transferred authority. It connects
// exclusively to the replacement Gordon runtime's authenticated Unix RPC; it
// never constructs an engine client or accepts an engine socket endpoint.
func NewPostHandoffMigrationRecovery(configPath string) (*MigrationService, error) {
	_, cfg, err := initConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load post-handoff migration configuration: %w", err)
	}
	store, err := NewMigrationCheckpointStore(migrationCheckpointPath(cfg.Server.DataDir))
	if err != nil {
		return nil, fmt.Errorf("open post-handoff migration checkpoint: %w", err)
	}
	return newPostHandoffMigrationRecovery(cfg, store, func(ctx context.Context, target RuntimeControlConfig) (RuntimeHandoffClient, error) {
		client, err := createRuntimeCommandClient(ctx, target)
		if err != nil {
			return nil, err
		}
		runtime, ok := client.(RuntimeHandoffClient)
		if !ok {
			return nil, fmt.Errorf("replacement Gordon runtime does not support recovery")
		}
		return runtime, nil
	})
}

func newPostHandoffMigrationRecovery(cfg Config, store *MigrationCheckpointStore, dial func(context.Context, RuntimeControlConfig) (RuntimeHandoffClient, error)) (*MigrationService, error) {
	if store == nil || dial == nil {
		return nil, fmt.Errorf("post-handoff migration recovery is not configured")
	}
	checkpoint, err := store.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no post-handoff migration checkpoint: %w", ErrMigrationNotReady)
		}
		return nil, fmt.Errorf("load post-handoff migration checkpoint: %w", err)
	}
	if !checkpoint.RuntimeChannelTransferred {
		return nil, fmt.Errorf("post-handoff runtime recovery is not available: %w", ErrMigrationNotReady)
	}
	endpoint, err := validatePostHandoffRuntimeEndpoint(*checkpoint, cfg.Server.DataDir)
	if err != nil {
		return nil, err
	}
	target := cfg.Runtime
	target.Endpoint = endpoint
	target.ListenAddress = ""
	if runtimeControlToken(target) == "" {
		return nil, fmt.Errorf("post-handoff runtime authentication is not configured")
	}
	runtime, err := dial(context.Background(), target)
	if err != nil {
		// Transport implementations may include request metadata in their error.
		// Recovery is a CLI surface, so never return an error that could disclose
		// the role credential used for this private RPC.
		return nil, fmt.Errorf("connect authenticated replacement Gordon runtime failed")
	}
	if runtime == nil {
		return nil, fmt.Errorf("replacement Gordon runtime is unavailable")
	}
	launcher, err := NewRuntimeComponentLauncher(runtime)
	if err != nil {
		return nil, fmt.Errorf("create post-handoff runtime launcher: %w", err)
	}
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(MigrationPreflightProbes{}), store, launcher)
	if err != nil {
		return nil, fmt.Errorf("create post-handoff migration orchestrator: %w", err)
	}
	checks, err := newMigrationTrafficChecks(runtime, runtime, store, edgesnapshotusecase.NewAppliedStateTrackerAny(), cfg)
	if err != nil {
		return nil, fmt.Errorf("create post-handoff migration traffic checks: %w", err)
	}
	switcher, err := NewTrafficSwitch(launcher, checks)
	if err != nil {
		return nil, fmt.Errorf("create post-handoff migration traffic switch: %w", err)
	}
	orchestrator.WithTrafficSwitcher(switcher)
	return (&MigrationService{store: store}).WithMigrationOrchestrator(orchestrator), nil
}

// ResumePostHandoff resumes the only safe incomplete state after authority has
// moved: a prepared cutover. Re-running prepare would require the old runtime
// ownership and is therefore deliberately not attempted.
func (s *MigrationService) ResumePostHandoff(ctx context.Context) (*MigrationCheckpoint, error) {
	checkpoint, err := s.Status()
	if err != nil {
		return nil, err
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("no migration checkpoint: %w", ErrMigrationNotReady)
	}
	if checkpoint.Phase == MigrationPhaseSwitched {
		return checkpoint, nil
	}
	if checkpoint.Phase != MigrationPhasePrepared || !checkpoint.RuntimeChannelTransferred {
		return nil, fmt.Errorf("post-handoff resume requires prepared migration: %w", ErrMigrationNotReady)
	}
	return s.Switch(ctx)
}

// validatePostHandoffRuntimeEndpoint converts the component-visible data-dir
// path in the checkpoint to the invoking CLI's host data directory. Both the
// source and translated paths must be the exact migration runtime socket; a
// checkpoint can therefore never redirect recovery to Docker or Podman.
func validatePostHandoffRuntimeEndpoint(checkpoint MigrationCheckpoint, dataDir string) (string, error) {
	componentPath, ok := runtimeBootstrapSocketPath(checkpoint.BootstrapRuntimeEndpoint, componentDataDirectory)
	if !ok || filepath.Base(filepath.Dir(componentPath)) != checkpoint.MigrationID {
		return "", fmt.Errorf("invalid post-handoff runtime transport")
	}
	root := resolveDataDir(dataDir)
	if !filepath.IsAbs(root) || strings.TrimSpace(checkpoint.MigrationID) == "" {
		return "", fmt.Errorf("invalid post-handoff runtime transport")
	}
	endpoint := "unix://" + filepath.Join(root, "migration", checkpoint.MigrationID, bootstrapRuntimeSocketName)
	if _, ok := runtimeBootstrapSocketPath(endpoint, root); !ok {
		return "", fmt.Errorf("invalid post-handoff runtime transport")
	}
	return endpoint, nil
}
