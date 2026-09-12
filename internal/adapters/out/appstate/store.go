// Package appstate implements the out.AppState boundary on bbolt:
// one state.db file with per-app buckets, single-writer transactions,
// and crash-safe commit semantics from bbolt itself. It replaces the
// prepared multi-file JSON adapter (no compatibility layer).
// It never reads secret values and never touches OCI manifest storage.
package appstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"

	"github.com/bnema/gordon/internal/domain"
)

// Bucket layout inside state.db.
var (
	bucketMeta             = []byte("meta")
	bucketCheckpoint       = []byte("checkpoint")
	bucketApps             = []byte("apps")
	bucketOwnershipHistory = []byte("ownership_history")
	keyStoreRecord         = []byte("store")
	keyAppRecord           = []byte("app")
	keyDesired             = []byte("desired")
	keyActive              = []byte("active")
	keyIntent              = []byte("intent")
	keyOwnership           = []byte("ownership")
	keyInhibitions         = []byte("recovery_inhibitions")
	keyOwnershipMigration  = []byte("ownership_history_v1")
	subRevisions           = []byte("revisions")
	subIntents             = []byte("intents")
	subOps                 = []byte("ops")
)

// Store layout constants.
const (
	appsDir   = "apps"
	stateFile = "state.db"
	dirPerm   = 0o750
)

// appRecord tracks the stable internal UUID for one public app name.
type appRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Store is a bbolt-backed out.AppState implementation.
type Store struct {
	db  *bolt.DB
	log zerowrap.Logger
	// retention bounds unreferenced-revision GC per app. Zero means
	// the compiled default (AppDefaultRevisionRetention).
	retention int
}

// WithRevisionRetention overrides the unreferenced-revision GC bound.
// Values <= 0 restore the compiled default.
func (s *Store) WithRevisionRetention(n int) *Store {
	if n > 0 {
		s.retention = n
	} else {
		s.retention = 0
	}
	return s
}

// Retention returns the effective unreferenced-revision GC bound.
func (s *Store) Retention() int {
	if s.retention > 0 {
		return s.retention
	}
	return domain.AppDefaultRevisionRetention
}

// NewStore creates the store rooted at dataDir (…/apps/state.db).
func NewStore(dataDir string, log zerowrap.Logger) (*Store, error) {
	root := filepath.Join(dataDir, appsDir)
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, fmt.Errorf("appstate: create store root: %w", domain.ErrAppStateIO)
	}
	db, err := bolt.Open(filepath.Join(root, stateFile), 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("appstate: open state db: %w", domain.ErrAppStateIO)
	}
	store := &Store{db: db, log: log}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// CorruptCheckpointForTest rewrites the checkpoint record. Tests use it
// to simulate corrupt or version-skewed state.db payloads.
func CorruptCheckpointForTest(s *Store, rewrite func([]byte) []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketCheckpoint)
		if bucket == nil {
			return nil
		}
		raw := append([]byte(nil), bucket.Get(keyStoreRecord)...)
		return bucket.Put(keyStoreRecord, rewrite(raw))
	})
}

// CorruptDesiredForTest rewrites one app's desired record. Tests use it
// to simulate a corrupt desired payload with other apps unaffected.
func CorruptDesiredForTest(s *Store, app string, rewrite func([]byte) []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		if bucket == nil {
			return nil
		}
		raw := append([]byte(nil), bucket.Get(keyDesired)...)
		return bucket.Put(keyDesired, rewrite(raw))
	})
}

// ResetOwnershipHistoryForTest drops the incarnation-indexed ownership
// history and its migration marker. Tests use it to reproduce a
// pre-migration database that already holds current-shaped records.
func ResetOwnershipHistoryForTest(s *Store) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketOwnershipHistory); err != nil && !errors.Is(err, bolterrors.ErrBucketNotFound) {
			return fmt.Errorf("appstate: reset ownership history: %w", domain.ErrAppStateIO)
		}
		meta := tx.Bucket(bucketMeta)
		if meta == nil {
			return nil
		}
		if err := meta.Delete(keyOwnershipMigration); err != nil {
			return fmt.Errorf("appstate: reset ownership migration: %w", domain.ErrAppStateIO)
		}
		return nil
	})
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("appstate: close state db: %w", domain.ErrAppStateIO)
	}
	return nil
}

// init ensures top-level buckets and the default checkpoint exist.
func (s *Store) init() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketMeta); err != nil {
			return fmt.Errorf("appstate: create meta bucket: %w", domain.ErrAppStateIO)
		}
		checkpointBucket, err := tx.CreateBucketIfNotExists(bucketCheckpoint)
		if err != nil {
			return fmt.Errorf("appstate: create checkpoint bucket: %w", domain.ErrAppStateIO)
		}
		if checkpointBucket.Get(keyStoreRecord) == nil {
			raw, err := marshal(domain.AppStoreCheckpoint{Version: domain.AppStoreVersion})
			if err != nil {
				return err
			}
			if err := checkpointBucket.Put(keyStoreRecord, raw); err != nil {
				return fmt.Errorf("appstate: init checkpoint: %w", domain.ErrAppStateIO)
			}
		}
		if _, err := tx.CreateBucketIfNotExists(bucketApps); err != nil {
			return fmt.Errorf("appstate: create apps bucket: %w", domain.ErrAppStateIO)
		}
		return migrateOwnershipHistoryLocked(tx)
	})
}

