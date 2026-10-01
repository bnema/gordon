package pruneguard

import (
	"sort"

	"github.com/bnema/gordon/internal/domain"
)

// VolumePlanInput is the complete input of one volume plan.
type VolumePlanInput struct {
	Volumes  []*domain.VolumeInfo
	Snapshot *domain.PruneProtectionSnapshot
	// Complete is false when the runtime volume inventory could not be
	// read in full. Candidates that would otherwise be eligible become
	// unknown.
	Complete bool
}

// PlanVolumes computes one verdict per runtime volume.
//
// A volume is eligible only when a durable ownership record explicitly
// records it as released, the runtime labels agree with that record, and
// no container uses it. Labels alone never make a volume deletable, and
// a retained record is never reinterpreted as released. A legitimately
// empty result is a success, not an error.
//
// Output is ordered by volume name so callers are deterministic.
func PlanVolumes(input VolumePlanInput) []domain.VolumeVerdict {
	ownershipIncomplete := input.Snapshot.HasGapFor(domain.InventorySourceOwnership)

	verdicts := make([]domain.VolumeVerdict, 0, len(input.Volumes))
	for _, volume := range input.Volumes {
		verdicts = append(verdicts, planVolume(volume, input, ownershipIncomplete))
	}
	sort.SliceStable(verdicts, func(i, j int) bool {
		return verdicts[i].Ref.Name < verdicts[j].Ref.Name
	})
	return verdicts
}

func planVolume(volume *domain.VolumeInfo, input VolumePlanInput, ownershipIncomplete bool) domain.VolumeVerdict {
	verdict := domain.VolumeVerdict{}
	if volume == nil {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownIdentity)
		return verdict
	}
	verdict.Ref = domain.RuntimeVolumeRef{Name: volume.Name}

	if !verdict.Ref.Valid() {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownIdentity)
		return verdict
	}

	if volume.InUse {
		verdict.Verdict, verdict.Reasons = protected(nil, domain.PruneReasonProtectedContainerUse)
		return verdict
	}

	claim, hasClaim := input.Snapshot.VolumeClaimFor(volume.Name)

	// Positive protection first: any attached or retained record wins,
	// even when the rest of the inventory is incomplete, and a newer
	// incarnation never un-protects an older claim.
	if input.Snapshot.VolumeProtects(volume.Name) {
		verdict.Verdict, verdict.Reasons = protected(nil, domain.PruneReasonProtectedOwnership)
		return verdict
	}

	if !hasClaim {
		switch ClassifyResource(volume.Labels) {
		case ProvenanceUnmanaged:
			verdict.Verdict, verdict.Reasons = protected(nil, domain.PruneReasonProtectedUnmanaged)
		case ProvenanceLegacyManaged:
			verdict.Verdict, verdict.Reasons = protected(nil, domain.PruneReasonProtectedManagedOnly)
		case ProvenanceServiceManaged:
			verdict.Verdict, verdict.Reasons = protected(nil, domain.PruneReasonProtectedServiceManaged)
		default:
			// App labels with no durable record: ownership history is
			// missing, and labels never override that.
			verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownProvenance)
		}
		return verdict
	}

	// Released: the durable record and the runtime labels must agree on
	// the owning app incarnation before the volume can be deleted.
	if !claim.MatchesLabels(volume.Labels) {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownProvenance)
		return verdict
	}

	if ownershipIncomplete || !input.Complete {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownInventory)
		return verdict
	}
	if !claim.Valid() {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownProvenance)
		return verdict
	}
	// Eligibility needs both halves of the proof: a durable released
	// record and the incarnation UUID that ties the runtime volume to
	// it. A record without an incarnation cannot prove which volume it
	// claims, so it never authorizes deletion.
	if claim.AppID == "" {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownProvenance)
		return verdict
	}

	verdict.Verdict, verdict.Reasons = eligibleResult(domain.PruneReasonEligibleReleasedVolume)
	return verdict
}
