// Package appstate implements the out.AppState boundary on the local
// filesystem: versioned JSON files, atomic temp+fsync+rename+dirsync
// writes, and flock(2) coordination on a store lock file. Stdlib only.
// It never reads secret values and never touches OCI manifest storage.
package appstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// Store layout and file names frozen by docs/plans/v2.50.0/02-state.md.
const (
	storeFile      = "store.json"
	storeLockFile  = "store.lock"
	appsDir        = "apps"
	desiredFile    = "desired.json"
	activeFile     = "active.json"
	intentFile     = "intent.json"
	intentsDir     = "intents"
	revisionsDir   = "revisions"
	journalDir     = "journal"
	ownershipFile  = "ownership.json"
	tmpPattern     = ".tmp-*"
	intentPrefix   = "apply-"
	revisionPrefix = "rev-"
	opPrefix       = "op-"
	dirPerm        = 0o750
	filePerm       = 0o640
)

// Store is a filesystem-backed out.AppState implementation.
type Store struct {
	root string
	log  zerowrap.Logger
}

// NewStore creates the store rooted at dataDir (…/apps lives below it).
func NewStore(dataDir string, log zerowrap.Logger) (*Store, error) {
	root := filepath.Join(dataDir, appsDir)
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, fmt.Errorf("appstate: create store root: %w", err)
	}
	return &Store{root: root, log: log}, nil
}

// withLock runs fn while holding an exclusive flock on store.lock.
func (s *Store) withLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockPath := filepath.Join(s.root, storeLockFile)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return fmt.Errorf("appstate: open lock file: %w", domain.ErrAppStateIO)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("appstate: acquire lock: %w", domain.ErrAppStateIO)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// writeAtomic writes data atomically: temp + fsync + rename + dir fsync.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("appstate: create dir: %w", domain.ErrAppStateIO)
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("appstate: create temp: %w", domain.ErrAppStateIO)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("appstate: write temp: %w", domain.ErrAppStateIO)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("appstate: sync temp: %w", domain.ErrAppStateIO)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("appstate: close temp: %w", domain.ErrAppStateIO)
	}
	if err := os.Chmod(tmpName, filePerm); err != nil {
		return fmt.Errorf("appstate: chmod temp: %w", domain.ErrAppStateIO)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("appstate: rename: %w", domain.ErrAppStateIO)
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	return nil
}

// syncDir fsyncs a directory so renames survive a crash.
// A missing directory is a no-op: nothing was recorded there.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("appstate: open dir: %w", domain.ErrAppStateIO)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("appstate: sync dir: %w", domain.ErrAppStateIO)
	}
	return nil
}

// marshal encodes a record as canonical JSON.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("appstate: encode: %w", domain.ErrAppStateIO)
	}
	return buf.Bytes(), nil
}

// readJSON reads and decodes JSON; corrupt data maps to ErrAppStateCorrupt.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("appstate: read %s: %w", filepath.Base(path), domain.ErrAppStateIO)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("appstate: corrupt %s: %w", filepath.Base(path), domain.ErrAppStateCorrupt)
	}
	return nil
}

// appDir returns the directory for one normalized app name.
func (s *Store) appDir(app string) string {
	return filepath.Join(s.root, app)
}

// Recover implements out.AppState.
func (s *Store) Recover(ctx context.Context) error {
	return s.withLock(ctx, func() error {
		apps, err := s.listAppsLocked()
		if err != nil {
			return err
		}
		for _, app := range apps {
			if err := s.materializeCommittedLocked(app); err != nil {
				return err
			}
		}
		return nil
	})
}

// LoadCheckpoint implements out.AppState.
func (s *Store) LoadCheckpoint(_ context.Context) (domain.AppStoreCheckpoint, error) {
	var checkpoint domain.AppStoreCheckpoint
	path := filepath.Join(s.root, storeFile)
	err := readJSON(path, &checkpoint)
	if errors.Is(err, os.ErrNotExist) {
		return domain.AppStoreCheckpoint{Version: domain.AppStoreVersion}, nil
	}
	if err != nil {
		return domain.AppStoreCheckpoint{}, err
	}
	if checkpoint.Version > domain.AppStoreVersion {
		return domain.AppStoreCheckpoint{}, fmt.Errorf("appstate: unsupported store version %d: %w", checkpoint.Version, domain.ErrAppStateIncompatible)
	}
	return checkpoint, nil
}