// migrateOwnershipHistoryLocked populates the incarnation-indexed
// ownership history once. A reused app name never overwrites the
// history of an earlier incarnation, so retained resources stay
// protected after a remove/recreate. Existing records are copied
// verbatim: claimsForOwnership maps every unrecognized lifecycle state
// conservatively to retained.
func migrateOwnershipHistoryLocked(tx *bolt.Tx) error {
	meta := tx.Bucket(bucketMeta)
	if meta == nil {
		return fmt.Errorf("appstate: missing meta bucket: %w", domain.ErrAppStateIO)
	}
	history, err := tx.CreateBucketIfNotExists(bucketOwnershipHistory)
	if err != nil {
		return fmt.Errorf("appstate: create ownership history bucket: %w", domain.ErrAppStateIO)
	}
	if meta.Get(keyOwnershipMigration) != nil {
		return nil
	}

	apps := tx.Bucket(bucketApps)
	if apps != nil {
		if err := apps.ForEach(func(name, _ []byte) error {
			bucket := apps.Bucket(name)
			if bucket == nil {
				return nil
			}
			var ownership domain.AppOwnership
			found, err := getRecord(bucket, keyOwnership, &ownership, "ownership")
			if err != nil {
				// A corrupt record cannot be migrated. It stays unwritten
				// and surfaces as an inventory gap on every snapshot.
				return nil
			}
			if !found {
				return nil
			}
			app := string(name)
			if ownership.App == "" {
				ownership.App = app
			}
			record, err := ensureAppRecordLocked(bucket, app, true)
			if err != nil {
				return err
			}
			if ownership.ID == "" {
				ownership.ID = record.ID
			}
			return putOwnershipHistoryLocked(history, ownership)
		}); err != nil {
			return err
		}
	}

	if err := meta.Put(keyOwnershipMigration, []byte("1")); err != nil {
		return fmt.Errorf("appstate: record ownership migration: %w", domain.ErrAppStateIO)
	}
	return nil
}

// putOwnershipHistoryLocked writes one incarnation's ownership record
// into the history bucket. Records without an incarnation ID have no
// stable key and are skipped.
func putOwnershipHistoryLocked(history *bolt.Bucket, ownership domain.AppOwnership) error {
	if history == nil || ownership.ID == "" {
		return nil
	}
	raw, err := marshal(ownership)
	if err != nil {
		return err
	}
	if err := history.Put([]byte(ownership.ID), raw); err != nil {
		return fmt.Errorf("appstate: record ownership history: %w", domain.ErrAppStateIO)
	}
	return nil
}

// marshal encodes a record as canonical JSON.
func marshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("appstate: encode: %w", domain.ErrAppStateIO)
	}
	return raw, nil
}

// unmarshal decodes JSON; broken payloads map to ErrAppStateCorrupt.
func unmarshal(raw []byte, v any, what string) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("appstate: corrupt %s: %w", what, domain.ErrAppStateCorrupt)
	}
	return nil
}

// appBucket returns the per-app bucket, creating it on writes.
func appBucket(tx *bolt.Tx, app string, create bool) (*bolt.Bucket, error) {
	apps := tx.Bucket(bucketApps)
	if apps == nil {
		return nil, fmt.Errorf("appstate: missing apps bucket: %w", domain.ErrAppStateIO)
	}
	if create {
		bucket, err := apps.CreateBucketIfNotExists([]byte(app))
		if err != nil {
			return nil, fmt.Errorf("appstate: create app bucket: %w", domain.ErrAppStateIO)
		}
		return bucket, nil
	}
	return apps.Bucket([]byte(app)), nil
}

// subBucket returns a named child bucket, creating it on writes.
func subBucket(parent *bolt.Bucket, name []byte, create bool) (*bolt.Bucket, error) {
	if parent == nil {
		return nil, nil
	}
	if create {
		bucket, err := parent.CreateBucketIfNotExists(name)
		if err != nil {
			return nil, fmt.Errorf("appstate: create bucket %s: %w", name, domain.ErrAppStateIO)
		}
		return bucket, nil
	}
	return parent.Bucket(name), nil
}

// ensureAppRecordLocked returns the stable UUID for an app, assigning one
// on first write. Reads leave the database untouched.
func ensureAppRecordLocked(bucket *bolt.Bucket, app string, create bool) (appRecord, error) {
	var record appRecord
	raw := bucket.Get(keyAppRecord)
	if raw == nil {
		if !create {
			return appRecord{Name: app}, nil
		}
		record = appRecord{ID: newAppID(), Name: app}
		encoded, err := marshal(record)
		if err != nil {
			return appRecord{}, err
		}
		if err := bucket.Put(keyAppRecord, encoded); err != nil {
			return appRecord{}, fmt.Errorf("appstate: record app id: %w", domain.ErrAppStateIO)
		}
		return record, nil
	}
	if err := unmarshal(raw, &record, "app"); err != nil {
		return appRecord{}, err
	}
	if record.Name == "" {
		record.Name = app
	}
	if record.ID == "" && create {
		record.ID = newAppID()
		encoded, err := marshal(record)
		if err != nil {
			return appRecord{}, err
		}
		if err := bucket.Put(keyAppRecord, encoded); err != nil {
			return appRecord{}, fmt.Errorf("appstate: record app id: %w", domain.ErrAppStateIO)
		}
	}
	return record, nil
}

// newAppID allocates a stable internal app UUID.
func newAppID() string {
	return "app-" + uuid.NewString()
}

// checkCtx aborts before opening a transaction when canceled.
func checkCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// getRecord reads one JSON record from a bucket.
func getRecord(bucket *bolt.Bucket, key []byte, v any, what string) (bool, error) {
	if bucket == nil {
		return false, nil
	}
	raw := bucket.Get(key)
	if raw == nil {
		return false, nil
	}
	if err := unmarshal(raw, v, what); err != nil {
		return false, err
	}
	return true, nil
}

// putRecord writes one JSON record into a bucket.
func putRecord(bucket *bolt.Bucket, key []byte, v any) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	if err := bucket.Put(key, raw); err != nil {
		return fmt.Errorf("appstate: write record: %w", domain.ErrAppStateIO)
	}
	return nil
}

// listKeys returns sorted string keys of a child bucket.
func listKeys(parent *bolt.Bucket, name []byte, reverse bool) ([]string, error) {
	child, err := subBucket(parent, name, false)
	if err != nil {
		return nil, err
	}
	if child == nil {
		return nil, nil
	}
	var keys []string
	if err := child.ForEach(func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("appstate: list records: %w", domain.ErrAppStateIO)
	}
	if reverse {
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	} else {
		sort.Strings(keys)
	}
	return keys, nil
}

// Recover implements out.AppState.
func (s *Store) Recover(ctx context.Context) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		appsBucket := tx.Bucket(bucketApps)
		if appsBucket == nil {
			return nil
		}
		return appsBucket.ForEach(func(app, _ []byte) error {
			// Skip nested-bucket cursor noise: app entries are buckets.
			bucket := appsBucket.Bucket(app)
			if bucket == nil {
				return nil
			}
			return materializeCommittedLocked(tx, bucket)
		})
	})
}

