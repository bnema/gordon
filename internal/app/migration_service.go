package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// MigrationService is the sole Phase 2 orchestration facade.  It deliberately
// does not launch components, switch listeners, delete volumes, or access a
// runtime socket; those mutations are introduced by later migration phases.
type MigrationService struct {
	preflight      *MigrationPreflight
	store          *MigrationCheckpointStore
	now            func() time.Time
	envManifest    *ComponentEnvManifest
	envError       error
	envDirectory   string
	config         Config
	candidateImage string
	orchestrator   *MigrationOrchestrator
}

// NewMigrationService accepts an optional, already-loaded component env plan.
// Keeping it optional preserves the Phase 2 facade for callers that have not
// yet composed split roles, while production always provides it.
func NewMigrationService(preflight *MigrationPreflight, store *MigrationCheckpointStore, envOptions ...MigrationEnvOptions) (*MigrationService, error) {
	if preflight == nil || store == nil {
		return nil, fmt.Errorf("migration preflight and checkpoint store are required")
	}
	if len(envOptions) > 1 {
		return nil, fmt.Errorf("only one migration environment configuration is allowed")
	}
	service := &MigrationService{preflight: preflight, store: store, now: time.Now}
	if len(envOptions) == 1 {
		options := envOptions[0]
		manifest, err := BuildMigrationComponentEnvManifest(options)
		if manifest == nil {
			return nil, err
		}
		service.envManifest, service.envError = manifest, err
		service.config = options.Config
		service.envDirectory = options.Directory
		if service.envDirectory == "" {
			service.envDirectory = filepath.Join(filepath.Dir(store.Path()), "env")
		}
	}
	return service, nil
}

// WithMigrationOrchestrator enables side-by-side component preparation for the
// production control service. The launcher stays behind an authenticated
// runtime boundary; this method does not introduce a local runtime adapter.
func (s *MigrationService) WithMigrationOrchestrator(orchestrator *MigrationOrchestrator) *MigrationService {
	if s != nil {
		s.orchestrator = orchestrator
	}
	return s
}

// WithMigrationCandidateImage supplies the explicitly configured Gordon image
// used by split components. The value is never copied to reports or errors.
func (s *MigrationService) WithMigrationCandidateImage(image string) *MigrationService {
	if s != nil {
		s.candidateImage = strings.TrimSpace(image)
	}
	return s
}

func (s *MigrationService) Plan(ctx context.Context) (MigrationPreflightReport, error) {
	if s == nil || s.preflight == nil {
		return MigrationPreflightReport{}, fmt.Errorf("migration service is not configured")
	}
	report := s.preflight.Check(ctx)
	if s.envError != nil {
		report.Checks = append(report.Checks, PreflightCheck{Name: "component_environment", Category: PreflightEnv, Status: PreflightFail, Remediation: s.envError.Error()})
		report.Ready = false
	}
	return report, nil
}

// Prepare records an idempotent retry point after all read-only preflight
// checks pass. Component launch is intentionally deferred to Phase 4.
func (s *MigrationService) Prepare(ctx context.Context, checkpoint MigrationCheckpoint) (*MigrationCheckpoint, error) {
	if s != nil && s.envError != nil {
		return nil, s.envError
	}
	report, err := s.Plan(ctx)
	if err != nil {
		return nil, err
	}
	if !report.Ready {
		return nil, fmt.Errorf("migration preflight failed")
	}
	checkpoint, err = s.prepareCheckpoint(checkpoint)
	if err != nil {
		return nil, err
	}
	if err := s.writeComponentEnv(&checkpoint); err != nil {
		return nil, err
	}
	if err := s.writeComponentConfig(&checkpoint); err != nil {
		return nil, err
	}
	if err := s.store.Save(checkpoint); err != nil {
		return nil, err
	}
	if s.orchestrator != nil {
		return s.orchestrator.Prepare(ctx, checkpoint)
	}
	return s.store.Load()
}

func (s *MigrationService) prepareCheckpoint(checkpoint MigrationCheckpoint) (MigrationCheckpoint, error) {
	if checkpoint.MigrationID == "" {
		existing, err := s.loadOrCreateCheckpoint()
		if err != nil {
			return MigrationCheckpoint{}, err
		}
		checkpoint = existing
	}
	if checkpoint.Phase == "" || checkpoint.Phase == MigrationPhasePlanned {
		checkpoint.Phase = MigrationPhasePrepared
	}
	if checkpoint.Phase != MigrationPhasePrepared {
		return MigrationCheckpoint{}, fmt.Errorf("prepare requires prepared phase")
	}
	if checkpoint.ComponentGeneration == 0 {
		checkpoint.ComponentGeneration = 1
	}
	if checkpoint.TargetImage == "" && s.orchestrator != nil {
		checkpoint.TargetImage = s.candidateImage
	}
	if checkpoint.TargetImage == "" && s.orchestrator != nil {
		return MigrationCheckpoint{}, fmt.Errorf("migration candidate image is not configured")
	}
	if checkpoint.OldServingPath == "" {
		checkpoint.OldServingPath = "monolith"
	}
	if err := s.setBootstrapListeners(&checkpoint); err != nil {
		return MigrationCheckpoint{}, err
	}
	if checkpoint.StartedAt.IsZero() {
		checkpoint.StartedAt = s.now().UTC()
	}
	return checkpoint, nil
}

