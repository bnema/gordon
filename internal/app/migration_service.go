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
	externalRoutes any
	candidateImage string
	orchestrator   *MigrationOrchestrator
}

// NewMigrationService requires the explicit, already-loaded component env
// plan used by every prepare and cleanup path.
func NewMigrationService(preflight *MigrationPreflight, store *MigrationCheckpointStore, options MigrationEnvOptions) (*MigrationService, error) {
	if preflight == nil || store == nil {
		return nil, fmt.Errorf("migration preflight and checkpoint store are required")
	}
	effectiveConfig, manifest, err := buildMigrationComponentEnvManifest(options)
	if manifest == nil {
		if err == nil {
			err = fmt.Errorf("component environment manifest is required")
		}
		return nil, err
	}
	service := &MigrationService{
		preflight: preflight, store: store, now: time.Now,
		envManifest: manifest, envError: err, config: effectiveConfig,
		externalRoutes: options.ExternalRoutes, envDirectory: options.Directory,
	}
	if service.envDirectory == "" {
		service.envDirectory = filepath.Join(filepath.Dir(store.Path()), "env")
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
	// RouteSnapshotGeneration remains zero until the generated edge reports a
	// completed application of authoritative route and traffic snapshots over
	// its authenticated control stream. Never invent a bootstrap generation.
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
	if checkpoint.BootstrapRuntimeEndpoint == "" {
		checkpoint.BootstrapRuntimeEndpoint = fmt.Sprintf("unix://%s", filepath.Join(componentDataDirectory, "migration", checkpoint.MigrationID, bootstrapRuntimeSocketName))
	}
	if checkpoint.BootstrapEdgeProbeEndpoint == "" {
		checkpoint.BootstrapEdgeProbeEndpoint = "127.0.0.1:18080"
	}
	// Only edge receives a bootstrap listener. It is deliberately a fixed
	// loopback probe that differs from the edge's configured in-container
	// listener, so runtime/control/registry never acquire a host TCP surface.
	if err := s.setPreparedEdgeProbeBinding(checkpoint); err != nil {
		return err
	}
	// Retain the configured monolith registry endpoint as diagnostic checkpoint
	// metadata. Cold migration does not require it to be serving: the operator
	// stops the host service before planning so public listeners are available.
	if checkpoint.OldServingProbeEndpoint == "" && s.config.Server.RegistryPort > 0 {
		checkpoint.OldServingProbeEndpoint = fmt.Sprintf("127.0.0.1:%d", s.config.Server.RegistryPort)
	}
	if checkpoint.PublicPortBindings == nil && s.config.Server.Port > 0 && s.config.Server.RegistryPort > 0 {
		// Split edge accepts plaintext only from the host TLS terminator. Confine
		// both final publishes to loopback so rootless hairpin admission cannot
		// expose an unauthenticated listener on a host network interface.
		checkpoint.PublicPortBindings = []MigrationPortBinding{{Role: "edge", HostIP: "127.0.0.1", HostPort: s.config.Server.Port, ContainerPort: s.config.Server.Port, Protocol: "tcp"}, {Role: "edge", HostIP: "127.0.0.1", HostPort: s.config.Server.RegistryPort, ContainerPort: s.config.Server.RegistryPort, Protocol: "tcp"}}
	}
	if !validBootstrapRuntimeEndpoint(checkpoint.BootstrapRuntimeEndpoint, checkpoint.PreparedPortBindings) {
		return fmt.Errorf("invalid runtime bootstrap transport")
	}
	return nil
}

func (s *MigrationService) setPreparedEdgeProbeBinding(checkpoint *MigrationCheckpoint) error {
	if s.config.Server.Port <= 0 {
		if len(checkpoint.PreparedPortBindings) != 0 {
			return fmt.Errorf("configured edge listener port is required")
		}
		return nil
	}
	binding, err := preparedEdgeProbeBinding(checkpoint.BootstrapEdgeProbeEndpoint, s.config.Server.Port)
	if err != nil {
		return err
	}
	if err := privateEdgeProbePortAvailable(checkpoint.BootstrapEdgeProbeEndpoint); err != nil {
		return err
	}
	if len(checkpoint.PreparedPortBindings) == 0 {
		checkpoint.PreparedPortBindings = []MigrationPortBinding{binding}
		return nil
	}
	if len(checkpoint.PreparedPortBindings) != 1 || checkpoint.PreparedPortBindings[0] != binding {
		return fmt.Errorf("invalid edge bootstrap port binding")
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
	if checkpoint == nil || !componentLabelValue.MatchString(checkpoint.MigrationID) {
		return fmt.Errorf("invalid migration ID for component configuration")
	}
	options := ComponentConfigOptions{ExternalRoutes: s.externalRoutes}
	if s.config.Server.Port > 0 {
		finalBinding, err := finalEdgeListenerBinding(checkpoint.PublicPortBindings, s.config.Server.Port)
		if err != nil {
			return err
		}
		options.FinalEdgeBinding = &finalBinding
	}
	files, err := WriteComponentConfigManifests(s.config, migrationComponentConfigDirectory(s.envDirectory, checkpoint.MigrationID, checkpoint.ComponentGeneration), options)
	if err != nil {
		return err
	}
	checkpoint.ConfigFileReferences = checkpoint.ConfigFileReferences[:0]
	for _, file := range files {
		checkpoint.ConfigFileReferences = append(checkpoint.ConfigFileReferences, file.Path)
	}
	return nil
}

func finalEdgeListenerBinding(bindings []MigrationPortBinding, edgePort int) (MigrationPortBinding, error) {
	var selected MigrationPortBinding
	matches := 0
	for _, binding := range bindings {
		if validFinalEdgeConfigBinding(binding, edgePort) {
			selected = binding
			matches++
		}
	}
	if matches != 1 {
		return MigrationPortBinding{}, fmt.Errorf("final edge listener binding is required")
	}
	return selected, nil
}

func migrationComponentConfigDirectory(envDirectory, migrationID string, generation uint64) string {
	parent := filepath.Dir(envDirectory)
	if filepath.Base(parent) != "migration" {
		parent = filepath.Join(parent, "migration")
	}
	return filepath.Join(parent, "config", migrationID, fmt.Sprintf("%d", generation))
}

func (s *MigrationService) writeComponentEnv(checkpoint *MigrationCheckpoint) error {
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
	edgeID := fmt.Sprintf("gordon-edge-%s-g%d", checkpoint.MigrationID, checkpoint.ComponentGeneration)
	s.envManifest.values[domain.ComponentRoleEdge]["GORDON_COMPONENT_ID"] = edgeID
	// The control token validator binds the migration-scoped edge credential
	// to this deterministic, non-secret identity. This lets it reject a
	// wrong-source acknowledgement without giving control socket authority.
	s.envManifest.values[domain.ComponentRoleControl]["GORDON_MIGRATION_EDGE_COMPONENT_ID"] = edgeID
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
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("safe traffic switch is not available: %w", ErrMigrationNotReady)
	}
	checkpoint, err := s.store.Load()
	if err != nil {
		return nil, fmt.Errorf("load migration checkpoint for switch: %w", err)
	}
	// A fresh post-handoff retry must converge from the durable terminal fact;
	// it neither needs the dead monolith nor a new runtime connection.
	if checkpoint.Phase == MigrationPhaseSwitched {
		return checkpoint, nil
	}
	if s.orchestrator == nil {
		return nil, fmt.Errorf("safe traffic switch is not available: %w", ErrMigrationNotReady)
	}
	// Handoff can occur immediately after the runtime becomes healthy. A
	// replacement CLI must therefore complete the persisted prepare plan through
	// the replacement runtime before it attempts any public listener change.
	if checkpoint.RuntimeChannelTransferred && !checkpoint.PrepareComplete {
		checkpoint, err = s.orchestrator.Prepare(ctx, *checkpoint)
		if err != nil {
			return nil, fmt.Errorf("complete post-handoff prepare: %w", err)
		}
	}
	return s.orchestrator.Switch(ctx, *checkpoint)
}

// MigrationCleanup removes the side-by-side prepared component generation and
// resets the durable checkpoint so a fresh prepare can start over. It is the
// only supported way to abandon a prepare that will not be switched. It fails
// closed for any checkpoint that is already serving traffic or has handed off
// runtime authority: those components must never be removed through the old
// authority, and after handoff the old authority is gone.
func (s *MigrationService) MigrationCleanup(ctx context.Context) error {
	if s == nil || s.store == nil || s.orchestrator == nil {
		return fmt.Errorf("migration cleanup is not available: %w", ErrMigrationNotReady)
	}
	checkpoint, err := s.store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no migration checkpoint: %w", ErrMigrationNotReady)
	}
	if err != nil {
		return fmt.Errorf("load migration checkpoint for cleanup: %w", err)
	}
	if checkpoint.Phase == MigrationPhaseSwitched {
		return fmt.Errorf("migration cleanup refused after cutover: %w", ErrMigrationNotReady)
	}
	if checkpoint.RuntimeChannelTransferred {
		return fmt.Errorf("migration cleanup refused after runtime handoff: %w", ErrMigrationNotReady)
	}
	plan, err := NewComponentLaunchPlan(*checkpoint)
	if err != nil {
		return err
	}
	if err := s.orchestrator.CleanupPrepared(ctx, plan); err != nil {
		return err
	}
	return s.store.Delete()
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
