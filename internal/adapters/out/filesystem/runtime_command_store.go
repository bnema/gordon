package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const (
	defaultRuntimeCommandResultEntries = 1024
	defaultRuntimeCommandResultBytes   = int64(4 << 20)
	defaultRuntimeCommandCorruptFiles  = 8
	defaultRuntimeCommandCorruptBytes  = int64(1 << 20)
)

// RuntimeCommandResultStoreConfig bounds the durable runtime idempotency cache.
type RuntimeCommandResultStoreConfig struct {
	Path            string
	MaxEntries      int
	MaxBytes        int64
	MaxCorruptFiles int
	MaxCorruptBytes int64
}

type runtimeCommandResultRecord struct {
	DedupeKey string                      `json:"dedupe_key"`
	Result    domain.RuntimeCommandResult `json:"result"`
}

type runtimeCommandResultData struct {
	Results []runtimeCommandResultRecord `json:"results"`
}

// RuntimeCommandResultStore atomically stores only successful and denied
// command outcomes. It is deliberately a small single-file cache: result
// persistence is part of command acknowledgement, not an unbounded audit log.
type RuntimeCommandResultStore struct {
	path            string
	maxEntries      int
	maxBytes        int64
	maxCorruptFiles int
	maxCorruptBytes int64
	mu              sync.Mutex
	results         map[string]runtimeCommandResultRecord
	order           []string // oldest first
	healthErr       error
	syncDir         func(string) error
}

func NewRuntimeCommandResultStore(cfg RuntimeCommandResultStoreConfig) (*RuntimeCommandResultStore, error) {
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("runtime command result store path is required")
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = defaultRuntimeCommandResultEntries
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultRuntimeCommandResultBytes
	}
	if cfg.MaxCorruptFiles <= 0 {
		cfg.MaxCorruptFiles = defaultRuntimeCommandCorruptFiles
	}
	if cfg.MaxCorruptBytes <= 0 {
		cfg.MaxCorruptBytes = defaultRuntimeCommandCorruptBytes
	}
	s := &RuntimeCommandResultStore{path: filepath.Clean(cfg.Path), maxEntries: cfg.MaxEntries, maxBytes: cfg.MaxBytes, maxCorruptFiles: cfg.MaxCorruptFiles, maxCorruptBytes: cfg.MaxCorruptBytes, results: make(map[string]runtimeCommandResultRecord), syncDir: syncParentDirectory}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return nil, fmt.Errorf("create runtime command result directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0700); err != nil { //nolint:gosec // owner-only directory permissions
		return nil, fmt.Errorf("secure runtime command result directory: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		s.healthErr = err
	}
	return s, nil
}

func (s *RuntimeCommandResultStore) Healthy() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthErr
}

func (s *RuntimeCommandResultStore) LoadRuntimeCommandResult(ctx context.Context, dedupeKey string) (domain.RuntimeCommandResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.RuntimeCommandResult{}, false, err
	}
	if !validRuntimeDedupeKey(dedupeKey) {
		return domain.RuntimeCommandResult{}, false, errors.New("invalid runtime command dedupe key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.healthErr != nil {
		return domain.RuntimeCommandResult{}, false, s.healthErr
	}
	record, ok := s.results[dedupeKey]
	if ok {
		s.touchLocked(dedupeKey)
	}
	return cloneRuntimeCommandResult(record.Result), ok, nil
}

func (s *RuntimeCommandResultStore) SaveRuntimeCommandResult(ctx context.Context, dedupeKey string, result domain.RuntimeCommandResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validRuntimeDedupeKey(dedupeKey) {
		return errors.New("invalid runtime command dedupe key")
	}
	if !validStoredRuntimeResult(result) {
		return errors.New("invalid terminal runtime command result")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.healthErr != nil {
		return s.healthErr
	}
	if _, exists := s.results[dedupeKey]; !exists {
		s.order = append(s.order, dedupeKey)
	}
	s.results[dedupeKey] = runtimeCommandResultRecord{DedupeKey: dedupeKey, Result: sanitizeStoredRuntimeResult(result)}
	s.trimLocked()
	if err := s.writeLocked(); err != nil {
		return err
	}
	return nil
}

func (s *RuntimeCommandResultStore) loadLocked() error {
	// Lstat is intentional: following an attacker-controlled result-store link
	// could expose or overwrite a file outside the runtime data directory.
	info, err := os.Lstat(s.path)
	if err == nil && (!info.Mode().IsRegular() || info.Size() > s.maxBytes) {
		return s.quarantineLocked(errors.New("invalid runtime command result store file"))
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat runtime command result store: %w", err)
	}
	contents, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read runtime command result store: %w", err)
	}
	var data runtimeCommandResultData
	if err := json.Unmarshal(contents, &data); err != nil {
		return s.quarantineLocked(fmt.Errorf("decode runtime command result store: %w", err))
	}
	for _, record := range data.Results {
		if !validRuntimeDedupeKey(record.DedupeKey) || !validStoredRuntimeResult(record.Result) {
			return s.quarantineLocked(errors.New("invalid runtime command result store record"))
		}
		if _, duplicate := s.results[record.DedupeKey]; duplicate {
			return s.quarantineLocked(errors.New("duplicate runtime command result store key"))
		}
		s.results[record.DedupeKey] = runtimeCommandResultRecord{DedupeKey: record.DedupeKey, Result: sanitizeStoredRuntimeResult(record.Result)}
		s.order = append(s.order, record.DedupeKey)
	}
	s.trimLocked()
	return nil
}

