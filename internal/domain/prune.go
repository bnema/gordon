package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file freezes the selective-prune data model: resource identities,
// protection roots, inventory completeness, verdicts, stable reason
// codes, and the aggregate plan/report shapes. It is pure data plus
// validation — no store reads, no runtime calls, no JSON concerns.
//
// Fail-closed rule: a candidate is only PruneVerdictEligible when every
// fact needed to prove it safe was read completely. Missing, unreadable,
// or unsupported information yields PruneVerdictUnknown for the
// dependent candidates; it never yields eligibility, and it is never an
// operation-wide shutdown.

// PruneVerdict is the fail-closed classification of one prune candidate.
type PruneVerdict string

const (
	// PruneVerdictEligible means every fact needed to prove the
	// candidate safe was available and no durable claim covers it.
	PruneVerdictEligible PruneVerdict = "eligible"
	// PruneVerdictProtected means a durable fact positively claims the
	// candidate, so it must survive this prune.
	PruneVerdictProtected PruneVerdict = "protected"
	// PruneVerdictUnknown means at least one fact needed to prove the
	// candidate safe was missing, unreadable, or unsupported.
	PruneVerdictUnknown PruneVerdict = "unknown"
)

// Valid reports whether v is one of the defined verdicts.
func (v PruneVerdict) Valid() bool {
	switch v {
	case PruneVerdictEligible, PruneVerdictProtected, PruneVerdictUnknown:
		return true
	default:
		return false
	}
}

// PruneReason is a stable machine-readable reason code. The wire forms
// are frozen: they appear in JSON and human output, so existing values
// must not be renamed or re-typed.
type PruneReason string

// Eligible reason codes.
const (
	// PruneReasonEligibleRetention marks a registry tag beyond the
	// latest + keep_last retention window with no protection root.
	PruneReasonEligibleRetention PruneReason = "eligible-retention-window"
	// PruneReasonEligibleUnreferencedBlob marks registry content no
	// retained or protected manifest references.
	PruneReasonEligibleUnreferencedBlob PruneReason = "eligible-unreferenced-blob"
	// PruneReasonEligibleDanglingRuntimeImage marks a runtime image with
	// no tag, no container use, and no protection root.
	PruneReasonEligibleDanglingRuntimeImage PruneReason = "eligible-dangling-runtime-image"
	// PruneReasonEligibleReleasedVolume marks an app-owned volume whose
	// durable lifecycle state explicitly records released/abandoned,
	// which no container uses.
	PruneReasonEligibleReleasedVolume PruneReason = "eligible-released-volume"
)

// Protected reason codes.
const (
	// PruneReasonProtectedLatest marks the latest tag of a repository.
	PruneReasonProtectedLatest PruneReason = "protected-latest"
	// PruneReasonProtectedRetentionWindow marks a tag inside the
	// configured latest + keep_last window.
	PruneReasonProtectedRetentionWindow PruneReason = "protected-retention-window"
	// PruneReasonProtectedDesiredRevision marks content a desired
	// revision references.
	PruneReasonProtectedDesiredRevision PruneReason = "protected-desired-revision"
	// PruneReasonProtectedActiveService marks content an ACTIVE service
	// references, including stopped and partially converged services.
	PruneReasonProtectedActiveService PruneReason = "protected-active-service"
	// PruneReasonProtectedRecoveryInhibition marks content a durable
	// recovery inhibition still claims.
	PruneReasonProtectedRecoveryInhibition PruneReason = "protected-recovery-inhibition"
	// PruneReasonProtectedApplyIntent marks content a staged or
	// committed apply intent can still materialize.
	PruneReasonProtectedApplyIntent PruneReason = "protected-apply-intent"
	// PruneReasonProtectedOperation marks content an unfinished
	// operation journal references.
	PruneReasonProtectedOperation PruneReason = "protected-operation"
	// PruneReasonProtectedOwnership marks content a durable ownership
	// record claims as attached or retained.
	PruneReasonProtectedOwnership PruneReason = "protected-ownership"
	// PruneReasonProtectedContainerUse marks runtime content any
	// container uses, running or stopped.
	PruneReasonProtectedContainerUse PruneReason = "protected-container-use"
	// PruneReasonProtectedPendingUpload marks registry content a recent
	// upload may still complete into.
	PruneReasonProtectedPendingUpload PruneReason = "protected-pending-upload"
	// PruneReasonProtectedSharedContent marks content referenced by
	// retained or protected roots through the OCI closure.
	PruneReasonProtectedSharedContent PruneReason = "protected-shared-content"
	// PruneReasonProtectedPinned marks explicitly pinned content.
	PruneReasonProtectedPinned PruneReason = "protected-pinned"
	// PruneReasonProtectedServiceManaged marks a standalone service
	// container's own managed resource.
	PruneReasonProtectedServiceManaged PruneReason = "protected-service-managed"
	// PruneReasonProtectedManagedOnly marks a Gordon-managed resource
	// with no app ownership record: the legacy/unknown provenance class.
	PruneReasonProtectedManagedOnly PruneReason = "protected-managed-without-ownership"
	// PruneReasonProtectedTagged marks a runtime image that repo tags
	// still point at: tag retention owns it, not runtime prune.
	PruneReasonProtectedTagged PruneReason = "protected-tagged-runtime-image"
	// PruneReasonProtectedUnmanaged marks a resource with no Gordon
	// provenance at all. Gordon never adopts or deletes it.
	PruneReasonProtectedUnmanaged PruneReason = "protected-unmanaged"
)

// Unknown reason codes.
const (
	// PruneReasonUnknownIdentity marks a candidate whose exact identity
	// could not be established.
	PruneReasonUnknownIdentity PruneReason = "unknown-identity"
	// PruneReasonUnknownProvenance marks a resource whose ownership
	// could not be established.
	PruneReasonUnknownProvenance PruneReason = "unknown-provenance"
	// PruneReasonUnknownInventory marks a candidate whose safety depends
	// on an inventory that was read incompletely.
	PruneReasonUnknownInventory PruneReason = "unknown-incomplete-inventory"
	// PruneReasonUnknownManifest marks content reachable only through a
	// missing, corrupt, or unreadable manifest.
	PruneReasonUnknownManifest PruneReason = "unknown-manifest-unreadable"
	// PruneReasonUnknownMediaType marks content behind an unsupported
	// OCI media type or manifest shape.
	PruneReasonUnknownMediaType PruneReason = "unknown-unsupported-media-type"
	// PruneReasonUnknownContainerUse marks runtime content whose
	// container usage could not be established.
	PruneReasonUnknownContainerUse PruneReason = "unknown-container-use"
)

