package appstate

import (
	"context"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/bnema/gordon/internal/domain"
)

// AcceptPromotion atomically publishes desired state and its protective journal.
// Replays are resolved before checking freshness or the app incarnation. A
// committed apply intent is materialized first, inside the same transaction:
// CommitApply stays the single commit point of an apply, so the promotion's
// CAS then loses against it instead of dropping it. Revisions beyond
// retention are swept in the same transaction.
func (s *Store) AcceptPromotion(ctx context.Context, rev domain.AppDesiredRevision, op domain.AppOperation) (existing *domain.AppOperation, claimed bool, err error) {
	if err := checkCtx(ctx); err != nil {
		return nil, false, err
	}
	if !validPromotion(rev, op) {
		return nil, false, fmt.Errorf("appstate: invalid promotion: %w", domain.ErrInvalidAppSpec)
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
		if err := verifyPromotionBaseLocked(tx, bucket, rev); err != nil {
			return err
		}
		if err := writePromotionLocked(bucket, rev, &op); err != nil {
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

// verifyPromotionBaseLocked materializes committed applies, then checks the
// desired-revision CAS and the app incarnation.
func verifyPromotionBaseLocked(tx *bolt.Tx, bucket *bolt.Bucket, rev domain.AppDesiredRevision) error {
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
	if id == "" || id != rev.Promotion.AppID {
		return fmt.Errorf("appstate: promotion incarnation changed: %w", domain.ErrAppStateConflict)
	}
	return nil
}

func validPromotion(rev domain.AppDesiredRevision, op domain.AppOperation) bool {
	return rev.Promotion != nil && rev.Promotion.AppID != "" && op.Op != "" && rev.App == op.App && rev.Revision != ""
}

func writePromotionLocked(bucket *bolt.Bucket, rev domain.AppDesiredRevision, op *domain.AppOperation) error {
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
	op.Steps = []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepPending, Image: rev.Promotion.Image, Digest: rev.Promotion.Digest}}
	ops, err := subBucket(bucket, subOps, true)
	if err != nil {
		return err
	}
	return putRecord(ops, []byte(op.Op), op)
}
