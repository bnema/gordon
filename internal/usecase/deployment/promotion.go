package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"

	"github.com/bnema/gordon/internal/domain"
)

// PromotionConfig is one published promotion generation: who may promote
// what, and which registry hosts name this installation.
type PromotionConfig struct {
	// Policies grants non-local principals the right to promote one
	// repository into one app service.
	Policies domain.PromotionPolicySet
	// RegistryHosts are the hosts a promotion reference may name besides
	// the installation registry domain (management, CI, and legacy
	// registry domains). A reference without a host is always accepted.
	RegistryHosts []string
}

// SetPromotionConfig atomically publishes a promotion generation. A config
// reload calls it; in-flight promotions keep the generation they read.
func (s *Service) SetPromotionConfig(cfg PromotionConfig) {
	cfg.RegistryHosts = append([]string(nil), cfg.RegistryHosts...)
	s.promotion.Store(&cfg)
}

func (s *Service) promotionConfig() PromotionConfig {
	if cfg := s.promotion.Load(); cfg != nil {
		return *cfg
	}
	return PromotionConfig{}
}

// PromotionInput is one request to run exactly one digest in one service.
type PromotionInput struct {
	App              string
	Service          string
	Image            string
	ExpectedRevision string
	// Op is the caller's idempotency key. Required.
	Op        string
	Principal domain.WorkloadPrincipal
	// LocalOwner bypasses the promotion policies: the owner-only admin
	// socket may promote any service.
	LocalOwner bool
	// Validate runs the structural and installation policy checks Apply
	// runs on the candidate spec. Required.
	Validate func(domain.AppSpec) error
}

// promotionPlan carries what the promotion read under the app lock. cfg is
// the single promotion generation the whole request uses.
type promotionPlan struct {
	input     PromotionInput
	cfg       PromotionConfig
	image     domain.PromotionImage
	canonical string
	request   domain.AppOperationRequest
	desired   domain.AppDesiredRevision
	ownership domain.AppOwnership
}

// StartPromotion is the first phase of a promotion. It validates and
// canonicalizes the image and authorizes the principal before taking any
// lock, so an unauthorized caller never contends on the app or triggers its
// reconciliation. Under the GC shared lease and the app lock it then
// converges an interrupted predecessor, replays a known key,
// verifies the manifest exists in the installation registry, and checks the
// app is converged except the target service. It then atomically accepts a
// new desired revision whose only change is that service's image together
// with its non-terminal journal. Execution is the unchanged ExecuteDeploy of
// the returned claim, targeted at the promoted service.
//
// A request that is already satisfied returns a terminal success without
// writing desired state; a request whose digest is desired but not yet
// active claims a deploy of the current desired revision for that service.
func (s *Service) StartPromotion(ctx context.Context, input PromotionInput) (*StartDeployResult, error) {
	plan, err := s.authorizePromotion(input)
	if err != nil {
		return nil, err
	}
	release, err := s.acquireAppContext(ctx, input.App)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "StartPromotion",
		"app":                 input.App,
		"service":             input.Service,
	})
	replay, err := s.preparePromotionLocked(ctx, &plan)
	if err != nil || replay != nil {
		return replay, err
	}
	return s.claimPromotionLocked(ctx, plan)
}

// preparePromotionLocked runs every locked check that needs no state write.
// It returns a replay result when the key already answered this request.
func (s *Service) preparePromotionLocked(ctx context.Context, plan *promotionPlan) (*StartDeployResult, error) {
	if err := s.deps.State.Recover(ctx); err != nil {
		return nil, fmt.Errorf("deployment: recover before promotion: %w", err)
	}
	if err := s.reconcileInterruptedDeploy(ctx, plan.input.App); err != nil {
		return nil, err
	}
	if replay, err := s.replayPromotion(ctx, *plan); err != nil || replay != nil {
		return replay, err
	}
	if err := s.requireManifest(plan.image); err != nil {
		return nil, err
	}
	return nil, s.loadPromotionBase(ctx, plan)
}

// authorizePromotion is pure: it checks the request shape and wiring, parses
// the image, and checks the principal may promote its repository into the
// service. Parsing comes first because the policy grants a repository, not
// an image reference. It reads the promotion generation once for the whole
// request.
func (s *Service) authorizePromotion(input PromotionInput) (promotionPlan, error) {
	if input.Op == "" || input.Validate == nil {
		return promotionPlan{}, fmt.Errorf("deployment: promotion requires an idempotency key and a spec validator: %w", domain.ErrInvalidAppSpec)
	}
	if err := domain.ValidateServiceName(input.Service); err != nil {
		return promotionPlan{}, fmt.Errorf("deployment: promotion service: %w", domain.ErrInvalidAppSpec)
	}
	registry := s.deps.Registry.Domain
	if registry == "" || s.deps.Manifests == nil {
		return promotionPlan{}, fmt.Errorf("deployment: no installation registry is wired: %w", domain.ErrPromotionUnavailable)
	}
	cfg := s.promotionConfig()
	image, err := domain.ParsePromotionImage(input.Image, append([]string{registry}, cfg.RegistryHosts...))
	if err != nil {
		return promotionPlan{}, err
	}
	if !input.LocalOwner && !cfg.Policies.Allows(input.Principal, input.App, input.Service, image.Repository) {
		return promotionPlan{}, fmt.Errorf("deployment: %s may not promote %s into %s/%s: %w",
			input.Principal, image.Repository, input.App, input.Service, domain.ErrPromotionDenied)
	}
	canonical := image.Canonical(registry)
	return promotionPlan{
		input: input, cfg: cfg, image: image, canonical: canonical,
		request: domain.AppOperationRequest{
			Kind: domain.AppOperationKindPromote, App: input.App, Service: input.Service, Image: canonical,
		},
	}, nil
}