// ListApps implements out.AppState.
func (s *Store) ListApps(_ context.Context) ([]string, error) {
	return s.listAppsLocked()
}

func (s *Store) listAppsLocked() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("appstate: list apps: %w", domain.ErrAppStateIO)
	}
	var apps []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		apps = append(apps, entry.Name())
	}
	sort.Strings(apps)
	return apps, nil
}

// LoadDesired implements out.AppState.
func (s *Store) LoadDesired(_ context.Context, app string) (domain.AppDesiredRevision, bool, error) {
	var desired domain.AppDesiredRevision
	err := readJSON(filepath.Join(s.appDir(app), desiredFile), &desired)
	if errors.Is(err, os.ErrNotExist) {
		return domain.AppDesiredRevision{}, false, nil
	}
	if err != nil {
		return domain.AppDesiredRevision{}, false, err
	}
	return desired, true, nil
}

// LoadRevision implements out.AppState.
func (s *Store) LoadRevision(_ context.Context, app, revision string) (domain.AppDesiredRevision, error) {
	var rev domain.AppDesiredRevision
	path := filepath.Join(s.appDir(app), revisionsDir, revision+".json")
	if err := readJSON(path, &rev); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.AppDesiredRevision{}, fmt.Errorf("appstate: revision %s not found: %w", revision, domain.ErrAppRevisionNotFound)
		}
		return domain.AppDesiredRevision{}, err
	}
	return rev, nil
}

// ListRevisions implements out.AppState.
func (s *Store) ListRevisions(_ context.Context, app string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.appDir(app), revisionsDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("appstate: list revisions: %w", domain.ErrAppStateIO)
	}
	var revs []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		revs = append(revs, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(revs)))
	return revs, nil
}

// LoadActive implements out.AppState.
func (s *Store) LoadActive(_ context.Context, app string) (domain.AppActive, bool, error) {
	var active domain.AppActive
	err := readJSON(filepath.Join(s.appDir(app), activeFile), &active)
	if errors.Is(err, os.ErrNotExist) {
		return domain.AppActive{}, false, nil
	}
	if err != nil {
		return domain.AppActive{}, false, err
	}
	return active, true, nil
}

// LoadIntent implements out.AppState. Absent intent means running.
func (s *Store) LoadIntent(_ context.Context, app string) (domain.AppStopIntent, error) {
	var intent domain.AppStopIntent
	err := readJSON(filepath.Join(s.appDir(app), intentFile), &intent)
	if errors.Is(err, os.ErrNotExist) {
		return domain.AppStopIntent{App: app}, nil
	}
	if err != nil {
		return domain.AppStopIntent{}, err
	}
	return intent, nil
}

// SaveIntent implements out.AppState.
func (s *Store) SaveIntent(ctx context.Context, intent domain.AppStopIntent) error {
	data, err := marshal(intent)
	if err != nil {
		return err
	}
	return s.withLock(ctx, func() error {
		return writeAtomic(filepath.Join(s.appDir(intent.App), intentFile), data)
	})
}

// LoadOwnership implements out.AppState.
func (s *Store) LoadOwnership(_ context.Context, app string) (domain.AppOwnership, error) {
	var ownership domain.AppOwnership
	err := readJSON(filepath.Join(s.appDir(app), ownershipFile), &ownership)
	if errors.Is(err, os.ErrNotExist) {
		return domain.AppOwnership{App: app}, nil
	}
	if err != nil {
		return domain.AppOwnership{}, err
	}
	return ownership, nil
}

// SaveOwnership implements out.AppState.
func (s *Store) SaveOwnership(ctx context.Context, ownership domain.AppOwnership) error {
	data, err := marshal(ownership)
	if err != nil {
		return err
	}
	return s.withLock(ctx, func() error {
		return writeAtomic(filepath.Join(s.appDir(ownership.App), ownershipFile), data)
	})
}

// StageApply implements out.AppState.
func (s *Store) StageApply(ctx context.Context, intent domain.AppApplyIntent) error {
	intent.State = domain.AppIntentStaged
	data, err := marshal(intent)
	if err != nil {
		return err
	}
	return s.withLock(ctx, func() error {
		return writeAtomic(s.intentPath(intent.App, intent.Intent), data)
	})
}

