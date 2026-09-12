package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// This file resolves backup targets from the declarative app state. There
// is no image, port, container-label, or attachment heuristic anywhere:
// a target exists because the app's ACTIVE record declares it, and its
// identity is always (app, service, resource).

// AppStateReader is the ACTIVE/intent subset target resolution needs.
// It is narrower than out.AppState so backup paths never gain write
// access.
type AppStateReader = out.AppStateReader

// declaredTargets walks the ACTIVE record of one app and returns its
// declared database and volume targets. STOPPED-intent apps have no
// target: their workloads are intentionally down.
func declaredTargets(ctx context.Context, state AppStateReader, app string) (databaseTargets []domain.DatabaseTarget, volumeTargets []domain.VolumeBackupTarget, err error) {
	intent, err := state.LoadIntent(ctx, app)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: load intent for %q: %w", app, err)
	}
	if intent.Stopped {
		return nil, nil, nil
	}
	active, ok, err := state.LoadActive(ctx, app)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: load active for %q: %w", app, err)
	}
	if !ok {
		return nil, nil, nil
	}
	names := make([]string, 0, len(active.Services))
	for name := range active.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		eff := active.Services[name]
		databaseTargets = append(databaseTargets, databaseTargetsForService(app, name, eff)...)
		volumeTargets = append(volumeTargets, volumeTargetsForService(app, name, eff)...)
	}
	return databaseTargets, volumeTargets, nil
}

// databaseTargetsForService returns the declared PostgreSQL databases of
// one service that its backup declaration references. Only PostgreSQL is
// supported, and the spec validation already rejects anything else.
func databaseTargetsForService(app, service string, eff domain.AppEffectiveService) []domain.DatabaseTarget {
	referenced := make(map[string]struct{}, len(eff.Spec.Backup.Postgres))
	for _, ref := range eff.Spec.Backup.Postgres {
		referenced[ref] = struct{}{}
	}
	var targets []domain.DatabaseTarget
	for _, db := range eff.Spec.Databases {
		if _, ok := referenced[db.Name]; !ok {
			continue
		}
		targets = append(targets, domain.DatabaseTarget{
			App:         app,
			Service:     service,
			Database:    db.Name,
			Schedule:    domain.BackupSchedule(db.Schedule),
			ContainerID: eff.Container,
		})
	}
	return targets
}

// volumeTargetsForService returns the declared volumes of one service that
// its backup declaration references, with the runtime volume name and
// mount path the archive export needs.
func volumeTargetsForService(app, service string, eff domain.AppEffectiveService) []domain.VolumeBackupTarget {
	referenced := make(map[string]struct{}, len(eff.Spec.Backup.Volume))
	for _, ref := range eff.Spec.Backup.Volume {
		referenced[ref] = struct{}{}
	}
	var targets []domain.VolumeBackupTarget
	for _, volume := range eff.Spec.Volumes {
		if _, ok := referenced[volume.Name]; !ok {
			continue
		}
		targets = append(targets, domain.VolumeBackupTarget{
			App:               app,
			Service:           service,
			VolumeName:        volume.Name,
			RuntimeVolumeName: domain.RuntimeVolumeName(app, service, volume.Name),
			MountPath:         volume.Path,
		})
	}
	return targets
}

// selectTarget applies the explicit-selector rules to one candidate list:
// an explicit service and resource must match exactly, while an omitted
// selector succeeds only when exactly one candidate remains. Nothing is
// ever chosen by guessing, and an ambiguity never lists anything but the
// safe target identity (service and resource names).
func selectTarget[T any](candidates []T, kind, service, resource string, key func(T) (string, string)) (T, error) {
	var zero T
	matched := candidates
	if service != "" {
		matched = filterTargets(matched, func(candidate T) bool {
			candidateService, _ := key(candidate)
			return candidateService == service
		})
	}
	if resource != "" {
		matched = filterTargets(matched, func(candidate T) bool {
			_, candidateResource := key(candidate)
			return strings.EqualFold(candidateResource, resource)
		})
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		return zero, fmt.Errorf("backup: %s %s not found", kind, selectorLabel(service, resource))
	default:
		names := make([]string, 0, len(matched))
		for _, candidate := range matched {
			candidateService, candidateResource := key(candidate)
			names = append(names, candidateService+"/"+candidateResource)
		}
		sort.Strings(names)
		return zero, fmt.Errorf(
			"backup: %s selector %s is ambiguous: candidates %s",
			kind, selectorLabel(service, resource), strings.Join(names, ", "),
		)
	}
}

func filterTargets[T any](candidates []T, keep func(T) bool) []T {
	matched := make([]T, 0, len(candidates))
	for _, candidate := range candidates {
		if keep(candidate) {
			matched = append(matched, candidate)
		}
	}
	return matched
}

// selectorLabel renders the selectors a caller supplied, for errors.
func selectorLabel(service, resource string) string {
	switch {
	case service != "" && resource != "":
		return service + "/" + resource
	case resource != "":
		return resource
	case service != "":
		return "of service " + service
	default:
		return "without selectors"
	}
}

func appNames(ctx context.Context, state AppStateReader) ([]string, error) {
	apps, err := state.ListApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: list apps: %w", err)
	}
	sort.Strings(apps)
	return apps, nil
}