// LoadCheckpoint implements out.AppState.
func (s *Store) LoadCheckpoint(ctx context.Context) (domain.AppStoreCheckpoint, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppStoreCheckpoint{}, err
	}
	var checkpoint domain.AppStoreCheckpoint
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketCheckpoint)
		if bucket == nil {
			checkpoint = domain.AppStoreCheckpoint{Version: domain.AppStoreVersion}
			return nil
		}
		raw := bucket.Get(keyStoreRecord)
		if raw == nil {
			checkpoint = domain.AppStoreCheckpoint{Version: domain.AppStoreVersion}
			return nil
		}
		return unmarshal(raw, &checkpoint, "store")
	}); err != nil {
		return domain.AppStoreCheckpoint{}, err
	}
	if checkpoint.Version > domain.AppStoreVersion {
		return domain.AppStoreCheckpoint{}, fmt.Errorf("appstate: unsupported store version %d: %w", checkpoint.Version, domain.ErrAppStateIncompatible)
	}
	return checkpoint, nil
}

// ListApps implements out.AppState.
func (s *Store) ListApps(ctx context.Context) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	var apps []string
	if err := s.db.View(func(tx *bolt.Tx) error {
		appsBucket := tx.Bucket(bucketApps)
		if appsBucket == nil {
			return nil
		}
		return appsBucket.ForEach(func(app, _ []byte) error {
			if appsBucket.Bucket(app) != nil {
				apps = append(apps, string(app))
			}
			return nil
		})
	}); err != nil {
		return nil, err
	}
	sort.Strings(apps)
	return apps, nil
}

// LoadDesired implements out.AppState.
func (s *Store) LoadDesired(ctx context.Context, app string) (domain.AppDesiredRevision, bool, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppDesiredRevision{}, false, err
	}
	var desired domain.AppDesiredRevision
	var ok bool
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		found, err := getRecord(bucket, keyDesired, &desired, "desired")
		if err != nil {
			return err
		}
		ok = found
		return nil
	}); err != nil {
		return domain.AppDesiredRevision{}, false, err
	}
	return desired, ok, nil
}

// LoadRevision implements out.AppState.
func (s *Store) LoadRevision(ctx context.Context, app, revision string) (domain.AppDesiredRevision, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppDesiredRevision{}, err
	}
	var rev domain.AppDesiredRevision
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		revisions, err := subBucket(bucket, subRevisions, false)
		if err != nil {
			return err
		}
		if revisions == nil {
			return fmt.Errorf("appstate: revision %s not found: %w", revision, domain.ErrAppRevisionNotFound)
		}
		raw := revisions.Get([]byte(revision))
		if raw == nil {
			return fmt.Errorf("appstate: revision %s not found: %w", revision, domain.ErrAppRevisionNotFound)
		}
		return unmarshal(raw, &rev, "revision")
	}); err != nil {
		return domain.AppDesiredRevision{}, err
	}
	return rev, nil
}

// ListRevisions implements out.AppState.
func (s *Store) ListRevisions(ctx context.Context, app string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	var revs []string
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		keys, err := listKeys(bucket, subRevisions, true)
		if err != nil {
			return err
		}
		revs = keys
		return nil
	}); err != nil {
		return nil, err
	}
	return revs, nil
}

// LoadActive implements out.AppState.
func (s *Store) LoadActive(ctx context.Context, app string) (domain.AppActive, bool, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppActive{}, false, err
	}
	var active domain.AppActive
	var ok bool
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		found, err := getRecord(bucket, keyActive, &active, "active")
		if err != nil {
			return err
		}
		ok = found
		return nil
	}); err != nil {
		return domain.AppActive{}, false, err
	}
	return active, ok, nil
}

// LoadIntent implements out.AppState. Absent intent means running.
func (s *Store) LoadIntent(ctx context.Context, app string) (domain.AppStopIntent, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppStopIntent{}, err
	}
	var intent domain.AppStopIntent
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		found, err := getRecord(bucket, keyIntent, &intent, "intent")
		if err != nil {
			return err
		}
		if !found {
			intent = domain.AppStopIntent{App: app}
		}
		return nil
	}); err != nil {
		return domain.AppStopIntent{}, err
	}
	return intent, nil
}

// SaveIntent implements out.AppState.
func (s *Store) SaveIntent(ctx context.Context, intent domain.AppStopIntent) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, intent.App, true)
		if err != nil {
			return err
		}
		if _, err := ensureAppRecordLocked(bucket, intent.App, true); err != nil {
			return err
		}
		return putRecord(bucket, keyIntent, intent)
	})
}

// LoadOwnership implements out.AppState.
func (s *Store) LoadOwnership(ctx context.Context, app string) (domain.AppOwnership, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppOwnership{}, err
	}
	var ownership domain.AppOwnership
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		if bucket == nil {
			ownership = domain.AppOwnership{App: app}
			return nil
		}
		found, err := getRecord(bucket, keyOwnership, &ownership, "ownership")
		if err != nil {
			return err
		}
		if !found {
			record, err := ensureAppRecordLocked(bucket, app, false)
			if err != nil {
				return err
			}
			ownership = domain.AppOwnership{App: app, ID: record.ID}
		}
		return nil
	}); err != nil {
		return domain.AppOwnership{}, err
	}
	return ownership, nil
}

// LoadRecoveryInhibitions implements out.AppState.
func (s *Store) LoadRecoveryInhibitions(ctx context.Context, app string) ([]domain.AppRecoveryInhibition, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	var inhibitions []domain.AppRecoveryInhibition
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		_, err = getRecord(bucket, keyInhibitions, &inhibitions, "recovery inhibitions")
		return err
	}); err != nil {
		return nil, err
	}
	return inhibitions, nil
}

