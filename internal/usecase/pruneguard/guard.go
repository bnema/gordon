// Package pruneguard holds the ownership-aware prune decisions frozen by
// docs/plans/v2.50.0/04-network.md §8 and 03-deployment.md §9.
//
// PREPARATION: every function here is pure — inputs in, verdicts out, no
// store reads, no runtime calls. The live wiring (volumes Service,
// images Service, prune scheduler consulting AppState; handlers mapping
// ErrPruneDisabled to 409 + {"error":"prune-disabled"}) lands at cutover.
// Until then these decisions are exercised by tests only, which is what
// keeps them referenced.
//
// Fail-closed order: in-use and unmanaged resources are never eligible;
// app-owned resources are never eligible (the engine never deletes
// volumes; remove retains them as orphans); legacy managed-label
// resources (unknown / old-V2 provenance) are never eligible once ANY
// app state exists; image prune fails closed on pinned or unknown
// content and on any candidate the protected set covers.
package pruneguard

import (
	"fmt"

	"github.com/bnema/gordon/internal/domain"
)

// VolumesPruneAllowed is the operation-level gate for PruneVolumes: while
// ANY app state exists the operation disables itself with the frozen
// prune-disabled code instead of guessing provenance.
func VolumesPruneAllowed(appStateExists bool) error {
	if appStateExists {
		return fmt.Errorf("%w: volume prune refuses to run while app state exists", domain.ErrPruneDisabled)
	}
	return nil
}

// ImagePruneSchedulerAllowed is the operation-level gate for the image
// prune scheduler: disabled while ANY app state exists.
func ImagePruneSchedulerAllowed(appStateExists bool) error {
	if appStateExists {
		return fmt.Errorf("%w: image prune scheduler refuses to run while app state exists", domain.ErrPruneDisabled)
	}
	return nil
}

// VolumeProvenance classifies one volume's label set.
type VolumeProvenance int

const (
	// ProvenanceUnmanaged is not Gordon-managed (or no labels at all).
	ProvenanceUnmanaged VolumeProvenance = iota
	// ProvenanceLegacyManaged carries the managed label but no app
	// ownership labels: the unknown / old-V2 class. Never implicitly
	// eligible once app state exists.
	ProvenanceLegacyManaged
	// ProvenanceAppOwned carries engine-stamped app ownership labels.
	// Never prune-eligible: volumes are never deleted.
	ProvenanceAppOwned
)

// ClassifyVolume maps a volume label set to its provenance.
func ClassifyVolume(labels map[string]string) VolumeProvenance {
	if labels == nil || labels[domain.LabelManaged] != "true" {
		return ProvenanceUnmanaged
	}
	if labels[domain.LabelApp] != "" {
		return ProvenanceAppOwned
	}
	return ProvenanceLegacyManaged
}

// VolumePruneEligible renders the per-volume verdict. inUse and runtime
// state arrive as arguments so the decision stays pure; callers pass the
// live values at cutover.
func VolumePruneEligible(provenance VolumeProvenance, inUse, appStateExists bool) (bool, string) {
	if inUse {
		return false, "in-use"
	}
	switch provenance {
	case ProvenanceAppOwned:
		return false, "app-owned: volumes are never deleted"
	case ProvenanceLegacyManaged:
		if appStateExists {
			return false, "prune-disabled: legacy managed volume while app state exists"
		}
		return true, "legacy-managed with no app state"
	default:
		return false, "not gordon-managed"
	}
}

// ImageCandidate is one prune candidate described by identity only.
type ImageCandidate struct {
	// Digest is the content digest (sha256:…); empty means unknown.
	Digest string
	Tags   []string
	// Pinned marks registry pinned indexes/manifests and any content
	// the operator pinned: never eligible.
	Pinned bool
}

// ProtectedImageSet unions the digest sets prune must never touch:
// active definitions of running and stopped apps, in-flight op digests,
// and registry pinned indexes/manifests plus their referenced content.
// Callers build each set from AppState/registry state at cutover.
func ProtectedImageSet(sets ...[]string) map[string]struct{} {
	protected := make(map[string]struct{})
	for _, set := range sets {
		for _, digest := range set {
			if digest != "" {
				protected[digest] = struct{}{}
			}
		}
	}
	return protected
}

// PartitionImageCandidates splits candidates into safe-to-prune and
// blocked. Blocked covers pinned content, unknown (digest-less)
// content, and anything the protected set covers.
func PartitionImageCandidates(candidates []ImageCandidate, protected map[string]struct{}) (safe, blocked []ImageCandidate) {
	for _, c := range candidates {
		if c.Pinned || c.Digest == "" {
			blocked = append(blocked, c)
			continue
		}
		if _, ok := protected[c.Digest]; ok {
			blocked = append(blocked, c)
			continue
		}
		safe = append(safe, c)
	}
	return safe, blocked
}

// CheckManualImagePrune fails the whole manual prune closed when ANY
// candidate is blocked: pinned/unknown/protected content refuses the
// operation instead of pruning around it.
func CheckManualImagePrune(candidates []ImageCandidate, protected map[string]struct{}) error {
	_, blocked := PartitionImageCandidates(candidates, protected)
	if len(blocked) > 0 {
		return fmt.Errorf("%w: %d image candidate(s) are pinned, unknown, or protected",
			domain.ErrPruneDisabled, len(blocked))
	}
	return nil
}
