package domain

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	managedControlSecretsVolumePrefix    = "gordon-control-secrets-"
	managedControlSecretsVolumeSuffixLen = 16
)

// FormatComponentID returns the canonical Gordon component container name.
func FormatComponentID(role ComponentRole, migrationID string, generation uint64) string {
	return fmt.Sprintf("gordon-%s-%s-g%d", role, migrationID, generation)
}

// FormatComponentNetworkID returns the canonical internal network lifecycle target ID.
func FormatComponentNetworkID(migrationID string, generation uint64) string {
	return fmt.Sprintf("gordon-network-%s-g%d", migrationID, generation)
}

// FormatComponentInternalNetwork returns the canonical component internal network name.
func FormatComponentInternalNetwork(migrationID string, generation uint64) string {
	return fmt.Sprintf("gordon-internal-%s-g%d", migrationID, generation)
}

// FormatComponentGenerationVolumeName returns the canonical named volume for a role generation.
func FormatComponentGenerationVolumeName(role ComponentRole, migrationID string, generation uint64) string {
	return FormatComponentID(role, migrationID, generation)
}

// ValidComponentMigrationID reports whether id is a safe migration identifier label.
func ValidComponentMigrationID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

// MatchComponentLifecycleTarget reports whether command names a Gordon-owned lifecycle target.
func MatchComponentLifecycleTarget(action RuntimeComponentLifecycleAction, targetComponentID string, role ComponentRole, migrationID string, generation uint64) bool {
	if !ValidComponentMigrationID(migrationID) || generation == 0 {
		return false
	}
	generationSuffix := "-g" + strconv.FormatUint(generation, 10)
	if action == RuntimeComponentLifecycleEnsureNetwork {
		prefix := "gordon-network-"
		return role == ComponentRoleRuntime && strings.HasPrefix(targetComponentID, prefix) &&
			strings.TrimSuffix(strings.TrimPrefix(targetComponentID, prefix), generationSuffix) == migrationID &&
			strings.HasSuffix(targetComponentID, generationSuffix)
	}
	if (action == RuntimeComponentLifecycleActivate || action == RuntimeComponentLifecycleDrain) && role != ComponentRoleEdge {
		return false
	}
	prefix := "gordon-" + string(role) + "-"
	return strings.HasPrefix(targetComponentID, prefix) &&
		strings.TrimSuffix(strings.TrimPrefix(targetComponentID, prefix), generationSuffix) == migrationID &&
		strings.HasSuffix(targetComponentID, generationSuffix)
}

// ApprovedGeneratedRolePath validates an approved generated config or env manifest path.
func ApprovedGeneratedRolePath(path, migrationRoot, kind, migrationID string, generation uint64, name string) bool {
	if !filepath.IsAbs(path) || migrationID == "" || generation == 0 || filepath.Base(path) != name {
		return false
	}
	if strings.TrimSpace(migrationRoot) != "" {
		root := filepath.Clean(migrationRoot)
		expected := filepath.Join(root, kind, migrationID, strconv.FormatUint(generation, 10), name)
		if !filepath.IsAbs(root) || path != expected {
			return false
		}
	}
	generationDir := filepath.Dir(path)
	migrationDir := filepath.Dir(generationDir)
	kindDir := filepath.Dir(migrationDir)
	return filepath.Base(generationDir) == strconv.FormatUint(generation, 10) &&
		filepath.Base(migrationDir) == migrationID &&
		filepath.Base(kindDir) == kind &&
		filepath.Base(filepath.Dir(kindDir)) == "migration"
}

// ValidManagedControlSecretsVolume reports whether value is a reserved control secrets volume name.
func ValidManagedControlSecretsVolume(value string) bool {
	if !strings.HasPrefix(value, managedControlSecretsVolumePrefix) ||
		len(value) != len(managedControlSecretsVolumePrefix)+managedControlSecretsVolumeSuffixLen {
		return false
	}
	for _, char := range strings.TrimPrefix(value, managedControlSecretsVolumePrefix) {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// MatchComponentGenerationVolume reports the expected generation volume for an inspected component.
func MatchComponentGenerationVolume(inspected *Container) (string, bool) {
	if inspected == nil || inspected.Labels == nil {
		return "", false
	}
	role := ComponentRole(inspected.Labels[LabelComponentRole])
	if !IsKnownComponentRole(role) || role == ComponentRoleEdge {
		return "", false
	}
	migrationID := inspected.Labels[LabelComponentMigrationID]
	generationLabel := inspected.Labels[LabelComponentGeneration]
	parsedGeneration, err := strconv.ParseUint(generationLabel, 10, 64)
	if err != nil || parsedGeneration == 0 || strconv.FormatUint(parsedGeneration, 10) != generationLabel ||
		!ValidComponentMigrationID(migrationID) || strings.TrimSpace(migrationID) != migrationID {
		return "", false
	}
	expected := FormatComponentGenerationVolumeName(role, migrationID, parsedGeneration)
	return expected, inspected.Name == expected
}
