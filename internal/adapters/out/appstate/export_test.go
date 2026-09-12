package appstate

// Test-only hooks live here instead of the production API: they rewrite
// durable records to reproduce corrupt or version-skewed states that
// production code can never create.

import (
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"

	"github.com/bnema/gordon/internal/domain"
)

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
