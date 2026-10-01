package appstate

import (
	"context"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/bnema/gordon/internal/domain"
)

// ProtectionSnapshot implements out.PruneProtectionStore: one bbolt
// View transaction reads every durable fact prune plans against, so no
// candidate is ever judged against facts from a different moment.
//
// Anything that cannot be read completely is reported as an inventory
// gap instead of being silently omitted: an apparently unprotected
// snapshot must never be an artifact of a corrupt record.
func (s *Store) ProtectionSnapshot(ctx context.Context) (*domain.PruneProtectionSnapshot, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}

	snapshot := &domain.PruneProtectionSnapshot{}
	if err := s.db.View(func(tx *bolt.Tx) error {
		return readProtectionLocked(tx, snapshot)
	}); err != nil {
		return nil, err
	}

	snapshot.Roots = dedupeRoots(snapshot.Roots)
	snapshot.VolumeClaims = dedupeClaims(snapshot.VolumeClaims)
	return snapshot, nil
}

// readProtectionLocked fills snapshot from one consistent read view.
func readProtectionLocked(tx *bolt.Tx, snapshot *domain.PruneProtectionSnapshot) error {
	if err := readOwnershipHistoryLocked(tx, snapshot); err != nil {
		return err
	}
	// A missing history bucket is a real gap: ownership history is the
	// only positive proof that a volume was ever Gordon's.
	if tx.Bucket(bucketOwnershipHistory) == nil {
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceOwnership,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "ownership history bucket is missing",
		})
	}

	apps := tx.Bucket(bucketApps)
	if apps == nil {
		return nil
	}
	return apps.ForEach(func(name, _ []byte) error {
		bucket := apps.Bucket(name)
		if bucket == nil {
			return nil
		}
		// A bucket we cannot decode is reported, never skipped: prune
		// must not treat an unreadable app as a removed app.
		readAppProtectionLocked(bucket, string(name), snapshot)
		return nil
	})
}

// readAppProtectionLocked collects desired, active, apply, operation,
// inhibition, and ownership facts of one app.
func readAppProtectionLocked(bucket *bolt.Bucket, app string, snapshot *domain.PruneProtectionSnapshot) {
	record, err := ensureAppRecordLocked(bucket, app, false)
	if err != nil {
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceAppState,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: fmt.Sprintf("app %s: app record unreadable", app),
		})
		// The same failure hides this app's ownership claims, so volume
		// planning must fail closed too.
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceOwnership,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: fmt.Sprintf("app %s: ownership unreadable with its app record", app),
		})
		return
	}
	appID := record.ID

	readDesiredProtectionLocked(bucket, app, snapshot)
	readActiveProtectionLocked(bucket, app, snapshot)
	readApplyIntentProtectionLocked(bucket, app, snapshot)
	readOperationProtectionLocked(bucket, app, snapshot)
	readInhibitionProtectionLocked(bucket, app, snapshot)
	readAppOwnershipProtectionLocked(bucket, app, appID, snapshot)
}

func readDesiredProtectionLocked(bucket *bolt.Bucket, app string, snapshot *domain.PruneProtectionSnapshot) {
	var desired domain.AppDesiredRevision
	found, err := getRecord(bucket, keyDesired, &desired, "desired")
	if err != nil {
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceAppState,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: fmt.Sprintf("app %s: desired record unreadable", app),
		})
		return
	}
	if !found {
		return
	}
	owner := app + "@desired"
	for _, service := range desired.Spec.Services {
		snapshot.Roots = append(snapshot.Roots,
			domain.ImageRefRoots(domain.ProtectionDesiredRevision, service.Image, owner+"."+service.Name)...)
	}
}

func readActiveProtectionLocked(bucket *bolt.Bucket, app string, snapshot *domain.PruneProtectionSnapshot) {
	var active domain.AppActive
	found, err := getRecord(bucket, keyActive, &active, "active")
	if err != nil {
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceAppState,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: fmt.Sprintf("app %s: active record unreadable", app),
		})
		return
	}
	if !found {
		return
	}
	// ACTIVE covers running, stopped, and partially converged services
	// alike: every entry names an image that must survive.
	for serviceName, service := range active.Services {
		owner := app + "@active." + serviceName
		snapshot.Roots = append(snapshot.Roots, domain.ImageRefRoots(domain.ProtectionActiveService, service.Image, owner)...)
		if service.Digest != "" {
			snapshot.Roots = append(snapshot.Roots, domain.ProtectionRoot{
				Kind: domain.ProtectionActiveService, Ref: service.Digest,
				Repository: domain.ImageRefRepository(service.Image), Owner: owner,
			})
		}
		if service.Container != "" {
			snapshot.Roots = append(snapshot.Roots, domain.ProtectionRoot{
				Kind: domain.ProtectionContainerUse, Ref: service.Container, Owner: owner,
			})
		}
	}
}