// SaveRecoveryInhibition implements out.AppState.
func (s *Store) SaveRecoveryInhibition(ctx context.Context, inhibition domain.AppRecoveryInhibition) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, inhibition.App, true)
		if err != nil {
			return err
		}
		record, err := ensureAppRecordLocked(bucket, inhibition.App, true)
		if err != nil {
			return err
		}
		if inhibition.AppID == "" {
			inhibition.AppID = record.ID
		}
		var inhibitions []domain.AppRecoveryInhibition
		if found, err := getRecord(bucket, keyInhibitions, &inhibitions, "recovery inhibitions"); err != nil {
			return err
		} else if !found {
			inhibitions = nil
		}
		replaced := false
		for i, existing := range inhibitions {
			if existing.Service == inhibition.Service && existing.ContainerID == inhibition.ContainerID {
				inhibitions[i] = inhibition
				replaced = true
				break
			}
		}
		if !replaced {
			inhibitions = append(inhibitions, inhibition)
		}
		return putRecord(bucket, keyInhibitions, inhibitions)
	})
}

// ClearRecoveryInhibition implements out.AppState.
func (s *Store) ClearRecoveryInhibition(ctx context.Context, app, service, containerID string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil || bucket == nil {
			return err
		}
		var inhibitions []domain.AppRecoveryInhibition
		found, err := getRecord(bucket, keyInhibitions, &inhibitions, "recovery inhibitions")
		if err != nil || !found {
			return err
		}
		kept := make([]domain.AppRecoveryInhibition, 0, len(inhibitions))
		for _, existing := range inhibitions {
			if existing.Service == service && existing.ContainerID == containerID {
				continue
			}
			kept = append(kept, existing)
		}
		if len(kept) == len(inhibitions) {
			return nil
		}
		return putRecord(bucket, keyInhibitions, kept)
	})
}

// SaveOwnership implements out.AppState.
func (s *Store) SaveOwnership(ctx context.Context, ownership domain.AppOwnership) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, ownership.App, true)
		if err != nil {
			return err
		}
		record, err := ensureAppRecordLocked(bucket, ownership.App, true)
		if err != nil {
			return err
		}
		if ownership.ID == "" {
			ownership.ID = record.ID
		}
		// A new UUID means a new incarnation: never carry the old
		// incarnation's resources implicitly. The old incarnation's
		// history record stays in the ownership-history bucket.
		if ownership.ID != record.ID && len(ownership.Volumes) == 0 && len(ownership.Secrets) == 0 && len(ownership.Networks) == 0 {
			record = appRecord{ID: ownership.ID, Name: ownership.App}
			encoded, err := marshal(record)
			if err != nil {
				return err
			}
			if err := bucket.Put(keyAppRecord, encoded); err != nil {
				return fmt.Errorf("appstate: record app id: %w", domain.ErrAppStateIO)
			}
		}
		if err := putRecord(bucket, keyOwnership, ownership); err != nil {
			return err
		}
		return putOwnershipHistoryLocked(tx.Bucket(bucketOwnershipHistory), ownership)
	})
}

// StageApply implements out.AppState.
func (s *Store) StageApply(ctx context.Context, intent domain.AppApplyIntent) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	intent.State = domain.AppIntentStaged
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, intent.App, true)
		if err != nil {
			return err
		}
		if _, err := ensureAppRecordLocked(bucket, intent.App, true); err != nil {
			return err
		}
		intents, err := subBucket(bucket, subIntents, true)
		if err != nil {
			return err
		}
		return putRecord(intents, []byte(intent.Intent), intent)
	})
}

// CommitApply implements out.AppState: the single atomic commit point.
func (s *Store) CommitApply(ctx context.Context, app, intentID string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, true)
		if err != nil {
			return err
		}
		intent, err := loadApplyIntentLocked(bucket, app, intentID)
		if err != nil {
			return err
		}
		if intent.State != domain.AppIntentStaged {
			return fmt.Errorf("appstate: intent %s is %s, not staged: %w", intentID, intent.State, domain.ErrAppStateConflict)
		}
		if err := verifySupersedesLocked(bucket, intent); err != nil {
			return err
		}
		intent.State = domain.AppIntentCommitted
		intents, err := subBucket(bucket, subIntents, true)
		if err != nil {
			return err
		}
		return putRecord(intents, []byte(intentID), intent)
	})
}

// MaterializeApply implements out.AppState. Idempotent: safe to replay.
func (s *Store) MaterializeApply(ctx context.Context, app, intentID string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, true)
		if err != nil {
			return err
		}
		intent, err := loadApplyIntentLocked(bucket, app, intentID)
		if err != nil {
			return err
		}
		if intent.State == domain.AppIntentStaged {
			return fmt.Errorf("appstate: intent %s is staged, commit first: %w", intentID, domain.ErrAppStateConflict)
		}
		if intent.State == domain.AppIntentApplied {
			return nil
		}
		if err := materializeIntentLocked(tx, bucket, intent); err != nil {
			return err
		}
		intent.State = domain.AppIntentApplied
		intents, err := subBucket(bucket, subIntents, true)
		if err != nil {
			return err
		}
		return putRecord(intents, []byte(intentID), intent)
	})
}

// materializeIntentLocked writes revision + desired + checkpoint deltas.
func materializeIntentLocked(tx *bolt.Tx, bucket *bolt.Bucket, intent domain.AppApplyIntent) error {
	if err := verifySupersedesLocked(bucket, intent); err != nil {
		return err
	}
	rev := domain.AppDesiredRevision{
		Revision:        intent.Revision,
		App:             intent.App,
		Supersedes:      intent.Supersedes,
		AcceptedAt:      intent.CreatedAt,
		SourceSHA256:    intent.SourceSHA256,
		Spec:            intent.Spec,
		Reservations:    intent.Reservations,
		SecretsRequired: secretsRequired(intent.Spec),
		SecretsEnv:      secretsEnv(intent.Spec),
		Status:          domain.AppStepPending,
	}
	revisions, err := subBucket(bucket, subRevisions, true)
	if err != nil {
		return err
	}
	if err := putRecord(revisions, []byte(intent.Revision), rev); err != nil {
		return err
	}
	if err := putRecord(bucket, keyDesired, rev); err != nil {
		return err
	}
	return foldReservationsLocked(tx, intent)
}

