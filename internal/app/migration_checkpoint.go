package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxMigrationCheckpointBytes int64 = 64 << 10

type MigrationPhase string

const (
	MigrationPhasePlanned  MigrationPhase = "planned"
	MigrationPhasePrepared MigrationPhase = "prepared"
	MigrationPhaseSwitched MigrationPhase = "switched"
)

type MigrationCheckpoint struct {
	MigrationID             string         `json:"migration_id"`
	SourceVersion           string         `json:"source_version,omitempty"`
	TargetVersion           string         `json:"target_version,omitempty"`
	TargetImage             string         `json:"target_image,omitempty"`
	StartedAt               time.Time      `json:"started_at"`
	Phase                   MigrationPhase `json:"phase"`
	ComponentGeneration     uint64         `json:"component_generation"`
	OldServingPath          string         `json:"old_serving_path,omitempty"`
	PreparedComponents      []string       `json:"prepared_components,omitempty"`
	EnvFileReferences       []string       `json:"env_file_references,omitempty"`
	RouteSnapshotGeneration uint64         `json:"route_snapshot_generation,omitempty"`
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
	return nil
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
