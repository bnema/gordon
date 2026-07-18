package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bnema/gordon/internal/domain"
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
		client, err := createPostHandoffRuntimeCommandClient(ctx, target, cfg.Server.DataDir)
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
	token, err := loadPostHandoffRuntimeToken(*checkpoint, cfg.Server.DataDir)
	if err != nil {
		return nil, err
	}
	target := cfg.Runtime
	target.Endpoint = endpoint
	target.ListenAddress = ""
	// The generated replacement runtime owns this credential. In particular,
	// do not reuse the source configuration's token or environment reference:
	// after handoff those can authenticate the stopped monolith, not runtime.
	target.Token = token
	target.TokenEnv = ""
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

const maxRecoveryRuntimeEnvBytes int64 = 64 << 10

// loadPostHandoffRuntimeToken reads precisely the generated runtime role file.
// Checkpoint references are untrusted durable input: every reference must use
// the generated migration directory and role filename, but only runtime.env is
// opened. This intentionally avoids loading edge or registry credentials.
func loadPostHandoffRuntimeToken(checkpoint MigrationCheckpoint, dataDir string) (string, error) {
	root := filepath.Clean(resolveDataDir(dataDir))
	if !filepath.IsAbs(root) || !componentLabelValue.MatchString(checkpoint.MigrationID) || checkpoint.ComponentGeneration == 0 {
		return "", fmt.Errorf("invalid post-handoff runtime environment")
	}
	directory := filepath.Join(root, "migration", "env", checkpoint.MigrationID, fmt.Sprintf("%d", checkpoint.ComponentGeneration))
	var runtimePath string
	seen := make(map[domain.ComponentRole]struct{}, len(checkpoint.EnvFileReferences))
	for _, reference := range checkpoint.EnvFileReferences {
		clean := filepath.Clean(reference)
		role := domain.ComponentRole(strings.TrimSuffix(filepath.Base(reference), ".env"))
		if !filepath.IsAbs(reference) || reference != clean || filepath.Dir(reference) != directory || !domain.IsKnownComponentRole(role) || filepath.Base(reference) != string(role)+".env" {
			return "", fmt.Errorf("invalid post-handoff runtime environment")
		}
		if _, duplicate := seen[role]; duplicate {
			return "", fmt.Errorf("invalid post-handoff runtime environment")
		}
		seen[role] = struct{}{}
		if role == domain.ComponentRoleRuntime {
			runtimePath = reference
		}
	}
	if runtimePath == "" {
		return "", fmt.Errorf("invalid post-handoff runtime environment")
	}
	return readPostHandoffRuntimeToken(runtimePath)
}

func readPostHandoffRuntimeToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxRecoveryRuntimeEnvBytes {
		return "", fmt.Errorf("invalid post-handoff runtime environment")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("invalid post-handoff runtime environment")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxRecoveryRuntimeEnvBytes {
		return "", fmt.Errorf("invalid post-handoff runtime environment")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecoveryRuntimeEnvBytes+1))
	if err != nil || int64(len(data)) > maxRecoveryRuntimeEnvBytes {
		return "", fmt.Errorf("invalid post-handoff runtime environment")
	}
	return parsePostHandoffRuntimeToken(data)
}

func parsePostHandoffRuntimeToken(data []byte) (string, error) {
	var token string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !validPostHandoffRuntimeEnvKey(key) {
			return "", fmt.Errorf("invalid post-handoff runtime environment")
		}
		if key != "GORDON_COMPONENT_RUNTIME_TOKEN" {
			continue
		}
		if token != "" || value == "" || value != strings.TrimSpace(value) {
			return "", fmt.Errorf("invalid post-handoff runtime environment")
		}
		token = value
	}
	if token == "" {
		return "", fmt.Errorf("invalid post-handoff runtime environment")
	}
	return token, nil
}

func validPostHandoffRuntimeEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for index, character := range key {
		if character != '_' && (character < 'A' || character > 'Z') && (index == 0 || character < '0' || character > '9') {
			return false
		}
	}
	return true
}
