// Package eventoutbox provides a crash-safe file outbox for component events.
package eventoutbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	defaultMaxEntries = 10000
	defaultMaxBytes   = int64(64 << 20)
)

type Config struct {
	Dir          string
	MaxEntries   int
	MaxBytes     int64
	InitialRetry time.Duration
	MaxRetry     time.Duration
}

type record struct {
	ID, Type, Origin, Timestamp, IdempotencyKey, AuditClassification string
	Generation                                                       uint64
	RetryCount                                                       int
	Repository, Reference, Digest                                    string
	Manifest                                                         []byte
	Annotations                                                      map[string]string
}

// Outbox persists an event before delivery. A successful Publish means the event
// is durable, not necessarily delivered; this keeps registry manifest PUTs
// available while control is temporarily unavailable.
type Outbox struct {
	dir                    string
	publisher              out.ComponentEventPublisher
	maxEntries             int
	maxBytes               int64
	initialRetry, maxRetry time.Duration

	mu        sync.Mutex
	healthErr error
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

var _ out.ComponentEventPublisher = (*Outbox)(nil)

func New(cfg Config, publisher out.ComponentEventPublisher) (*Outbox, error) {
	if cfg.Dir == "" {
		return nil, errors.New("event outbox directory is required")
	}
	if publisher == nil {
		return nil, errors.New("component event publisher is required")
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = defaultMaxEntries
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if cfg.InitialRetry <= 0 {
		cfg.InitialRetry = 100 * time.Millisecond
	}
	if cfg.MaxRetry < cfg.InitialRetry {
		cfg.MaxRetry = 5 * time.Second
	}
	if err := os.MkdirAll(cfg.Dir, 0700); err != nil {
		return nil, fmt.Errorf("create event outbox: %w", err)
	}
	if err := os.Chmod(cfg.Dir, 0700); err != nil { //nolint:gosec // owner-only directory permissions
		return nil, fmt.Errorf("secure event outbox: %w", err)
	}
	o := &Outbox{dir: cfg.Dir, publisher: publisher, maxEntries: cfg.MaxEntries, maxBytes: cfg.MaxBytes, initialRetry: cfg.InitialRetry, maxRetry: cfg.MaxRetry}
	if err := o.checkCorrupt(); err != nil {
		o.healthErr = err
	}
	return o, nil
}

// Start replays entries left by a previous process and keeps retrying transient
// delivery failures. It is safe to call once for the lifetime of an outbox.
func (o *Outbox) Start(ctx context.Context) {
	o.mu.Lock()
	if o.cancel != nil {
		o.mu.Unlock()
		return
	}
	run, cancel := context.WithCancel(ctx)
	o.cancel = cancel
	o.wg.Add(1)
	o.mu.Unlock()
	go func() { defer o.wg.Done(); o.replay(run) }()
}
func (o *Outbox) Close() {
	o.mu.Lock()
	cancel := o.cancel
	o.cancel = nil
	o.mu.Unlock()
	if cancel != nil {
		cancel()
		o.wg.Wait()
	}
}
func (o *Outbox) Healthy() error { o.mu.Lock(); defer o.mu.Unlock(); return o.healthErr }

func (o *Outbox) PublishComponentEvent(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if _, ok := event.Payload.(domain.RegistryImagePushedPayload); !ok {
		return errors.New("event outbox only accepts typed registry image pushed events")
	}
	path, err := o.persist(event)
	if err != nil {
		o.setHealth(err)
		return err
	}
	// Do not make the durable write dependent on a live control process. The
	// replay loop (or next startup) owns retry. A best effort immediate attempt
	// reduces normal delivery latency.
	if err := o.deliver(ctx, path); err != nil {
		o.setHealth(err)
	} else {
		o.clearDeliveryHealth()
	}
	return nil
}

func (o *Outbox) persist(event domain.ComponentEventEnvelope) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entries, bytes, err := o.entriesLocked()
	if err != nil {
		return "", err
	}
	if len(entries) >= o.maxEntries || bytes >= o.maxBytes {
		return "", errors.New("event outbox is full")
	}
	p := event.Payload.(domain.RegistryImagePushedPayload)
	r := record{ID: event.ID, Type: string(event.Type), Origin: string(event.Origin), Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano), IdempotencyKey: event.IdempotencyKey, Generation: event.Generation, RetryCount: event.RetryCount, AuditClassification: string(event.AuditClassification), Repository: p.Repository, Reference: p.Reference, Digest: p.Digest, Manifest: append([]byte(nil), p.Manifest...), Annotations: cloneAnnotations(p.Annotations)}
	data, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("marshal event outbox record: %w", err)
	}
	if bytes+int64(len(data)) > o.maxBytes {
		return "", errors.New("event outbox is full")
	}
	// Its hash makes duplicate invocation idempotent. The timestamp prefix sorts
	// replay deterministically while retaining a collision-free stable suffix.
	h := sha256.Sum256([]byte(event.DedupeKey()))
	name := fmt.Sprintf("%020d-%s.json", event.Timestamp.UnixNano(), hex.EncodeToString(h[:]))
	path := filepath.Join(o.dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := atomicWrite(path, data); err != nil {
		return "", fmt.Errorf("persist component event: %w", err)
	}
	return path, nil
}
func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".event-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (o *Outbox) replay(ctx context.Context) {
	backoff := o.initialRetry
	for ctx.Err() == nil {
		paths, err := o.entries()
		if err != nil {
			o.setHealth(err)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = next(backoff, o.maxRetry)
			continue
		}
		if len(paths) == 0 {
			backoff = o.initialRetry
			if !sleep(ctx, o.initialRetry) {
				return
			}
			continue
		}
		progress := false
		for _, path := range paths {
			if ctx.Err() != nil {
				return
			}
			if err := o.deliver(ctx, path); err != nil {
				o.setHealth(err)
				break
			}
			progress = true
		}
		if progress {
			backoff = o.initialRetry
			o.clearDeliveryHealth()
			continue
		}
		if !sleep(ctx, backoff) {
			return
		}
		backoff = next(backoff, o.maxRetry)
	}
}
func (o *Outbox) deliver(ctx context.Context, path string) error {
	event, err := readRecord(path)
	if err != nil {
		return o.quarantine(path, err)
	}
	if err := o.publisher.PublishComponentEvent(ctx, event); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ack delivered component event: %w", err)
	}
	return nil
}
func readRecord(path string) (domain.ComponentEventEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.ComponentEventEnvelope{}, err
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return domain.ComponentEventEnvelope{}, fmt.Errorf("decode outbox record: %w", err)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, r.Timestamp)
	if err != nil {
		return domain.ComponentEventEnvelope{}, err
	}
	e := domain.ComponentEventEnvelope{ID: r.ID, Type: domain.ComponentEventType(r.Type), Origin: domain.ComponentRole(r.Origin), Timestamp: timestamp, Generation: r.Generation, IdempotencyKey: r.IdempotencyKey, RetryCount: r.RetryCount, AuditClassification: domain.ComponentEventAuditClassification(r.AuditClassification), Payload: domain.RegistryImagePushedPayload{Repository: r.Repository, Reference: r.Reference, Digest: r.Digest, Manifest: append([]byte(nil), r.Manifest...), Annotations: cloneAnnotations(r.Annotations)}}
	if err := e.Validate(); err != nil {
		return domain.ComponentEventEnvelope{}, err
	}
	return e, nil
}
func (o *Outbox) quarantine(path string, cause error) error {
	q := path + ".corrupt"
	if err := os.Rename(path, q); err != nil {
		return fmt.Errorf("quarantine corrupt outbox entry: %w", err)
	}
	err := fmt.Errorf("corrupt event outbox entry %s: %w", filepath.Base(path), cause)
	o.setHealth(err)
	return err
}
func (o *Outbox) checkCorrupt() error {
	paths, err := o.entries()
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := readRecord(path); err != nil {
			return o.quarantine(path, err)
		}
	}
	return nil
}
func (o *Outbox) entries() ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entries, _, err := o.entriesLocked()
	return entries, err
}
func (o *Outbox) entriesLocked() ([]string, int64, error) {
	ds, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, 0, err
	}
	paths := make([]string, 0, len(ds))
	var total int64
	for _, d := range ds {
		if d.IsDir() || filepath.Ext(d.Name()) != ".json" {
			continue
		}
		info, e := d.Info()
		if e != nil {
			return nil, 0, e
		}
		paths = append(paths, filepath.Join(o.dir, d.Name()))
		total += info.Size()
	}
	sort.Strings(paths)
	return paths, total, nil
}
func (o *Outbox) setHealth(err error) { o.mu.Lock(); o.healthErr = err; o.mu.Unlock() }
func (o *Outbox) clearDeliveryHealth() {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		o.setHealth(err)
		return
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".corrupt") {
			return
		}
	}
	o.setHealth(nil)
}
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func next(v, max time.Duration) time.Duration {
	if v >= max || v > max/2 {
		return max
	}
	return v * 2
}

func cloneAnnotations(annotations map[string]string) map[string]string {
	if len(annotations) == 0 {
		return nil
	}
	copy := make(map[string]string, len(annotations))
	for key, value := range annotations {
		copy[key] = value
	}
	return copy
}
