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

// SetImageConfig is one published set-image generation: who may set an image
// what, and which registry hosts name this installation.
type SetImageConfig struct {
	// Policies grants non-local principals the right to set the image of one
	// repository into one app service.
	Policies domain.SetImagePolicySet
	// RegistryHosts are the hosts a set-image reference may name besides
	// the installation registry domain (management, CI, and legacy
	// registry domains). A reference without a host is always accepted.
	RegistryHosts []string
}

// SetSetImageConfig atomically publishes a set-image generation. A config
// reload calls it; in-flight image changes keep the generation they read.
func (s *Service) SetSetImageConfig(cfg SetImageConfig) {
	cfg.RegistryHosts = append([]string(nil), cfg.RegistryHosts...)
	s.setImage.Store(&cfg)
}

func (s *Service) setImageConfig() SetImageConfig {
	if cfg := s.setImage.Load(); cfg != nil {
		return *cfg
	}
	return SetImageConfig{}
}

// SetImageInput is one request to run exactly one digest in one service.
type SetImageInput struct {
	App              string
	Service          string
	Image            string
	ExpectedRevision string
	// Op is the caller's idempotency key. Required.
	Op        string
	Principal domain.WorkloadPrincipal
	// LocalOwner bypasses the set-image policies: the owner-only admin
	// socket may set the image of any service.
	LocalOwner bool
	// Validate runs the structural and installation policy checks Apply
	// runs on the candidate spec. Required.
	Validate func(domain.AppSpec) error
}

// setImagePlan carries what the image change read under the app lock. cfg is
// the single set-image generation the whole request uses.
type setImagePlan struct {
	input     SetImageInput
	cfg       SetImageConfig
	image     domain.DigestImage
	canonical string
	request   domain.AppOperationRequest
	desired   domain.AppDesiredRevision
	ownership domain.AppOwnership
}

// StartSetImage is the first phase of an image change. It validates and
// canonicalizes the image and authorizes the principal before taking any
// lock, so an unauthorized caller never contends on the app or triggers its
// reconciliation. Under the GC shared lease and the app lock it then
// converges an interrupted predecessor, replays a known key,
// verifies the manifest exists in the installation registry, and checks the
// app is converged except the target service. It then atomically accepts a
// new desired revision whose only change is that service's image together
// with its non-terminal journal. Execution is the unchanged ExecuteDeploy of
// the returned claim, targeted at the changed service.
//
// A request that is already satisfied returns a terminal success without
// writing desired state; a request whose digest is desired but not yet
// active claims a deploy of the current desired revision for that service.
func (s *Service) StartSetImage(ctx context.Context, input SetImageInput) (*StartDeployResult, error) {
	plan, err := s.authorizeSetImage(input)
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
		zerowrap.FieldUseCase: "StartSetImage",
		"app":                 input.App,
		"service":             input.Service,
	})
	replay, err := s.prepareSetImageLocked(ctx, &plan)
	if err != nil || replay != nil {
		return replay, err
	}
	return s.claimSetImageLocked(ctx, plan)
}

// prepareSetImageLocked runs every locked check that needs no state write.
// It returns a replay result when the key already answered this request.
func (s *Service) prepareSetImageLocked(ctx context.Context, plan *setImagePlan) (*StartDeployResult, error) {
	if err := s.deps.State.Recover(ctx); err != nil {
		return nil, fmt.Errorf("deployment: recover before image change: %w", err)
	}
	if err := s.reconcileInterruptedDeploy(ctx, plan.input.App); err != nil {
		return nil, err
	}
	if replay, err := s.replaySetImage(ctx, *plan); err != nil || replay != nil {
		return replay, err
	}
	if err := s.requireManifest(plan.image); err != nil {
		return nil, err
	}
	return nil, s.loadSetImageBase(ctx, plan)
}

// authorizeSetImage is pure: it checks the request shape and wiring, parses
// the image, and checks the principal may set an image from its repository into the
// service. Parsing comes first because the policy grants a repository, not
// an image reference. It reads the set-image generation once for the whole
// request.
func (s *Service) authorizeSetImage(input SetImageInput) (setImagePlan, error) {
	if input.Op == "" || input.Validate == nil {
		return setImagePlan{}, fmt.Errorf("deployment: image change requires an idempotency key and a spec validator: %w", domain.ErrInvalidAppSpec)
	}
	if err := domain.ValidateServiceName(input.Service); err != nil {
		return setImagePlan{}, fmt.Errorf("deployment: image change service: %w", domain.ErrInvalidAppSpec)
	}
	registry := s.deps.Registry.Domain
	if registry == "" || s.deps.Manifests == nil {
		return setImagePlan{}, fmt.Errorf("deployment: no installation registry is wired: %w", domain.ErrSetImageUnavailable)
	}
	cfg := s.setImageConfig()
	image, err := domain.ParseDigestImage(input.Image, append([]string{registry}, cfg.RegistryHosts...))
	if err != nil {
		return setImagePlan{}, err
	}
	if !input.LocalOwner && !cfg.Policies.Allows(input.Principal, input.App, input.Service, image.Repository) {
		return setImagePlan{}, fmt.Errorf("deployment: %s may not set image %s for %s/%s: %w",
			input.Principal, image.Repository, input.App, input.Service, domain.ErrSetImageDenied)
	}
	canonical := image.Canonical(registry)
	return setImagePlan{
		input: input, cfg: cfg, image: image, canonical: canonical,
		request: domain.AppOperationRequest{
			Kind: domain.AppOperationKindSetImage, App: input.App, Service: input.Service, Image: canonical,
		},
	}, nil
}

