package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultComponentEventStoreCapacity = 1024
	maxComponentEventKeyBytes          = 512
	// Each entry has a bounded key and timestamp. This allows enough JSON
	// overhead while preventing an attacker-controlled ledger from consuming
	// unbounded memory during restart.
	componentEventEntryBytes = maxComponentEventKeyBytes + 128
)

// ComponentEventStore is a small durable, bounded control-plane idempotency
// store. Atomic rename means a crash leaves either the previous valid file or
// the complete next file, never a partially written acknowledgement.
type ComponentEventStore struct {
	path     string
	capacity int
	maxBytes int64
	syncDir  func(string) error
	mu       sync.Mutex
}

type componentEventStoreData struct {
	Processed map[string]time.Time `json:"processed"`
	Intents   map[string]time.Time `json:"intents"`
}

func NewComponentEventStore(path string, capacity int) (*ComponentEventStore, error) {
	if capacity <= 0 {
		capacity = defaultComponentEventStoreCapacity
	}
	if strings.TrimSpace(path) == "" || filepath.Clean(path) != path || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return nil, fmt.Errorf("component event store path is required and must be clean")
	}
	store := &ComponentEventStore{
		path:     path,
		capacity: capacity,
		maxBytes: int64(capacity*2*componentEventEntryBytes + 256),
		syncDir:  syncParentDirectory,
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, err := store.readLocked(); err != nil {
		// Do not reset corrupt acknowledgement or suppression state: callers
		// must fail closed rather than accidentally replaying completed work.
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
	if !validComponentEventKey(key) || processedAt.IsZero() {
		return errors.New("invalid component event acknowledgement")
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
	if !validComponentEventMap(intents, s.capacity) {
		return errors.New("invalid component deployment intents")
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
	// Lstat, rather than Stat, prevents a store path from redirecting reads to
	// an arbitrary file. Rejecting non-regular files also avoids FIFO blocking.
	info, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return data, fmt.Errorf("lstat component event store: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || hasMultipleLinks(info) || info.Size() > s.maxBytes {
		return data, errors.New("invalid component event store file")
	}
	contents, err := s.readFileLocked(info)
	if err != nil {
		return data, err
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
	if !validComponentEventMap(data.Processed, s.capacity) || !validComponentEventMap(data.Intents, s.capacity) {
		return data, errors.New("invalid component event store data")
	}
	return data, nil
}

func (s *ComponentEventStore) readFileLocked(expected fs.FileInfo) ([]byte, error) {
	// O_NOFOLLOW closes the Lstat/Open race for symlinks; SameFile also rejects
	// replacement of the checked inode between those operations.
	file, err := os.OpenFile(s.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open component event store: %w", err)
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat component event store: %w", statErr)
	}
	if !os.SameFile(expected, openedInfo) || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0077 != 0 || hasMultipleLinks(openedInfo) || openedInfo.Size() > s.maxBytes {
		_ = file.Close()
		return nil, errors.New("component event store changed while opening")
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, s.maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read component event store: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close component event store: %w", closeErr)
	}
	if int64(len(contents)) > s.maxBytes {
		return nil, errors.New("component event store exceeds maximum size")
	}
	return contents, nil
}

func hasMultipleLinks(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}

func validComponentEventMap(values map[string]time.Time, capacity int) bool {
	if len(values) > capacity {
		return false
	}
	for key, value := range values {
		if !validComponentEventKey(key) || value.IsZero() {
			return false
		}
	}
	return true
}

func validComponentEventKey(key string) bool {
	return len(key) > 0 && len(key) <= maxComponentEventKeyBytes && !strings.ContainsRune(key, '\x00')
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
	if int64(len(contents)) > s.maxBytes {
		return errors.New("component event store exceeds maximum size")
	}
	if err := atomicWriteRestricted(s.path, contents, s.syncDir); err != nil {
		return fmt.Errorf("write component event store: %w", err)
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
