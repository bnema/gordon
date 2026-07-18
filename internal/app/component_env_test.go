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

	secret := "fixture-value-not-for-reports"
	manifest, err := BuildComponentEnvManifest(ComponentEnvManifestOptions{
		Config: cfg,
		Environment: map[string]string{
			"GORDON_SERVER_PORT":         "8080",
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

	assert.ElementsMatch(t, []string{"GORDON_SERVER_PORT", "OTEL_EXPORTER_OTLP_HEADERS", "PASSWORD_STORE_DIR", "SAFE_FEATURE_FLAG"}, manifest.KeysForRole(domain.ComponentRoleControl))
	assert.ElementsMatch(t, []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "DOCKER_HOST", "OTEL_EXPORTER_OTLP_HEADERS"}, manifest.KeysForRole(domain.ComponentRoleRuntime))
	assert.ElementsMatch(t, []string{"CLOUDFLARE_DNS_API_TOKEN", "OTEL_EXPORTER_OTLP_HEADERS"}, manifest.KeysForRole(domain.ComponentRoleEdge))
	assert.ElementsMatch(t, []string{"OTEL_EXPORTER_OTLP_HEADERS"}, manifest.KeysForRole(domain.ComponentRoleRegistry))
	assert.NotContains(t, strings.Join(manifest.KeysForRole(domain.ComponentRoleEdge), ","), "DOCKER_HOST")
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
		Config:      Config{},
		Environment: map[string]string{"GORDON_SERVER_PORT": "8080"},
		Directory:   filepath.Join(t.TempDir(), "env"),
	})
	require.NoError(t, err)
	checkpoint, err := service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1})
	require.NoError(t, err)
	require.NotEmpty(t, checkpoint.EnvFileReferences)
	require.Len(t, checkpoint.ConfigFileReferences, 4)
	assert.NotContains(t, strings.Join(checkpoint.EnvFileReferences, " "), "8080")
	for _, reference := range checkpoint.ConfigFileReferences {
		info, statErr := os.Stat(reference)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}