// pruneReasons lists every defined reason code in canonical output
// order, grouped by verdict category.
var pruneReasons = []PruneReason{
	PruneReasonEligibleRetention,
	PruneReasonEligibleUnreferencedBlob,
	PruneReasonEligibleDanglingRuntimeImage,
	PruneReasonEligibleReleasedVolume,

	PruneReasonProtectedLatest,
	PruneReasonProtectedRetentionWindow,
	PruneReasonProtectedDesiredRevision,
	PruneReasonProtectedActiveService,
	PruneReasonProtectedRecoveryInhibition,
	PruneReasonProtectedApplyIntent,
	PruneReasonProtectedOperation,
	PruneReasonProtectedOwnership,
	PruneReasonProtectedContainerUse,
	PruneReasonProtectedPendingUpload,
	PruneReasonProtectedSharedContent,
	PruneReasonProtectedPinned,
	PruneReasonProtectedServiceManaged,
	PruneReasonProtectedManagedOnly,
	PruneReasonProtectedTagged,
	PruneReasonProtectedUnmanaged,

	PruneReasonUnknownIdentity,
	PruneReasonUnknownProvenance,
	PruneReasonUnknownInventory,
	PruneReasonUnknownManifest,
	PruneReasonUnknownMediaType,
	PruneReasonUnknownContainerUse,
}

// pruneReasonCategory maps every reason code to the verdict it justifies.
var pruneReasonCategory = map[PruneReason]PruneVerdict{
	PruneReasonEligibleRetention:            PruneVerdictEligible,
	PruneReasonEligibleUnreferencedBlob:     PruneVerdictEligible,
	PruneReasonEligibleDanglingRuntimeImage: PruneVerdictEligible,
	PruneReasonEligibleReleasedVolume:       PruneVerdictEligible,
	PruneReasonProtectedLatest:              PruneVerdictProtected,
	PruneReasonProtectedRetentionWindow:     PruneVerdictProtected,
	PruneReasonProtectedDesiredRevision:     PruneVerdictProtected,
	PruneReasonProtectedActiveService:       PruneVerdictProtected,
	PruneReasonProtectedRecoveryInhibition:  PruneVerdictProtected,
	PruneReasonProtectedApplyIntent:         PruneVerdictProtected,
	PruneReasonProtectedOperation:           PruneVerdictProtected,
	PruneReasonProtectedOwnership:           PruneVerdictProtected,
	PruneReasonProtectedContainerUse:        PruneVerdictProtected,
	PruneReasonProtectedPendingUpload:       PruneVerdictProtected,
	PruneReasonProtectedSharedContent:       PruneVerdictProtected,
	PruneReasonProtectedPinned:              PruneVerdictProtected,
	PruneReasonProtectedServiceManaged:      PruneVerdictProtected,
	PruneReasonProtectedManagedOnly:         PruneVerdictProtected,
	PruneReasonProtectedTagged:              PruneVerdictProtected,
	PruneReasonProtectedUnmanaged:           PruneVerdictProtected,
	PruneReasonUnknownIdentity:              PruneVerdictUnknown,
	PruneReasonUnknownProvenance:            PruneVerdictUnknown,
	PruneReasonUnknownInventory:             PruneVerdictUnknown,
	PruneReasonUnknownManifest:              PruneVerdictUnknown,
	PruneReasonUnknownMediaType:             PruneVerdictUnknown,
	PruneReasonUnknownContainerUse:          PruneVerdictUnknown,
}

// Valid reports whether r is a defined reason code.
func (r PruneReason) Valid() bool {
	_, ok := pruneReasonCategory[r]
	return ok
}

// Category returns the verdict r justifies. An undefined reason returns
// the empty verdict, which fails validation.
func (r PruneReason) Category() PruneVerdict {
	return pruneReasonCategory[r]
}

// PruneReasons returns every defined reason code in canonical order.
func PruneReasons() []PruneReason {
	out := make([]PruneReason, len(pruneReasons))
	copy(out, pruneReasons)
	return out
}

