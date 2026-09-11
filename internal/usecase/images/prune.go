package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/pruneguard"
	"github.com/bnema/gordon/internal/usecase/registrystate"
)

// Supported OCI/Docker manifest media types. Anything else makes the
// content behind it unknown rather than unreferenced.
const (
	mediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaTypeOCIManifest        = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIImageIndex      = "application/vnd.oci.image.index.v1+json"
)

// WithPrunePorts wires the prune ports: the coherent protection store,
// the runtime inventory/deletion port, and the GC barrier. All three
// are required for prune; a missing port fails closed.
func (s *Service) WithPrunePorts(protection out.PruneProtectionStore, pruneRuntime out.PruneRuntime, barrier out.GCBarrier) *Service {
	s.protection = protection
	s.pruneRuntime = pruneRuntime
	s.barrier = barrier
	return s
}

// acquireExclusive takes the GC exclusive lease for the whole prune run,
// or a no-op lease when no barrier is wired.
func (s *Service) acquireExclusive(ctx context.Context) (out.GCLease, error) {
	if s.barrier == nil {
		return noopGCLease{}, nil
	}
	return s.barrier.AcquireExclusive(ctx)
}

// noopGCLease is the lease used when no barrier is wired.
type noopGCLease struct{}

func (noopGCLease) Release() {}

// Prune plans and (unless DryRun) executes one image prune run.
//
// The whole plan is computed and validated before anything is deleted.
// A valid plan with zero eligible resources succeeds: protected and
// unknown candidates are normal outcomes, never an operation-wide
// refusal.
func (s *Service) Prune(ctx context.Context, opts domain.ImagePruneOptions) (domain.ImagePruneReport, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Prune",
		"keepLast":            opts.KeepLast,
		"pruneDangling":       opts.PruneDangling,
		"pruneRegistry":       opts.PruneRegistry,
		"dryRun":              opts.DryRun,
	})
	log := zerowrap.FromCtx(ctx)

	lease, err := s.acquireExclusive(ctx)
	if err != nil {
		return domain.ImagePruneReport{}, fmt.Errorf("images: acquire prune lease: %w", err)
	}
	defer lease.Release()

	if s.protection == nil {
		return domain.ImagePruneReport{}, fmt.Errorf("images: prune requires a protection store: %w", domain.ErrPruneDisabled)
	}

	snapshot, err := s.protection.ProtectionSnapshot(ctx)
	if err != nil {
		return domain.ImagePruneReport{}, fmt.Errorf("images: read protection snapshot: %w", err)
	}

	plan := &domain.PrunePlan{Gaps: append([]domain.InventoryGap(nil), snapshot.Gaps...)}

	if opts.PruneDangling {
		verdicts, gaps, err := s.planRuntimeImages(ctx, snapshot)
		if err != nil {
			return domain.ImagePruneReport{}, err
		}
		plan.Images = verdicts
		plan.Gaps = append(plan.Gaps, gaps...)
	}

	// Registry planning and deletion share one registry mutation lock,
	// so a concurrent push can never make content look unreferenced
	// between the plan and the deletion it authorizes. Runtime deletion
	// runs outside that lock: it does not touch registry storage, and
	// holding the lock across it would stall every registry push.
	if !opts.PruneRegistry {
		return s.finishPlan(ctx, plan, opts.DryRun, log)
	}

	s.mutationMu.Lock()
	tags, blobs, gaps := s.planRegistry(snapshot, opts.KeepLast)
	plan.Tags = tags
	plan.Blobs = blobs
	plan.Gaps = append(plan.Gaps, gaps...)

	if err := plan.Validate(); err != nil {
		s.mutationMu.Unlock()
		return domain.ImagePruneReport{}, fmt.Errorf("images: refusing to execute an invalid prune plan: %w", err)
	}
	report := domain.ImagePruneReport{Plan: *domain.NewPruneReport(plan, !opts.DryRun)}
	if opts.DryRun {
		s.mutationMu.Unlock()
		return report, nil
	}
	if err := s.executeRegistry(ctx, plan, &report, log); err != nil {
		s.mutationMu.Unlock()
		return report, err
	}
	s.mutationMu.Unlock()

	s.executeRuntimeImages(ctx, plan, &report, log)
	return report, nil
}