func readApplyIntentProtectionLocked(bucket *bolt.Bucket, app string, snapshot *domain.PruneProtectionSnapshot) {
	keys, err := listKeys(bucket, subIntents, false)
	if err != nil {
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceAppState,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: fmt.Sprintf("app %s: apply intents unreadable", app),
		})
		return
	}
	for _, intentID := range keys {
		intent, err := loadApplyIntentLocked(bucket, app, intentID)
		if err != nil {
			snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
				Source: domain.InventorySourceAppState,
				Reason: domain.PruneReasonUnknownInventory,
				Detail: fmt.Sprintf("app %s: apply intent %s unreadable", app, intentID),
			})
			continue
		}
		// Only staged and committed intents can still materialize; an
		// applied intent is already covered by ACTIVE.
		if intent.State != domain.AppIntentStaged && intent.State != domain.AppIntentCommitted {
			continue
		}
		owner := app + "@intent." + intentID
		for _, service := range intent.Spec.Services {
			snapshot.Roots = append(snapshot.Roots,
				domain.ImageRefRoots(domain.ProtectionApplyIntent, service.Image, owner+"."+service.Name)...)
		}
	}
}

func readOperationProtectionLocked(bucket *bolt.Bucket, app string, snapshot *domain.PruneProtectionSnapshot) {
	keys, err := listKeys(bucket, subOps, false)
	if err != nil {
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceOperation,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: fmt.Sprintf("app %s: operation journal unreadable", app),
		})
		return
	}
	ops, err := subBucket(bucket, subOps, false)
	if err != nil {
		return
	}
	if ops == nil {
		return
	}
	for _, opID := range keys {
		var op domain.AppOperation
		found, err := getRecord(ops, []byte(opID), &op, "operation")
		if err != nil || !found {
			snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
				Source: domain.InventorySourceOperation,
				Reason: domain.PruneReasonUnknownInventory,
				Detail: fmt.Sprintf("app %s: operation %s unreadable", app, opID),
			})
			continue
		}
		// An operation with no terminal outcome may still be running:
		// every image it named is protected.
		if op.Outcome != "" {
			continue
		}
		owner := app + "@op." + opID
		for _, step := range op.Steps {
			if step.Digest != "" {
				snapshot.Roots = append(snapshot.Roots, domain.ProtectionRoot{
					Kind: domain.ProtectionOperation, Ref: step.Digest,
					Repository: domain.ImageRefRepository(step.Image), Owner: owner,
				})
			}
			snapshot.Roots = append(snapshot.Roots,
				domain.ImageRefRoots(domain.ProtectionOperation, step.Image, owner)...)
		}
	}
}

func readInhibitionProtectionLocked(bucket *bolt.Bucket, app string, snapshot *domain.PruneProtectionSnapshot) {
	var inhibitions []domain.AppRecoveryInhibition
	found, err := getRecord(bucket, keyInhibitions, &inhibitions, "recovery inhibitions")
	if err != nil {
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceAppState,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: fmt.Sprintf("app %s: recovery inhibitions unreadable", app),
		})
		return
	}
	if !found {
		return
	}
	for _, inhibition := range inhibitions {
		if inhibition.ContainerID == "" {
			continue
		}
		snapshot.Roots = append(snapshot.Roots, domain.ProtectionRoot{
			Kind:  domain.ProtectionRecoveryInhibition,
			Ref:   inhibition.ContainerID,
			Owner: app + "@" + inhibition.Service,
		})
	}
}

// readAppOwnershipProtectionLocked records the current incarnation's
// claims. It is the per-app record, which always reflects the live
// incarnation; historical claims come from the ownership-history
// bucket.
func readAppOwnershipProtectionLocked(bucket *bolt.Bucket, app, appID string, snapshot *domain.PruneProtectionSnapshot) {
	var ownership domain.AppOwnership
	found, err := getRecord(bucket, keyOwnership, &ownership, "ownership")
	if err != nil {
		snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceOwnership,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: fmt.Sprintf("app %s: ownership record unreadable", app),
		})
		return
	}
	if !found {
		return
	}
	// The ownership record's own ID is authoritative: the app record's
	// UUID can lag behind it after a re-deploy.
	claimAppID := ownership.ID
	if claimAppID == "" {
		claimAppID = appID
	}
	claims, gap := claimsForOwnership(app, claimAppID, ownership)
	if gap != nil {
		snapshot.Gaps = append(snapshot.Gaps, *gap)
	}
	snapshot.VolumeClaims = append(snapshot.VolumeClaims, claims...)
	snapshot.ImageClaims = append(snapshot.ImageClaims, imageClaimsForOwnership(app, claimAppID, ownership)...)
}