// CanonicalReasons returns reasons deduplicated and ordered by the
// frozen canonical order, so verdict output is deterministic.
func CanonicalReasons(reasons ...PruneReason) []PruneReason {
	if len(reasons) == 0 {
		return nil
	}
	seen := make(map[PruneReason]struct{}, len(reasons))
	for _, reason := range reasons {
		seen[reason] = struct{}{}
	}
	out := make([]PruneReason, 0, len(seen))
	for _, reason := range pruneReasons {
		if _, ok := seen[reason]; ok {
			out = append(out, reason)
		}
	}
	// Undefined reasons still surface, ordered after the known ones, so
	// a programming error is visible instead of silently dropped.
	var unknown []PruneReason
	for reason := range seen {
		if !reason.Valid() {
			unknown = append(unknown, reason)
		}
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	return append(out, unknown...)
}

// PruneResourceKind classifies the namespace a candidate identity lives in.
type PruneResourceKind string

const (
	// PruneResourceRegistryTag is a repository tag in registry storage.
	PruneResourceRegistryTag PruneResourceKind = "registry-tag"
	// PruneResourceOCIBlob is digest-addressed registry content.
	PruneResourceOCIBlob PruneResourceKind = "oci-blob"
	// PruneResourceRuntimeImage is a runtime image identified by image ID.
	PruneResourceRuntimeImage PruneResourceKind = "runtime-image"
	// PruneResourceVolume is a runtime volume identified by name.
	PruneResourceVolume PruneResourceKind = "volume"
)

// RegistryTagRef identifies one registry tag by repository and tag.
type RegistryTagRef struct {
	Repository string
	Tag        string
}

// NewRegistryTagRef validates and returns a registry tag identity.
func NewRegistryTagRef(repository, tag string) (RegistryTagRef, error) {
	ref := RegistryTagRef{Repository: strings.TrimSpace(repository), Tag: strings.TrimSpace(tag)}
	if !ref.Valid() {
		return RegistryTagRef{}, fmt.Errorf("invalid registry tag reference %q/%q", repository, tag)
	}
	return ref, nil
}

// Valid reports whether the reference identifies one exact registry tag.
func (r RegistryTagRef) Valid() bool {
	return r.Repository != "" && r.Tag != "" && !strings.ContainsAny(r.Repository, " \t\n") && !strings.ContainsAny(r.Tag, " \t\n")
}

// String renders repository:tag for humans. The form is ambiguous for
// repositories that themselves contain a colon, so it must never be
// parsed back: Key is the canonical identity.
func (r RegistryTagRef) String() string {
	if !r.Valid() {
		return ""
	}
	return r.Repository + ":" + r.Tag
}

// Key is the deterministic, unambiguous map key for this reference.
func (r RegistryTagRef) Key() string {
	if !r.Valid() {
		return ""
	}
	return r.Repository + "\x00" + r.Tag
}

// OCIRef identifies digest-addressed registry content. Scope is the
// repository the content belongs to; an empty scope addresses the
// global blob store.
type OCIRef struct {
	Scope  string
	Digest string
}

// NewOCIRef validates and returns a digest identity.
func NewOCIRef(scope, digest string) (OCIRef, error) {
	ref := OCIRef{Scope: strings.TrimSpace(scope), Digest: strings.TrimSpace(digest)}
	if !ref.Valid() {
		return OCIRef{}, fmt.Errorf("invalid OCI reference %q@%q", scope, digest)
	}
	return ref, nil
}

// Valid reports whether the reference carries a plausible content digest.
func (r OCIRef) Valid() bool {
	return ValidOCIDigest(r.Digest) && !strings.ContainsAny(r.Scope, " \t\n")
}

// String renders scope@digest, or the bare digest for global content.
func (r OCIRef) String() string {
	if r.Digest == "" {
		return ""
	}
	if r.Scope == "" {
		return r.Digest
	}
	return r.Scope + "@" + r.Digest
}

// Key is the deterministic map key for this reference.
func (r OCIRef) Key() string {
	if !r.Valid() {
		return ""
	}
	return r.Scope + "\x00" + r.Digest
}

// RuntimeImageRef identifies one runtime image by its exact image ID.
type RuntimeImageRef struct {
	ID string
}

// NewRuntimeImageRef validates and returns a runtime image identity.
func NewRuntimeImageRef(id string) (RuntimeImageRef, error) {
	ref := RuntimeImageRef{ID: strings.TrimSpace(id)}
	if !ref.Valid() {
		return RuntimeImageRef{}, fmt.Errorf("invalid runtime image ID %q", id)
	}
	return ref, nil
}

// Valid reports whether the image ID is usable as a deletion target.
// Runtime placeholders such as <none> never are.
func (r RuntimeImageRef) Valid() bool {
	if r.ID == "" || strings.ContainsAny(r.ID, " \t\n") {
		return false
	}
	return !strings.HasPrefix(r.ID, "<none>")
}

// String renders the image ID.
func (r RuntimeImageRef) String() string { return r.ID }

// RuntimeVolumeRef identifies one runtime volume by name.
type RuntimeVolumeRef struct {
	Name string
}

// NewRuntimeVolumeRef validates and returns a runtime volume identity.
func NewRuntimeVolumeRef(name string) (RuntimeVolumeRef, error) {
	ref := RuntimeVolumeRef{Name: strings.TrimSpace(name)}
	if !ref.Valid() {
		return RuntimeVolumeRef{}, fmt.Errorf("invalid runtime volume name %q", name)
	}
	return ref, nil
}

// Valid reports whether the volume name is usable as a deletion target.
func (r RuntimeVolumeRef) Valid() bool {
	return r.Name != "" && !strings.ContainsAny(r.Name, " \t\n")
}

// String renders the volume name.
func (r RuntimeVolumeRef) String() string { return r.Name }

// ValidOCIDigest reports whether digest has the OCI
// algorithm:encoded form with a hex-encoded value.
func ValidOCIDigest(digest string) bool {
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || algorithm == "" || len(encoded) < 32 {
		return false
	}
	for _, r := range algorithm {
		if !isDigestAlgorithmRune(r) {
			return false
		}
	}
	for _, r := range encoded {
		if !isHexRune(r) {
			return false
		}
	}
	return true
}

func isDigestAlgorithmRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	case r == '.', r == '-', r == '_', r == '+':
		return true
	default:
		return false
	}
}

func isHexRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		return true
	default:
		return false
	}
}

// ProtectionRootKind classifies one durable fact that retains content.
type ProtectionRootKind string

const (
	// ProtectionLatest is the latest tag of a repository.
	ProtectionLatest ProtectionRootKind = "latest"
	// ProtectionRetentionWindow is the configured latest + keep_last set.
	ProtectionRetentionWindow ProtectionRootKind = "retention-window"
	// ProtectionDesiredRevision is a desired revision's image reference.
	ProtectionDesiredRevision ProtectionRootKind = "desired-revision"
	// ProtectionActiveService is an ACTIVE service's image reference,
	// including stopped and partially converged services.
	ProtectionActiveService ProtectionRootKind = "active-service"
	// ProtectionRecoveryInhibition is a generation-scoped inhibition.
	ProtectionRecoveryInhibition ProtectionRootKind = "recovery-inhibition"
	// ProtectionApplyIntent is a staged or committed apply intent.
	ProtectionApplyIntent ProtectionRootKind = "apply-intent"
	// ProtectionOperation is an unfinished operation journal record.
	ProtectionOperation ProtectionRootKind = "operation"
	// ProtectionOwnership is a durable ownership claim on a resource.
	ProtectionOwnership ProtectionRootKind = "ownership"
	// ProtectionContainerUse is runtime container usage.
	ProtectionContainerUse ProtectionRootKind = "container-use"
	// ProtectionPendingUpload is a recent registry upload.
	ProtectionPendingUpload ProtectionRootKind = "pending-upload"
	// ProtectionSharedContent is transitive OCI closure content.
	ProtectionSharedContent ProtectionRootKind = "shared-content"
	// ProtectionPinned is explicitly pinned content.
	ProtectionPinned ProtectionRootKind = "pinned"
	// ProtectionServiceManaged is standalone service ownership.
	ProtectionServiceManaged ProtectionRootKind = "service-managed"
)