// setBootstrapListeners records the deterministic private Gordon runtime
// socket. It creates no host TCP publish and never exposes an engine socket.
func (s *MigrationService) setBootstrapListeners(checkpoint *MigrationCheckpoint) error {
	if checkpoint == nil {
		return fmt.Errorf("migration checkpoint is required")
	}
	if len(checkpoint.PreparedPortBindings) != 0 {
		return fmt.Errorf("runtime bootstrap must not publish TCP ports")
	}
	if checkpoint.BootstrapRuntimeEndpoint == "" {
		checkpoint.BootstrapRuntimeEndpoint = fmt.Sprintf("unix://%s", filepath.Join(componentDataDirectory, "migration", checkpoint.MigrationID, bootstrapRuntimeSocketName))
	}
	if checkpoint.BootstrapEdgeProbeEndpoint == "" {
		checkpoint.BootstrapEdgeProbeEndpoint = "127.0.0.1:18080"
	}
	if checkpoint.OldServingProbeEndpoint == "" && s.config.Server.Port > 0 {
		checkpoint.OldServingProbeEndpoint = fmt.Sprintf("127.0.0.1:%d", s.config.Server.Port)
	}
	if checkpoint.PublicPortBindings == nil && s.config.Server.Port > 0 && s.config.Server.RegistryPort > 0 {
		checkpoint.PublicPortBindings = []MigrationPortBinding{{Role: "edge", HostIP: "0.0.0.0", HostPort: s.config.Server.Port, ContainerPort: s.config.Server.Port, Protocol: "tcp"}, {Role: "edge", HostIP: "0.0.0.0", HostPort: s.config.Server.RegistryPort, ContainerPort: s.config.Server.RegistryPort, Protocol: "tcp"}}
	}
	if !validBootstrapRuntimeEndpoint(checkpoint.BootstrapRuntimeEndpoint, checkpoint.PreparedPortBindings) {
		return fmt.Errorf("invalid runtime bootstrap transport")
	}
	return nil
}

func (s *MigrationService) loadOrCreateCheckpoint() (MigrationCheckpoint, error) {
	existing, err := s.store.Load()
	if err == nil {
		return *existing, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return MigrationCheckpoint{MigrationID: "migration"}, nil
	}
	return MigrationCheckpoint{}, err
}

func (s *MigrationService) writeComponentConfig(checkpoint *MigrationCheckpoint) error {
	if s.envManifest == nil {
		return nil
	}
	if checkpoint == nil || !componentLabelValue.MatchString(checkpoint.MigrationID) {
		return fmt.Errorf("invalid migration ID for component configuration")
	}
	files, err := WriteComponentConfigManifests(s.config, migrationComponentConfigDirectory(s.envDirectory, checkpoint.MigrationID, checkpoint.ComponentGeneration))
	if err != nil {
		return err
	}
	checkpoint.ConfigFileReferences = checkpoint.ConfigFileReferences[:0]
	for _, file := range files {
		checkpoint.ConfigFileReferences = append(checkpoint.ConfigFileReferences, file.Path)
	}
	return nil
}

func migrationComponentConfigDirectory(envDirectory, migrationID string, generation uint64) string {
	parent := filepath.Dir(envDirectory)
	if filepath.Base(parent) != "migration" {
		parent = filepath.Join(parent, "migration")
	}
	return filepath.Join(parent, "config", migrationID, fmt.Sprintf("%d", generation))
}

func (s *MigrationService) writeComponentEnv(checkpoint *MigrationCheckpoint) error {
	if s.envManifest == nil {
		return nil
	}
	if checkpoint == nil || !componentLabelValue.MatchString(checkpoint.MigrationID) {
		return fmt.Errorf("invalid migration ID for component environment")
	}
	// This non-secret, deterministic identity is the only identity the edge is
	// allowed to put in an applied-state acknowledgement. Control compares it
	// to the mTLS/token-established identity and never trusts a discovered name.
	if s.envManifest.values == nil {
		s.envManifest.values = make(map[domain.ComponentRole]map[string]string)
	}
	if s.envManifest.values[domain.ComponentRoleEdge] == nil {
		s.envManifest.values[domain.ComponentRoleEdge] = make(map[string]string)
	}
	s.envManifest.values[domain.ComponentRoleEdge]["GORDON_COMPONENT_ID"] = fmt.Sprintf("gordon-edge-%s-g%d", checkpoint.MigrationID, checkpoint.ComponentGeneration)
	files, err := s.envManifest.WriteFiles(filepath.Join(s.envDirectory, checkpoint.MigrationID, fmt.Sprintf("%d", checkpoint.ComponentGeneration)))
	if err != nil {
		return err
	}
	checkpoint.EnvFileReferences = checkpoint.EnvFileReferences[:0]
	for _, file := range files {
		checkpoint.EnvFileReferences = append(checkpoint.EnvFileReferences, file.Path)
	}
	return nil
}

// Switch delegates public cutover to the configured orchestrator. A service
// without the runtime-owned switch channel fails closed.
func (s *MigrationService) Switch(ctx context.Context) (*MigrationCheckpoint, error) {
	if s == nil || s.orchestrator == nil {
		return nil, fmt.Errorf("safe traffic switch is not available: %w", ErrMigrationNotReady)
	}
	checkpoint, err := s.store.Load()
	if err != nil {
		return nil, fmt.Errorf("load migration checkpoint for switch: %w", err)
	}
	return s.orchestrator.Switch(ctx, *checkpoint)
}

func (s *MigrationService) Status() (*MigrationCheckpoint, error) {
	checkpoint, err := s.store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return checkpoint, err
}

// Resume advances only through the same safe prepare checkpoint; a previous
// generation/phase can never regress because Store.Save enforces monotonicity.
func (s *MigrationService) Resume(ctx context.Context) (*MigrationCheckpoint, error) {
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
	checkpoint.Phase = MigrationPhasePrepared
	return s.Prepare(ctx, *checkpoint)
}

var ErrMigrationNotReady = errors.New("migration operation is not ready")