// finishPlan validates the complete plan and executes the runtime part.
// Every deletion happens after the whole plan proved itself well formed.
func (s *Service) finishPlan(ctx context.Context, plan *domain.PrunePlan, dryRun bool, log zerowrap.Logger) (domain.ImagePruneReport, error) {
	if err := plan.Validate(); err != nil {
		return domain.ImagePruneReport{}, fmt.Errorf("images: refusing to execute an invalid prune plan: %w", err)
	}
	report := domain.ImagePruneReport{Plan: *domain.NewPruneReport(plan, !dryRun)}
	if dryRun {
		return report, nil
	}
	s.executeRuntimeImages(ctx, plan, &report, log)
	return report, nil
}

// planRuntimeImages plans runtime image deletion from one coherent
// inventory. An unwired or unreadable runtime inventory yields gaps
// instead of an empty (apparently safe) runtime.
func (s *Service) planRuntimeImages(ctx context.Context, snapshot *domain.PruneProtectionSnapshot) ([]domain.RuntimeImageVerdict, []domain.InventoryGap, error) {
	if s.pruneRuntime == nil {
		return nil, []domain.InventoryGap{{
			Source: domain.InventorySourceRuntimeImages,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "runtime prune port is not wired",
		}}, nil
	}
	inventory, err := s.pruneRuntime.InventoryRuntime(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("images: read runtime inventory: %w", err)
	}
	verdicts := pruneguard.PlanRuntimeImages(pruneguard.RuntimePlanInput{
		Images:                inventory.Images,
		Containers:            inventory.Containers,
		Snapshot:              snapshot,
		ProtectedContainerIDs: snapshot.RefsOfKind(domain.ProtectionRecoveryInhibition),
		Complete:              inventory.Complete(),
	})
	return verdicts, inventory.Gaps, nil
}

// executeRuntimeImages deletes exactly the planned runtime image IDs.
// A single deletion failure is isolated: it is reported and the run
// continues with the remaining candidates.
func (s *Service) executeRuntimeImages(ctx context.Context, plan *domain.PrunePlan, report *domain.ImagePruneReport, log zerowrap.Logger) {
	for _, ref := range plan.EligibleImages() {
		if err := s.pruneRuntime.RemoveImageExact(ctx, ref); err != nil {
			log.Warn().Err(err).Str("image", ref.ID).Msg("runtime image prune failed; skipping")
			report.Plan.Failures = append(report.Plan.Failures, domain.PruneFailure{
				Kind: domain.PruneResourceRuntimeImage, Ref: ref.ID, Err: err.Error(),
			})
			continue
		}
		report.Plan.Deleted = append(report.Plan.Deleted, domain.PruneCandidateReport{
			Kind: domain.PruneResourceRuntimeImage, Ref: ref.ID, Verdict: domain.PruneVerdictEligible,
		})
		report.Runtime.DeletedCount++
	}
}

// planRegistry plans tag retention and blob garbage collection from the
// registry inventory. It never deletes: the caller executes only after
// the complete plan validates.
//
// The returned gaps record every fact that could not be read. A tag
// whose manifest is unreadable stays unknown, and a blob whose
// reference set cannot be fully proven stays unknown too.
func (s *Service) planRegistry(snapshot *domain.PruneProtectionSnapshot, keepLast int) ([]domain.RegistryTagVerdict, []domain.OCIBlobVerdict, []domain.InventoryGap) {
	if keepLast <= 0 {
		// Documented behavior: keep_last=0 skips registry tag and blob
		// cleanup entirely.
		return nil, nil, nil
	}

	repositories, err := s.manifestStorage.ListRepositories()
	if err != nil {
		return nil, nil, []domain.InventoryGap{{
			Source: domain.InventorySourceRegistry,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "list repositories failed",
		}}
	}
	sort.Strings(repositories)

	var tags []pruneguard.RegistryTagCandidate
	var gaps []domain.InventoryGap
	for _, repository := range repositories {
		repositoryTags, repositoryGaps := s.inventoryRepository(repository)
		tags = append(tags, repositoryTags...)
		gaps = append(gaps, repositoryGaps...)
	}

	complete := len(gaps) == 0
	verdicts := pruneguard.PlanRegistryTags(pruneguard.RegistryPlanInput{
		Tags:     tags,
		Snapshot: snapshot,
		KeepLast: keepLast,
		Complete: complete,
	})

	blobs, blobGaps := s.planRegistryBlobs(snapshot, verdicts, tags, complete, repositories)
	gaps = append(gaps, blobGaps...)
	return verdicts, blobs, gaps
}