// ProtectionRoot is one durable fact that must retain content.
type ProtectionRoot struct {
	Kind ProtectionRootKind
	// Ref is the canonical identity the root protects in the namespace
	// of its resource kind: a RegistryTagRef.Key, an OCIRef digest, a
	// runtime image ID, or a runtime volume name.
	Ref string
	// Repository qualifies a digest Ref with the repository whose
	// manifest names it, so prune can traverse the full OCI closure
	// (config, layers, child manifests, subject) instead of protecting
	// only the digest itself. Empty when not repository-scoped.
	Repository string
	// Owner names the app, service, or container carrying the root when
	// known; it is informational and never used for matching.
	Owner string
}

// Valid reports whether the root names a kind and a non-empty identity.
func (r ProtectionRoot) Valid() bool {
	return r.Kind != "" && r.Ref != ""
}

// ReasonForRootKind maps a protection root kind to the verdict reason it
// justifies. An unmapped kind protects nothing and is reported as absent.
func ReasonForRootKind(kind ProtectionRootKind) (PruneReason, bool) {
	reason, ok := protectionRootReason[kind]
	return reason, ok
}

var protectionRootReason = map[ProtectionRootKind]PruneReason{
	ProtectionLatest:             PruneReasonProtectedLatest,
	ProtectionRetentionWindow:    PruneReasonProtectedRetentionWindow,
	ProtectionDesiredRevision:    PruneReasonProtectedDesiredRevision,
	ProtectionActiveService:      PruneReasonProtectedActiveService,
	ProtectionRecoveryInhibition: PruneReasonProtectedRecoveryInhibition,
	ProtectionApplyIntent:        PruneReasonProtectedApplyIntent,
	ProtectionOperation:          PruneReasonProtectedOperation,
	ProtectionOwnership:          PruneReasonProtectedOwnership,
	ProtectionContainerUse:       PruneReasonProtectedContainerUse,
	ProtectionPendingUpload:      PruneReasonProtectedPendingUpload,
	ProtectionSharedContent:      PruneReasonProtectedSharedContent,
	ProtectionPinned:             PruneReasonProtectedPinned,
	ProtectionServiceManaged:     PruneReasonProtectedServiceManaged,
}

// VolumeClaimState is the durable lifecycle state of one app-owned volume.
type VolumeClaimState string

const (
	// VolumeClaimAttached means a live app generation still uses it.
	VolumeClaimAttached VolumeClaimState = "attached"
	// VolumeClaimRetained means the app was removed and the volume is
	// retained without an owner: never implicitly deletable.
	VolumeClaimRetained VolumeClaimState = "retained"
	// VolumeClaimReleased means the owning app explicitly released the
	// volume; only this state is ever eligible for pruning.
	VolumeClaimReleased VolumeClaimState = "released"
)

// Valid reports whether s is a defined claim state.
func (s VolumeClaimState) Valid() bool {
	switch s {
	case VolumeClaimAttached, VolumeClaimRetained, VolumeClaimReleased:
		return true
	default:
		return false
	}
}

// Protects reports whether the claim state forbids deletion. Unknown or
// empty states protect by default.
func (s VolumeClaimState) Protects() bool { return s != VolumeClaimReleased }

// ImageClaim is one durable app ownership claim on a runtime image. The
// claim is matched by exact identity (the app's pinned reference and/or
// manifest digest); image labels are never proof on their own.
type ImageClaim struct {
	// Reference is the app-pinned image reference (registry/repo:tag).
	Reference string
	// Digest is the pinned manifest digest, when known.
	Digest string
	// App is the owning app name, AppID its stable internal UUID.
	App     string
	AppID   string
	Service string
	State   VolumeClaimState
}

// Valid reports whether the claim is complete enough to act on.
func (c ImageClaim) Valid() bool {
	return (c.Reference != "" || c.Digest != "") && c.App != "" && c.State.Valid()
}

// Identities returns every identity the claim may be matched against,
// including the registry tag key of a tagged reference.
func (c ImageClaim) Identities() []string {
	var out []string
	if c.Reference != "" {
		out = append(out, c.Reference)
		if tagRef, ok := ParseRegistryTagRef(c.Reference); ok {
			out = append(out, tagRef.Key())
		}
	}
	if c.Digest != "" {
		out = append(out, c.Digest)
	}
	return out
}

// VolumeClaim is one durable app ownership claim on a runtime volume.
type VolumeClaim struct {
	// Name is the exact runtime volume name.
	Name string
	// App is the owning app name, AppID its stable internal UUID. They
	// are informational for matching and must never be inferred from
	// the volume name.
	App     string
	AppID   string
	Service string
	State   VolumeClaimState
}

// Valid reports whether the claim is complete enough to act on.
func (c VolumeClaim) Valid() bool {
	return c.Name != "" && c.App != "" && c.State.Valid()
}

// MatchesLabels reports whether the durable claim agrees with the
// runtime labels of its volume. Both sides must be present: a claim is
// never confirmed by absent labels, and labels never override a
// contradictory or missing durable record.
func (c VolumeClaim) MatchesLabels(labels map[string]string) bool {
	if len(labels) == 0 || !c.Valid() {
		return false
	}
	if labels[LabelManaged] != "true" {
		return false
	}
	claimed := labels[LabelApp]
	if claimed == "" {
		return false
	}
	if claimed != c.App && (c.AppID == "" || claimed != c.AppID) {
		return false
	}
	// A stamped incarnation UUID must agree with the durable record. An
	// absent stamped ID is not a match either: without it the claim
	// cannot prove which incarnation owns the volume, so a name-only
	// match must never authorize deletion.
	stampedID := labels[LabelAppID]
	if stampedID == "" || c.AppID == "" || stampedID != c.AppID {
		return false
	}
	if service := labels[LabelAppService]; service != "" && c.Service != "" && service != c.Service {
		return false
	}
	return true
}

// InventorySource identifies one fact source feeding prune planning.
type InventorySource string

