package deployment

import (
	"context"
	"errors"
	"fmt"
)

// sweepAppSecretFiles removes the secret file directories of app whose
// container no longer exists in the runtime. Callers hold the app lock,
// so no candidate of this app can be between file write and create.
func (s *Service) sweepAppSecretFiles(ctx context.Context, app string) error {
	return s.sweepSecretFiles(ctx, app)
}

// SweepSecretFiles removes every secret file directory whose container no
// longer exists, across all apps. It takes no locks: it runs once at
// startup, before any listener (and so any deploy) exists.
func (s *Service) SweepSecretFiles(ctx context.Context) error {
	return s.sweepSecretFiles(ctx, "")
}

// sweepSecretFiles removes orphaned directories, limited to one app when
// filter is not empty. Directory names are container names because the
// files are written before the container ID exists.
func (s *Service) sweepSecretFiles(ctx context.Context, filter string) error {
	store := s.deps.SecretFiles
	if store == nil {
		return nil
	}
	sets, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("deployment: list secret files: %w", err)
	}
	if filter != "" {
		kept := sets[:0]
		for _, set := range sets {
			if set.App == filter {
				kept = append(kept, set)
			}
		}
		sets = kept
	}
	if len(sets) == 0 {
		return nil
	}
	containers, err := s.deps.Runtime.ListContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("deployment: list containers for secret file sweep: %w", err)
	}
	live := make(map[string]struct{}, len(containers))
	for _, c := range containers {
		live[c.Name] = struct{}{}
	}
	var failures []error
	for _, set := range sets {
		if _, ok := live[set.Container]; ok {
			continue
		}
		if err := store.Remove(ctx, set.App, set.Container); err != nil {
			failures = append(failures, fmt.Errorf("deployment: remove secret files of app %q: %w", set.App, err))
		}
	}
	return errors.Join(failures...)
}
