package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ComponentEventStore is a small durable, bounded control-plane idempotency
// store. Atomic rename means a crash leaves either the previous valid file or
// the complete next file, never a partially written acknowledgement.
type ComponentEventStore struct {
	path     string
	capacity int
	syncDir  func(string) error
	mu       sync.Mutex
}

type componentEventStoreData struct {
	Processed map[string]time.Time `json:"processed"`
	Intents   map[string]time.Time `json:"intents"`
}

func NewComponentEventStore(path string, capacity int) (*ComponentEventStore, error) {
	if capacity <= 0 {
		capacity = 1024
	}
	if path == "" {
		return nil, fmt.Errorf("component event store path is required")
	}
	store := &ComponentEventStore{path: filepath.Clean(path), capacity: capacity, syncDir: syncParentDirectory}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, err := store.readLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *ComponentEventStore) IsComponentEventProcessed(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readLocked()
	if err != nil {
		return false, err
	}
	_, ok := data.Processed[key]
	return ok, nil
}

func (s *ComponentEventStore) MarkComponentEventProcessed(ctx context.Context, key string, processedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readLocked()
	if err != nil {
		return err
	}
	data.Processed[key] = processedAt.UTC()
	trimOldest(data.Processed, s.capacity)
	return s.writeLocked(data)
}

// LoadManualDeploymentIntents implements controlplane.ManualIntentStore.
func (s *ComponentEventStore) LoadManualDeploymentIntents(ctx context.Context) (map[string]time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	return cloneTimeMap(data.Intents), nil
}

// SaveManualDeploymentIntents implements controlplane.ManualIntentStore.
func (s *ComponentEventStore) SaveManualDeploymentIntents(ctx context.Context, intents map[string]time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readLocked()
	if err != nil {
		return err
	}
	data.Intents = cloneTimeMap(intents)
	trimOldest(data.Intents, s.capacity)
	return s.writeLocked(data)
}

func (s *ComponentEventStore) readLocked() (componentEventStoreData, error) {
	data := componentEventStoreData{Processed: make(map[string]time.Time), Intents: make(map[string]time.Time)}
	contents, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return data, fmt.Errorf("read component event store: %w", err)
	}
	if err := json.Unmarshal(contents, &data); err != nil {
		return data, fmt.Errorf("decode component event store: %w", err)
	}
	if data.Processed == nil {
		data.Processed = make(map[string]time.Time)
	}
	if data.Intents == nil {
		data.Intents = make(map[string]time.Time)
	}
	return data, nil
}

func (s *ComponentEventStore) writeLocked(data componentEventStoreData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create component event store directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0700); err != nil { //nolint:gosec // owner-only directory permissions
		return fmt.Errorf("secure component event store directory: %w", err)
	}
	contents, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode component event store: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".component-events-*")
	if err != nil {
		return fmt.Errorf("create component event store temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("set component event store permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write component event store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync component event store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close component event store: %w", err)
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		return fmt.Errorf("replace component event store: %w", err)
	}
	if err := s.syncDir(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("sync component event store directory: %w", err)
	}
	return nil
}

func trimOldest(values map[string]time.Time, capacity int) {
	if len(values) <= capacity {
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return values[keys[i]].Before(values[keys[j]]) })
	for _, key := range keys[:len(values)-capacity] {
		delete(values, key)
	}
}

func cloneTimeMap(values map[string]time.Time) map[string]time.Time {
	clone := make(map[string]time.Time, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