// inventoryRepository inventories one repository's tags and their
// manifests. A tag whose manifest cannot be read is kept as a candidate
// with ManifestKnown=false, so the planner fails closed on it.
func (s *Service) inventoryRepository(repository string) ([]pruneguard.RegistryTagCandidate, []domain.InventoryGap) {
	tagNames, err := s.manifestStorage.ListTags(repository)
	if err != nil {
		return nil, []domain.InventoryGap{{
			Source: domain.InventorySourceRegistry,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "list tags failed for " + repository,
		}}
	}

	sort.Strings(tagNames)
	candidates := make([]pruneguard.RegistryTagCandidate, 0, len(tagNames))
	var gaps []domain.InventoryGap
	for _, tagName := range tagNames {
		ref := domain.RegistryTagRef{Repository: repository, Tag: tagName}
		if !ref.Valid() {
			// A tag we cannot name is a tag whose content we cannot
			// prove unreferenced, so the whole registry plan fails closed.
			gaps = append(gaps, domain.InventoryGap{
				Source: domain.InventorySourceRegistry,
				Reason: domain.PruneReasonUnknownIdentity,
				Detail: "unnamed or malformed tag in " + repository,
			})
			continue
		}
		candidate := pruneguard.RegistryTagCandidate{Ref: ref}
		modTime, err := s.manifestStorage.GetManifestModTime(repository, tagName)
		if err != nil {
			gaps = append(gaps, domain.InventoryGap{
				Source: domain.InventorySourceRegistry,
				Reason: domain.PruneReasonUnknownInventory,
				Detail: "manifest modification time unreadable for " + ref.String(),
			})
		}
		candidate.ModTime = modTime

		data, _, err := s.manifestStorage.GetManifest(repository, tagName)
		if err != nil {
			gaps = append(gaps, domain.InventoryGap{
				Source: domain.InventorySourceManifests,
				Reason: domain.PruneReasonUnknownManifest,
				Detail: "manifest unreadable for " + ref.String(),
			})
			candidates = append(candidates, candidate)
			continue
		}
		digest := digestOfManifest(data)
		_, mediaType, err := parseManifestDocument(data)
		if err != nil {
			gaps = append(gaps, domain.InventoryGap{
				Source: domain.InventorySourceManifests,
				Reason: domain.PruneReasonUnknownManifest,
				Detail: "manifest corrupt for " + ref.String(),
			})
			candidates = append(candidates, candidate)
			continue
		}
		if !supportedManifestMediaType(mediaType) {
			gaps = append(gaps, domain.InventoryGap{
				Source: domain.InventorySourceManifests,
				Reason: domain.PruneReasonUnknownMediaType,
				Detail: "unsupported manifest media type for " + ref.String(),
			})
			candidates = append(candidates, candidate)
			continue
		}
		candidate.Digest = digest
		candidate.ManifestKnown = true
		candidates = append(candidates, candidate)
	}
	return candidates, gaps
}