// replaySetImage returns the stored journal of a key that already answered
// this exact request by this principal. The key replays before any freshness
// check, so a retry after later mutations reports the original outcome. A key
// that answered anything else conflicts.
func (s *Service) replaySetImage(ctx context.Context, plan setImagePlan) (*StartDeployResult, error) {
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
	return setImageReplay(plan, op), nil
}

func setImageReplay(plan setImagePlan, op domain.AppOperation) *StartDeployResult {
	return &StartDeployResult{Claim: DeployClaim{
		App: plan.input.App, Op: op.Op, Service: plan.input.Service, Revision: op.InputRevision, Journal: op,
	}}
}

// requireManifest verifies the changed digest is stored in the installation
// registry. The caller holds the GC shared lease, so prune cannot delete the
// manifest between this read and the durable journal that protects it.
func (s *Service) requireManifest(image domain.DigestImage) error {
	if _, _, err := s.deps.Manifests.GetManifest(image.Repository, image.Digest); err != nil {
		if errors.Is(err, domain.ErrManifestNotFound) {
			return fmt.Errorf("deployment: %s@%s: %w", image.Repository, image.Digest, domain.ErrManifestNotFound)
		}
		return fmt.Errorf("deployment: read manifest %s@%s: %w", image.Repository, image.Digest, err)
	}
	return nil
}

// loadSetImageBase loads desired state and refuses a stopped app, an
// unknown service, a stale expected revision, and a base that differs from
// ACTIVE outside the changed service.
func (s *Service) loadSetImageBase(ctx context.Context, plan *setImagePlan) error {
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
		return fmt.Errorf("deployment: app %q is stopped; start it before setting the image: %w", app, domain.ErrAppStateConflict)
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

// claimSetImageLocked applies the no-op rules, then accepts the candidate
// revision and its journal in one store transaction. The no-op path checks
// for an unfinished operation through claimOperation; the accepting path
// checks it here, once.
func (s *Service) claimSetImageLocked(ctx context.Context, plan setImagePlan) (*StartDeployResult, error) {
	if s.desiredRunsDigest(plan) {
		return s.claimSetImageNoop(ctx, plan)
	}
	if err := s.refuseUnfinishedOperation(ctx, plan.input.App, plan.input.Op); err != nil {
		return nil, err
	}
	rev, err := s.setImageRevision(plan)
	if err != nil {
		return nil, err
	}
	op := s.setImageOperation(plan, rev.Revision)
	existing, claimed, err := s.deps.State.AcceptSetImage(ctx, rev, op)
	if err != nil {
		return nil, fmt.Errorf("deployment: accept image change: %w", err)
	}
	if !claimed {
		if existing == nil || existing.Principal != op.Principal {
			return nil, fmt.Errorf("deployment: operation key %s already answered a different request: %w", op.Op, domain.ErrAppStateConflict)
		}
		return setImageReplay(plan, *existing), nil
	}
	s.markOperationLive(plan.input.App, existing.Op)
	log := zerowrap.FromCtx(ctx)
	log.Info().Str("op", existing.Op).Str("revision", rev.Revision).Str("digest", plan.image.Digest).
		Str("principal", existing.Principal).Msg("deployment: image change accepted")
	return &StartDeployResult{
		Claim: DeployClaim{
			App: plan.input.App, Op: existing.Op, Service: plan.input.Service, Revision: rev.Revision,
			Resolved: rev, Journal: *existing,
		},
		Owned: true,
	}, nil
}

// desiredRunsDigest reports whether the desired service already names the
// changed repository and digest on an installation registry host.
func (s *Service) desiredRunsDigest(plan setImagePlan) bool {
	svc, _ := specService(plan.desired.Spec, plan.input.Service)
	hosts := append([]string{s.deps.Registry.Domain}, plan.cfg.RegistryHosts...)
	current, err := domain.ParseDigestImage(svc.Image, hosts)
	return err == nil && current == plan.image
}

// claimSetImageNoop handles a digest desired already declares. When ACTIVE
// runs it too the request is satisfied: a terminal success journal answers
// the key and no desired state is written. Otherwise a deploy of the current
// desired revision is claimed for that service.
func (s *Service) claimSetImageNoop(ctx context.Context, plan setImagePlan) (*StartDeployResult, error) {
	op := s.setImageOperation(plan, plan.desired.Revision)
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
		return setImageReplay(plan, claimed), nil
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

// setImageRevision builds the candidate: the desired spec with one service
// image replaced, validated exactly like an applied manifest.
func (s *Service) setImageRevision(plan setImagePlan) (domain.AppDesiredRevision, error) {
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
	rev.SetImage = &domain.AppSetImageProvenance{
		Service: plan.input.Service, Image: plan.canonical, Digest: plan.image.Digest,
		Principal: plan.input.Principal.String(), Operation: plan.input.Op, AppID: plan.ownership.ID,
	}
	return rev, nil
}

func (s *Service) setImageOperation(plan setImagePlan, revision string) domain.AppOperation {
	return domain.AppOperation{
		Op:            plan.input.Op,
		Kind:          domain.AppOperationKindSetImage,
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