func (s *RuntimeCommandResultStore) writeLocked() error {
	data := runtimeCommandResultData{Results: make([]runtimeCommandResultRecord, 0, len(s.order))}
	for _, key := range s.order {
		data.Results = append(data.Results, s.results[key])
	}
	contents, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode runtime command result store: %w", err)
	}
	if int64(len(contents)) > s.maxBytes {
		return errors.New("runtime command result store exceeds maximum size")
	}
	if err := atomicWriteRestricted(s.path, contents, s.syncDir); err != nil {
		return fmt.Errorf("write runtime command result store: %w", err)
	}
	return nil
}

func (s *RuntimeCommandResultStore) touchLocked(key string) {
	for index, candidate := range s.order {
		if candidate == key {
			copy(s.order[index:], s.order[index+1:])
			s.order[len(s.order)-1] = key
			return
		}
	}
}

func (s *RuntimeCommandResultStore) trimLocked() {
	for len(s.order) > s.maxEntries {
		delete(s.results, s.order[0])
		s.order = s.order[1:]
	}
	// A result record is small and bounded, but trim incrementally when a large
	// (yet valid) error message from an older store would exceed the byte cap.
	for len(s.order) > 0 && s.serializedSizeLocked() > s.maxBytes {
		delete(s.results, s.order[0])
		s.order = s.order[1:]
	}
}

func (s *RuntimeCommandResultStore) serializedSizeLocked() int64 {
	data := runtimeCommandResultData{Results: make([]runtimeCommandResultRecord, 0, len(s.order))}
	for _, key := range s.order {
		data.Results = append(data.Results, s.results[key])
	}
	contents, _ := json.Marshal(data)
	return int64(len(contents))
}

func (s *RuntimeCommandResultStore) quarantineLocked(cause error) error {
	quarantine := s.path + "." + time.Now().UTC().Format("20060102150405.000000000") + ".corrupt"
	if err := os.Rename(s.path, quarantine); err != nil {
		return fmt.Errorf("quarantine runtime command result store: %w", err)
	}
	if err := s.syncDir(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("sync runtime command result quarantine: %w", err)
	}
	s.pruneCorruptLocked()
	return fmt.Errorf("corrupt runtime command result store: %w", cause)
}

func (s *RuntimeCommandResultStore) pruneCorruptLocked() {
	entries, err := os.ReadDir(filepath.Dir(s.path))
	if err != nil {
		return
	}
	type candidate struct {
		path string
		info fs.FileInfo
	}
	var candidates []candidate
	prefix := filepath.Base(s.path) + "."
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".corrupt") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr == nil && info.Mode().IsRegular() {
			candidates = append(candidates, candidate{filepath.Join(filepath.Dir(s.path), entry.Name()), info})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].info.ModTime().Equal(candidates[j].info.ModTime()) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].info.ModTime().Before(candidates[j].info.ModTime())
	})
	var bytes int64
	for _, candidate := range candidates {
		bytes += candidate.info.Size()
	}
	for len(candidates) > s.maxCorruptFiles || bytes > s.maxCorruptBytes {
		oldest := candidates[0]
		if os.Remove(oldest.path) == nil {
			bytes -= oldest.info.Size()
		}
		candidates = candidates[1:]
	}
}

func validRuntimeDedupeKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func terminalRuntimeResult(result domain.RuntimeCommandResult) bool {
	return result.Status == domain.RuntimeCommandStatusSucceeded || result.Status == domain.RuntimeCommandStatusDenied
}

func validStoredRuntimeResult(result domain.RuntimeCommandResult) bool {
	if !terminalRuntimeResult(result) || strings.TrimSpace(string(result.CommandID)) == "" || strings.TrimSpace(result.IdempotencyKey) == "" || result.Generation == 0 || result.StartedAt.IsZero() || result.CompletedAt.IsZero() {
		return false
	}
	if result.Status == domain.RuntimeCommandStatusSucceeded && result.Error != nil {
		return false
	}
	return result.Error == nil || (len(result.Error.Code) <= 256 && len(result.Error.Message) <= 1024)
}
func sanitizeStoredRuntimeResult(result domain.RuntimeCommandResult) domain.RuntimeCommandResult {
	result.StartedAt = result.StartedAt.UTC()
	result.CompletedAt = result.CompletedAt.UTC()
	if result.Error != nil {
		result.Error = &domain.RuntimeCommandError{Code: strings.TrimSpace(result.Error.Code), Message: strings.TrimSpace(result.Error.Message), Retryable: result.Error.Retryable}
	}
	return result
}

func cloneRuntimeCommandResult(result domain.RuntimeCommandResult) domain.RuntimeCommandResult {
	return sanitizeStoredRuntimeResult(result)
}

func atomicWriteRestricted(path string, contents []byte, syncDirectory func(string) error) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".runtime-command-results-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncParentDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