// planRegistryBlobs computes one verdict per stored blob.
//
// Blobs reachable from the retained (protected or kept) manifest set
// are protected. When any retained closure could not be read or parsed
// completely, unreferenced blobs become unknown instead of eligible: an
// incomplete reference set is never proof of unreachability.
func (s *Service) planRegistryBlobs(
	snapshot *domain.PruneProtectionSnapshot,
	verdicts []domain.RegistryTagVerdict,
	tags []pruneguard.RegistryTagCandidate,
	inventoryComplete bool,
	repositories []string,
) ([]domain.OCIBlobVerdict, []domain.InventoryGap) {
	now := time.Now().UTC()

	// Roots are every tag the plan does not delete: retained tags and
	// any tag whose manifest could not be read at all (which has no
	// verdict entry yet still must keep protecting its content).
	planned := make(map[string]struct{}, len(verdicts))
	var roots []retainedRoot
	for _, verdict := range verdicts {
		planned[verdict.Ref.Key()] = struct{}{}
		if verdict.Verdict == domain.PruneVerdictEligible {
			continue
		}
		roots = append(roots, retainedRoot{Repository: verdict.Ref.Repository, Reference: verdict.Ref.Tag})
	}
	for _, tag := range tags {
		if _, hasVerdict := planned[tag.Ref.Key()]; hasVerdict {
			continue
		}
		roots = append(roots, retainedRoot{Repository: tag.Ref.Repository, Reference: tag.Ref.Tag})
	}

	closure := newClosure()
	// A tag the inventory could not read may reference any blob, so an
	// incomplete tag inventory makes the whole reference set unprovable.
	closure.complete = inventoryComplete
	for _, root := range roots {
		s.traverseClosure(root, closure)
	}
	// Durable roots qualified by repository and digest keep the FULL OCI
	// closure of their manifest: a moved tag must not let prune delete the
	// config or layers of a digest an active deployment still pins. Roots
	// whose repository has no local content are skipped: the image was
	// never pushed here, so there is no local closure to preserve. An
	// unresolved root in a local repository marks the closure incomplete,
	// so unreferenced blobs stay unknown instead of eligible.
	localRepositories := make(map[string]struct{}, len(repositories))
	for _, repository := range repositories {
		localRepositories[repository] = struct{}{}
	}
	for _, root := range snapshot.Roots {
		if root.Repository == "" || !domain.ValidOCIDigest(root.Ref) {
			continue
		}
		if _, local := localRepositories[root.Repository]; !local {
			continue
		}
		s.traverseClosure(retainedRoot{Repository: root.Repository, Reference: root.Ref}, closure)
	}

	// Durable roots claim digests directly as well as through tag
	// closure: an app service, apply intent, or unfinished operation
	// can pin content by digest alone. Those digests must never be
	// judged unreferenced just because no retained tag reaches them.
	snapshotComplete := snapshot.Complete()

	pending := s.registryState.PendingDigests(now)

	blobs, err := s.blobStorage.ListBlobs()
	if err != nil {
		return nil, append(closure.gaps, domain.InventoryGap{
			Source: domain.InventorySourceRegistry,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "list blobs failed",
		})
	}
	sort.Strings(blobs)

	gaps := closure.gaps
	decisions := blobDecision{
		snapshot:         snapshot,
		closure:          closure,
		pending:          pending,
		now:              now,
		snapshotComplete: snapshotComplete,
	}
	out := make([]domain.OCIBlobVerdict, 0, len(blobs))
	for _, digest := range blobs {
		out = append(out, s.decideBlob(digest, decisions))
	}
	return out, gaps
}

// blobDecision carries the shared inputs of one blob verdict.
type blobDecision struct {
	snapshot         *domain.PruneProtectionSnapshot
	closure          *closure
	pending          map[string]struct{}
	now              time.Time
	snapshotComplete bool
}

// decideBlob resolves one stored blob, strongest protection first.
func (s *Service) decideBlob(digest string, in blobDecision) domain.OCIBlobVerdict {
	verdict := domain.OCIBlobVerdict{Ref: domain.OCIRef{Digest: digest}}
	protect := func(reason domain.PruneReason) domain.OCIBlobVerdict {
		verdict.Verdict = domain.PruneVerdictProtected
		verdict.Reasons = domain.CanonicalReasons(reason)
		return verdict
	}
	unknown := func(reason domain.PruneReason) domain.OCIBlobVerdict {
		verdict.Verdict = domain.PruneVerdictUnknown
		verdict.Reasons = domain.CanonicalReasons(reason)
		return verdict
	}

	if !verdict.Ref.Valid() {
		return unknown(domain.PruneReasonUnknownIdentity)
	}
	// Durable state can claim a digest directly, without any retained
	// tag reaching it.
	if matched, reasons := rootReasonsForDigest(in.snapshot, digest); matched {
		verdict.Verdict = domain.PruneVerdictProtected
		verdict.Reasons = reasons
		return verdict
	}
	if in.closure.references(digest) {
		return protect(domain.PruneReasonProtectedSharedContent)
	}
	if _, isPending := in.pending[digest]; isPending {
		return protect(domain.PruneReasonProtectedPendingUpload)
	}
	if !in.closure.complete || !in.snapshotComplete {
		return unknown(domain.PruneReasonUnknownManifest)
	}
	// A blob that cannot be aged cannot be proven past the upload TTL,
	// so it stays unknown instead of becoming eligible.
	modTime, err := s.blobStorage.GetBlobModTime(digest)
	if err != nil {
		return unknown(domain.PruneReasonUnknownInventory)
	}
	if in.now.Sub(modTime) <= registrystate.PendingBlobTTL {
		return protect(domain.PruneReasonProtectedPendingUpload)
	}
	verdict.Verdict = domain.PruneVerdictEligible
	verdict.Reasons = domain.CanonicalReasons(domain.PruneReasonEligibleUnreferencedBlob)
	return verdict
}

