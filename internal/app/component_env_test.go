package app

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/docker"
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
			"PASSWORD_STORE_DIR":         "/host/password-store",
			"GNUPGHOME":                  "/host/gnupg",
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

	assert.ElementsMatch(t, []string{"GORDON_COMPONENT_RUNTIME_TOKEN", "GORDON_SERVER_PORT", "GNUPGHOME", "OTEL_EXPORTER_OTLP_HEADERS", "PASSWORD_STORE_DIR", "SAFE_FEATURE_FLAG"}, manifest.KeysForRole(domain.ComponentRoleControl))
	paths := managedPassPaths()
	assert.Equal(t, paths.GPGHome, manifest.values[domain.ComponentRoleControl]["GNUPGHOME"])
	assert.Equal(t, paths.StoreDir, manifest.values[domain.ComponentRoleControl]["PASSWORD_STORE_DIR"])
	assert.NotEqual(t, "/host/gnupg", manifest.values[domain.ComponentRoleControl]["GNUPGHOME"])
	assert.NotEqual(t, "/host/password-store", manifest.values[domain.ComponentRoleControl]["PASSWORD_STORE_DIR"])
	assert.ElementsMatch(t, []string{"GORDON_COMPONENT_RUNTIME_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "OTEL_EXPORTER_OTLP_HEADERS"}, manifest.KeysForRole(domain.ComponentRoleRuntime))
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

func TestAuthEnabledMigrationRequiresEnvironmentSigningSecretAndScopesItsValue(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Enabled = true
	cfg.Auth.Type = "token"
	cfg.Auth.SecretsBackend = "unsafe"
	cfg.Auth.TokenSecret = "legacy-operator-owned-reference-must-not-transfer"
	secret := "migration-signing-secret-at-least-32-bytes"

	missingManifest, err := BuildComponentEnvManifest(ComponentEnvManifestOptions{Config: cfg, Environment: map[string]string{}})
	require.Error(t, err)
	require.NotNil(t, missingManifest)
	var missing *MissingEnvVarError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, []string{TokenSecretEnvVar}, missing.Keys)
	assert.NotContains(t, err.Error(), cfg.Auth.TokenSecret)

	shortManifest, err := BuildComponentEnvManifest(ComponentEnvManifestOptions{Config: cfg, Environment: map[string]string{TokenSecretEnvVar: "too-short"}})
	require.Error(t, err)
	require.NotNil(t, shortManifest)
	assert.Equal(t, "component environment variable is invalid: "+TokenSecretEnvVar, err.Error())
	assert.NotContains(t, err.Error(), "too-short")
	assert.NotContains(t, err.Error(), "32")
	var shortMissing *MissingEnvVarError
	assert.False(t, errors.As(err, &shortMissing), "a present but short key must be distinguished from an absent key")
	for _, role := range componentRoles {
		assert.NotContains(t, shortManifest.KeysForRole(role), TokenSecretEnvVar)
	}

	manifest, err := BuildComponentEnvManifest(ComponentEnvManifestOptions{Config: cfg, Environment: map[string]string{TokenSecretEnvVar: secret}})
	require.NoError(t, err)
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry} {
		assert.Contains(t, manifest.KeysForRole(role), TokenSecretEnvVar)
		assert.Equal(t, secret, manifest.values[role][TokenSecretEnvVar])
	}
	assert.NotContains(t, manifest.KeysForRole(domain.ComponentRoleEdge), TokenSecretEnvVar)
	assert.NotContains(t, manifest.RedactedSummary(), secret)
	assert.NotContains(t, manifest.RedactedSummary(), cfg.Auth.TokenSecret)
	serialized, err := json.Marshal(manifest)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), secret)
	assert.NotContains(t, string(serialized), cfg.Auth.TokenSecret)

	files, err := manifest.WriteFiles(filepath.Join(t.TempDir(), "auth-env"))
	require.NoError(t, err)
	for _, file := range files {
		body, readErr := os.ReadFile(file.Path)
		require.NoError(t, readErr)
		if file.Role == domain.ComponentRoleEdge {
			assert.NotContains(t, string(body), secret)
			continue
		}
		assert.Contains(t, string(body), TokenSecretEnvVar+"="+secret)
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

func TestMigrationEnvUsesSelectedLocalEndpointDespiteConflictingAmbientVariables(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///ambient/docker.sock")
	endpoint, err := selectedLocalRuntimeEndpointFromDetection(docker.DetectRuntimeSocket("/run/user/1000/podman/native-api"))
	require.NoError(t, err)

	manifest, err := BuildMigrationComponentEnvManifest(MigrationEnvOptions{
		Environment: map[string]string{
			"DOCKER_HOST": "unix:///ambient/docker.sock",
			"PODMAN_HOST": "ssh://ambient.example.invalid/run/podman.sock",
		},
		runtimeEndpoint: endpoint,
	})
	require.NoError(t, err)
	assert.Equal(t, "unix:///run/user/1000/podman/native-api", manifest.values[domain.ComponentRoleRuntime]["DOCKER_HOST"])
	assert.NotContains(t, manifest.RedactedSummary(), "native-api")
}

func TestMigrationEnvPropagatesSelectedRuntimeEndpointWithoutHostEndpoint(t *testing.T) {
	const selectedPath = "/run/user/1000/podman/podman.sock"
	endpoint, err := newSelectedLocalRuntimeEndpoint(selectedPath)
	require.NoError(t, err)
	manifest, err := BuildMigrationComponentEnvManifest(MigrationEnvOptions{
		Environment:     map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"},
		runtimeEndpoint: endpoint,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"DOCKER_HOST"}, manifest.KeysForRole(domain.ComponentRoleRuntime))
	assert.Equal(t, "unix://"+selectedPath, manifest.values[domain.ComponentRoleRuntime]["DOCKER_HOST"])
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		assert.Empty(t, manifest.KeysForRole(role))
	}
	assert.NotContains(t, manifest.RedactedSummary(), selectedPath)
}