const (
	// InventorySourceAppState is desired/active/intent state.
	InventorySourceAppState InventorySource = "app-state"
	// InventorySourceOwnership is durable ownership records.
	InventorySourceOwnership InventorySource = "ownership"
	// InventorySourceOperation is the operation journal.
	InventorySourceOperation InventorySource = "operation"
	// InventorySourceRegistry is registry repositories and tags.
	InventorySourceRegistry InventorySource = "registry"
	// InventorySourceManifests is manifest content and closure.
	InventorySourceManifests InventorySource = "manifests"
	// InventorySourceRuntimeContainers is runtime container usage.
	InventorySourceRuntimeContainers InventorySource = "runtime-containers"
	// InventorySourceRuntimeImages is runtime image inventory.
	InventorySourceRuntimeImages InventorySource = "runtime-images"
	// InventorySourceRuntimeVolumes is runtime volume inventory.
	InventorySourceRuntimeVolumes InventorySource = "runtime-volumes"
)

// InventoryGap records one fact that could not be established. A gap
// never blocks an operation by itself: it makes the dependent
// candidates unknown.
type InventoryGap struct {
	Source InventorySource
	Reason PruneReason
	Detail string
}

// Valid reports whether the gap carries a source and an unknown reason.
func (g InventoryGap) Valid() bool {
	return g.Source != "" && g.Reason.Valid() && g.Reason.Category() == PruneVerdictUnknown
}

// PruneProtectionSnapshot is one coherent read of every durable fact
// that protects prune candidates. Build it in a single read transaction
// so no candidate is judged against facts from a different moment.
// The zero value is a complete snapshot that protects nothing.
type PruneProtectionSnapshot struct {
	// Roots is every protection root. Refs are canonical identities.
	Roots []ProtectionRoot
	// VolumeClaims is every durable app volume claim, including
	// released ones.
	VolumeClaims []VolumeClaim
	// ImageClaims is every durable app image claim, including released
	// ones. Runtime image prune requires a matching released claim;
	// image labels alone never authorize deletion.
	ImageClaims []ImageClaim
	// Gaps records every fact that could not be read completely.
	Gaps []InventoryGap
}

// Complete reports whether the snapshot was read without gaps.
func (s *PruneProtectionSnapshot) Complete() bool {
	if s == nil {
		return false
	}
	for _, gap := range s.Gaps {
		if gap.Valid() {
			return false
		}
	}
	return true
}

// Protects reports whether any root of the given kind claims ref.
// Canonical identity strings are matched exactly: a non-canonical or
// empty ref never matches, so an unparseable candidate stays unknown
// rather than silently eligible.
func (s *PruneProtectionSnapshot) Protects(kind ProtectionRootKind, ref string) bool {
	if s == nil || ref == "" {
		return false
	}
	for _, root := range s.Roots {
		if root.Kind == kind && root.Ref == ref {
			return true
		}
	}
	return false
}

// ProtectsDigest reports whether any root of any kind claims the digest.
// Digest identities are shared across registry and runtime namespaces,
// so protection is namespace-independent.
func (s *PruneProtectionSnapshot) ProtectsDigest(digest string) bool {
	if s == nil || !ValidOCIDigest(digest) {
		return false
	}
	for _, root := range s.Roots {
		if root.Ref == digest || strings.HasSuffix(root.Ref, "@"+digest) {
			return true
		}
	}
	return false
}

// ProtectsRuntimeImage reports whether any root claims the image ID or
// any of its repo digests.
func (s *PruneProtectionSnapshot) ProtectsRuntimeImage(imageID string, repoDigests []string) bool {
	if s == nil {
		return false
	}
	if imageID != "" && s.Protects(ProtectionContainerUse, imageID) {
		return true
	}
	if s.ProtectsAnyRef(imageID) {
		return true
	}
	for _, digest := range repoDigests {
		if s.ProtectsAnyRef(digest) {
			return true
		}
	}
	return false
}

// ProtectsAnyRef reports whether any root of any kind claims ref.
func (s *PruneProtectionSnapshot) ProtectsAnyRef(ref string) bool {
	if s == nil || ref == "" {
		return false
	}
	for _, root := range s.Roots {
		if root.Ref == ref || strings.HasSuffix(root.Ref, "@"+ref) {
			return true
		}
	}
	return false
}

// ImageClaimsFor returns every claim whose identities include any of the
// given identities, in record order.
func (s *PruneProtectionSnapshot) ImageClaimsFor(identities ...string) []ImageClaim {
	if s == nil || len(identities) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if identity != "" {
			wanted[identity] = struct{}{}
		}
	}
	var out []ImageClaim
	for _, claim := range s.ImageClaims {
		for _, identity := range claim.Identities() {
			if _, ok := wanted[identity]; ok {
				out = append(out, claim)
				break
			}
		}
	}
	return out
}

// ImageClaimFor returns the last recorded claim matching any identity.
func (s *PruneProtectionSnapshot) ImageClaimFor(identities ...string) (ImageClaim, bool) {
	claims := s.ImageClaimsFor(identities...)
	if len(claims) == 0 {
		return ImageClaim{}, false
	}
	return claims[len(claims)-1], true
}

// ImageProtects reports whether any matching claim forbids deletion.
func (s *PruneProtectionSnapshot) ImageProtects(identities ...string) bool {
	for _, claim := range s.ImageClaimsFor(identities...) {
		if claim.State.Protects() {
			return true
		}
	}
	return false
}

// VolumeClaimFor returns the last recorded claim for name.
func (s *PruneProtectionSnapshot) VolumeClaimFor(name string) (VolumeClaim, bool) {
	if s == nil || name == "" {
		return VolumeClaim{}, false
	}
	for index := len(s.VolumeClaims) - 1; index >= 0; index-- {
		if s.VolumeClaims[index].Name == name {
			return s.VolumeClaims[index], true
		}
	}
	return VolumeClaim{}, false
}

// VolumeClaimsFor returns every claim recorded for name, in record
// order. A volume may be claimed by more than one app incarnation when
// a name is reused, and every claim must be considered.
func (s *PruneProtectionSnapshot) VolumeClaimsFor(name string) []VolumeClaim {
	if s == nil || name == "" {
		return nil
	}
	var claims []VolumeClaim
	for _, claim := range s.VolumeClaims {
		if claim.Name == name {
			claims = append(claims, claim)
		}
	}
	return claims
}