// foldReservationsLocked folds one committed intent's deltas into the checkpoint.
func foldReservationsLocked(tx *bolt.Tx, intent domain.AppApplyIntent) error {
	bucket := tx.Bucket(bucketCheckpoint)
	if bucket == nil {
		return fmt.Errorf("appstate: missing checkpoint bucket: %w", domain.ErrAppStateIO)
	}
	var checkpoint domain.AppStoreCheckpoint
	raw := bucket.Get(keyStoreRecord)
	if raw == nil {
		checkpoint = domain.AppStoreCheckpoint{Version: domain.AppStoreVersion}
	} else if err := unmarshal(raw, &checkpoint, "store"); err != nil {
		return err
	}
	kept := checkpoint.Reservations[:0]
	for _, res := range checkpoint.Reservations {
		if res.App != intent.App {
			kept = append(kept, res)
		}
	}
	kept = append(kept, intent.Reservations...)
	checkpoint.Version = domain.AppStoreVersion
	checkpoint.Reservations = kept
	encoded, err := marshal(checkpoint)
	if err != nil {
		return err
	}
	if err := bucket.Put(keyStoreRecord, encoded); err != nil {
		return fmt.Errorf("appstate: write checkpoint: %w", domain.ErrAppStateIO)
	}
	return nil
}

// materializeCommittedLocked finishes all committed intents for one app.
// The caller holds the write tx, so revision, desired, checkpoint, and
// intent updates commit atomically.
func materializeCommittedLocked(tx *bolt.Tx, bucket *bolt.Bucket) error {
	intents, err := subBucket(bucket, subIntents, false)
	if err != nil {
		return err
	}
	if intents == nil {
		return nil
	}
	type pending struct {
		id     string
		intent domain.AppApplyIntent
	}
	var committed []pending
	if err := intents.ForEach(func(k, v []byte) error {
		var intent domain.AppApplyIntent
		if err := unmarshal(v, &intent, "intent"); err != nil {
			return err
		}
		if intent.State == domain.AppIntentCommitted {
			committed = append(committed, pending{id: string(k), intent: intent})
		}
		return nil
	}); err != nil {
		return err
	}
	for _, item := range committed {
		if err := materializeIntentLocked(tx, bucket, item.intent); err != nil {
			if errors.Is(err, domain.ErrAppStateConflict) {
				// A newer apply superseded this committed intent while the
				// daemon was away; it must never overwrite newer desired
				// state and can be dropped.
				if delErr := intents.Delete([]byte(item.id)); delErr != nil {
					return fmt.Errorf("appstate: drop superseded intent: %w", domain.ErrAppStateIO)
				}
				continue
			}
			return err
		}
		item.intent.State = domain.AppIntentApplied
		if err := putRecord(intents, []byte(item.id), item.intent); err != nil {
			return err
		}
	}
	return nil
}

// verifySupersedesLocked rejects an intent whose expected former revision
// no longer matches the current desired revision: a concurrent apply that
// committed first wins, and the stale intent can never overwrite it.
func verifySupersedesLocked(bucket *bolt.Bucket, intent domain.AppApplyIntent) error {
	var desired domain.AppDesiredRevision
	found, err := getRecord(bucket, keyDesired, &desired, "desired")
	if err != nil {
		return err
	}
	current := ""
	if found {
		current = desired.Revision
	}
	if current != intent.Supersedes {
		return fmt.Errorf("appstate: intent %s expected desired %q but found %q: %w",
			intent.Intent, intent.Supersedes, current, domain.ErrAppStateConflict)
	}
	return nil
}

// LoadApplyIntent implements out.AppState.
func (s *Store) LoadApplyIntent(ctx context.Context, app, intentID string) (domain.AppApplyIntent, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppApplyIntent{}, err
	}
	var intent domain.AppApplyIntent
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		loaded, err := loadApplyIntentLocked(bucket, app, intentID)
		if err != nil {
			return err
		}
		intent = loaded
		return nil
	}); err != nil {
		return domain.AppApplyIntent{}, err
	}
	return intent, nil
}

func loadApplyIntentLocked(bucket *bolt.Bucket, app, intentID string) (domain.AppApplyIntent, error) {
	var intent domain.AppApplyIntent
	if bucket == nil {
		return domain.AppApplyIntent{}, fmt.Errorf("appstate: intent %s not found: %w", intentID, domain.ErrAppIntentNotFound)
	}
	intents, err := subBucket(bucket, subIntents, false)
	if err != nil {
		return domain.AppApplyIntent{}, err
	}
	if intents == nil {
		return domain.AppApplyIntent{}, fmt.Errorf("appstate: intent %s not found: %w", intentID, domain.ErrAppIntentNotFound)
	}
	raw := intents.Get([]byte(intentID))
	if raw == nil {
		return domain.AppApplyIntent{}, fmt.Errorf("appstate: intent %s not found: %w", intentID, domain.ErrAppIntentNotFound)
	}
	if err := unmarshal(raw, &intent, "intent"); err != nil {
		return domain.AppApplyIntent{}, err
	}
	_ = app
	return intent, nil
}

// ListIntents implements out.AppState.
func (s *Store) ListIntents(ctx context.Context, app string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	var ids []string
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		keys, err := listKeys(bucket, subIntents, false)
		if err != nil {
			return err
		}
		ids = keys
		return nil
	}); err != nil {
		return nil, err
	}
	return ids, nil
}

// CollectGarbage implements out.AppState.
func (s *Store) CollectGarbage(ctx context.Context, app string, inFlight []string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, true)
		if err != nil {
			return err
		}
		protected, err := protectedRevisionsLocked(bucket, app, inFlight)
		if err != nil {
			return err
		}
		if err := sweepIntentsLocked(bucket, inFlight); err != nil {
			return err
		}
		return sweepRevisionsLocked(bucket, protected, s.Retention())
	})
}

