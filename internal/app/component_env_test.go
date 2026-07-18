package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestComponentEnvManifestDetectsConfigDrivenVariablesAndMinimizesRoles(t *testing.T) {
	cfg := Config{}
	cfg.Auth.SecretsBackend = string(domain.SecretsBackendPass)
	cfg.TLS.ACME.Enabled = true
	cfg.TLS.ACME.Challenge = string(domain.ACMEChallengeCloudflareDNS01)
	cfg.Backups.Volumes.Enabled = true
	cfg.Backups.Volumes.S3.Bucket = "fixtures"
	cfg.Telemetry.Enabled = true
	cfg.Runtime.Token = "private-runtime-handoff-token"

	secret := "fixture-value-not-for-reports"
	manifest, err := BuildComponentEnvManifest(ComponentEnvManifestOptions{
		Config: cfg,
		Environment: map[string]string{
			"GORDON_SERVER_PORT":         "8080",
			TokenSecretEnvVar:            "fixture-token-secret-at-least-32-characters",
			"GORDON_MIGRATION_IMAGE":     "fixture.invalid/gordon:next",
			"PASSWORD_STORE_DIR":         "/redacted/pass",
			"CLOUDFLARE_DNS_API_TOKEN":   secret,
			"AWS_ACCESS_KEY_ID":          "fixture-access",
			"AWS_SECRET_ACCESS_KEY":      secret,
			"OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer " + secret,
			"DOCKER_HOST":                "unix:///redacted/podman.sock",
			"SAFE_FEATURE_FLAG":          "enabled",
			"WORKLOAD_DATABASE_PASSWORD": secret,
		},
		ExplicitAllowlist: []string{"SAFE_FEATURE_FLAG"},
	})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"GORDON_COMPONENT_RUNTIME_TOKEN", "GORDON_SERVER_PORT", TokenSecretEnvVar, "OTEL_EXPORTER_OTLP_HEADERS", "PASSWORD_STORE_DIR", "SAFE_FEATURE_FLAG"}, manifest.KeysForRole(domain.ComponentRoleControl))
	assert.ElementsMatch(t, []string{"GORDON_COMPONENT_RUNTIME_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "DOCKER_HOST", TokenSecretEnvVar, "OTEL_EXPORTER_OTLP_HEADERS"}, manifest.KeysForRole(domain.ComponentRoleRuntime))
	assert.ElementsMatch(t, []string{"CLOUDFLARE_DNS_API_TOKEN", "OTEL_EXPORTER_OTLP_HEADERS", "GORDON_COMPONENT_EDGE_TOKEN", "GORDON_MIGRATION_PROBE_TOKEN", registryForwardTokenEnvVar}, manifest.KeysForRole(domain.ComponentRoleEdge))
	assert.ElementsMatch(t, []string{"OTEL_EXPORTER_OTLP_HEADERS", "GORDON_COMPONENT_REGISTRY_TOKEN", registryForwardTokenEnvVar}, manifest.KeysForRole(domain.ComponentRoleRegistry))
	assert.NotContains(t, strings.Join(manifest.KeysForRole(domain.ComponentRoleEdge), ","), "DOCKER_HOST")
	assert.Contains(t, manifest.KeysForRole(domain.ComponentRoleEdge), "GORDON_COMPONENT_EDGE_TOKEN")
	assert.Contains(t, manifest.KeysForRole(domain.ComponentRoleRegistry), "GORDON_COMPONENT_REGISTRY_TOKEN")
	assert.NotContains(t, manifest.KeysForRole(domain.ComponentRoleEdge), "GORDON_COMPONENT_RUNTIME_TOKEN")
	assert.NotContains(t, strings.Join(manifest.KeysForRole(domain.ComponentRoleRegistry), ","), "WORKLOAD_DATABASE_PASSWORD")
	assert.NotContains(t, manifest.RedactedSummary(), secret)

	files, err := manifest.WriteFiles(filepath.Join(t.TempDir(), "component-env"))
	require.NoError(t, err)
	require.Len(t, files, 4)
	for _, file := range files {
		info, statErr := os.Stat(file.Path)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		body, readErr := os.ReadFile(file.Path)
		require.NoError(t, readErr)
		assert.NotContains(t, body, "WORKLOAD_DATABASE_PASSWORD")
	}
}

func TestRegistryForwardCredentialIsDomainSeparatedAndRoleMinimized(t *testing.T) {
	seed := "private-runtime-handoff-token"
	cfg := Config{}
	cfg.Runtime.Token = seed
	manifest, err := BuildComponentEnvManifest(ComponentEnvManifestOptions{Config: cfg})
	require.NoError(t, err)

	credential := migrationRegistryForwardToken(seed)
	assert.NotEmpty(t, credential)
	assert.NotEqual(t, seed, credential)
	assert.NotEqual(t, migrationComponentToken(seed, domain.ComponentRoleEdge), credential)
	assert.NotEqual(t, migrationComponentToken(seed, domain.ComponentRoleRegistry), credential)
	for _, role := range []domain.ComponentRole{domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		assert.Contains(t, manifest.KeysForRole(role), registryForwardTokenEnvVar)
	}
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime} {
		assert.NotContains(t, manifest.KeysForRole(role), registryForwardTokenEnvVar)
	}
	assert.NotContains(t, manifest.RedactedSummary(), credential)
}

