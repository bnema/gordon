package apps

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// CountManagedContainers returns the number of containers Gordon currently
// manages: ACTIVE app services bound to a container, excluding apps whose
// durable intent is stopped. It reads persisted state only, so the result is
// correct after boot and after every lifecycle operation, including failures.
func CountManagedContainers(ctx context.Context, state out.AppStateReader) (int64, error) {
	names, err := state.ListApps(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list apps: %w", err)
	}
	var total int64
	for _, name := range names {
		intent, err := state.LoadIntent(ctx, name)
		if err != nil {
			return 0, fmt.Errorf("failed to load intent for app %q: %w", name, err)
		}
		if intent.Stopped {
			continue
		}
		active, ok, err := state.LoadActive(ctx, name)
		if err != nil {
			return 0, fmt.Errorf("failed to load active state for app %q: %w", name, err)
		}
		if !ok {
			continue
		}
		for _, svc := range active.Services {
			if svc.Container != "" {
				total++
			}
		}
	}
	return total, nil
}