// protectedRevisionsLocked returns revisions that GC must never evict.
func protectedRevisionsLocked(bucket *bolt.Bucket, app string, inFlight []string) (map[string]struct{}, error) {
	protected := map[string]struct{}{}
	for _, rev := range inFlight {
		protected[rev] = struct{}{}
	}
	var desired domain.AppDesiredRevision
	if found, err := getRecord(bucket, keyDesired, &desired, "desired"); err != nil {
		return nil, err
	} else if found {
		protected[desired.Revision] = struct{}{}
	}
	var active domain.AppActive
	if found, err := getRecord(bucket, keyActive, &active, "active"); err != nil {
		return nil, err
	} else if found {
		for _, svc := range active.Services {
			protected[svc.EffectiveRevision] = struct{}{}
		}
	}
	intents, err := subBucket(bucket, subIntents, false)
	if err != nil {
		return nil, err
	}
	if intents != nil {
		if err := intents.ForEach(func(_, v []byte) error {
			var intent domain.AppApplyIntent
			if err := unmarshal(v, &intent, "intent"); err != nil {
				return err
			}
			// Only unfinished work pins its revision: staged orphans
			// and committed-but-unmaterialized intents. Applied intents
			// are history; their revisions age out by retention.
			if intent.State == domain.AppIntentStaged || intent.State == domain.AppIntentCommitted {
				protected[intent.Revision] = struct{}{}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	_ = app
	return protected, nil
}

// sweepIntentsLocked removes applied intents and orphaned staged intents.
// Committed intents are never swept here: they carry unmaterialized
// deltas that recovery must complete.
func sweepIntentsLocked(bucket *bolt.Bucket, inFlight []string) error {
	intents, err := subBucket(bucket, subIntents, false)
	if err != nil {
		return err
	}
	if intents == nil {
		return nil
	}
	// A live apply's intent must never be swept as an orphan: the caller
	// names its own in-flight intent so concurrent work is preserved.
	protected := make(map[string]struct{}, len(inFlight))
	for _, id := range inFlight {
		protected[id] = struct{}{}
	}
	var drop [][]byte
	if err := intents.ForEach(func(k, v []byte) error {
		if _, ok := protected[string(k)]; ok {
			return nil
		}
		var intent domain.AppApplyIntent
		if err := unmarshal(v, &intent, "intent"); err != nil {
			return err
		}
		if intent.State == domain.AppIntentApplied || intent.State == domain.AppIntentStaged {
			key := append([]byte(nil), k...)
			drop = append(drop, key)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, key := range drop {
		if err := intents.Delete(key); err != nil {
			return fmt.Errorf("appstate: remove intent: %w", domain.ErrAppStateIO)
		}
	}
	return nil
}

// sweepRevisionsLocked removes unreferenced revisions beyond retention.
func sweepRevisionsLocked(bucket *bolt.Bucket, protected map[string]struct{}, retention int) error {
	revisions, err := subBucket(bucket, subRevisions, false)
	if err != nil {
		return err
	}
	if revisions == nil {
		return nil
	}
	var keys []string
	if err := revisions.ForEach(func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}); err != nil {
		return fmt.Errorf("appstate: list revisions: %w", domain.ErrAppStateIO)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	kept := 0
	for _, rev := range keys {
		if _, ok := protected[rev]; ok {
			continue
		}
		if kept >= retention {
			if err := revisions.Delete([]byte(rev)); err != nil {
				return fmt.Errorf("appstate: remove revision: %w", domain.ErrAppStateIO)
			}
			continue
		}
		kept++
	}
	return nil
}

// SaveOperation implements out.AppState.
func (s *Store) SaveOperation(ctx context.Context, op domain.AppOperation) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, op.App, true)
		if err != nil {
			return err
		}
		if _, err := ensureAppRecordLocked(bucket, op.App, true); err != nil {
			return err
		}
		ops, err := subBucket(bucket, subOps, true)
		if err != nil {
			return err
		}
		return putRecord(ops, []byte(op.Op), op)
	})
}

// AppExists implements out.AppState: a read-only live-identity check
// that never creates an app bucket.
func (s *Store) AppExists(ctx context.Context, app string) (bool, error) {
	if err := checkCtx(ctx); err != nil {
		return false, err
	}
	exists := false
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		if bucket == nil {
			return nil
		}
		known, err := appHasLiveIdentityLocked(bucket)
		if err != nil {
			return err
		}
		exists = known
		return nil
	})
	if err != nil {
		return false, err
	}
	return exists, nil
}

// ClaimOperation implements out.AppState: one atomic check-and-write for
// a mutation request key.
func (s *Store) ClaimOperation(ctx context.Context, candidate domain.AppOperation) (domain.AppOperation, bool, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppOperation{}, false, err
	}
	if candidate.Op == "" {
		return domain.AppOperation{}, false, fmt.Errorf("appstate: claim operation without a key: %w", domain.ErrInvalidAppSpec)
	}
	var existing domain.AppOperation
	claimed := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, candidate.App, false)
		if err != nil {
			return err
		}
		if stored, ok, err := claimedOperation(bucket, candidate); err != nil {
			return err
		} else if ok {
			existing = stored
			return nil
		}
		if bucket == nil {
			return fmt.Errorf("appstate: app %q has no state: %w", candidate.App, domain.ErrAppNotFound)
		}
		known, err := appHasLiveIdentityLocked(bucket)
		if err != nil {
			return err
		}
		if !known {
			return fmt.Errorf("appstate: app %q has no live identity: %w", candidate.App, domain.ErrAppNotFound)
		}
		writeBucket, err := appBucket(tx, candidate.App, true)
		if err != nil {
			return err
		}
		if _, err := ensureAppRecordLocked(writeBucket, candidate.App, true); err != nil {
			return err
		}
		ops, err := subBucket(writeBucket, subOps, true)
		if err != nil {
			return err
		}
		if err := putRecord(ops, []byte(candidate.Op), candidate); err != nil {
			return err
		}
		existing = candidate
		claimed = true
		return nil
	})
	if err != nil {
		return domain.AppOperation{}, false, err
	}
	return existing, claimed, nil
}

// claimedOperation returns the journal already stored under the candidate
// key when its request identity matches. A key answering a different
// request identity is a conflict, never a replay.
func claimedOperation(bucket *bolt.Bucket, candidate domain.AppOperation) (domain.AppOperation, bool, error) {
	if bucket == nil {
		return domain.AppOperation{}, false, nil
	}
	ops, err := subBucket(bucket, subOps, false)
	if err != nil || ops == nil {
		return domain.AppOperation{}, false, err
	}
	raw := ops.Get([]byte(candidate.Op))
	if raw == nil {
		return domain.AppOperation{}, false, nil
	}
	var stored domain.AppOperation
	if err := unmarshal(raw, &stored, "operation"); err != nil {
		return domain.AppOperation{}, false, err
	}
	if stored.Request != candidate.Request {
		return domain.AppOperation{}, false, fmt.Errorf(
			"appstate: operation key %s already answered a different request: %w",
			candidate.Op, domain.ErrAppStateConflict,
		)
	}
	return stored, true, nil
}