func TestMigrationProbeCredentialIsDomainSeparatedAndEdgeOnly(t *testing.T) {
	seed := "private-runtime-handoff-token"
	cfg := Config{}
	cfg.Runtime.Token = seed
	manifest, err := BuildComponentEnvManifest(ComponentEnvManifestOptions{Config: cfg})
	require.NoError(t, err)
	assert.NotEqual(t, seed, migrationProbeToken(seed))
	assert.NotEqual(t, migrationComponentToken(seed, domain.ComponentRoleEdge), migrationProbeToken(seed))
	assert.Contains(t, manifest.KeysForRole(domain.ComponentRoleEdge), "GORDON_MIGRATION_PROBE_TOKEN")
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry} {
		assert.NotContains(t, manifest.KeysForRole(role), "GORDON_MIGRATION_PROBE_TOKEN")
	}
	assert.NotContains(t, manifest.RedactedSummary(), migrationProbeToken(seed))
}

func TestComponentEnvManifestMissingAndExplicitVariableErrorsAreKeyOnly(t *testing.T) {
	cfg := Config{}
	cfg.TLS.ACME.Enabled = true
	cfg.TLS.ACME.Challenge = string(domain.ACMEChallengeCloudflareDNS01)
	secret := "never-display-this-value"
	_, err := BuildComponentEnvManifest(ComponentEnvManifestOptions{Config: cfg, Environment: map[string]string{"CLOUDFLARE_DNS_API_TOKEN": ""}})
	require.Error(t, err)
	var missing *MissingEnvVarError
	require.True(t, errors.As(err, &missing))
	assert.Equal(t, []string{"CLOUDFLARE_DNS_API_TOKEN"}, missing.Keys)
	assert.NotContains(t, err.Error(), secret)

	for _, key := range []string{"CUSTOM_TOKEN", "WORKLOAD_DATABASE_PASSWORD"} {
		_, err = BuildComponentEnvManifest(ComponentEnvManifestOptions{
			Environment:       map[string]string{key: secret},
			ExplicitAllowlist: []string{key},
		})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
	}
}

func TestMigrationEnvOptionsAcceptOnlyAllowlistedExplicitEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "explicit.env")
	require.NoError(t, os.WriteFile(path, []byte("SAFE_FEATURE_FLAG=fixture-value-not-reported\n"), 0o600))
	manifest, err := BuildMigrationComponentEnvManifest(MigrationEnvOptions{
		Environment:       map[string]string{},
		ExplicitAllowlist: []string{"SAFE_FEATURE_FLAG"},
		ExplicitEnvFile:   path,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"SAFE_FEATURE_FLAG"}, manifest.KeysForRole(domain.ComponentRoleControl))

	require.NoError(t, os.WriteFile(path, []byte("CUSTOM_TOKEN=fixture-value-not-reported\n"), 0o600))
	_, err = BuildMigrationComponentEnvManifest(MigrationEnvOptions{ExplicitAllowlist: []string{"CUSTOM_TOKEN"}, ExplicitEnvFile: path})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "fixture-value-not-reported")
}

func TestMigrationPrepareWritesOnlyRoleScopedReferences(t *testing.T) {
	preflight := NewMigrationPreflight(MigrationPreflightProbes{
		Runtime: func(_ context.Context) (RuntimePreflightTarget, error) {
			return RuntimePreflightTarget{Engine: "podman", Rootless: true, APIReachable: true}, nil
		},
		Image: func(context.Context) error { return nil }, Config: func(context.Context) error { return nil }, DataDir: func(context.Context) error { return nil }, Registry: func(context.Context) error { return nil }, Env: func(context.Context) error { return nil }, Secrets: func(context.Context) error { return nil }, Ports: func(context.Context) error { return nil }, Network: func(context.Context) error { return nil }, Inventory: func(context.Context) error { return nil }, Disk: func(context.Context) error { return nil }, Credentials: func(context.Context) error { return nil },
	})
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	service, err := NewMigrationService(preflight, store, MigrationEnvOptions{
		Config:         Config{},
		Environment:    map[string]string{"GORDON_SERVER_PORT": "8080"},
		Directory:      filepath.Join(t.TempDir(), "env"),
		ExternalRoutes: map[string]any{"public.example.test": "198.51.100.10:8443"},
	})
	require.NoError(t, err)
	checkpoint, err := service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1})
	require.NoError(t, err)
	require.NotEmpty(t, checkpoint.EnvFileReferences)
	require.Len(t, checkpoint.ConfigFileReferences, 4)
	assert.NotContains(t, strings.Join(checkpoint.EnvFileReferences, " "), "8080")
	controlConfig := componentConfigReferences(checkpoint.ConfigFileReferences)[domain.ComponentRoleControl]
	controlContents, readErr := os.ReadFile(controlConfig)
	require.NoError(t, readErr)
	assert.Contains(t, string(controlContents), "public.example.test")
	for _, reference := range checkpoint.ConfigFileReferences {
		info, statErr := os.Stat(reference)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}
