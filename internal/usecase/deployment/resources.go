package deployment

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/domain"
)

// ResolveServiceResources validates installation ceilings and fills unset limits.
// Swap's ceiling is the installation memory ceiling (total memory plus swap).
func (l ResourceLimits) ResolveServiceResources(svc domain.AppService) (domain.AppResources, error) {
	resources, err := l.resolveServiceResources(svc)
	return resources.AppResources, err
}

// resolvedResources keeps the exact quota alongside its reporting projection.
type resolvedResources struct {
	domain.AppResources
	NanoCPUs int64
}

func (l ResourceLimits) resolveServiceResources(svc domain.AppService) (resolvedResources, error) {
	r := resolvedResources{AppResources: svc.Resources}
	if err := r.Validate(svc.Name); err != nil {
		return r, err
	}
	requested, err := domain.CPUsToNanoCPUs(r.CPUs)
	if err != nil {
		return r, fmt.Errorf("%w: service %q resources.cpus: %v", domain.ErrInvalidAppSpec, svc.Name, err)
	}
	if l.NanoCPUs > 0 && requested > l.NanoCPUs {
		return r, fmt.Errorf("%w: service %q resources.cpus requested %g exceeds installation limit %g", domain.ErrInvalidAppSpec, svc.Name, float64(requested)/1e9, float64(l.NanoCPUs)/1e9)
	}
	r.NanoCPUs = requested
	if r.NanoCPUs == 0 {
		r.NanoCPUs = l.NanoCPUs
	}
	r.CPUs = float64(r.NanoCPUs) / 1e9
	for _, limit := range []struct {
		key       string
		requested int64
	}{
		{"memory", r.Memory}, {"memory_swap", r.MemorySwap},
	} {
		if l.MemoryBytes > 0 && limit.requested > l.MemoryBytes {
			return r, fmt.Errorf("%w: service %q resources.%s requested %d bytes exceeds installation limit %d bytes", domain.ErrInvalidAppSpec, svc.Name, limit.key, limit.requested, l.MemoryBytes)
		}
	}
	if r.Memory == 0 {
		r.Memory = l.MemoryBytes
	}
	return r, nil
}

// ValidateResourceSpec is shared by apply and deployment preflight.
func (l ResourceLimits) ValidateResourceSpec(spec domain.AppSpec) error {
	for _, svc := range spec.Services {
		if _, err := l.ResolveServiceResources(svc); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) effectiveResourcesChanged(spec domain.AppService, previous *domain.AppResources) bool {
	// Older ACTIVE records did not record effective resources. Their running
	// containers already inherited installation limits; absence is unknown,
	// not evidence that the container is unlimited.
	if previous == nil && spec.Resources == (domain.AppResources{}) {
		return false
	}
	resources, err := s.deps.Limits.ResolveServiceResources(spec)
	var old domain.AppResources
	if previous != nil {
		old = *previous
	}
	return err != nil || resources != old
}

// preflightServicePolicies keeps all installation grants and ceilings ahead of mutation.
func (s *Service) preflightServicePolicies(ctx context.Context, app string, svc domain.AppService, pinned []pinnedService) error {
	if _, err := s.deps.Limits.ResolveServiceResources(svc); err != nil {
		return err
	}
	if _, err := s.resolveServiceBinds(app, svc); err != nil {
		return err
	}
	if err := s.preflightServiceDevices(ctx, app, svc, pinned); err != nil {
		return err
	}
	_, err := s.resolveServiceCapabilities(app, svc)
	return err
}