// replayPromotion returns the stored journal of a key that already answered
// this exact request by this principal. The key replays before any freshness
// check, so a retry after later mutations reports the original outcome. A key
// that answered anything else conflicts.
func (s *Service) replayPromotion(ctx context.Context, plan promotionPlan) (*StartDeployResult, error) {
	op, err := s.deps.State.LoadOperation(ctx, plan.input.App, plan.input.Op)
	if errors.Is(err, domain.ErrAppOperationNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deployment: load operation %q: %w", plan.input.Op, err)
	}
	if op.Request != plan.request || op.Principal != plan.input.Principal.String() {
		return nil, fmt.Errorf("deployment: operation key %s already answered a different request: %w", plan.input.Op, domain.ErrAppStateConflict)
	}
	return promotionReplay(plan, op), nil
}

func promotionReplay(plan promotionPlan, op domain.AppOperation) *StartDeployResult {
	return &StartDeployResult{Claim: DeployClaim{
		App: plan.input.App, Op: op.Op, Service: plan.input.Service, Revision: op.InputRevision, Journal: op,
	}}
}

// requireManifest verifies the promoted digest is stored in the installation
// registry. The caller holds the GC shared lease, so prune cannot delete the
// manifest between this read and the durable journal that protects it.
func (s *Service) requireManifest(image domain.PromotionImage) error {
	if _, _, err := s.deps.Manifests.GetManifest(image.Repository, image.Digest); err != nil {
		if errors.Is(err, domain.ErrManifestNotFound) {
			return fmt.Errorf("deployment: %s@%s: %w", image.Repository, image.Digest, domain.ErrManifestNotFound)
		}
		return fmt.Errorf("deployment: read manifest %s@%s: %w", image.Repository, image.Digest, err)
	}
	return nil
}

// loadPromotionBase loads desired state and refuses a stopped app, an
// unknown service, a stale expected revision, and a base that differs from
// ACTIVE outside the promoted service.
func (s *Service) loadPromotionBase(ctx context.Context, plan *promotionPlan) error {
	app, service := plan.input.App, plan.input.Service
	desired, err := s.resolveRevision(ctx, DeployInput{App: app})
	if err != nil {
		return err
	}
	if plan.input.ExpectedRevision != "" && plan.input.ExpectedRevision != desired.Revision {
		return fmt.Errorf("deployment: desired revision is %s, not %s: %w", desired.Revision, plan.input.ExpectedRevision, domain.ErrAppStateConflict)
	}
	if !specHasService(desired.Spec, service) {
		return fmt.Errorf("deployment: app %q has no service %q: %w", app, service, domain.ErrAppServiceNotFound)
	}
	intent, err := s.deps.State.LoadIntent(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load intent: %w", err)
	}
	if intent.Stopped {
		return fmt.Errorf("deployment: app %q is stopped; start it before promoting: %w", app, domain.ErrAppStateConflict)
	}
	if err := s.checkConverged(ctx, app, service, desired); err != nil {
		return err
	}
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load ownership: %w", err)
	}
	if ownership.ID == "" {
		return fmt.Errorf("deployment: app %q has no live incarnation: %w", app, domain.ErrAppNotFound)
	}
	plan.desired, plan.ownership = desired, ownership
	return nil
}

func specHasService(spec domain.AppSpec, name string) bool {
	_, ok := specService(spec, name)
	return ok
}

func specService(spec domain.AppSpec, name string) (domain.AppService, bool) {
	for _, svc := range spec.Services {
		if svc.Name == name {
			return svc, true
		}
	}
	return domain.AppService{}, false
}

// claimPromotionLocked applies the no-op rules, then accepts the candidate
// revision and its journal in one store transaction. The no-op path checks
// for an unfinished operation through claimOperation; the accepting path
// checks it here, once.
func (s *Service) claimPromotionLocked(ctx context.Context, plan promotionPlan) (*StartDeployResult, error) {
	if s.desiredRunsDigest(plan) {
		return s.claimPromotionNoop(ctx, plan)
	}
	if err := s.refuseUnfinishedOperation(ctx, plan.input.App, plan.input.Op); err != nil {
		return nil, err
	}
	rev, err := s.promotionRevision(plan)
	if err != nil {
		return nil, err
	}
	op := s.promotionOperation(plan, rev.Revision)
	existing, claimed, err := s.deps.State.AcceptPromotion(ctx, rev, op)
	if err != nil {
		return nil, fmt.Errorf("deployment: accept promotion: %w", err)
	}
	if !claimed {
		if existing == nil || existing.Principal != op.Principal {
			return nil, fmt.Errorf("deployment: operation key %s already answered a different request: %w", op.Op, domain.ErrAppStateConflict)
		}
		return promotionReplay(plan, *existing), nil
	}
	s.markOperationLive(plan.input.App, existing.Op)
	log := zerowrap.FromCtx(ctx)
	log.Info().Str("op", existing.Op).Str("revision", rev.Revision).Str("digest", plan.image.Digest).
		Str("principal", existing.Principal).Msg("deployment: promotion accepted")
	return &StartDeployResult{
		Claim: DeployClaim{
			App: plan.input.App, Op: existing.Op, Service: plan.input.Service, Revision: rev.Revision,
			Resolved: rev, Journal: *existing,
		},
		Owned: true,
	}, nil
}