// appHasLiveIdentityLocked reports whether an existing app bucket holds a
// live identity: desired state, active state, or an ownership
// incarnation. Operation journals alone are not identity — a retired app
// keeps them so its own key still replays, while a new key must never
// recreate state for the freed name.
func appHasLiveIdentityLocked(bucket *bolt.Bucket) (bool, error) {
	if bucket.Get(keyDesired) != nil || bucket.Get(keyActive) != nil {
		return true, nil
	}
	var ownership domain.AppOwnership
	found, err := getRecord(bucket, keyOwnership, &ownership, "ownership")
	if err != nil {
		return false, err
	}
	return found && ownership.ID != "", nil
}

// LoadOperation implements out.AppState.
func (s *Store) LoadOperation(ctx context.Context, app, opID string) (domain.AppOperation, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppOperation{}, err
	}
	var op domain.AppOperation
	if err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		ops, err := subBucket(bucket, subOps, false)
		if err != nil {
			return err
		}
		if ops == nil {
			return fmt.Errorf("appstate: operation %s not found: %w", opID, domain.ErrAppOperationNotFound)
		}
		raw := ops.Get([]byte(opID))
		if raw == nil {
			return fmt.Errorf("appstate: operation %s not found: %w", opID, domain.ErrAppOperationNotFound)
		}
		return unmarshal(raw, &op, "operation")
	}); err != nil {
		return domain.AppOperation{}, err
	}
	return op, nil
}

// LoadLatestOperation implements out.AppState.
func (s *Store) LoadLatestOperation(ctx context.Context, app string) (domain.AppOperation, bool, error) {
	if err := checkCtx(ctx); err != nil {
		return domain.AppOperation{}, false, err
	}
	var latest domain.AppOperation
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, false)
		if err != nil {
			return err
		}
		ops, err := subBucket(bucket, subOps, false)
		if err != nil || ops == nil {
			return err
		}
		return ops.ForEach(func(_, raw []byte) error {
			var op domain.AppOperation
			if err := unmarshal(raw, &op, "operation"); err != nil {
				return err
			}
			if !found || op.StartedAt.After(latest.StartedAt) {
				latest, found = op, true
			}
			return nil
		})
	})
	if err != nil {
		return domain.AppOperation{}, false, err
	}
	return latest, found, nil
}

// RetireApp implements out.AppState. In one transaction it archives the
// live incarnation's ownership record (with every resource marked
// retained), drops the live ownership record, resets the app's UUID so a
// later reapply allocates a NEW incarnation, and clears desired, active,
// intent, staged intents, and revisions. The archived record stays the
// positive proof that retained volumes and secrets exist, so prune keeps
// protecting them while a name reuse can never adopt them.
func (s *Store) RetireApp(ctx context.Context, app string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, app, true)
		if err != nil {
			return err
		}
		if err := archiveRetainedOwnershipLocked(tx, bucket); err != nil {
			return err
		}
		return clearIncarnationStateLocked(bucket, app)
	})
}

// archiveRetainedOwnershipLocked archives the live ownership record with
// every resource marked retained, so prune keeps protecting them. A
// missing record is not an error.
func archiveRetainedOwnershipLocked(tx *bolt.Tx, bucket *bolt.Bucket) error {
	var ownership domain.AppOwnership
	found, err := getRecord(bucket, keyOwnership, &ownership, "ownership")
	if err != nil {
		return err
	}
	if !found || ownership.ID == "" {
		return nil
	}
	for i := range ownership.Volumes {
		ownership.Volumes[i].State = domain.AppResourceRetained
	}
	for i := range ownership.Secrets {
		ownership.Secrets[i].State = domain.AppResourceRetained
	}
	for i := range ownership.Images {
		ownership.Images[i].State = domain.AppResourceReleased
	}
	return putOwnershipHistoryLocked(tx.Bucket(bucketOwnershipHistory), ownership)
}

// clearIncarnationStateLocked drops the live ownership record and all
// operational state, then resets the app record so the next write assigns
// a fresh incarnation UUID.
func clearIncarnationStateLocked(bucket *bolt.Bucket, app string) error {
	for _, key := range [][]byte{keyOwnership, keyDesired, keyActive, keyIntent, keyInhibitions} {
		if err := bucket.Delete(key); err != nil {
			return fmt.Errorf("appstate: retire %s: %w", app, domain.ErrAppStateIO)
		}
	}
	for _, sub := range [][]byte{subRevisions, subIntents} {
		if err := bucket.DeleteBucket(sub); err != nil && !errors.Is(err, bolterrors.ErrBucketNotFound) {
			return fmt.Errorf("appstate: retire %s: %w", app, domain.ErrAppStateIO)
		}
	}
	encoded, err := marshal(appRecord{Name: app})
	if err != nil {
		return err
	}
	if err := bucket.Put(keyAppRecord, encoded); err != nil {
		return fmt.Errorf("appstate: reset app record: %w", domain.ErrAppStateIO)
	}
	return nil
}

// SaveActive implements out.AppState.
func (s *Store) SaveActive(ctx context.Context, active domain.AppActive) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, active.App, true)
		if err != nil {
			return err
		}
		if _, err := ensureAppRecordLocked(bucket, active.App, true); err != nil {
			return err
		}
		return putRecord(bucket, keyActive, active)
	})
}

// RegisterBackendBinds implements out.AppState. Backend claims join the
// global checkpoint in the same write tx that checks them, so two
// concurrent deploys cannot both claim one bind.
func (s *Store) RegisterBackendBinds(ctx context.Context, binds []domain.AppListenerReservation) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	if err := validateBackendClaims(binds); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		checkpoint, bucket, err := loadCheckpointLocked(tx)
		if err != nil {
			return err
		}
		if err := checkBackendConflicts(checkpoint.Reservations, binds); err != nil {
			return err
		}
		checkpoint.Reservations = replaceContainerClaims(checkpoint.Reservations, binds)
		checkpoint.Version = domain.AppStoreVersion
		return putCheckpointLocked(bucket, checkpoint)
	})
}

