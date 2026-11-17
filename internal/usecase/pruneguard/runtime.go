package pruneguard

import (
	"sort"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// RuntimePlanInput is the complete input of one runtime image plan.
type RuntimePlanInput struct {
	Images     []domain.RuntimeImage
	Containers []domain.RuntimeContainerUse
	Snapshot   *domain.PruneProtectionSnapshot
	// ProtectedContainerIDs are container IDs that must retain their
	// images whatever their running state: durable recovery
	// inhibitions name the generation that a replacement may already
	// have superseded. A protected ID with no matching container makes
	// every candidate unknown, because its image cannot be identified.
	ProtectedContainerIDs []string
	// Complete is false when the runtime inventory could not be read in
	// full. Candidates that would otherwise be eligible become unknown.
	Complete bool
	// IncludeUnmanagedDangling makes an untagged image with no Gordon
	// provenance and no container use eligible instead of protected.
	IncludeUnmanagedDangling bool
}

// containerUseIndex indexes container image usage for one planning run.
type containerUseIndex struct {
	imageIDs map[string]struct{}
	refs     map[string]struct{}
	// protectedImageIDs are the images held by protected containers.
	protectedImageIDs map[string]struct{}
	// parentIDs are the images another inventoried image was built from.
	parentIDs map[string]struct{}
	// unknown marks a container whose image identity could not be
	// resolved. It makes every image candidate unknown: an image used
	// by any container, including an unknown one, must survive.
	unknown bool
}

func buildContainerUseIndex(containers []domain.RuntimeContainerUse, protectedIDs []string) containerUseIndex {
	index := containerUseIndex{
		imageIDs:          make(map[string]struct{}, len(containers)),
		refs:              make(map[string]struct{}, len(containers)),
		protectedImageIDs: make(map[string]struct{}),
	}
	byContainerID := make(map[string]domain.RuntimeContainerUse, len(containers))
	for _, use := range containers {
		if use.ContainerID != "" {
			byContainerID[use.ContainerID] = use
		}
		if use.ImageID != "" {
			index.imageIDs[use.ImageID] = struct{}{}
		}
		if use.ImageRef != "" {
			index.refs[use.ImageRef] = struct{}{}
		}
		if use.ImageUnknown || (use.ImageID == "" && use.ImageRef == "") {
			index.unknown = true
		}
	}

	for _, containerID := range protectedIDs {
		use, ok := byContainerID[containerID]
		if !ok || use.ImageID == "" {
			// The protected generation's image cannot be identified, so
			// no candidate can be proven unused.
			index.unknown = true
			continue
		}
		index.protectedImageIDs[use.ImageID] = struct{}{}
	}
	return index
}

func (i containerUseIndex) usesImage(image domain.RuntimeImage) bool {
	if _, ok := i.imageIDs[image.ID]; ok {
		return true
	}
	for _, tag := range image.RepoTags {
		if _, ok := i.refs[tag]; ok {
			return true
		}
	}
	for _, digest := range image.RepoDigests {
		if _, ok := i.refs[digest]; ok {
			return true
		}
	}
	return false
}

// PlanRuntimeImages computes one verdict per runtime image. Output is
// ordered by image ID so callers and reports are deterministic.
func PlanRuntimeImages(input RuntimePlanInput) []domain.RuntimeImageVerdict {
	uses := buildContainerUseIndex(input.Containers, input.ProtectedContainerIDs)
	uses.parentIDs = parentImageIDs(input.Images)

	verdicts := make([]domain.RuntimeImageVerdict, 0, len(input.Images))
	for _, image := range input.Images {
		verdicts = append(verdicts, planRuntimeImage(image, uses, input))
	}
	sort.SliceStable(verdicts, func(i, j int) bool {
		return verdicts[i].Ref.ID < verdicts[j].Ref.ID
	})
	return verdicts
}

func planRuntimeImage(image domain.RuntimeImage, uses containerUseIndex, input RuntimePlanInput) domain.RuntimeImageVerdict {
	verdict := domain.RuntimeImageVerdict{Ref: domain.RuntimeImageRef{ID: image.ID}}

	if !verdict.Ref.Valid() {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownIdentity)
		return verdict
	}

	// A durable root wins over provenance: a still-referenced image
	// stays protected even if its labels are gone. Repo tags are the
	// runtime-side identity of app-pinned content, so they are matched
	// against tag-keyed roots as well as digests. The bare digest of each
	// repo digest is matched too: roots such as an active service's pinned
	// digest name content without its repository.
	if matched, reasons := rootReasonsOrFallback(input.Snapshot, imageIdentities(image)...); matched {
		verdict.Verdict, verdict.Reasons = protected(reasons, domain.PruneReasonProtectedSharedContent)
		return verdict
	}

	// Live or ambiguous container use protects before any ownership claim
	// is consulted: an image a container could be running must survive.
	if useVerdict, reasons, decided := containerUseVerdict(image, uses); decided {
		return domain.RuntimeImageVerdict{Ref: domain.RuntimeImageRef{ID: image.ID}, Verdict: useVerdict, Reasons: reasons}
	}

	eligibleReason := domain.PruneReasonEligibleDanglingRuntimeImage
	// Labels are hints only. Deletion requires positive durable ownership:
	// a matching claim from an app's ownership record. Every matching
	// claim is considered: an attached or retained record protects even
	// when a newer released record for the same image also exists.
	if claimVerdict, reasons, decided := imageClaimVerdict(image, input.Snapshot); decided {
		// The opt-in must stay after containerUseVerdict (tags, container
		// use, recovery inhibition) and before the completeness check, so
		// it can only release an untagged, unused image from a complete view.
		if !optedInUnmanaged(image, reasons, uses, input) {
			return domain.RuntimeImageVerdict{Ref: domain.RuntimeImageRef{ID: image.ID}, Verdict: claimVerdict, Reasons: reasons}
		}
		eligibleReason = domain.PruneReasonEligibleUnmanagedDanglingImage
	}

	// Safety is only provable against a complete view of both the
	// runtime and the durable facts that claim the image.
	if !input.Complete || !input.Snapshot.Complete() {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownInventory)
		return verdict
	}

	verdict.Verdict, verdict.Reasons = eligibleResult(eligibleReason)
	return verdict
}