// CommitApply implements out.AppState: the single atomic commit point.
func (s *Store) CommitApply(ctx context.Context, app, intentID string) error {
	return s.withLock(ctx, func() error {
		intent, err := s.loadApplyIntentLocked(app, intentID)
		if err != nil {
			return err
		}
		if intent.State != domain.AppIntentStaged {
			return fmt.Errorf("appstate: intent %s is %s, not staged: %w", intentID, intent.State, domain.ErrAppStateConflict)
		}
		intent.State = domain.AppIntentCommitted
		data, err := marshal(intent)
		if err != nil {
			return err
		}
		return writeAtomic(s.intentPath(app, intentID), data)
	})
}

// MaterializeApply implements out.AppState. Idempotent: safe to replay.
func (s *Store) MaterializeApply(ctx context.Context, app, intentID string) error {
	return s.withLock(ctx, func() error {
		intent, err := s.loadApplyIntentLocked(app, intentID)
		if err != nil {
			return err
		}
		if intent.State == domain.AppIntentStaged {
			return fmt.Errorf("appstate: intent %s is staged, commit first: %w", intentID, domain.ErrAppStateConflict)
		}
		if intent.State == domain.AppIntentApplied {
			return nil
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
		revData, err := marshal(rev)
		if err != nil {
			return err
		}
		if err := writeAtomic(s.revisionPath(app, intent.Revision), revData); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(s.appDir(app), desiredFile), revData); err != nil {
			return err
		}
		if err := s.foldReservationsLocked(intent); err != nil {
			return err
		}
		intent.State = domain.AppIntentApplied
		appliedData, err := marshal(intent)
		if err != nil {
			return err
		}
		return writeAtomic(s.intentPath(app, intentID), appliedData)
	})
}

// foldReservationsLocked folds one committed intent's deltas into the checkpoint.
func (s *Store) foldReservationsLocked(intent domain.AppApplyIntent) error {
	checkpoint, err := s.LoadCheckpoint(context.Background())
	if err != nil {
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
	data, err := marshal(checkpoint)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.root, storeFile), data)
}

// materializeCommittedLocked finishes all committed intents for one app.
func (s *Store) materializeCommittedLocked(app string) error {
	ids, err := s.listIntentsLocked(app)
	if err != nil {
		return err
	}
	for _, id := range ids {
		intent, err := s.loadApplyIntentLocked(app, id)
		if err != nil {
			return err
		}
		if intent.State != domain.AppIntentCommitted {
			continue
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
		revData, err := marshal(rev)
		if err != nil {
			return err
		}
		if err := writeAtomic(s.revisionPath(app, intent.Revision), revData); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(s.appDir(app), desiredFile), revData); err != nil {
			return err
		}
		if err := s.foldReservationsLocked(intent); err != nil {
			return err
		}
		intent.State = domain.AppIntentApplied
		appliedData, err := marshal(intent)
		if err != nil {
			return err
		}
		if err := writeAtomic(s.intentPath(app, id), appliedData); err != nil {
			return err
		}
	}
	return nil
}

// LoadApplyIntent implements out.AppState.
func (s *Store) LoadApplyIntent(_ context.Context, app, intentID string) (domain.AppApplyIntent, error) {
	return s.loadApplyIntentLocked(app, intentID)
}

func (s *Store) loadApplyIntentLocked(app, intentID string) (domain.AppApplyIntent, error) {
	var intent domain.AppApplyIntent
	if err := readJSON(s.intentPath(app, intentID), &intent); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.AppApplyIntent{}, fmt.Errorf("appstate: intent %s not found: %w", intentID, domain.ErrAppIntentNotFound)
		}
		return domain.AppApplyIntent{}, err
	}
	return intent, nil
}

// ListIntents implements out.AppState.
func (s *Store) ListIntents(_ context.Context, app string) ([]string, error) {
	return s.listIntentsLocked(app)
}