// desiredRunsDigest reports whether the desired service already names the
// promoted repository and digest on an installation registry host.
func (s *Service) desiredRunsDigest(plan promotionPlan) bool {
	svc, _ := specService(plan.desired.Spec, plan.input.Service)
	hosts := append([]string{s.deps.Registry.Domain}, plan.cfg.RegistryHosts...)
	current, err := domain.ParsePromotionImage(svc.Image, hosts)
	return err == nil && current == plan.image
}

// claimPromotionNoop handles a digest desired already declares. When ACTIVE
// runs it too the request is satisfied: a terminal success journal answers
// the key and no desired state is written. Otherwise a deploy of the current
// desired revision is claimed for that service.
func (s *Service) claimPromotionNoop(ctx context.Context, plan promotionPlan) (*StartDeployResult, error) {
	op := s.promotionOperation(plan, plan.desired.Revision)
	steps := []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepPending}}
	active, ok, err := s.deps.State.LoadActive(ctx, plan.input.App)
	if err != nil {
		return nil, fmt.Errorf("deployment: load active: %w", err)
	}
	desiredSvc, _ := specService(plan.desired.Spec, plan.input.Service)
	eff, running := active.Services[plan.input.Service]
	satisfied := ok && running && eff.Digest == plan.image.Digest && domain.SameAppService(eff.Spec, desiredSvc)
	if satisfied {
		op.Outcome = domain.AppOutcomeSuccess
		steps = []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{
				ID: "service." + plan.input.Service + ".replace", State: domain.AppStepSucceeded, Service: plan.input.Service,
				Before: eff.Container, After: eff.Container,
				Detail: domain.AppServiceUnchanged + ": already running " + plan.canonical,
			},
		}
	}
	claimed, owned, err := s.claimOperation(ctx, plan.input.Op, op, steps)
	if err != nil {
		return nil, err
	}
	if !owned || satisfied {
		return promotionReplay(plan, claimed), nil
	}
	s.markOperationLive(plan.input.App, claimed.Op)
	return &StartDeployResult{
		Claim: DeployClaim{
			App: plan.input.App, Op: claimed.Op, Service: plan.input.Service, Revision: plan.desired.Revision,
			Resolved: plan.desired, Journal: claimed,
		},
		Owned: true,
	}, nil
}

// promotionRevision builds the candidate: the desired spec with one service
// image replaced, validated exactly like an applied manifest.
func (s *Service) promotionRevision(plan promotionPlan) (domain.AppDesiredRevision, error) {
	base := plan.desired
	spec := base.Spec
	spec.Services = append([]domain.AppService(nil), base.Spec.Services...)
	for i := range spec.Services {
		if spec.Services[i].Name == plan.input.Service {
			spec.Services[i].Image = plan.canonical
		}
	}
	if err := plan.input.Validate(spec); err != nil {
		return domain.AppDesiredRevision{}, err
	}
	source, err := json.Marshal(spec)
	if err != nil {
		return domain.AppDesiredRevision{}, fmt.Errorf("deployment: encode candidate spec: %w", err)
	}
	sum := sha256.Sum256(source)
	rev := base
	rev.Revision = newRevisionID()
	rev.Supersedes = base.Revision
	rev.AcceptedAt = time.Now().UTC()
	rev.SourceSHA256 = hex.EncodeToString(sum[:])
	rev.Spec = spec
	rev.Status = domain.AppStepPending
	rev.Promotion = &domain.AppPromotionProvenance{
		Service: plan.input.Service, Image: plan.canonical, Digest: plan.image.Digest,
		Principal: plan.input.Principal.String(), Operation: plan.input.Op, AppID: plan.ownership.ID,
	}
	return rev, nil
}

func (s *Service) promotionOperation(plan promotionPlan, revision string) domain.AppOperation {
	return domain.AppOperation{
		Op:            plan.input.Op,
		Kind:          domain.AppOperationKindPromote,
		App:           plan.input.App,
		InputRevision: revision,
		StartedAt:     time.Now().UTC(),
		Request:       plan.request,
		Principal:     plan.input.Principal.String(),
	}
}

// newRevisionID allocates a time-ordered rev- identifier (uuid v7).
func newRevisionID() string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	var buf [32]byte
	hex.Encode(buf[:], id[:])
	return "rev-" + string(buf[:])
}
