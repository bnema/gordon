package appstate

import (
	"context"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/bnema/gordon/internal/domain"
)

// AcceptSetImage atomically publishes desired state and its protective journal.
// Replays are resolved before checking freshness or the app incarnation. A
// committed apply intent is materialized first, inside the same transaction:
// CommitApply stays the single commit point of an apply, so the set-image
// CAS then loses against it instead of dropping it. Revisions beyond
// retention are swept in the same transaction.
func (s *Store) AcceptSetImage(ctx context.Context, rev domain.AppDesiredRevision, op domain.AppOperation) (existing *domain.AppOperation, claimed bool, err error) {
	if err := checkCtx(ctx); err != nil {
		return nil, false, err
	}
	if !validSetImage(rev, op) {
		return nil, false, fmt.Errorf("appstate: invalid set-image revision: %w", domain.ErrInvalidAppSpec)
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := appBucket(tx, rev.App, false)
		if err != nil {
			return err
		}
		if stored, ok, err := claimedOperation(bucket, op); err != nil {
			return err
		} else if ok {
			existing = &stored
			return nil
		}
		if bucket == nil {
			return domain.ErrAppNotFound
		}
		if err := verifySetImageBaseLocked(tx, bucket, rev); err != nil {
			return err
		}
		if err := writeSetImageLocked(bucket, rev, &op); err != nil {
			return err
		}
		protected, err := protectedRevisionsLocked(bucket, rev.App, nil)
		if err != nil {
			return err
		}
		if err := sweepRevisionsLocked(bucket, protected, s.Retention()); err != nil {
			return err
		}
		existing, claimed = &op, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return existing, claimed, nil
}

// verifySetImageBaseLocked materializes committed applies, then checks the
// desired-revision CAS and the app incarnation.
func verifySetImageBaseLocked(tx *bolt.Tx, bucket *bolt.Bucket, rev domain.AppDesiredRevision) error {
	if err := materializeCommittedLocked(tx, bucket); err != nil {
		return err
	}
	if err := verifySupersedesLocked(bucket, domain.AppApplyIntent{App: rev.App, Supersedes: rev.Supersedes}); err != nil {
		return err
	}
	id, err := incarnationIDLocked(bucket, rev.App)
	if err != nil {
		return err
	}
	if id == "" || id != rev.SetImage.AppID {
		return fmt.Errorf("appstate: set-image incarnation changed: %w", domain.ErrAppStateConflict)
	}
	return nil
}

func validSetImage(rev domain.AppDesiredRevision, op domain.AppOperation) bool {
	return rev.SetImage != nil && rev.SetImage.AppID != "" && op.Op != "" && rev.App == op.App && rev.Revision != ""
}

func writeSetImageLocked(bucket *bolt.Bucket, rev domain.AppDesiredRevision, op *domain.AppOperation) error {
	revisions, err := subBucket(bucket, subRevisions, true)
	if err != nil {
		return err
	}
	if revisions.Get([]byte(rev.Revision)) != nil {
		return domain.ErrAppStateConflict
	}
	if err := putRecord(revisions, []byte(rev.Revision), rev); err != nil {
		return err
	}
	if err := putRecord(bucket, keyDesired, rev); err != nil {
		return err
	}
	op.Steps = []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepPending, Image: rev.SetImage.Image, Digest: rev.SetImage.Digest}}
	ops, err := subBucket(bucket, subOps, true)
	if err != nil {
		return err
	}
	return putRecord(ops, []byte(op.Op), op)
}