// readOwnershipHistoryLocked records every historical incarnation's
// claims. A reused app name never overwrites them, so a volume retained
// by a removed app stays protected after the name is taken again.
func readOwnershipHistoryLocked(tx *bolt.Tx, snapshot *domain.PruneProtectionSnapshot) error {
	history := tx.Bucket(bucketOwnershipHistory)
	if history == nil {
		return nil
	}
	return history.ForEach(func(incarnation, value []byte) error {
		if value == nil {
			return nil
		}
		var ownership domain.AppOwnership
		if err := unmarshal(value, &ownership, "ownership history"); err != nil {
			snapshot.Gaps = append(snapshot.Gaps, domain.InventoryGap{
				Source: domain.InventorySourceOwnership,
				Reason: domain.PruneReasonUnknownInventory,
				Detail: fmt.Sprintf("ownership history %s unreadable", incarnation),
			})
			return nil
		}
		claimApp := ownership.App
		if claimApp == "" {
			claimApp = string(incarnation)
		}
		claims, gap := claimsForOwnership(claimApp, ownership.ID, ownership)
		if gap != nil {
			snapshot.Gaps = append(snapshot.Gaps, *gap)
			return nil
		}
		snapshot.VolumeClaims = append(snapshot.VolumeClaims, claims...)
		snapshot.ImageClaims = append(snapshot.ImageClaims, imageClaimsForOwnership(claimApp, ownership.ID, ownership)...)
		return nil
	})
}

// imageClaimsForOwnership converts one ownership record into image claims.
// An absent or unrecognized lifecycle state maps conservatively to
// retained: only an explicit released record can ever be eligible.
func imageClaimsForOwnership(app, appID string, ownership domain.AppOwnership) []domain.ImageClaim {
	claims := make([]domain.ImageClaim, 0, len(ownership.Images))
	for _, image := range ownership.Images {
		if image.Reference == "" && image.Digest == "" {
			continue
		}
		claims = append(claims, domain.ImageClaim{
			Reference: image.Reference,
			Digest:    image.Digest,
			App:       app,
			AppID:     appID,
			Service:   image.Service,
			State:     mapOwnedImageState(image.State),
		})
	}
	return claims
}

// mapOwnedImageState maps a durable owned-image state to a claim state.
// Unknown values protect.
func mapOwnedImageState(state string) domain.VolumeClaimState {
	switch state {
	case domain.AppResourceReleased:
		return domain.VolumeClaimReleased
	case domain.AppResourceAttached:
		return domain.VolumeClaimAttached
	default:
		return domain.VolumeClaimRetained
	}
}

// claimsForOwnership converts one ownership record into volume claims.
// An absent or unrecognized lifecycle state maps conservatively to
// retained: only an explicit released record can ever be eligible.
func claimsForOwnership(app, appID string, ownership domain.AppOwnership) ([]domain.VolumeClaim, *domain.InventoryGap) {
	claims := make([]domain.VolumeClaim, 0, len(ownership.Volumes))
	for _, volume := range ownership.Volumes {
		name := volume.RuntimeName
		if name == "" {
			name = volume.Name
		}
		if name == "" {
			continue
		}
		claims = append(claims, domain.VolumeClaim{
			Name:    name,
			App:     app,
			AppID:   appID,
			Service: volume.Service,
			State:   mapOwnedVolumeState(volume.State),
		})
	}
	return claims, nil
}

// mapOwnedVolumeState maps a durable owned-volume state to a claim
// state. Unknown values protect.
func mapOwnedVolumeState(state string) domain.VolumeClaimState {
	switch state {
	case domain.AppResourceReleased:
		return domain.VolumeClaimReleased
	case domain.AppResourceAttached:
		return domain.VolumeClaimAttached
	default:
		return domain.VolumeClaimRetained
	}
}

// dedupeRoots drops duplicate roots, preserving first-seen order.
func dedupeRoots(roots []domain.ProtectionRoot) []domain.ProtectionRoot {
	seen := make(map[string]struct{}, len(roots))
	out := make([]domain.ProtectionRoot, 0, len(roots))
	for _, root := range roots {
		if !root.Valid() {
			continue
		}
		key := string(root.Kind) + "\x00" + root.Ref + "\x00" + root.Repository
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, root)
	}
	return out
}

// dedupeClaims drops identical claims, preserving first-seen order. The
// lifecycle state is part of the identity: a released claim and an
// attached claim for the same runtime volume are different facts, and
// collapsing them would lose the one that protects the volume.
func dedupeClaims(claims []domain.VolumeClaim) []domain.VolumeClaim {
	seen := make(map[string]struct{}, len(claims))
	out := make([]domain.VolumeClaim, 0, len(claims))
	for _, claim := range claims {
		key := claim.Name + "\x00" + claim.AppID + "\x00" + claim.App + "\x00" + claim.Service + "\x00" + string(claim.State)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, claim)
	}
	return out
}
