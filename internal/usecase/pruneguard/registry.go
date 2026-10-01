package pruneguard

import (
	"sort"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// RegistryTagCandidate is one registry tag as inventoried.
type RegistryTagCandidate struct {
	Ref domain.RegistryTagRef
	// Digest is the manifest digest the tag resolved to; empty when
	// unknown.
	Digest string
	// ModTime is the tag's last modification time.
	ModTime time.Time
	// ManifestKnown is false when the tag's manifest could not be read,
	// so the content behind it cannot be proven unreferenced.
	ManifestKnown bool
}

// RegistryPlanInput is the complete input of one registry tag plan.
// KeepLast semantics match the product contract: latest plus the
// keep_last newest non-latest tags per repository. KeepLast <= 0 skips
// registry tag cleanup entirely, so the function returns no verdicts.
type RegistryPlanInput struct {
	Tags     []RegistryTagCandidate
	Snapshot *domain.PruneProtectionSnapshot
	KeepLast int
	// Complete is false when the registry inventory could not be read
	// in full. Candidates that would otherwise be eligible become
	// unknown instead.
	Complete bool
}

// PlanRegistryTags computes one verdict per candidate tag. Output is
// ordered by repository then tag, so callers and reports are
// deterministic regardless of input order.
func PlanRegistryTags(input RegistryPlanInput) []domain.RegistryTagVerdict {
	if input.KeepLast <= 0 {
		return nil
	}

	kept := retentionWindow(input.Tags, input.KeepLast)

	verdicts := make([]domain.RegistryTagVerdict, 0, len(input.Tags))
	for _, candidate := range input.Tags {
		verdicts = append(verdicts, planRegistryTag(candidate, kept, input))
	}
	sortRegistryTagVerdicts(verdicts)
	return verdicts
}

func planRegistryTag(candidate RegistryTagCandidate, kept map[string]struct{}, input RegistryPlanInput) domain.RegistryTagVerdict {
	verdict := domain.RegistryTagVerdict{Ref: candidate.Ref, Digest: candidate.Digest}

	if !candidate.Ref.Valid() {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownIdentity)
		return verdict
	}

	// A durable root wins over every other consideration: protected
	// candidates stay protected even when the inventory is incomplete.
	refs := []string{candidate.Ref.Key(), candidate.Ref.String()}
	if candidate.Digest != "" {
		refs = append(refs, candidate.Digest)
	}
	if matched, reasons := rootReasonsOrFallback(input.Snapshot, refs...); matched {
		verdict.Verdict, verdict.Reasons = protected(reasons, domain.PruneReasonProtectedSharedContent)
		return verdict
	}

	if candidate.Ref.Tag == "latest" {
		verdict.Verdict, verdict.Reasons = protected(nil, domain.PruneReasonProtectedLatest)
		return verdict
	}
	if _, inWindow := kept[candidate.Ref.Key()]; inWindow {
		verdict.Verdict, verdict.Reasons = protected(nil, domain.PruneReasonProtectedRetentionWindow)
		return verdict
	}

	if !candidate.ManifestKnown {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownManifest)
		return verdict
	}
	// Safety is only provable against a complete view of both the
	// registry and the durable facts that claim it. An incomplete app
	// snapshot could be hiding a root for this very tag.
	if !input.Complete || !input.Snapshot.Complete() {
		verdict.Verdict, verdict.Reasons = unknownResult(domain.PruneReasonUnknownInventory)
		return verdict
	}

	verdict.Verdict, verdict.Reasons = eligibleResult(domain.PruneReasonEligibleRetention)
	return verdict
}

// retentionWindow returns the canonical keys of the latest tag plus the
// keep_last newest non-latest tags of every repository. Ordering is by
// modification time descending, then tag descending, so equal
// timestamps still produce one deterministic window.
func retentionWindow(tags []RegistryTagCandidate, keepLast int) map[string]struct{} {
	byRepository := make(map[string][]RegistryTagCandidate)
	for _, tag := range tags {
		if !tag.Ref.Valid() {
			continue
		}
		byRepository[tag.Ref.Repository] = append(byRepository[tag.Ref.Repository], tag)
	}

	kept := make(map[string]struct{})
	for _, repositoryTags := range byRepository {
		sort.SliceStable(repositoryTags, func(i, j int) bool {
			if repositoryTags[i].ModTime.Equal(repositoryTags[j].ModTime) {
				return repositoryTags[i].Ref.Tag > repositoryTags[j].Ref.Tag
			}
			return repositoryTags[i].ModTime.After(repositoryTags[j].ModTime)
		})

		count := 0
		for _, tag := range repositoryTags {
			if tag.Ref.Tag == "latest" {
				kept[tag.Ref.Key()] = struct{}{}
				continue
			}
			if count >= keepLast {
				break
			}
			kept[tag.Ref.Key()] = struct{}{}
			count++
		}
	}
	return kept
}

func sortRegistryTagVerdicts(verdicts []domain.RegistryTagVerdict) {
	sort.SliceStable(verdicts, func(i, j int) bool {
		if verdicts[i].Ref.Repository != verdicts[j].Ref.Repository {
			return verdicts[i].Ref.Repository < verdicts[j].Ref.Repository
		}
		return verdicts[i].Ref.Tag < verdicts[j].Ref.Tag
	})
}