func (s *Store) listIntentsLocked(app string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.appDir(app), intentsDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("appstate: list intents: %w", domain.ErrAppStateIO)
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

// CollectGarbage implements out.AppState.
func (s *Store) CollectGarbage(ctx context.Context, app string, inFlight []string) error {
	return s.withLock(ctx, func() error {
		protected, err := s.protectedRevisionsLocked(app, inFlight)
		if err != nil {
			return err
		}
		if err := s.sweepIntentsLocked(app); err != nil {
			return err
		}
		return s.sweepRevisionsLocked(app, protected)
	})
}

// protectedRevisionsLocked returns revisions that GC must never evict.
func (s *Store) protectedRevisionsLocked(app string, inFlight []string) (map[string]struct{}, error) {
	protected := map[string]struct{}{}
	for _, rev := range inFlight {
		protected[rev] = struct{}{}
	}
	ctx := context.Background()
	if desired, ok, err := s.LoadDesired(ctx, app); err == nil && ok {
		protected[desired.Revision] = struct{}{}
	} else if err != nil {
		return nil, err
	}
	if active, ok, err := s.LoadActive(ctx, app); err == nil && ok {
		for _, svc := range active.Services {
			protected[svc.EffectiveRevision] = struct{}{}
		}
	} else if err != nil {
		return nil, err
	}
	ids, err := s.listIntentsLocked(app)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		intent, err := s.loadApplyIntentLocked(app, id)
		if err != nil {
			return nil, err
		}
		protected[intent.Revision] = struct{}{}
	}
	return protected, nil
}

// sweepIntentsLocked removes applied intents and orphaned staged intents.
// Committed intents are never swept here: they carry unmaterialized
// deltas that recovery must complete. Staged intents are safe to remove
// under the exclusive store lock — any staged intent visible here is an
// orphan, since the process staging it would hold this same lock.
func (s *Store) sweepIntentsLocked(app string) error {
	ids, err := s.listIntentsLocked(app)
	if err != nil {
		return err
	}
	for _, id := range ids {
		intent, err := s.loadApplyIntentLocked(app, id)
		if err != nil {
			return err
		}
		if intent.State != domain.AppIntentApplied && intent.State != domain.AppIntentStaged {
			continue
		}
		if err := os.Remove(s.intentPath(app, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("appstate: remove intent: %w", domain.ErrAppStateIO)
		}
	}
	return nil
}

// sweepRevisionsLocked removes unreferenced revisions beyond retention.
func (s *Store) sweepRevisionsLocked(app string, protected map[string]struct{}) error {
	revs, err := s.ListRevisions(context.Background(), app)
	if err != nil {
		return err
	}
	kept := 0
	for _, rev := range revs {
		if _, ok := protected[rev]; ok {
			continue
		}
		if kept >= domain.AppRevisionRetention {
			if err := os.Remove(s.revisionPath(app, rev)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("appstate: remove revision: %w", domain.ErrAppStateIO)
			}
			continue
		}
		kept++
	}
	return syncDir(filepath.Join(s.appDir(app), revisionsDir))
}

// SaveOperation implements out.AppState.
func (s *Store) SaveOperation(ctx context.Context, op domain.AppOperation) error {
	data, err := marshal(op)
	if err != nil {
		return err
	}
	return s.withLock(ctx, func() error {
		return writeAtomic(filepath.Join(s.appDir(op.App), journalDir, op.Op+".json"), data)
	})
}

// LoadOperation implements out.AppState.
func (s *Store) LoadOperation(_ context.Context, app, opID string) (domain.AppOperation, error) {
	var op domain.AppOperation
	if err := readJSON(filepath.Join(s.appDir(app), journalDir, opID+".json"), &op); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.AppOperation{}, fmt.Errorf("appstate: operation %s not found: %w", opID, domain.ErrAppOperationNotFound)
		}
		return domain.AppOperation{}, err
	}
	return op, nil
}

// SaveActive implements out.AppState.
func (s *Store) SaveActive(ctx context.Context, active domain.AppActive) error {
	data, err := marshal(active)
	if err != nil {
		return err
	}
	return s.withLock(ctx, func() error {
		return writeAtomic(filepath.Join(s.appDir(active.App), activeFile), data)
	})
}

// intentPath returns the intent file path.
func (s *Store) intentPath(app, intentID string) string {
	return filepath.Join(s.appDir(app), intentsDir, intentID+".json")
}

// revisionPath returns the revision file path.
func (s *Store) revisionPath(app, revision string) string {
	return filepath.Join(s.appDir(app), revisionsDir, revision+".json")
}

// secretsRequired derives pass paths from a normalized spec.
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
