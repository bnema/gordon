package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const (
	maxMigrationCheckpointBytes int64 = 64 << 10

	// Runtime bootstrap is a private Gordon gRPC Unix socket, never an engine
	// socket or a host-published TCP listener.
	bootstrapRuntimeSocketName = "runtime-control.sock"
	componentDataDirectory     = "/var/lib/gordon"
	migrationAttestationDir    = "attestation"
)

// migrationCheckpointPath places the durable cutover attestation beneath the
// generated migration state directory. Candidate control receives only this
// writable child; it never receives write access to the runtime Unix socket.
func migrationCheckpointPath(dataDir string) string {
	return filepath.Join(resolveDataDir(dataDir), "migration", "migration", migrationAttestationDir, "checkpoint.json")
}

type MigrationPhase string

const (
	MigrationPhasePlanned  MigrationPhase = "planned"
	MigrationPhasePrepared MigrationPhase = "prepared"
	MigrationPhaseSwitched MigrationPhase = "switched"
)

// MigrationPortBinding is an explicit, serialized migration listener binding.
// It is intentionally numeric and deterministic: no runtime-assigned port may
// be persisted in a checkpoint or role artifact.
type MigrationPortBinding struct {
	Role          string `json:"role"`
	HostIP        string `json:"host_ip"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// RuntimeBootstrapEndpoints keeps the component listener and host dial target
// as separate capabilities. Its fields are deliberately private: the host
// path must never enter a checkpoint, status response, log, or lifecycle
// command. Descriptors are reconstructed from validated configuration on each
// prepare/recovery process.
type RuntimeBootstrapEndpoints struct {
	componentEndpointValue string
	hostMigrationRootValue string
	hostDialPathValue      string
	migrationID            string
}

func (e RuntimeBootstrapEndpoints) componentEndpoint() string { return e.componentEndpointValue }
func (e RuntimeBootstrapEndpoints) hostDialPath() string      { return e.hostDialPathValue }

func (e RuntimeBootstrapEndpoints) valid() bool {
	componentPath, ok := runtimeBootstrapSocketPath(e.componentEndpointValue, componentDataDirectory)
	if !ok || filepath.Base(filepath.Dir(componentPath)) != e.migrationID || !componentLabelValue.MatchString(e.migrationID) {
		return false
	}
	hostRoot := filepath.Clean(e.hostMigrationRootValue)
	hostPath := filepath.Clean(e.hostDialPathValue)
	return filepath.IsAbs(hostRoot) && hostRoot == e.hostMigrationRootValue && filepath.Base(hostRoot) == "migration" &&
		filepath.IsAbs(hostPath) && hostPath == e.hostDialPathValue && hostPath == filepath.Join(hostRoot, e.migrationID, bootstrapRuntimeSocketName) &&
		!pathContainsSymlink(filepath.Dir(hostPath))
}

type MigrationCheckpoint struct {
	MigrationID         string         `json:"migration_id"`
	SourceVersion       string         `json:"source_version,omitempty"`
	TargetVersion       string         `json:"target_version,omitempty"`
	TargetImage         string         `json:"target_image,omitempty"`
	StartedAt           time.Time      `json:"started_at"`
	Phase               MigrationPhase `json:"phase"`
	ComponentGeneration uint64         `json:"component_generation"`
	OldServingPath      string         `json:"old_serving_path,omitempty"`
	PreparedComponents  []string       `json:"prepared_components,omitempty"`
	// PrepareComplete is set only after every role has passed runtime-owned
	// health checks and the edge has its managed app-network attachments. A
	// transferred runtime channel alone is never sufficient to switch traffic.
	PrepareComplete           bool `json:"prepare_complete,omitempty"`
	RuntimeChannelTransferred bool `json:"runtime_channel_transferred,omitempty"`
	// BootstrapRuntimeEndpoint is the component-visible private Unix endpoint.
	// It is always beneath the runtime data directory's migration state.
	BootstrapControlEndpoint   string `json:"bootstrap_control_endpoint,omitempty"`
	BootstrapRuntimeEndpoint   string `json:"bootstrap_runtime_endpoint,omitempty"`
	BootstrapEdgeProbeEndpoint string `json:"bootstrap_edge_probe_endpoint,omitempty"`
	// bootstrapRuntimeEndpoints is process-local and intentionally omitted from
	// JSON so a host filesystem path cannot become status-visible.
	bootstrapRuntimeEndpoints RuntimeBootstrapEndpoints
	// OldServingProbeEndpoint is a fixed literal-loopback endpoint for proving
	// the retained monolith path. It is metadata only; no dynamic runtime
	// address or credential is persisted.
	OldServingProbeEndpoint string                 `json:"old_serving_probe_endpoint,omitempty"`
	PreparedPortBindings    []MigrationPortBinding `json:"prepared_port_bindings,omitempty"`
	PublicPortBindings      []MigrationPortBinding `json:"public_port_bindings,omitempty"`
	// EdgeAppNetworks records only managed network names selected from the
	// runtime snapshot; it never contains container IDs or socket details.
	EdgeAppNetworks       []string `json:"edge_app_networks,omitempty"`
	ConnectedEdgeNetworks []string `json:"connected_edge_networks,omitempty"`
	EnvFileReferences     []string `json:"env_file_references,omitempty"`
	ConfigFileReferences  []string `json:"config_file_references,omitempty"`
	// RouteSnapshotGeneration is written only after the expected edge has
	// authenticated a completed application of the matching route and traffic
	// snapshots. Prepare deliberately leaves it zero.
	RouteSnapshotGeneration uint64 `json:"route_snapshot_generation,omitempty"`
	// AppliedEdgeComponentID binds the persisted generation to the generated
	// edge identity that reported it. It contains no runtime/container ID.
	AppliedEdgeComponentID string `json:"applied_edge_component_id,omitempty"`
	// SwitchAttempts and LastRetryPhase are deliberately metadata only; they
	// allow a failed cutover to be resumed without deleting the old path.
	SwitchAttempts uint64 `json:"switch_attempts,omitempty"`
	LastRetryPhase string `json:"last_retry_phase,omitempty"`
	// CutoverFailureCode is a fixed, non-engine status for an interrupted
	// listener handoff. It intentionally excludes ports, paths, IDs and engine
	// text so a fresh status can safely explain whether switch may be retried.
	CutoverFailureCode      string `json:"cutover_failure_code,omitempty"`
	CutoverFailureRetryable bool   `json:"cutover_failure_retryable,omitempty"`
	// CutoverSubphase is a fixed intent record written and synced before the
	// runtime mutates listener ownership. It contains no engine-derived data.
	CutoverSubphase domain.MigrationCutoverSubphase `json:"cutover_subphase,omitempty"`
}

type MigrationCheckpointStore struct {
	path string
	mu   sync.Mutex
}

func NewMigrationCheckpointStore(path string) (*MigrationCheckpointStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("migration checkpoint path is required")
	}
	return &MigrationCheckpointStore{path: filepath.Clean(path)}, nil
}

func (s *MigrationCheckpointStore) Path() string { return s.path }

func (s *MigrationCheckpointStore) Load() (*MigrationCheckpoint, error) {
	if s == nil {
		return nil, fmt.Errorf("migration checkpoint store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *MigrationCheckpointStore) load() (*MigrationCheckpoint, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("lstat migration checkpoint: %w", err)
	}
	if !safeCheckpointFileInfo(info) {
		return nil, fmt.Errorf("invalid migration checkpoint file")
	}
	if info.Size() > maxMigrationCheckpointBytes {
		return nil, fmt.Errorf("migration checkpoint exceeds size limit")
	}
	file, err := openRegularCheckpoint(s.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMigrationCheckpointBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read migration checkpoint: %w", err)
	}
	if int64(len(data)) > maxMigrationCheckpointBytes {
		return nil, fmt.Errorf("migration checkpoint exceeds size limit")
	}
	var checkpoint MigrationCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("invalid migration checkpoint")
	}
	if err := validateCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

// Save serializes all writers, including separate control and monolith
// processes. A writer always merges its stale view with the current durable
// checkpoint while holding the adjacent advisory lock, so an authenticated
// edge acknowledgement cannot be overwritten by preparation progress.
func (s *MigrationCheckpointStore) Save(checkpoint MigrationCheckpoint) error {
	if s == nil {
		return fmt.Errorf("migration checkpoint store is required")
	}
	if err := validateCheckpoint(checkpoint); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withLock(func() error {
		merged := checkpoint
		if old, err := s.load(); err == nil {
			merged, err = mergeMigrationCheckpoints(*old, checkpoint)
			if err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := validateCheckpoint(merged); err != nil {
			return err
		}
		return s.writeAtomic(merged)
	})
}

// Delete removes the durable checkpoint so a fresh prepare can start from a
// clean slate. It holds the same cross-process advisory lock as Save and
// fsyncs the parent directory, so the removal survives a crash. A missing
// checkpoint is a durable no-op; a checkpoint that is not a safe owner-only
// regular file is refused rather than unlinked.
func (s *MigrationCheckpointStore) Delete() error {
	if s == nil {
		return fmt.Errorf("migration checkpoint store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withLock(func() error {
		if err := safeCheckpointDestination(s.path); err != nil {
			return err
		}
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove migration checkpoint: %w", err)
		}
		return syncCheckpointParent(filepath.Dir(s.path))
	})
}

// RecordMigrationCutoverSubphase durably records a fixed mutation intent.
// The runtime calls it before each listener-transfer mutation; the write is
// atomic and fsynced by writeAtomic before the engine is invoked.
func (s *MigrationCheckpointStore) RecordMigrationCutoverSubphase(_ context.Context, command domain.RuntimeSelfUpdateCommand, subphase domain.MigrationCutoverSubphase) error {
	if s == nil || subphase == domain.MigrationCutoverSubphaseNone || !domain.IsMigrationCutoverSubphase(subphase) {
		return fmt.Errorf("invalid migration cutover subphase")
	}
	migrationID := strings.TrimPrefix(command.PolicyDecisionID, "migration:")
	if command.LifecycleAction != domain.RuntimeComponentLifecycleActivate || command.TargetComponentRole != domain.ComponentRoleEdge || migrationID == "" {
		return fmt.Errorf("invalid migration cutover command")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withLock(func() error {
		checkpoint, err := s.load()
		if err != nil {
			return fmt.Errorf("load migration checkpoint for cutover subphase: %w", err)
		}
		if checkpoint.Phase != MigrationPhasePrepared || !matchesRuntimeCutover(*checkpoint, command, migrationID) {
			return fmt.Errorf("migration cutover does not match prepared checkpoint")
		}
		checkpoint.CutoverSubphase = subphase
		checkpoint.CutoverFailureCode = ""
		checkpoint.CutoverFailureRetryable = false
		checkpoint.LastRetryPhase = "switch"
		return s.writeAtomic(*checkpoint)
	})
}

// MigrationCutoverSubphase returns the current fixed intent marker after
// checking the same authenticated lifecycle identity used for commit.
func (s *MigrationCheckpointStore) MigrationCutoverSubphase(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.MigrationCutoverSubphase, error) {
	if s == nil {
		return domain.MigrationCutoverSubphaseNone, fmt.Errorf("migration checkpoint store is required")
	}
	migrationID := strings.TrimPrefix(command.PolicyDecisionID, "migration:")
	if command.LifecycleAction != domain.RuntimeComponentLifecycleActivate || command.TargetComponentRole != domain.ComponentRoleEdge || migrationID == "" {
		return domain.MigrationCutoverSubphaseNone, fmt.Errorf("invalid migration cutover command")
	}
	checkpoint, err := s.Load()
	if err != nil {
		return domain.MigrationCutoverSubphaseNone, fmt.Errorf("load migration checkpoint for cutover state: %w", err)
	}
	if !matchesRuntimeCutover(*checkpoint, command, migrationID) {
		return domain.MigrationCutoverSubphaseNone, fmt.Errorf("migration cutover does not match checkpoint")
	}
	return checkpoint.CutoverSubphase, nil
}

// CommitMigrationCutover is called by the replacement runtime after it has
// made the final edge listener healthy. It validates the exact authenticated
// lifecycle identity while holding the same cross-process checkpoint lock, so
// a killed old CLI cannot lose or forge the switched result.
func (s *MigrationCheckpointStore) CommitMigrationCutover(_ context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if s == nil {
		return fmt.Errorf("migration checkpoint store is required")
	}
	if command.LifecycleAction != domain.RuntimeComponentLifecycleActivate || command.TargetComponentRole != domain.ComponentRoleEdge {
		return fmt.Errorf("invalid migration cutover command")
	}
	if !strings.HasPrefix(command.PolicyDecisionID, "migration:") {
		return fmt.Errorf("invalid migration cutover identity")
	}
	migrationID := strings.TrimPrefix(command.PolicyDecisionID, "migration:")
	if migrationID == "" || command.Generation == 0 {
		return fmt.Errorf("invalid migration cutover identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withLock(func() error {
		checkpoint, err := s.load()
		if err != nil {
			return fmt.Errorf("load migration checkpoint for cutover: %w", err)
		}
		if checkpoint.Phase == MigrationPhaseSwitched {
			if matchesRuntimeCutover(*checkpoint, command, migrationID) {
				return nil
			}
			return fmt.Errorf("migration cutover does not match switched checkpoint")
		}
		if checkpoint.Phase != MigrationPhasePrepared || !matchesRuntimeCutover(*checkpoint, command, migrationID) {
			return fmt.Errorf("migration cutover does not match prepared checkpoint")
		}
		checkpoint.Phase = MigrationPhaseSwitched
		checkpoint.CutoverSubphase = domain.MigrationCutoverSubphaseBeforeCommit
		checkpoint.LastRetryPhase = ""
		checkpoint.CutoverFailureCode = ""
		checkpoint.CutoverFailureRetryable = false
		if err := validateCheckpoint(*checkpoint); err != nil {
			return err
		}
		return s.writeAtomic(*checkpoint)
	})
}

// RecordMigrationCutoverFailure persists only an allowlisted failure outcome
// after the runtime has restored the old listener. The engine error itself
// remains process-local: status needs a retry decision, not host internals.
func (s *MigrationCheckpointStore) RecordMigrationCutoverFailure(_ context.Context, command domain.RuntimeSelfUpdateCommand, code string, retryable bool) error {
	if s == nil || !validCutoverFailureCode(code) {
		return fmt.Errorf("invalid migration cutover failure")
	}
	migrationID := strings.TrimPrefix(command.PolicyDecisionID, "migration:")
	if command.LifecycleAction != domain.RuntimeComponentLifecycleActivate || command.TargetComponentRole != domain.ComponentRoleEdge || migrationID == "" {
		return fmt.Errorf("invalid migration cutover command")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withLock(func() error {
		checkpoint, err := s.load()
		if err != nil {
			return fmt.Errorf("load migration checkpoint for cutover failure: %w", err)
		}
		if checkpoint.Phase != MigrationPhasePrepared || !matchesRuntimeCutover(*checkpoint, command, migrationID) {
			return fmt.Errorf("migration cutover does not match prepared checkpoint")
		}
		checkpoint.CutoverFailureCode = code
		checkpoint.CutoverFailureRetryable = retryable
		checkpoint.CutoverSubphase = domain.MigrationCutoverSubphaseNone
		checkpoint.LastRetryPhase = "switch"
		return s.writeAtomic(*checkpoint)
	})
}

func validCutoverFailureCode(code string) bool {
	return code == "listener_release_timeout" || code == "cutover_failed"
}

func matchesRuntimeCutover(checkpoint MigrationCheckpoint, command domain.RuntimeSelfUpdateCommand, migrationID string) bool {
	return checkpoint.MigrationID == migrationID && checkpoint.ComponentGeneration == command.Generation && checkpoint.RouteSnapshotGeneration != 0 &&
		checkpoint.AppliedEdgeComponentID == command.TargetComponentID && checkpoint.OldServingPath == command.OldServingComponentID &&
		slices.Equal(componentPublicPorts(checkpoint.PublicPortBindings, domain.ComponentRoleEdge), command.FinalPortPublishes)
}

// withLock uses a persistent, owner-only lock file rather than the checkpoint
// inode itself: atomic rename changes that inode, while this adjacent inode is
// stable across every durable replacement. flock is released by the kernel if
// a writer crashes.
func (s *MigrationCheckpointStore) withLock(fn func() error) (result error) {
	parent := filepath.Dir(s.path)
	if err := prepareCheckpointParent(parent); err != nil {
		return err
	}
	lock, err := openCheckpointLock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer func() {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil && result == nil {
			result = fmt.Errorf("unlock migration checkpoint: %w", err)
		}
		if err := lock.Close(); err != nil && result == nil {
			result = fmt.Errorf("close migration checkpoint lock: %w", err)
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock migration checkpoint: %w", err)
	}
	return fn()
}

func openCheckpointLock(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !safeCheckpointFileInfo(info) {
			return nil, fmt.Errorf("invalid migration checkpoint lock")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("lstat migration checkpoint lock: %w", err)
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open migration checkpoint lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !safeCheckpointFileInfo(info) {
		_ = file.Close()
		return nil, fmt.Errorf("invalid migration checkpoint lock")
	}
	return file, nil
}

func mergeMigrationCheckpoints(old, candidate MigrationCheckpoint) (MigrationCheckpoint, error) {
	if !sameMigrationCheckpointIdentity(old, candidate) || phaseRank(candidate.Phase) < phaseRank(old.Phase) {
		return MigrationCheckpoint{}, fmt.Errorf("migration checkpoint regression rejected")
	}
	if old.AppliedEdgeComponentID != "" && candidate.AppliedEdgeComponentID != "" && old.AppliedEdgeComponentID != candidate.AppliedEdgeComponentID {
		return MigrationCheckpoint{}, fmt.Errorf("migration checkpoint edge attestation conflict")
	}
	merged := candidate
	merged.PreparedComponents = stableStringUnion(old.PreparedComponents, candidate.PreparedComponents)
	merged.ConnectedEdgeNetworks = stableStringUnion(old.ConnectedEdgeNetworks, candidate.ConnectedEdgeNetworks)
	merged.RuntimeChannelTransferred = old.RuntimeChannelTransferred || candidate.RuntimeChannelTransferred
	merged.PrepareComplete = old.PrepareComplete || candidate.PrepareComplete
	mergeCutoverSubphase(old, candidate, &merged)
	if old.RouteSnapshotGeneration > candidate.RouteSnapshotGeneration {
		merged.RouteSnapshotGeneration = old.RouteSnapshotGeneration
		merged.AppliedEdgeComponentID = old.AppliedEdgeComponentID
	} else if old.RouteSnapshotGeneration == candidate.RouteSnapshotGeneration && merged.AppliedEdgeComponentID == "" {
		merged.AppliedEdgeComponentID = old.AppliedEdgeComponentID
	} else if old.AppliedEdgeComponentID != "" && candidate.AppliedEdgeComponentID == "" {
		return MigrationCheckpoint{}, fmt.Errorf("migration checkpoint edge attestation conflict")
	}
	if old.SwitchAttempts > candidate.SwitchAttempts {
		merged.SwitchAttempts = old.SwitchAttempts
		merged.LastRetryPhase = old.LastRetryPhase
	}
	mergeCutoverFailure(old, candidate, &merged)
	return merged, nil
}

func mergeCutoverSubphase(old, candidate MigrationCheckpoint, merged *MigrationCheckpoint) {
	if merged == nil {
		return
	}
	// An explicit recorded failure follows a verified rollback and intentionally
	// clears a prior intent so the next activation starts a new transaction.
	if candidate.CutoverFailureCode != "" {
		merged.CutoverSubphase = domain.MigrationCutoverSubphaseNone
		return
	}
	if old.CutoverSubphase != domain.MigrationCutoverSubphaseNone && candidate.CutoverSubphase == domain.MigrationCutoverSubphaseNone && candidate.Phase != MigrationPhaseSwitched {
		// A stale control writer cannot erase the runtime's durable mutation.
		merged.CutoverSubphase = old.CutoverSubphase
	}
}

func mergeCutoverFailure(old, candidate MigrationCheckpoint, merged *MigrationCheckpoint) {
	if merged == nil || old.CutoverFailureCode == "" || candidate.CutoverFailureCode != "" || candidate.Phase == MigrationPhaseSwitched {
		return
	}
	merged.CutoverFailureCode = old.CutoverFailureCode
	merged.CutoverFailureRetryable = old.CutoverFailureRetryable
}

func sameMigrationCheckpointIdentity(old, candidate MigrationCheckpoint) bool {
	return old.MigrationID == candidate.MigrationID && old.StartedAt.Equal(candidate.StartedAt) && old.ComponentGeneration == candidate.ComponentGeneration &&
		sameMigrationVersions(old, candidate) && sameMigrationEndpoints(old, candidate) && sameMigrationReferences(old, candidate)
}

func sameMigrationVersions(old, candidate MigrationCheckpoint) bool {
	return immutableCheckpointString(old.SourceVersion, candidate.SourceVersion) &&
		immutableCheckpointString(old.TargetVersion, candidate.TargetVersion) &&
		immutableCheckpointString(old.TargetImage, candidate.TargetImage) &&
		immutableCheckpointString(old.OldServingPath, candidate.OldServingPath)
}

func sameMigrationEndpoints(old, candidate MigrationCheckpoint) bool {
	return immutableCheckpointString(old.BootstrapControlEndpoint, candidate.BootstrapControlEndpoint) &&
		immutableCheckpointString(old.BootstrapRuntimeEndpoint, candidate.BootstrapRuntimeEndpoint) &&
		immutableCheckpointString(old.BootstrapEdgeProbeEndpoint, candidate.BootstrapEdgeProbeEndpoint) &&
		immutableCheckpointString(old.OldServingProbeEndpoint, candidate.OldServingProbeEndpoint) &&
		immutableCheckpointSlice(old.PreparedPortBindings, candidate.PreparedPortBindings) &&
		immutableCheckpointSlice(old.PublicPortBindings, candidate.PublicPortBindings)
}

func sameMigrationReferences(old, candidate MigrationCheckpoint) bool {
	return immutableCheckpointSlice(old.EdgeAppNetworks, candidate.EdgeAppNetworks) &&
		immutableCheckpointSlice(old.EnvFileReferences, candidate.EnvFileReferences) &&
		immutableCheckpointSlice(old.ConfigFileReferences, candidate.ConfigFileReferences)
}

func immutableCheckpointString(old, candidate string) bool {
	return old == "" || old == candidate
}

func immutableCheckpointSlice[T comparable](old, candidate []T) bool {
	return len(old) == 0 || slices.Equal(old, candidate)
}

func stableStringUnion(first, second []string) []string {
	result := make([]string, 0, len(first)+len(second))
	for _, values := range [][]string{first, second} {
		for _, value := range values {
			if !slices.Contains(result, value) {
				result = append(result, value)
			}
		}
	}
	return result
}

func (s *MigrationCheckpointStore) writeAtomic(checkpoint MigrationCheckpoint) error {
	parent := filepath.Dir(s.path)
	if err := prepareCheckpointParent(parent); err != nil {
		return err
	}
	data, err := encodeCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	temporary, err := writeTemporaryCheckpoint(parent, data)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := safeCheckpointDestination(s.path); err != nil {
		return err
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return fmt.Errorf("replace migration checkpoint: %w", err)
	}
	return syncCheckpointParent(parent)
}

func prepareCheckpointParent(parent string) error {
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create checkpoint directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("lstat checkpoint directory: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("invalid checkpoint directory")
	}
	if err := os.Chmod(parent, 0o700); err != nil { // #nosec G302 -- checkpoint directories must be owner-only.
		return fmt.Errorf("restrict checkpoint directory: %w", err)
	}
	return nil
}
func encodeCheckpoint(checkpoint MigrationCheckpoint) ([]byte, error) {
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, fmt.Errorf("encode migration checkpoint: %w", err)
	}
	if int64(len(data)) > maxMigrationCheckpointBytes {
		return nil, fmt.Errorf("migration checkpoint exceeds size limit")
	}
	return data, nil
}
func writeTemporaryCheckpoint(parent string, data []byte) (name string, result error) {
	file, err := os.CreateTemp(parent, ".migration-checkpoint-*")
	if err != nil {
		return "", fmt.Errorf("create migration checkpoint: %w", err)
	}
	name = file.Name()
	defer func() {
		if result != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("restrict migration checkpoint: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return "", fmt.Errorf("write migration checkpoint: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync migration checkpoint: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close migration checkpoint: %w", err)
	}
	return name, nil
}
func safeCheckpointDestination(path string) error {
	info, err := os.Lstat(path)
	if err == nil && !safeCheckpointFileInfo(info) {
		return fmt.Errorf("invalid migration checkpoint file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("lstat migration checkpoint: %w", err)
	}
	return nil
}

func safeCheckpointFileInfo(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0o600
}

func openRegularCheckpoint(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("open migration checkpoint: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !safeCheckpointFileInfo(info) {
		_ = file.Close()
		return nil, fmt.Errorf("invalid migration checkpoint file")
	}
	return file, nil
}
func syncCheckpointParent(parent string) error {
	dir, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open checkpoint directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync checkpoint directory: %w", err)
	}
	return nil
}

func validateCheckpoint(checkpoint MigrationCheckpoint) error {
	if strings.TrimSpace(checkpoint.MigrationID) == "" || phaseRank(checkpoint.Phase) < 0 || checkpoint.StartedAt.IsZero() ||
		(checkpoint.CutoverFailureCode != "" && !validCutoverFailureCode(checkpoint.CutoverFailureCode)) ||
		(checkpoint.CutoverFailureRetryable && checkpoint.CutoverFailureCode == "") ||
		!domain.IsMigrationCutoverSubphase(checkpoint.CutoverSubphase) ||
		(checkpoint.Phase == MigrationPhaseSwitched && (checkpoint.CutoverFailureCode != "" || checkpoint.CutoverFailureRetryable)) {
		return fmt.Errorf("invalid migration checkpoint")
	}
	if !validCheckpointReferences(checkpoint.EnvFileReferences) || !validCheckpointTopology(checkpoint) || !validCheckpointAppliedEdge(checkpoint) {
		return fmt.Errorf("invalid migration checkpoint")
	}
	return nil
}

func validCheckpointReferences(references []string) bool {
	for _, reference := range references {
		if strings.TrimSpace(reference) == "" {
			return false
		}
	}
	return true
}

func validCheckpointTopology(checkpoint MigrationCheckpoint) bool {
	for _, endpoint := range []string{checkpoint.BootstrapEdgeProbeEndpoint, checkpoint.OldServingProbeEndpoint} {
		if endpoint != "" && validLoopbackProbeEndpoint(endpoint) != nil {
			return false
		}
	}
	if len(checkpoint.PreparedPortBindings) != 0 && !validPreparedEdgeProbeBindings(checkpoint.BootstrapEdgeProbeEndpoint, checkpoint.PreparedPortBindings) {
		return false
	}
	for _, binding := range checkpoint.PublicPortBindings {
		if !validMigrationPortBinding(binding) {
			return false
		}
	}
	if checkpoint.BootstrapRuntimeEndpoint == "" {
		return true
	}
	path, ok := runtimeBootstrapSocketPath(checkpoint.BootstrapRuntimeEndpoint, componentDataDirectory)
	return ok && filepath.Base(filepath.Dir(path)) == checkpoint.MigrationID
}

func validCheckpointAppliedEdge(checkpoint MigrationCheckpoint) bool {
	return checkpoint.AppliedEdgeComponentID == "" || checkpoint.RouteSnapshotGeneration != 0 && componentLabelValue.MatchString(checkpoint.AppliedEdgeComponentID)
}

// preparedEdgeProbeBinding turns the checkpointed literal loopback endpoint
// into the sole allowed prepare-time host publish. Keeping this conversion in
// one place prevents a checkpoint from smuggling another role or listener into
// the runtime lifecycle command.
func preparedEdgeProbeBinding(endpoint string, containerPort int) (MigrationPortBinding, error) {
	host, rawPort, err := net.SplitHostPort(strings.TrimSpace(endpoint))
	if err != nil || host != "127.0.0.1" || net.ParseIP(host) == nil || containerPort < 1 || containerPort > 65535 {
		return MigrationPortBinding{}, fmt.Errorf("invalid edge bootstrap probe endpoint")
	}
	hostPort, err := strconv.Atoi(rawPort)
	if err != nil || hostPort < 1 || hostPort > 65535 || hostPort == containerPort {
		return MigrationPortBinding{}, fmt.Errorf("invalid edge bootstrap probe endpoint")
	}
	return MigrationPortBinding{Role: "edge", HostIP: host, HostPort: hostPort, ContainerPort: containerPort, Protocol: "tcp"}, nil
}

func validPreparedEdgeProbeBindings(endpoint string, bindings []MigrationPortBinding) bool {
	if len(bindings) != 1 {
		return false
	}
	expected, err := preparedEdgeProbeBinding(endpoint, bindings[0].ContainerPort)
	return err == nil && bindings[0] == expected
}

// privateEdgeProbePortAvailable holds the exact literal-loopback address long
// enough to prove that the bootstrap publish can be claimed before preparation
// writes state or asks runtime to create any component. Runtime's eventual
// engine bind remains authoritative against races after this read-only check.
func privateEdgeProbePortAvailable(endpoint string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(endpoint))
	if err != nil || host != "127.0.0.1" || net.ParseIP(host) == nil {
		return fmt.Errorf("invalid edge bootstrap probe endpoint")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("edge bootstrap probe port is unavailable")
	}
	if err := listener.Close(); err != nil {
		return fmt.Errorf("edge bootstrap probe port is unavailable")
	}
	return nil
}

func validMigrationPortBinding(binding MigrationPortBinding) bool {
	if binding.Role != string("control") && binding.Role != string("runtime") && binding.Role != string("registry") && binding.Role != string("edge") {
		return false
	}
	if binding.HostPort < 1 || binding.HostPort > 65535 || binding.ContainerPort < 1 || binding.ContainerPort > 65535 {
		return false
	}
	return binding.Protocol == "tcp" && (binding.HostIP == "127.0.0.1" || binding.HostIP == "0.0.0.0")
}

// Prepared migration publishes are intentionally narrower than final public
// listeners: only runtime receives one private bootstrap publish. Edge and
// registry must use the internal network until cutover.
func validBootstrapRuntimeEndpoint(endpoint string, _ []MigrationPortBinding) bool {
	path, ok := runtimeBootstrapSocketPath(endpoint, componentDataDirectory)
	return ok && filepath.Base(path) == bootstrapRuntimeSocketName
}

// runtimeBootstrapSocketPath accepts only a clean, absolute unix:// endpoint
// directly below <data>/migration/<migration-id>. It deliberately rejects TCP,
// relative paths, traversal, and any alternate socket name.
func runtimeBootstrapSocketPath(endpoint, dataDir string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "unix" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	path := filepath.Clean(parsed.Path)
	root := filepath.Join(filepath.Clean(dataDir), "migration")
	if !filepath.IsAbs(path) || path != parsed.Path || filepath.Base(path) != bootstrapRuntimeSocketName || filepath.Dir(filepath.Dir(path)) != root || !componentLabelValue.MatchString(filepath.Base(filepath.Dir(path))) {
		return "", false
	}
	return path, true
}

// newRuntimeBootstrapEndpoints translates only the fixed component endpoint
// into the exact host bind source below <configured data_dir>/migration/<id>.
// No endpoint text is accepted from status/checkpoint input for the host side.
func newRuntimeBootstrapEndpoints(componentEndpoint, dataDir, migrationID string) (RuntimeBootstrapEndpoints, error) {
	componentPath, ok := runtimeBootstrapSocketPath(componentEndpoint, componentDataDirectory)
	if !ok || filepath.Base(filepath.Dir(componentPath)) != migrationID || !componentLabelValue.MatchString(migrationID) {
		return RuntimeBootstrapEndpoints{}, fmt.Errorf("invalid runtime bootstrap transport")
	}
	dataRoot := strings.TrimSpace(resolveDataDir(dataDir))
	cleanRoot := filepath.Clean(dataRoot)
	if dataRoot == "" || !filepath.IsAbs(dataRoot) || dataRoot != cleanRoot || pathContainsSymlink(cleanRoot) {
		return RuntimeBootstrapEndpoints{}, fmt.Errorf("invalid runtime bootstrap transport")
	}
	hostMigrationRoot := filepath.Join(cleanRoot, "migration")
	hostPath := filepath.Join(hostMigrationRoot, migrationID, bootstrapRuntimeSocketName)
	endpoints := RuntimeBootstrapEndpoints{componentEndpointValue: componentEndpoint, hostMigrationRootValue: hostMigrationRoot, hostDialPathValue: hostPath, migrationID: migrationID}
	if !endpoints.valid() {
		return RuntimeBootstrapEndpoints{}, fmt.Errorf("invalid runtime bootstrap transport")
	}
	return endpoints, nil
}

// pathContainsSymlink rejects every existing symlink in a descriptor path.
// Missing trailing components are allowed during prepare and are checked again
// by the dialer after the replacement runtime creates its socket.
func pathContainsSymlink(path string) bool {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return true
	}
	current := string(filepath.Separator)
	for part := range strings.SplitSeq(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

func phaseRank(phase MigrationPhase) int {
	switch phase {
	case MigrationPhasePlanned:
		return 0
	case MigrationPhasePrepared:
		return 1
	case MigrationPhaseSwitched:
		return 2
	default:
		return -1
	}
}