// VolumeProtects reports whether any durable claim on name forbids
// deletion. Any protecting claim wins: a new incarnation attaching a
// volume that an older incarnation released must still protect it.
func (s *PruneProtectionSnapshot) VolumeProtects(name string) bool {
	for _, claim := range s.VolumeClaimsFor(name) {
		if claim.State.Protects() {
			return true
		}
	}
	return false
}

// ProtectsTagRef reports whether any root claims the tag identity.
func (s *PruneProtectionSnapshot) ProtectsTagRef(ref RegistryTagRef) bool {
	if s == nil || !ref.Valid() {
		return false
	}
	return s.ProtectsAnyRef(ref.Key()) || s.ProtectsAnyRef(ref.String())
}

// RootReasons returns the canonical, deduplicated verdict reasons of
// every root claiming any of refs. An unmapped root kind contributes
// nothing: it retains content for consumers that match it directly but
// cannot justify a verdict reason on its own.
func (s *PruneProtectionSnapshot) RootReasons(refs ...string) []PruneReason {
	if s == nil {
		return nil
	}
	set := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref != "" {
			set[ref] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	var reasons []PruneReason
	for _, root := range s.Roots {
		if _, ok := set[root.Ref]; !ok {
			continue
		}
		reason, mapped := protectionRootReason[root.Kind]
		if !mapped {
			continue
		}
		reasons = append(reasons, reason)
	}
	return CanonicalReasons(reasons...)
}

// HasGapFor reports whether any recorded gap came from source. A nil
// snapshot reports a gap: it proves nothing.
func (s *PruneProtectionSnapshot) HasGapFor(source InventorySource) bool {
	if s == nil {
		return true
	}
	for _, gap := range s.Gaps {
		if gap.Source == source && gap.Valid() {
			return true
		}
	}
	return false
}

// RefsOfKind returns every distinct root identity of one kind, in
// first-seen order.
func (s *PruneProtectionSnapshot) RefsOfKind(kind ProtectionRootKind) []string {
	if s == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var refs []string
	for _, root := range s.Roots {
		if root.Kind != kind || root.Ref == "" {
			continue
		}
		if _, dup := seen[root.Ref]; dup {
			continue
		}
		seen[root.Ref] = struct{}{}
		refs = append(refs, root.Ref)
	}
	return refs
}

// WithRoots returns a copy of the snapshot with extra roots appended.
// It is how one planning stage layers facts it derived itself (for
// example the OCI closure of already-protected manifests) onto the
// durable snapshot without mutating it.
func (s *PruneProtectionSnapshot) WithRoots(roots ...ProtectionRoot) *PruneProtectionSnapshot {
	if len(roots) == 0 {
		return s
	}
	clone := &PruneProtectionSnapshot{Roots: make([]ProtectionRoot, 0, len(roots))}
	if s != nil {
		clone.Roots = make([]ProtectionRoot, 0, len(s.Roots)+len(roots))
		clone.Roots = append(clone.Roots, s.Roots...)
		clone.VolumeClaims = append([]VolumeClaim(nil), s.VolumeClaims...)
		clone.ImageClaims = append([]ImageClaim(nil), s.ImageClaims...)
		clone.Gaps = append([]InventoryGap(nil), s.Gaps...)
	}
	for _, root := range roots {
		if root.Valid() {
			clone.Roots = append(clone.Roots, root)
		}
	}
	return clone
}

// ImageRefRoots returns the protection roots that retain one image
// reference. A tag reference yields a tag-keyed root; a
// digest-qualified reference yields both the tag root (when it has a
// tag) and a digest root, so a candidate matching either identity stays
// protected. An unparseable reference yields no root: it protects
// nothing here and is left to the caller to fail closed on.
func ImageRefRoots(kind ProtectionRootKind, imageRef, owner string) []ProtectionRoot {
	ref := strings.TrimSpace(imageRef)
	if ref == "" {
		return nil
	}

	var roots []ProtectionRoot
	name, digest := splitImageDigest(ref)
	if digest != "" {
		roots = append(roots, ProtectionRoot{Kind: kind, Ref: digest, Repository: ImageRefRepository(name), Owner: owner})
	}
	if name != "" {
		if repository, tag, ok := splitImageTag(name); ok {
			tagRef := RegistryTagRef{Repository: repository, Tag: tag}
			if tagRef.Valid() {
				roots = append(roots, ProtectionRoot{Kind: kind, Ref: tagRef.Key(), Owner: owner})
			}
		} else if ValidOCIDigest(name) {
			roots = append(roots, ProtectionRoot{Kind: kind, Ref: name, Owner: owner})
		}
	}
	return roots
}

// ImageRefRepository returns the registry-relative repository of an image
// reference: registry host, tag, and digest are stripped. It reports ""
// when no repository can be derived.
func ImageRefRepository(ref string) string {
	name, _ := splitImageDigest(ref)
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if repository, _, ok := splitImageTag(name); ok {
		name = repository
	}
	// Strip a registry host following Docker's rule: the first path
	// component is a registry only when it contains a dot or colon, or
	// is localhost.
	if first, rest, ok := strings.Cut(name, "/"); ok {
		if strings.ContainsAny(first, ".:") || strings.EqualFold(first, "localhost") {
			name = rest
		}
	}
	return name
}

// splitImageDigest splits "name@digest" into its parts. A reference
// without an @ keeps its whole value as the name.
func splitImageDigest(ref string) (name, digest string) {
	at := strings.Index(ref, "@")
	if at < 0 {
		return ref, ""
	}
	digest = ref[at+1:]
	if !ValidOCIDigest(digest) {
		return ref, ""
	}
	return ref[:at], digest
}

// splitImageTag splits "registry/repo:tag" into repository and tag. A
// reference without a tag suffix reports ok=false.
func splitImageTag(ref string) (repository, tag string, ok bool) {
	colon := strings.LastIndex(ref, ":")
	slash := strings.LastIndex(ref, "/")
	if colon <= slash || colon >= len(ref)-1 {
		return "", "", false
	}
	return ref[:colon], ref[colon+1:], true
}

// ParseRegistryTagRef parses a runtime repository tag such as
// "registry.example/shop:1" into a registry tag identity. A reference
// carrying no usable tag reports ok=false.
func ParseRegistryTagRef(repoTag string) (RegistryTagRef, bool) {
	repo, tag, ok := splitImageTag(strings.TrimSpace(repoTag))
	if !ok {
		return RegistryTagRef{}, false
	}
	ref := RegistryTagRef{Repository: repo, Tag: tag}
	if !ref.Valid() {
		return RegistryTagRef{}, false
	}
	return ref, true
}

// RegistryTagVerdict is the decision for one registry tag.
type RegistryTagVerdict struct {
	Ref RegistryTagRef
	// Digest is the manifest digest the tag resolved to, when known.
	Digest  string
	Verdict PruneVerdict
	Reasons []PruneReason
}

// OCIBlobVerdict is the decision for digest-addressed registry content.
type OCIBlobVerdict struct {
	Ref     OCIRef
	Verdict PruneVerdict
	Reasons []PruneReason
}

// RuntimeImageVerdict is the decision for one runtime image.
type RuntimeImageVerdict struct {
	Ref     RuntimeImageRef
	Verdict PruneVerdict
	Reasons []PruneReason
}

// VolumeVerdict is the decision for one runtime volume.
type VolumeVerdict struct {
	Ref     RuntimeVolumeRef
	Verdict PruneVerdict
	Reasons []PruneReason
}

// PrunePlan is the complete, validated decision set of one prune run.
// Execution deletes exactly the eligible entries: no stage may delete
// before the whole plan is computed and validated.
type PrunePlan struct {
	Tags    []RegistryTagVerdict
	Blobs   []OCIBlobVerdict
	Images  []RuntimeImageVerdict
	Volumes []VolumeVerdict
	Gaps    []InventoryGap
}

// EligibleTags returns the registry tags safe to delete, in plan order.
func (p *PrunePlan) EligibleTags() []RegistryTagRef {
	out := make([]RegistryTagRef, 0, len(p.Tags))
	for _, verdict := range p.Tags {
		if verdict.Verdict == PruneVerdictEligible {
			out = append(out, verdict.Ref)
		}
	}
	return out
}

// EligibleBlobs returns the OCI content safe to delete, in plan order.
func (p *PrunePlan) EligibleBlobs() []OCIRef {
	out := make([]OCIRef, 0, len(p.Blobs))
	for _, verdict := range p.Blobs {
		if verdict.Verdict == PruneVerdictEligible {
			out = append(out, verdict.Ref)
		}
	}
	return out
}

// EligibleImages returns the runtime images safe to delete, in plan order.
func (p *PrunePlan) EligibleImages() []RuntimeImageRef {
	out := make([]RuntimeImageRef, 0, len(p.Images))
	for _, verdict := range p.Images {
		if verdict.Verdict == PruneVerdictEligible {
			out = append(out, verdict.Ref)
		}
	}
	return out
}

// EligibleVolumes returns the runtime volumes safe to delete, in plan order.
func (p *PrunePlan) EligibleVolumes() []RuntimeVolumeRef {
	out := make([]RuntimeVolumeRef, 0, len(p.Volumes))
	for _, verdict := range p.Volumes {
		if verdict.Verdict == PruneVerdictEligible {
			out = append(out, verdict.Ref)
		}
	}
	return out
}

// CountByVerdict counts entries of one verdict across every resource kind.
func (p *PrunePlan) CountByVerdict(verdict PruneVerdict) int {
	count := 0
	for _, item := range p.Tags {
		if item.Verdict == verdict {
			count++
		}
	}
	for _, item := range p.Blobs {
		if item.Verdict == verdict {
			count++
		}
	}
	for _, item := range p.Images {
		if item.Verdict == verdict {
			count++
		}
	}
	for _, item := range p.Volumes {
		if item.Verdict == verdict {
			count++
		}
	}
	return count
}

// Validate returns the first structural inconsistency of the
// plan. It never re-derives safety: it proves the plan is well formed
// enough to execute, so a malformed plan can never delete anything.
func (p *PrunePlan) Validate() error {
	for _, item := range p.Tags {
		if !item.Ref.Valid() {
			return fmt.Errorf("registry tag verdict has invalid identity %q", item.Ref.String())
		}
		if err := validateVerdictReasons(item.Verdict, item.Reasons); err != nil {
			return fmt.Errorf("registry tag %q: %w", item.Ref.String(), err)
		}
	}
	for _, item := range p.Blobs {
		if err := validateOCIVerdict(item.Ref, item.Verdict, item.Reasons); err != nil {
			return err
		}
	}
	for _, item := range p.Images {
		if err := validateRuntimeImageVerdict(item.Ref, item.Verdict, item.Reasons); err != nil {
			return err
		}
	}
	for _, item := range p.Volumes {
		if err := validateVolumeVerdict(item.Ref, item.Verdict, item.Reasons); err != nil {
			return err
		}
	}
	for _, gap := range p.Gaps {
		if !gap.Valid() {
			return fmt.Errorf("inventory gap has invalid source or reason %q/%q", gap.Source, gap.Reason)
		}
	}
	return nil
}

// validateOCIVerdict proves one blob verdict is well formed. An eligible
// entry must name a valid digest, so an executable plan can never carry
// a deletion target the adapter would reject.
func validateOCIVerdict(ref OCIRef, verdict PruneVerdict, reasons []PruneReason) error {
	if verdict == PruneVerdictEligible && !ref.Valid() {
		return fmt.Errorf("OCI blob verdict has invalid identity %q", ref.String())
	}
	if err := validateVerdictReasons(verdict, reasons); err != nil {
		return fmt.Errorf("OCI blob %q: %w", ref.String(), err)
	}
	return nil
}

// validateRuntimeImageVerdict proves one runtime image verdict is well formed.
func validateRuntimeImageVerdict(ref RuntimeImageRef, verdict PruneVerdict, reasons []PruneReason) error {
	if verdict == PruneVerdictEligible && !ref.Valid() {
		return fmt.Errorf("runtime image verdict has invalid identity %q", ref.String())
	}
	if err := validateVerdictReasons(verdict, reasons); err != nil {
		return fmt.Errorf("runtime image %q: %w", ref.String(), err)
	}
	return nil
}

// validateVolumeVerdict proves one volume verdict is well formed.
func validateVolumeVerdict(ref RuntimeVolumeRef, verdict PruneVerdict, reasons []PruneReason) error {
	if verdict == PruneVerdictEligible && !ref.Valid() {
		return fmt.Errorf("volume verdict has invalid identity %q", ref.String())
	}
	if err := validateVerdictReasons(verdict, reasons); err != nil {
		return fmt.Errorf("volume %q: %w", ref.String(), err)
	}
	return nil
}

// Candidates returns every planned candidate with its verdict, in a
// deterministic order: tags, blobs, runtime images, then volumes.
func (p *PrunePlan) Candidates() []PruneCandidateReport {
	total := len(p.Tags) + len(p.Blobs) + len(p.Images) + len(p.Volumes)
	out := make([]PruneCandidateReport, 0, total)
	for _, item := range p.Tags {
		out = append(out, PruneCandidateReport{
			Kind: PruneResourceRegistryTag, Ref: item.Ref.String(), Verdict: item.Verdict, Reasons: item.Reasons,
		})
	}
	for _, item := range p.Blobs {
		out = append(out, PruneCandidateReport{
			Kind: PruneResourceOCIBlob, Ref: item.Ref.String(), Verdict: item.Verdict, Reasons: item.Reasons,
		})
	}
	for _, item := range p.Images {
		out = append(out, PruneCandidateReport{
			Kind: PruneResourceRuntimeImage, Ref: item.Ref.String(), Verdict: item.Verdict, Reasons: item.Reasons,
		})
	}
	for _, item := range p.Volumes {
		out = append(out, PruneCandidateReport{
			Kind: PruneResourceVolume, Ref: item.Ref.String(), Verdict: item.Verdict, Reasons: item.Reasons,
		})
	}
	return out
}

// PruneCandidateReport is one candidate's verdict in a prune report.
type PruneCandidateReport struct {
	Kind    PruneResourceKind
	Ref     string
	Verdict PruneVerdict
	Reasons []PruneReason
}

// PruneFailure records one exact deletion that failed. A failure never
// aborts the run and never aborts the other candidates.
type PruneFailure struct {
	Kind PruneResourceKind
	Ref  string
	Err  string
}

// PruneReport is the operator-facing result of one prune run. Dry-run
// and execution share this shape; Applied distinguishes them.
type PruneReport struct {
	// Applied is false for a dry run, where nothing was deleted.
	Applied bool
	// Candidates lists every planned candidate with its verdict, so an
	// operator can see why each resource was kept or skipped.
	Candidates []PruneCandidateReport
	// Gaps lists every inventory that could not be read in full.
	Gaps []InventoryGap
	// Deleted lists exactly the identities that were removed.
	Deleted []PruneCandidateReport
	// Failures lists exact deletions that failed without aborting the run.
	Failures []PruneFailure
	// ReclaimedBytes and ReclaimedKnown are set only when the adapter
	// can attribute exact bytes to the deletions it performed.
	ReclaimedBytes int64
	ReclaimedKnown bool
}

// CountByVerdict counts every planned candidate with one verdict.
func (r *PruneReport) CountByVerdict(verdict PruneVerdict) int {
	count := 0
	for _, candidate := range r.Candidates {
		if candidate.Verdict == verdict {
			count++
		}
	}
	return count
}

// CandidatesOfKind returns the planned candidates of one resource kind
// with one verdict, in report order.
func (r *PruneReport) CandidatesOfKind(kind PruneResourceKind, verdict PruneVerdict) []PruneCandidateReport {
	var out []PruneCandidateReport
	for _, candidate := range r.Candidates {
		if candidate.Kind == kind && candidate.Verdict == verdict {
			out = append(out, candidate)
		}
	}
	return out
}

// NewPruneReport builds the shared report shape from a validated plan.
// applied reports whether the caller went on to execute it.
func NewPruneReport(plan *PrunePlan, applied bool) *PruneReport {
	report := &PruneReport{Applied: applied}
	if plan == nil {
		return report
	}
	report.Candidates = plan.Candidates()
	report.Gaps = plan.Gaps
	return report
}

// validateVerdictReasons proves a verdict is well formed: it is defined,
// it carries at least one reason, and every reason justifies it.
func validateVerdictReasons(verdict PruneVerdict, reasons []PruneReason) error {
	if !verdict.Valid() {
		return fmt.Errorf("undefined verdict %q", verdict)
	}
	if len(reasons) == 0 {
		return fmt.Errorf("verdict %q carries no reason", verdict)
	}
	for _, reason := range reasons {
		if !reason.Valid() {
			return fmt.Errorf("undefined reason %q", reason)
		}
		if reason.Category() != verdict {
			return fmt.Errorf("reason %q does not justify verdict %q", reason, verdict)
		}
	}
	return nil
}

// RuntimeImage is one runtime image as the runtime reports it.
type RuntimeImage struct {
	// ID is the exact runtime image identifier.
	ID string
	// RepoTags are the repository tags pointing at this image.
	RepoTags []string
	// RepoDigests are the manifest digests the runtime knows for it.
	RepoDigests []string
	Labels      map[string]string
	// Size is reported in bytes.
	Size    int64
	Created time.Time
}

// RuntimeContainerUse is one container's image usage.
type RuntimeContainerUse struct {
	ContainerID string
	// ImageID is the resolved image identity; empty when unknown.
	ImageID string
	// ImageRef is the reference the container was created from.
	ImageRef string
	Running  bool
	// ImageUnknown marks a container whose image could not be resolved.
	// It protects every image candidate whose safety depends on it.
	ImageUnknown bool
}

// RuntimeInventory is one coherent read of runtime resources. It is
// what the planner consumes; adapters build it without inspecting
// each other.
type RuntimeInventory struct {
	Images     []RuntimeImage
	Containers []RuntimeContainerUse
	// Volumes are the runtime's named volumes with labels and usage.
	Volumes []*VolumeInfo
	Gaps    []InventoryGap
}

// Complete reports whether the inventory was read without gaps.
func (i *RuntimeInventory) Complete() bool {
	if i == nil {
		return false
	}
	for _, gap := range i.Gaps {
		if gap.Valid() {
			return false
		}
	}
	return true
}