// optedInUnmanaged reports whether an untagged, unused image protected
// only for lacking Gordon provenance is released by the operator opt-in.
// Callers reach it after the tag and container-use gates, so the image is
// already proven untagged and unused; the completeness gate still applies.
// A parent of another inventoried image is never released: the runtime
// refuses to remove an image with children; these are mostly build
// intermediates listed by All:true.
func optedInUnmanaged(image domain.RuntimeImage, reasons []domain.PruneReason, uses containerUseIndex, input RuntimePlanInput) bool {
	if _, parent := uses.parentIDs[image.ID]; parent {
		return false
	}
	return input.IncludeUnmanagedDangling &&
		len(reasons) == 1 && reasons[0] == domain.PruneReasonProtectedUnmanaged
}

// parentImageIDs indexes the images another inventoried image names as its
// parent. An empty ParentID is unknown and names no parent.
func parentImageIDs(images []domain.RuntimeImage) map[string]struct{} {
	parents := make(map[string]struct{})
	for _, image := range images {
		if image.ParentID != "" {
			parents[image.ParentID] = struct{}{}
		}
	}
	return parents
}

// containerUseVerdict decides a candidate from container/tag state alone.
func containerUseVerdict(image domain.RuntimeImage, uses containerUseIndex) (domain.PruneVerdict, []domain.PruneReason, bool) {
	if hasRepoTags(image.RepoTags) {
		verdict, reasons := protected(nil, domain.PruneReasonProtectedTagged)
		return verdict, reasons, true
	}
	if uses.usesImage(image) {
		verdict, reasons := protected(nil, domain.PruneReasonProtectedContainerUse)
		return verdict, reasons, true
	}
	if _, held := uses.protectedImageIDs[image.ID]; held {
		verdict, reasons := protected(nil, domain.PruneReasonProtectedRecoveryInhibition)
		return verdict, reasons, true
	}
	if uses.unknown {
		verdict, reasons := unknownResult(domain.PruneReasonUnknownContainerUse)
		return verdict, reasons, true
	}
	return "", nil, false
}

