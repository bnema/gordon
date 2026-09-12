package apps

import (
	"context"
	"fmt"
	"sort"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// AppServiceImpl implements in.AppService over the deployment engine. The
// daemon owns this implementation; CLI always reaches it via the daemon.
// All mutations run recovery-before-mutation through the engine.
type AppServiceImpl struct {
	store       out.AppState
	deploy      deployEngine
	secrets     out.SecretWriter
	log         zerowrap.Logger
	listeners   map[string]domain.EntryPointListener
	barrier     out.GCBarrier
	imagePolicy domain.ImageSourcePolicy
}

// deployEngine is the subset of the deployment engine the app service needs.
type deployEngine interface {
	Deploy(ctx context.Context, input deployment.DeployInput) (*deployment.DeployResult, error)
	Stop(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
	Start(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
	Restart(ctx context.Context, app, service, opID string) (*deployment.LifecycleResult, error)
	Remove(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
}

// NewAppServiceImpl wires the driving-port implementation.
func NewAppServiceImpl(store out.AppState, deploy deployEngine, secrets out.SecretWriter, log zerowrap.Logger) *AppServiceImpl {
	return &AppServiceImpl{store: store, deploy: deploy, secrets: secrets, log: log}
}

// WithEntrypoints supplies the installation entrypoint listeners used to
// validate L4 publish declarations at apply time.
func (s *AppServiceImpl) WithEntrypoints(listeners map[string]domain.EntryPointListener) *AppServiceImpl {
	s.listeners = listeners
	return s
}

// WithGCBarrier supplies the process-wide GC barrier used to serialize an
// apply against destructive prune.
func (s *AppServiceImpl) WithGCBarrier(barrier out.GCBarrier) *AppServiceImpl {
	s.barrier = barrier
	return s
}

// WithImagePolicy supplies the registry policy used for manifest validation.
func (s *AppServiceImpl) WithImagePolicy(policy domain.ImageSourcePolicy) *AppServiceImpl {
	s.imagePolicy = policy
	return s
}

var _ in.AppService = (*AppServiceImpl)(nil)

// Apply implements in.AppService.
func (s *AppServiceImpl) Apply(ctx context.Context, spec domain.AppSpec, source []byte, dryRun bool) (*in.AppApplyResult, *in.AppDryRunResult, error) {
	core := NewService(s.store, s.log).
		WithEntrypoints(s.listeners).
		WithGCBarrier(s.barrier).
		WithImagePolicy(s.imagePolicy)
	applyResult, dryResult, err := core.Apply(ctx, spec, source, dryRun)
	if err != nil {
		return nil, nil, err
	}
	if dryResult != nil {
		return nil, &in.AppDryRunResult{
			App:   spec.Name,
			Valid: true,
			Diff:  dryResult.Diff,
		}, nil
	}
	if applyResult == nil {
		return nil, &in.AppDryRunResult{App: spec.Name, Valid: true}, nil
	}
	return &in.AppApplyResult{
		App:               applyResult.App,
		FormerRevision:    applyResult.FormerRevision,
		ResultingRevision: applyResult.ResultingRevision,
		Noop:              applyResult.Noop,
		Pending:           applyResult.Pending,
		Diff:              applyResult.Diff,
		IntentID:          applyResult.IntentID,
	}, nil, nil
}

// List implements in.AppService.
func (s *AppServiceImpl) List(ctx context.Context) ([]in.AppSummary, error) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(apps)
	summaries := make([]in.AppSummary, 0, len(apps))
	for _, app := range apps {
		desired, _, err := s.store.LoadDesired(ctx, app)
		if err != nil {
			return nil, err
		}
		active, ok, err := s.store.LoadActive(ctx, app)
		if err != nil {
			return nil, err
		}
		intent, err := s.store.LoadIntent(ctx, app)
		if err != nil {
			return nil, err
		}
		summary := in.AppSummary{App: app, Desired: desired.Revision, Stopped: intent.Stopped}
		if ok {
			summary.Active = active.ConvergedRevision
			summary.Converged = active.Converged
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// Show implements in.AppService.
func (s *AppServiceImpl) Show(ctx context.Context, app string) (*in.AppDetail, error) {
	desired, _, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return nil, err
	}
	active, _, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return nil, err
	}
	intent, err := s.store.LoadIntent(ctx, app)
	if err != nil {
		return nil, err
	}
	detail := &in.AppDetail{
		App:               app,
		DesiredRevision:   desired.Revision,
		DesiredStatus:     desired.Status,
		Converged:         active.Converged,
		ConvergedRevision: active.ConvergedRevision,
		Services:          map[string]in.AppServiceView{},
		Stopped:           intent.Stopped,
	}
	ownership, err := s.store.LoadOwnership(ctx, app)
	if err != nil {
		return nil, err
	}
	for name, svc := range active.Services {
		detail.Services[name] = in.AppServiceView{
			EffectiveRevision: svc.EffectiveRevision,
			Digest:            svc.Digest,
			Container:         svc.Container,
			RestartUnsafe:     ownership.Services[name].RestartUnsafe,
		}
	}
	latest, ok, err := s.store.LoadLatestOperation(ctx, app)
	if err != nil {
		return nil, err
	}
	if ok {
		detail.LastOp = latest.Op
		detail.LastOutcome = latest.Outcome
	}
	return detail, nil
}

// Diff implements in.AppService.
func (s *AppServiceImpl) Diff(ctx context.Context, app string) (domain.AppDiff, error) {
	desired, ok, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return domain.AppDiff{}, err
	}
	if !ok {
		return domain.AppDiff{}, fmt.Errorf("apps: app %q has no desired state: %w", app, domain.ErrAppRevisionNotFound)
	}
	active, _, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return domain.AppDiff{}, err
	}
	return domain.DiffAppSpec(desired.Spec, activeSpec(active)), nil
}

// Deploy implements in.AppService.
func (s *AppServiceImpl) Deploy(ctx context.Context, app, revision, service, idempotencyKey string) (*domain.AppOperation, error) {
	if op, ok := s.replayOperation(ctx, app, idempotencyKey); ok {
		return op, nil
	}
	result, err := s.deploy.Deploy(ctx, deployment.DeployInput{App: app, Revision: revision, Service: service, Op: idempotencyKey})
	if err != nil && result == nil {
		return nil, err
	}
	op, loadErr := s.store.LoadOperation(ctx, app, result.Op)
	if loadErr != nil {
		return nil, loadErr
	}
	if err != nil {
		return &op, err
	}
	return &op, nil
}

// Stop implements in.AppService.
func (s *AppServiceImpl) Stop(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error) {
	if op, ok := s.replayOperation(ctx, app, idempotencyKey); ok {
		return op, nil
	}
	result, err := s.deploy.Stop(ctx, app, idempotencyKey)
	if err != nil {
		return nil, err
	}
	op, err := s.store.LoadOperation(ctx, app, result.Op)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// Start implements in.AppService.
func (s *AppServiceImpl) Start(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error) {
	if op, ok := s.replayOperation(ctx, app, idempotencyKey); ok {
		return op, nil
	}
	result, err := s.deploy.Start(ctx, app, idempotencyKey)
	if err != nil {
		return nil, err
	}
	op, err := s.store.LoadOperation(ctx, app, result.Op)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// Restart implements in.AppService.
func (s *AppServiceImpl) Restart(ctx context.Context, app, service, idempotencyKey string) (*domain.AppOperation, error) {
	if op, ok := s.replayOperation(ctx, app, idempotencyKey); ok {
		return op, nil
	}
	result, err := s.deploy.Restart(ctx, app, service, idempotencyKey)
	if err != nil {
		return nil, err
	}
	op, err := s.store.LoadOperation(ctx, app, result.Op)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// Remove implements in.AppService.
func (s *AppServiceImpl) Remove(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error) {
	if op, ok := s.replayOperation(ctx, app, idempotencyKey); ok {
		return op, nil
	}
	result, err := s.deploy.Remove(ctx, app, idempotencyKey)
	if err != nil {
		return nil, err
	}
	op, err := s.store.LoadOperation(ctx, app, result.Op)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func (s *AppServiceImpl) replayOperation(ctx context.Context, app, key string) (*domain.AppOperation, bool) {
	if key == "" {
		return nil, false
	}
	op, err := s.store.LoadOperation(ctx, app, key)
	if err != nil {
		return nil, false
	}
	return &op, true
}

// OperationByKey implements in.AppService.
func (s *AppServiceImpl) OperationByKey(ctx context.Context, app, key string) (*domain.AppOperation, error) {
	op, err := s.store.LoadOperation(ctx, app, key)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// SetSecrets implements in.AppService.
func (s *AppServiceImpl) SetSecrets(ctx context.Context, app, service string, values map[string]string) error {
	if service == "" {
		return fmt.Errorf("apps: service is required: %w", domain.ErrInvalidAppSpec)
	}
	registered, err := s.secretNames(ctx, app, service)
	if err != nil {
		return err
	}
	for key, value := range values {
		name, ok := registered[key]
		if !ok {
			return fmt.Errorf("apps: secret %q not registered for %s/%s: %w", key, app, service, domain.ErrAppSecretMissing)
		}
		path, err := s.secretPath(ctx, app, service, name)
		if err != nil {
			return err
		}
		if err := s.secrets.SetSecret(ctx, path, value); err != nil {
			return fmt.Errorf("apps: write secret %q: %w", key, err)
		}
	}
	return nil
}

// DeleteSecret implements in.AppService.
func (s *AppServiceImpl) DeleteSecret(ctx context.Context, app, service, key string) error {
	if service == "" || key == "" {
		return fmt.Errorf("apps: service and key are required: %w", domain.ErrInvalidAppSpec)
	}
	registered, err := s.secretNames(ctx, app, service)
	if err != nil {
		return err
	}
	name, ok := registered[key]
	if !ok {
		return fmt.Errorf("apps: secret %q not registered for %s/%s: %w", key, app, service, domain.ErrAppSecretMissing)
	}
	// Refused while the name is still referenced by desired or active.
	if referenced, err := s.secretReferenced(ctx, app, service, name); err != nil {
		return err
	} else if referenced {
		return fmt.Errorf("apps: secret %q still referenced: %w", key, domain.ErrAppStateConflict)
	}
	path, err := s.secretPath(ctx, app, service, name)
	if err != nil {
		return err
	}
	return s.secrets.DeleteSecret(ctx, path)
}

// secretPath returns the UUID-keyed pass path for one secret name.
// Empty ID means legacy name-keyed paths (records predating UUIDs).
func (s *AppServiceImpl) secretPath(ctx context.Context, app, service, name string) (string, error) {
	ownership, err := s.store.LoadOwnership(ctx, app)
	if err != nil {
		return "", err
	}
	return domain.AppSecretPathForID(ownership.ID, app, service, name), nil
}

// secretNames returns env-key → secret-name registrations.
func (s *AppServiceImpl) secretNames(ctx context.Context, app, service string) (map[string]string, error) {
	merged := map[string]string{}
	desired, ok, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return nil, err
	}
	if ok {
		for _, svc := range desired.Spec.Services {
			if svc.Name == service {
				for envKey, name := range svc.Secrets {
					merged[envKey] = name
				}
			}
		}
	}
	active, ok, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return nil, err
	}
	if ok {
		for name, svc := range active.Services {
			if name == service {
				for envKey, secretName := range svc.Spec.Secrets {
					merged[envKey] = secretName
				}
			}
		}
	}
	if len(merged) == 0 {
		return nil, fmt.Errorf("apps: service %q has no registered secrets: %w", service, domain.ErrAppSecretMissing)
	}
	return merged, nil
}

// secretReferenced reports desired/active references to a secret name.
func (s *AppServiceImpl) secretReferenced(ctx context.Context, app, service, name string) (bool, error) {
	desired, ok, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return false, err
	}
	if ok {
		for _, svc := range desired.Spec.Services {
			if svc.Name != service {
				continue
			}
			for _, secretName := range svc.Secrets {
				if secretName == name {
					return true, nil
				}
			}
		}
	}
	active, ok, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return false, err
	}
	if ok {
		for svcName, svc := range active.Services {
			if svcName != service {
				continue
			}
			for _, secretName := range svc.Spec.Secrets {
				if secretName == name {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