// validateBackendClaims rejects non-backend or malformed claims before
// any storage mutation.
func validateBackendClaims(binds []domain.AppListenerReservation) error {
	for _, bind := range binds {
		if bind.Owner != domain.OwnerGordonBackend {
			return fmt.Errorf("appstate: backend claim owner must be %q: %w", domain.OwnerGordonBackend, domain.ErrAppReservationConflict)
		}
		if bind.Proto != "tcp" && bind.Proto != "udp" {
			return fmt.Errorf("appstate: backend claim proto must be tcp or udp: %w", domain.ErrAppReservationConflict)
		}
		if bind.IP != "127.0.0.1" || bind.Port <= 0 || bind.ContainerID == "" {
			return fmt.Errorf("appstate: malformed backend claim: %w", domain.ErrAppReservationConflict)
		}
	}
	return nil
}

// loadCheckpointLocked reads the global checkpoint in a write tx.
func loadCheckpointLocked(tx *bolt.Tx) (domain.AppStoreCheckpoint, *bolt.Bucket, error) {
	bucket := tx.Bucket(bucketCheckpoint)
	if bucket == nil {
		return domain.AppStoreCheckpoint{}, nil, fmt.Errorf("appstate: missing checkpoint bucket: %w", domain.ErrAppStateIO)
	}
	var checkpoint domain.AppStoreCheckpoint
	raw := bucket.Get(keyStoreRecord)
	if raw == nil {
		return domain.AppStoreCheckpoint{Version: domain.AppStoreVersion}, bucket, nil
	}
	if err := unmarshal(raw, &checkpoint, "store"); err != nil {
		return domain.AppStoreCheckpoint{}, nil, err
	}
	return checkpoint, bucket, nil
}

// putCheckpointLocked persists the global checkpoint in a write tx.
func putCheckpointLocked(bucket *bolt.Bucket, checkpoint domain.AppStoreCheckpoint) error {
	encoded, err := marshal(checkpoint)
	if err != nil {
		return err
	}
	if err := bucket.Put(keyStoreRecord, encoded); err != nil {
		return fmt.Errorf("appstate: write checkpoint: %w", domain.ErrAppStateIO)
	}
	return nil
}

// checkBackendConflicts fails closed on another app's identical loopback
// claim. Same-app claims never self-conflict.
func checkBackendConflicts(existing []domain.AppListenerReservation, binds []domain.AppListenerReservation) error {
	for _, bind := range binds {
		for _, other := range existing {
			if other.App == bind.App {
				continue
			}
			if backendOverlaps(other, bind) {
				return fmt.Errorf("appstate: backend bind %s conflicts with %s/%s: %w",
					backendDescribe(bind), other.App, other.Service, domain.ErrAppReservationConflict)
			}
		}
	}
	return nil
}

// replaceContainerClaims swaps one container's backend claims for the
// new set, preserving every other claim (old and replacement coexist
// until the old container's verified withdrawal).
func replaceContainerClaims(existing []domain.AppListenerReservation, binds []domain.AppListenerReservation) []domain.AppListenerReservation {
	if len(binds) == 0 {
		return existing
	}
	kept := existing[:0]
	for _, res := range existing {
		if res.Owner == domain.OwnerGordonBackend && res.App == binds[0].App && res.ContainerID == binds[0].ContainerID {
			continue
		}
		kept = append(kept, res)
	}
	return append(kept, binds...)
}

// ReleaseBackendBinds implements out.AppState.
func (s *Store) ReleaseBackendBinds(ctx context.Context, app, containerID string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketCheckpoint)
		if bucket == nil {
			return fmt.Errorf("appstate: missing checkpoint bucket: %w", domain.ErrAppStateIO)
		}
		var checkpoint domain.AppStoreCheckpoint
		raw := bucket.Get(keyStoreRecord)
		if raw == nil {
			return nil
		}
		if err := unmarshal(raw, &checkpoint, "store"); err != nil {
			return err
		}
		kept := checkpoint.Reservations[:0]
		for _, existing := range checkpoint.Reservations {
			if existing.Owner == domain.OwnerGordonBackend && existing.App == app && existing.ContainerID == containerID {
				continue
			}
			kept = append(kept, existing)
		}
		checkpoint.Reservations = kept
		encoded, err := marshal(checkpoint)
		if err != nil {
			return err
		}
		if err := bucket.Put(keyStoreRecord, encoded); err != nil {
			return fmt.Errorf("appstate: write checkpoint: %w", domain.ErrAppStateIO)
		}
		return nil
	})
}

// backendOverlaps reports exact loopback bind collisions per protocol.
// A TCP and a UDP claim on the same host port never collide: they are
// distinct sockets. Claims are exact 127.0.0.1:port allocations
// (kernel-arbitrated), so only the same IP+port collides — never user
// wildcard publishes on other IPs.
func backendOverlaps(a, b domain.AppListenerReservation) bool {
	if a.Proto != b.Proto || (a.Proto != "tcp" && a.Proto != "udp") {
		return false
	}
	return a.IP == "127.0.0.1" && b.IP == "127.0.0.1" && a.Port == b.Port
}

func backendDescribe(bind domain.AppListenerReservation) string {
	return fmt.Sprintf("%s://%s:%d", bind.Proto, bind.IP, bind.Port)
}
func secretsRequired(spec domain.AppSpec) []string {
	var paths []string
	for _, svc := range spec.Services {
		for _, name := range svc.Secrets {
			paths = append(paths, domain.AppSecretPath(spec.Name, svc.Name, name))
		}
	}
	sort.Strings(paths)
	return paths
}

// secretsEnv maps pass paths back to env keys.
func secretsEnv(spec domain.AppSpec) map[string]string {
	env := map[string]string{}
	for _, svc := range spec.Services {
		for envKey, name := range svc.Secrets {
			env[domain.AppSecretPath(spec.Name, svc.Name, name)] = envKey
		}
	}
	return env
}
