// Package pruneguard holds the ownership-aware prune policy: pure
// planning from complete inputs to per-candidate verdicts. Every
// exported function here is pure — inputs in, verdicts out, no store
// reads, no runtime calls, no deletion.
//
// The policy is fail-closed per candidate, never per operation:
//
//   - A resource is eligible only when every fact needed to prove it
//     safe was read completely and no durable root claims it.
//   - A durable root (desired/active/recovery/apply/operation/ownership
//     /container use/pending upload/shared closure/pin) makes the
//     candidate protected.
//   - Anything else — unknown identity, unknown provenance, incomplete
//     inventory, unreadable manifest — makes the candidate unknown.
//     Unknown candidates are never deleted; they do not disable the
//     whole operation.
//
// Ownership is never inferred from names, and labels never override a
// contradictory or missing durable ownership record.
package pruneguard

import (
	"github.com/bnema/gordon/internal/domain"
)

// Provenance classifies a runtime resource by the labels it carries.
type Provenance int

const (
	// ProvenanceUnmanaged carries no Gordon labels: never adopted,
	// never deleted.
	ProvenanceUnmanaged Provenance = iota
	// ProvenanceLegacyManaged carries the managed label but no app
	// ownership label: the legacy/unknown class.
	ProvenanceLegacyManaged
	// ProvenanceServiceManaged carries standalone service labels.
	ProvenanceServiceManaged
	// ProvenanceAppOwned carries app ownership labels.
	ProvenanceAppOwned
)

// ClassifyResource maps a label set to its provenance. App ownership
// wins over service labels, and the managed marker alone is never
// treated as app ownership.
func ClassifyResource(labels map[string]string) Provenance {
	if len(labels) == 0 {
		return ProvenanceUnmanaged
	}
	if labels[domain.LabelApp] != "" {
		return ProvenanceAppOwned
	}
	if labels[domain.LabelService] != "" || labels[domain.LabelServiceName] != "" {
		return ProvenanceServiceManaged
	}
	if labels[domain.LabelManaged] == "true" {
		return ProvenanceLegacyManaged
	}
	return ProvenanceUnmanaged
}

// protected builds a protected verdict with canonical reasons, falling
// back to a reason that always fits when no root kind mapped.
func protected(reasons []domain.PruneReason, fallback domain.PruneReason) (domain.PruneVerdict, []domain.PruneReason) {
	canonical := domain.CanonicalReasons(reasons...)
	if len(canonical) == 0 {
		canonical = domain.CanonicalReasons(fallback)
	}
	return domain.PruneVerdictProtected, canonical
}

// eligibleResult builds an eligible verdict with its single reason.
func eligibleResult(reason domain.PruneReason) (domain.PruneVerdict, []domain.PruneReason) {
	return domain.PruneVerdictEligible, domain.CanonicalReasons(reason)
}

// unknownResult builds an unknown verdict with its single reason.
func unknownResult(reason domain.PruneReason) (domain.PruneVerdict, []domain.PruneReason) {
	return domain.PruneVerdictUnknown, domain.CanonicalReasons(reason)
}

// rootReasonsOrFallback collects the mapped reasons of every root
// claiming refs, falling back to a shared-content reason when a root
// matched but its kind cannot justify a verdict reason.
func rootReasonsOrFallback(snapshot *domain.PruneProtectionSnapshot, refs ...string) (bool, []domain.PruneReason) {
	if snapshot == nil {
		return false, nil
	}
	matched := false
	for _, ref := range refs {
		if ref != "" && snapshot.ProtectsAnyRef(ref) {
			matched = true
			break
		}
	}
	if !matched {
		return false, nil
	}
	reasons := snapshot.RootReasons(refs...)
	if len(reasons) == 0 {
		reasons = domain.CanonicalReasons(domain.PruneReasonProtectedSharedContent)
	}
	return true, reasons
}
