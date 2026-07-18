package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxMigrationCheckpointBytes int64 = 64 << 10

	// Runtime bootstrap is a private Gordon gRPC Unix socket, never an engine
	// socket or a host-published TCP listener.
	bootstrapRuntimeSocketName = "runtime-control.sock"
	componentDataDirectory     = "/var/lib/gordon"
)

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

type MigrationCheckpoint struct {
	MigrationID               string         `json:"migration_id"`
	SourceVersion             string         `json:"source_version,omitempty"`
	TargetVersion             string         `json:"target_version,omitempty"`
	TargetImage               string         `json:"target_image,omitempty"`
	StartedAt                 time.Time      `json:"started_at"`
	Phase                     MigrationPhase `json:"phase"`
	ComponentGeneration       uint64         `json:"component_generation"`
	OldServingPath            string         `json:"old_serving_path,omitempty"`
	PreparedComponents        []string       `json:"prepared_components,omitempty"`
	RuntimeChannelTransferred bool           `json:"runtime_channel_transferred,omitempty"`
	// BootstrapRuntimeEndpoint is the component-visible private Unix endpoint.
	// It is always beneath the runtime data directory's migration state.
	BootstrapControlEndpoint   string `json:"bootstrap_control_endpoint,omitempty"`
	BootstrapRuntimeEndpoint   string `json:"bootstrap_runtime_endpoint,omitempty"`
	BootstrapEdgeProbeEndpoint string `json:"bootstrap_edge_probe_endpoint,omitempty"`
	// OldServingProbeEndpoint is a fixed literal-loopback endpoint for proving
	// the retained monolith path. It is metadata only; no dynamic runtime
	// address or credential is persisted.
	OldServingProbeEndpoint string                 `json:"old_serving_probe_endpoint,omitempty"`
	PreparedPortBindings    []MigrationPortBinding `json:"prepared_port_bindings,omitempty"`
	PublicPortBindings      []MigrationPortBinding `json:"public_port_bindings,omitempty"`
	// EdgeAppNetworks records only managed network names selected from the
	// runtime snapshot; it never contains container IDs or socket details.
	EdgeAppNetworks         []string `json:"edge_app_networks,omitempty"`
	ConnectedEdgeNetworks   []string `json:"connected_edge_networks,omitempty"`
	EnvFileReferences       []string `json:"env_file_references,omitempty"`
	ConfigFileReferences    []string `json:"config_file_references,omitempty"`
	RouteSnapshotGeneration uint64   `json:"route_snapshot_generation,omitempty"`
	// SwitchAttempts and LastRetryPhase are deliberately metadata only; they
	// allow a failed cutover to be resumed without deleting the old path.
	SwitchAttempts uint64 `json:"switch_attempts,omitempty"`
	LastRetryPhase string `json:"last_retry_phase,omitempty"`
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
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid migration checkpoint file")
	}
	if info.Size() > maxMigrationCheckpointBytes {
		return nil, fmt.Errorf("migration checkpoint exceeds size limit")
	}
	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open migration checkpoint: %w", err)
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

// Save atomically replaces the checkpoint only with a monotonic retry state.
// It neither inspects nor removes volumes or other runtime resources.
func (s *MigrationCheckpointStore) Save(checkpoint MigrationCheckpoint) error {
	if s == nil {
		return fmt.Errorf("migration checkpoint store is required")
	}
	if err := validateCheckpoint(checkpoint); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, err := s.load(); err == nil {
		if old.MigrationID != checkpoint.MigrationID || phaseRank(checkpoint.Phase) < phaseRank(old.Phase) || checkpoint.ComponentGeneration < old.ComponentGeneration {
			return fmt.Errorf("migration checkpoint regression rejected")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.writeAtomic(checkpoint)
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
func writeTemporaryCheckpoint(parent string, data []byte) (string, error) {
	file, err := os.CreateTemp(parent, ".migration-checkpoint-*")
	if err != nil {
		return "", fmt.Errorf("create migration checkpoint: %w", err)
	}
	name := file.Name()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return "", fmt.Errorf("restrict migration checkpoint: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", fmt.Errorf("write migration checkpoint: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", fmt.Errorf("sync migration checkpoint: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close migration checkpoint: %w", err)
	}
	return name, nil
}
func safeCheckpointDestination(path string) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid migration checkpoint file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("lstat migration checkpoint: %w", err)
	}
	return nil
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
	if strings.TrimSpace(checkpoint.MigrationID) == "" || phaseRank(checkpoint.Phase) < 0 {
		return fmt.Errorf("invalid migration checkpoint")
	}
	if checkpoint.StartedAt.IsZero() {
		return fmt.Errorf("invalid migration checkpoint")
	}
	for _, ref := range checkpoint.EnvFileReferences {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("invalid migration checkpoint")
		}
	}
	for _, endpoint := range []string{checkpoint.BootstrapEdgeProbeEndpoint, checkpoint.OldServingProbeEndpoint} {
		if endpoint != "" && validLoopbackProbeEndpoint(endpoint) != nil {
			return fmt.Errorf("invalid migration checkpoint")
		}
	}
	// Bootstrap uses no TCP publication. Retain the field only so old
	// checkpoints fail closed instead of silently acquiring a host gateway.
	if len(checkpoint.PreparedPortBindings) != 0 {
		return fmt.Errorf("invalid migration checkpoint")
	}
	for _, binding := range checkpoint.PublicPortBindings {
		if !validMigrationPortBinding(binding) {
			return fmt.Errorf("invalid migration checkpoint")
		}
	}
	if checkpoint.BootstrapRuntimeEndpoint != "" && !validBootstrapRuntimeEndpoint(checkpoint.BootstrapRuntimeEndpoint, checkpoint.PreparedPortBindings) {
		return fmt.Errorf("invalid migration checkpoint")
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
