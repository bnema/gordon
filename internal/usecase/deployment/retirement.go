package deployment

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// stopGraceOrDefault returns the grace a runtime call must use for one
// container. A zero declared grace means the app default: an app path
// never hands the runtime an immediate kill.
func stopGraceOrDefault(grace time.Duration) time.Duration {
	if grace <= 0 {
		return domain.AppDefaultStopGrace
	}
	return grace
}

// serviceStopGrace returns the effective stop grace of one service.
func serviceStopGrace(eff domain.AppEffectiveService) time.Duration {
	return stopGraceOrDefault(eff.Spec.StopGrace)
}

// activeStopGrace returns the effective stop grace recorded for one
// service of an ACTIVE record, or the app default when the record has no
// such service. Retiring a replaced container uses the grace of the
// generation being stopped, never the grace of its replacement.
func activeStopGrace(active domain.AppActive, service string) time.Duration {
	return stopGraceOrDefault(active.Services[service].Spec.StopGrace)
}

// retireOptions control one exact-container retirement.
type retireOptions struct {
	// Service names the app service for warnings.
	Service string
	// Grace is the effective stop grace of the container being retired.
	Grace time.Duration
	// Force removes a container that was never published and holds no
	// committed state, without waiting for a grace period.
	Force bool
	// ClearInhibition drops the generation's recovery inhibition once
	// the container is confirmed gone. It is only safe when nothing can
	// recreate this generation: an operator stop or a removal. A
	// replacement keeps its inhibition until the new generation is
	// published, because recovery must not recreate the replaced writer
	// from the still-active old revision.
	ClearInhibition bool
}

// containerRetirement is the outcome of one exact-container retirement.
type containerRetirement struct {
	// ContainerID is the container the retirement targeted.
	ContainerID string
	// Gone is true only when the runtime confirmed the container no
	// longer exists. Not-found counts as confirmation.
	Gone bool
	// Warnings are bounded, log-free leftovers for operator action.
	Warnings []CleanupWarning
}

// retireContainer is the single retirement path for one exact container:
// stop it with the effective grace (or force-remove a never-published
// candidate), confirm the exact ID is gone, and only then release its
// backend claims and clear its recovery inhibition. A container that is
// not confirmed gone keeps its claims and its inhibition, so no later
// generation can dial a recycled loopback port and no recovery pass can
// revive a half-removed generation.
func (s *Service) retireContainer(ctx context.Context, app string, opts retireOptions, containerID string) containerRetirement {
	result := containerRetirement{ContainerID: containerID}
	if containerID == "" {
		result.Gone = true
		return result
	}
	if err := s.stopForRetirement(ctx, opts, containerID); err != nil {
		result.Warnings = append(result.Warnings, CleanupWarning{
			Service: opts.Service, Leftover: containerID, Detail: "stop: " + err.Error(),
		})
		return result
	}
	if err := s.deps.Runtime.RemoveContainer(ctx, containerID, opts.Force); err != nil && !errors.Is(err, domain.ErrContainerNotFound) {
		result.Warnings = append(result.Warnings, CleanupWarning{
			Service: opts.Service, Leftover: containerID, Detail: "remove: " + err.Error(),
		})
		return result
	}
	result.Gone = true
	if err := s.deps.State.ReleaseBackendBinds(ctx, app, containerID); err != nil {
		result.Warnings = append(result.Warnings, CleanupWarning{
			Service: opts.Service, Leftover: containerID, Detail: "release backend claims: " + err.Error(),
		})
	}
	if opts.ClearInhibition {
		// Confirmed gone: the marker has nothing left to protect, and a
		// stale record would only confuse the next recreation.
		if err := s.clearRecoveryInhibition(ctx, app, opts.Service, containerID); err != nil {
			result.Warnings = append(result.Warnings, CleanupWarning{
				Service: opts.Service, Leftover: containerID, Detail: "clear recovery inhibition: " + err.Error(),
			})
		}
	}
	return result
}

// stopForRetirement stops one container. A forced retirement removes
// without a grace period: only a candidate that was never published and
// holds no committed state may be retired that way.
func (s *Service) stopForRetirement(ctx context.Context, opts retireOptions, containerID string) error {
	if opts.Force {
		return nil
	}
	if err := s.deps.Runtime.StopContainer(ctx, containerID, stopGraceOrDefault(opts.Grace)); err != nil && !errors.Is(err, domain.ErrContainerNotFound) {
		return err
	}
	return nil
}

// retireCandidate removes a candidate that was never published and
// releases its backend claims. The candidate holds no committed state, so
// it is force-removed without a grace period. Cleanup failures are
// returned as bounded warnings: a candidate that could not be removed
// must not hide the failure the caller is already reporting.
func (s *Service) retireCandidate(ctx context.Context, app, service, containerID string) []CleanupWarning {
	retired := s.retireContainer(ctx, app, retireOptions{Service: service, Force: true}, containerID)
	return retired.Warnings
}

// cleanupDetail renders the first bounded warning of a failed retirement
// for an error message. Journal and log content never travel here.
func cleanupDetail(retired containerRetirement) string {
	if len(retired.Warnings) == 0 {
		return "container not confirmed gone"
	}
	return retired.Warnings[0].Detail
}

// collectCleanupWarnings gathers one operation's per-service leftovers in
// stable service order. It is the single source of the operation-level
// warning list.
func collectCleanupWarnings(results map[string]ServiceResult) []CleanupWarning {
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	var warnings []CleanupWarning
	for _, name := range names {
		warnings = append(warnings, results[name].CleanupWarnings...)
	}
	return warnings
}

// journalWarnings maps leftovers into the bounded journal shape. Service
// names are filled from the owning service so a warning is always
// attributable without parsing its text.
func journalWarnings(results map[string]ServiceResult) []domain.AppOperationWarning {
	var warnings []domain.AppOperationWarning
	for _, warning := range collectCleanupWarnings(results) {
		warnings = append(warnings, domain.AppOperationWarning{
			Service:  warning.Service,
			Leftover: warning.Leftover,
			Detail:   warning.Detail,
		})
	}
	return warnings
}