func TestSelectedLocalRuntimeEndpointRejectsNonLocalOrAmbiguousPathsWithoutValueLeakage(t *testing.T) {
	for name, path := range map[string]string{
		"absent":   "",
		"remote":   "ssh://private-host.example/run/podman.sock",
		"relative": "private/podman.sock",
		"unclean":  "/run/user/1000/../private/podman.sock",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newSelectedLocalRuntimeEndpoint(path)
			require.Error(t, err)
			if path != "" {
				assert.NotContains(t, err.Error(), path)
			}
		})
	}
}

func TestPrivateExplicitEnvOpenAnchorsDescriptorAcrossPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "explicit.env")
	const original = "SAFE_FEATURE_FLAG=original\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

	file, err := openPrivateRegularFileNoFollow(path)
	require.NoError(t, err)
	defer file.Close()
	require.NoError(t, os.Rename(path, path+".opened"))
	require.NoError(t, os.WriteFile(path, []byte("SAFE_FEATURE_FLAG=replacement\n"), 0o600))

	data, err := io.ReadAll(file)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
}

func TestExplicitEnvFileRejectsSymlinkAndOversizedDescriptor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.env")
	link := filepath.Join(root, "explicit.env")
	require.NoError(t, os.WriteFile(target, []byte("SAFE_FEATURE_FLAG=value\n"), 0o600))
	require.NoError(t, os.Symlink(target, link))
	_, err := readExplicitComponentEnvFile(link, []string{"SAFE_FEATURE_FLAG"})
	require.Error(t, err)

	oversized := filepath.Join(root, "oversized.env")
	require.NoError(t, os.WriteFile(oversized, make([]byte, maxExplicitComponentEnvBytes+1), 0o600))
	_, err = readExplicitComponentEnvFile(oversized, []string{"SAFE_FEATURE_FLAG"})
	require.Error(t, err)
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