// rootReasonsForDigest reports whether the durable snapshot claims the
// digest directly, and with which verdict reasons.
func rootReasonsForDigest(snapshot *domain.PruneProtectionSnapshot, digest string) (bool, []domain.PruneReason) {
	if snapshot == nil {
		return false, nil
	}
	matched := false
	for _, root := range snapshot.Roots {
		if root.Ref == digest {
			matched = true
			break
		}
	}
	if !matched {
		return false, nil
	}
	reasons := snapshot.RootReasons(digest)
	if len(reasons) == 0 {
		reasons = domain.CanonicalReasons(domain.PruneReasonProtectedSharedContent)
	}
	return true, reasons
}

// retainedRoot is one manifest reference whose closure must survive.
type retainedRoot struct {
	Repository string
	Reference  string
}

// closure is the transitive reference set of every retained manifest.
type closure struct {
	digests  map[string]struct{}
	visited  map[string]struct{}
	complete bool
	gaps     []domain.InventoryGap
}

func newClosure() *closure {
	return &closure{
		digests:  make(map[string]struct{}),
		visited:  make(map[string]struct{}),
		complete: true,
	}
}

func (c *closure) references(digest string) bool {
	_, ok := c.digests[digest]
	return ok
}

// traverseClosure walks one retained reference transitively. A missing,
// corrupt, or unsupported node marks the closure incomplete: the caller
// then treats unreferenced blobs as unknown instead of eligible.
func (s *Service) traverseClosure(root retainedRoot, c *closure) {
	visitKey := root.Repository + "\x00" + root.Reference
	if _, seen := c.visited[visitKey]; seen {
		return
	}
	c.visited[visitKey] = struct{}{}

	data, _, err := s.manifestStorage.GetManifest(root.Repository, root.Reference)
	if err != nil {
		c.complete = false
		c.gaps = append(c.gaps, domain.InventoryGap{
			Source: domain.InventorySourceManifests,
			Reason: domain.PruneReasonUnknownManifest,
			Detail: "retained manifest unreadable for " + root.Repository + "@" + root.Reference,
		})
		return
	}
	references, mediaType, err := parseManifestDocument(data)
	if err != nil {
		c.complete = false
		c.gaps = append(c.gaps, domain.InventoryGap{
			Source: domain.InventorySourceManifests,
			Reason: domain.PruneReasonUnknownManifest,
			Detail: "retained manifest corrupt for " + root.Repository + "@" + root.Reference,
		})
		return
	}
	if !supportedManifestMediaType(mediaType) {
		c.complete = false
		c.gaps = append(c.gaps, domain.InventoryGap{
			Source: domain.InventorySourceManifests,
			Reason: domain.PruneReasonUnknownMediaType,
			Detail: "retained manifest media type unsupported for " + root.Repository + "@" + root.Reference,
		})
		return
	}

	// The manifest body itself is addressable content: record its digest
	// so a stored manifest copy is never judged unreferenced.
	c.digests[digestOfManifest(data)] = struct{}{}

	for _, digest := range references.blobs {
		c.digests[digest] = struct{}{}
	}
	for _, child := range references.childManifests {
		c.digests[child] = struct{}{}
		s.traverseClosure(retainedRoot{Repository: root.Repository, Reference: child}, c)
	}
}