// noClaimVerdict classifies a runtime image with no durable ownership
// record. App labels without a record are never eligible.
func noClaimVerdict(image domain.RuntimeImage) (domain.PruneVerdict, []domain.PruneReason) {
	switch ClassifyResource(image.Labels) {
	case ProvenanceUnmanaged:
		return protected(nil, domain.PruneReasonProtectedUnmanaged)
	case ProvenanceLegacyManaged:
		return protected(nil, domain.PruneReasonProtectedManagedOnly)
	case ProvenanceServiceManaged:
		return protected(nil, domain.PruneReasonProtectedServiceManaged)
	default:
		return unknownResult(domain.PruneReasonUnknownProvenance)
	}
}

// imageClaimVerdict classifies an image from every matching ownership claim.
// It reports whether the claims decided the verdict; false means all matching
// claims are released and the ordinary eligibility gates still apply.
func imageClaimVerdict(image domain.RuntimeImage, snapshot *domain.PruneProtectionSnapshot) (domain.PruneVerdict, []domain.PruneReason, bool) {
	claims := snapshot.ImageClaimsFor(imageIdentities(image)...)
	if len(claims) == 0 {
		verdict, reasons := noClaimVerdict(image)
		return verdict, reasons, true
	}
	protecting, released, invalid := false, false, false
	for _, claim := range claims {
		if claim.State.Protects() {
			protecting = true
			break
		}
		if !claim.Valid() {
			invalid = true
			continue
		}
		released = true
	}
	switch {
	case protecting:
		verdict, reasons := protected(nil, domain.PruneReasonProtectedOwnership)
		return verdict, reasons, true
	case invalid || !released:
		verdict, reasons := unknownResult(domain.PruneReasonUnknownProvenance)
		return verdict, reasons, true
	default:
		return "", nil, false
	}
}

// imageIdentities returns every identity a durable image claim may match:
// the runtime image ID, full repo tags and digests, the digest part of a
// repo digest, and the registry tag key of a repo tag.
func imageIdentities(image domain.RuntimeImage) []string {
	identities := make([]string, 0, 1+len(image.RepoTags)+2*len(image.RepoDigests))
	identities = append(identities, image.ID)
	repoDigests := image.RepoDigests
	for _, repoTag := range image.RepoTags {
		if domain.IsDigestOnlyImageRef(repoTag) {
			// Podman lists digest pulls in RepoTags: keep the identity.
			repoDigests = append(repoDigests, repoTag)
			continue
		}
		if domain.IsPlaceholderRepoTag(repoTag) {
			continue
		}
		identities = append(identities, repoTag)
		if tagRef, ok := domain.ParseRegistryTagRef(repoTag); ok {
			identities = append(identities, tagRef.Key())
		}
	}
	for _, repoDigest := range repoDigests {
		if repoDigest == "" {
			continue
		}
		identities = append(identities, repoDigest)
		if _, digest, ok := strings.Cut(repoDigest, "@"); ok && digest != "" {
			identities = append(identities, digest)
		}
	}
	return identities
}

// hasRepoTags reports whether the runtime still has a usable tag for the
// image. Placeholder tags never count.
func hasRepoTags(repoTags []string) bool {
	for _, tag := range repoTags {
		if domain.IsPlaceholderRepoTag(tag) {
			continue
		}
		return true
	}
	return false
}