// executeRegistry deletes exactly the planned tag references and then
// exactly the planned unreferenced blobs. It runs after the complete
// plan has been validated, never before.
func (s *Service) executeRegistry(ctx context.Context, plan *domain.PrunePlan, report *domain.ImagePruneReport, log zerowrap.Logger) error {
	for _, ref := range plan.EligibleTags() {
		if err := s.manifestStorage.DeleteManifest(ref.Repository, ref.Tag); err != nil {
			log.Warn().Err(err).Str("tag", ref.String()).Msg("failed to delete manifest, skipping")
			report.Plan.Failures = append(report.Plan.Failures, domain.PruneFailure{
				Kind: domain.PruneResourceRegistryTag, Ref: ref.String(), Err: err.Error(),
			})
			continue
		}
		report.Plan.Deleted = append(report.Plan.Deleted, domain.PruneCandidateReport{
			Kind: domain.PruneResourceRegistryTag, Ref: ref.String(), Verdict: domain.PruneVerdictEligible,
		})
		report.Registry.TagsRemoved++
	}

	for _, ref := range plan.EligibleBlobs() {
		size, err := s.blobStorage.DeleteBlob(ref.Digest)
		if err != nil {
			log.Warn().Err(err).Str("digest", ref.Digest).Msg("failed to delete blob, skipping")
			report.Plan.Failures = append(report.Plan.Failures, domain.PruneFailure{
				Kind: domain.PruneResourceOCIBlob, Ref: ref.String(), Err: err.Error(),
			})
			continue
		}
		report.Plan.Deleted = append(report.Plan.Deleted, domain.PruneCandidateReport{
			Kind: domain.PruneResourceOCIBlob, Ref: ref.String(), Verdict: domain.PruneVerdictEligible,
		})
		report.Registry.BlobsRemoved++
		// Each stored blob is deleted once, so its size is exact.
		report.Registry.SpaceReclaimed += size
		report.Plan.ReclaimedBytes += size
		report.Plan.ReclaimedKnown = true
	}

	uploadsRemoved, uploadBytes, err := s.blobStorage.CleanupStaleUploads(registrystate.PendingBlobTTL)
	if err != nil {
		log.Warn().Err(err).Msg("failed to clean up stale uploads, continuing")
		return nil
	}
	report.Registry.UploadsRemoved = uploadsRemoved
	report.Registry.UploadSpaceReclaimed = uploadBytes
	return nil
}

// manifestReferences is the parsed reference set of one manifest.
type manifestReferences struct {
	blobs          []string
	childManifests []string
}

// parseManifestDocument parses one manifest and reports its media type.
func parseManifestDocument(data []byte) (manifestReferences, string, error) {
	var document struct {
		MediaType     string `json:"mediaType"`
		SchemaVersion int    `json:"schemaVersion"`
		Config        struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return manifestReferences{}, "", err
	}

	refs := manifestReferences{
		blobs:          make([]string, 0, len(document.Layers)+1),
		childManifests: make([]string, 0, len(document.Manifests)),
	}
	if document.Config.Digest != "" {
		refs.blobs = append(refs.blobs, document.Config.Digest)
	}
	for _, layer := range document.Layers {
		if layer.Digest != "" {
			refs.blobs = append(refs.blobs, layer.Digest)
		}
	}
	for _, child := range document.Manifests {
		if child.Digest != "" {
			refs.childManifests = append(refs.childManifests, child.Digest)
		}
	}

	mediaType := document.MediaType
	if mediaType == "" {
		// Older clients omit mediaType. Infer from the shape: a
		// document with manifests is an index, one with layers and a
		// config is a manifest. Anything else is unsupported.
		switch {
		case len(document.Manifests) > 0:
			mediaType = mediaTypeOCIImageIndex
		case document.Config.Digest != "" || len(document.Layers) > 0:
			mediaType = mediaTypeOCIManifest
		}
	}
	return refs, mediaType, nil
}

// supportedManifestMediaType reports whether the closure walker can
// trust the reference set of a manifest with this media type.
func supportedManifestMediaType(mediaType string) bool {
	switch mediaType {
	case mediaTypeDockerManifest, mediaTypeDockerManifestList, mediaTypeOCIManifest, mediaTypeOCIImageIndex:
		return true
	default:
		// digest-addressed content often carries a content type that
		// does not name a media type; treat it as supported only when
		// it is one of the known values above.
		return false
	}
}

// digestOfManifest returns the sha256 digest of a stored manifest body.
func digestOfManifest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
