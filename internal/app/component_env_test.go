package app

import (
	"context"
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

func TestAuthDisabledMigrationDoesNotTransferJWTOrManagedPassEnvironment(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Enabled = false
	cfg.Auth.Type = "token"
	cfg.Auth.SecretsBackend = string(domain.SecretsBackendPass)
	endpoint, err := newSelectedLocalRuntimeEndpoint("/run/user/1000/podman/podman.sock")
	require.NoError(t, err)

	manifest, err := BuildMigrationComponentEnvManifest(MigrationEnvOptions{
		Config: cfg,
		Environment: map[string]string{
			"XDG_RUNTIME_DIR":          "/run/user/1000",
			"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus",
			TokenSecretEnvVar:          "unused-auth-disabled-signing-secret-at-least-32-bytes",
		},
		runtimeEndpoint: endpoint,
	})
	require.NoError(t, err)

	for _, role := range componentRoles {
		assert.NotContains(t, manifest.KeysForRole(role), TokenSecretEnvVar)
	}
	assert.ElementsMatch(t, []string{"GNUPGHOME", "PASSWORD_STORE_DIR"}, manifest.KeysForRole(domain.ComponentRoleControl))
	assert.Equal(t, []string{"DOCKER_HOST"}, manifest.KeysForRole(domain.ComponentRoleRuntime))
	for _, role := range []domain.ComponentRole{domain.ComponentRoleRuntime, domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		assert.NotContains(t, manifest.KeysForRole(role), "GNUPGHOME")
		assert.NotContains(t, manifest.KeysForRole(role), "PASSWORD_STORE_DIR")
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

func TestAuthEnabledMissingSigningSecretFailsPlanBeforeComponentLaunchWithoutValueLeak(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Enabled = true
	cfg.Auth.Type = "token"
	cfg.Auth.SecretsBackend = "unsafe"
	cfg.Auth.TokenSecret = "legacy-signing-reference-never-transfer-or-report"
	preflight := NewMigrationPreflight(passingMigrationProbes(nil))
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	launcher := &recordingComponentLauncher{}
	orchestrator, err := NewMigrationOrchestrator(preflight, store, launcher)
	require.NoError(t, err)
	service, err := NewMigrationService(preflight, store, MigrationEnvOptions{
		Config: cfg,
		Environment: map[string]string{
			"XDG_RUNTIME_DIR":          "/run/user/1000",
			"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus",
		},
		Directory: filepath.Join(t.TempDir(), "env"),
	})
	require.NoError(t, err)
	service.WithMigrationOrchestrator(orchestrator).WithMigrationCandidateImage("example.invalid/gordon:v3")

	report, err := service.Plan(context.Background())
	require.NoError(t, err)
	assert.False(t, report.Ready)
	check := findPreflightCheck(t, report, "component_environment")
	assert.Equal(t, PreflightFail, check.Status)
	assert.Contains(t, check.Remediation, TokenSecretEnvVar)
	assert.NotContains(t, check.Remediation, cfg.Auth.TokenSecret)

	_, err = service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), TokenSecretEnvVar)
	assert.NotContains(t, err.Error(), cfg.Auth.TokenSecret)
	assert.Empty(t, launcher.calls)
	assert.NoFileExists(t, store.Path())
}

func TestAuthEnabledShortSigningSecretFailsBeforeComponentLaunchWithKeyOnlyDiagnostic(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Enabled = true
	cfg.Auth.Type = "token"
	secret := "short-private-fixture"
	preflight := NewMigrationPreflight(passingMigrationProbes(nil))
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	launcher := &recordingComponentLauncher{}
	orchestrator, err := NewMigrationOrchestrator(preflight, store, launcher)
	require.NoError(t, err)
	service, err := NewMigrationService(preflight, store, MigrationEnvOptions{
		Config:      cfg,
		Environment: map[string]string{TokenSecretEnvVar: secret},
		Directory:   filepath.Join(t.TempDir(), "env"),
	})
	require.NoError(t, err)
	service.WithMigrationOrchestrator(orchestrator).WithMigrationCandidateImage("example.invalid/gordon:v3")

	report, err := service.Plan(context.Background())
	require.NoError(t, err)
	require.False(t, report.Ready)
	check := findPreflightCheck(t, report, "component_environment")
	assert.Equal(t, "component environment variable is invalid: "+TokenSecretEnvVar, check.Remediation)
	assert.NotContains(t, check.Remediation, secret)
	assert.NotContains(t, check.Remediation, "32")

	_, err = service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration"})
	require.Error(t, err)
	assert.Equal(t, "component environment variable is invalid: "+TokenSecretEnvVar, err.Error())
	assert.Empty(t, launcher.calls)
	assert.NoFileExists(t, store.Path())
}

func TestAuthSigningSecretNeverAppearsInMigrationReportsOrCheckpoint(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Enabled = true
	cfg.Auth.Type = "token"
	cfg.Auth.SecretsBackend = "unsafe"
	secret := "checkpoint-private-signing-secret-at-least-32-bytes"
	preflight := NewMigrationPreflight(passingMigrationProbes(nil))
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	service, err := NewMigrationService(preflight, store, MigrationEnvOptions{
		Config:      cfg,
		Environment: map[string]string{TokenSecretEnvVar: secret},
		Directory:   filepath.Join(t.TempDir(), "env"),
	})
	require.NoError(t, err)

	report, err := service.Plan(context.Background())
	require.NoError(t, err)
	require.True(t, report.Ready)
	checkpoint, err := service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1})
	require.NoError(t, err)
	status, err := service.Status()
	require.NoError(t, err)

	for _, value := range []any{report, checkpoint, status} {
		serialized, marshalErr := json.Marshal(value)
		require.NoError(t, marshalErr)
		assert.NotContains(t, string(serialized), secret)
	}
	stored, err := os.ReadFile(store.Path())
	require.NoError(t, err)
	assert.NotContains(t, string(stored), secret)
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

func TestMigrationPrepareCanonicalizesEnvOnlyAuthConfigAcrossConsumingRoles(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Enabled = true
	cfg.Auth.Type = "token"
	cfg.Auth.SecretsBackend = "unsafe"
	cfg.Auth.Username = "operator"
	cfg.Auth.TokenExpiry = "48h"
	secret := "migration-canonical-auth-secret-at-least-32-bytes"
	t.Setenv(TokenSecretEnvVar, secret)
	preflight := NewMigrationPreflight(passingMigrationProbes(nil))
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	service, err := NewMigrationService(preflight, store, MigrationEnvOptions{
		Config: cfg,
		Environment: map[string]string{
			"GORDON_AUTH_ACCESS_TOKEN_TTL": "37m",
			TokenSecretEnvVar:              secret,
		},
		Directory: filepath.Join(t.TempDir(), "env"),
	})
	require.NoError(t, err)

	checkpoint, err := service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1})
	require.NoError(t, err)
	configs := componentConfigReferences(checkpoint.ConfigFileReferences)

	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime} {
		_, roleConfig, configErr := initConfig(configs[role])
		require.NoError(t, configErr)
		assert.Equal(t, "37m", roleConfig.Auth.AccessTokenTTL, "%s must use the effective env-only access token TTL", role)
		assert.Equal(t, cfg.Auth.TokenExpiry, roleConfig.Auth.TokenExpiry)
		assert.Equal(t, cfg.Auth.Username, roleConfig.Auth.Username)
	}
	registryConfig, err := initRegistryConfig(configs[domain.ComponentRoleRegistry])
	require.NoError(t, err)
	assert.Equal(t, "37m", registryConfig.Auth.AccessTokenTTL)
	assert.Equal(t, cfg.Auth.TokenExpiry, registryConfig.Auth.TokenExpiry)
	assert.Equal(t, cfg.Auth.Username, registryConfig.Auth.Username)

	for _, reference := range checkpoint.ConfigFileReferences {
		contents, readErr := os.ReadFile(reference)
		require.NoError(t, readErr)
		assert.NotContains(t, string(contents), secret)
	}
	for _, reference := range checkpoint.EnvFileReferences {
		contents, readErr := os.ReadFile(reference)
		require.NoError(t, readErr)
		if filepath.Base(reference) == "edge.env" {
			assert.NotContains(t, string(contents), TokenSecretEnvVar)
			continue
		}
		assert.Contains(t, string(contents), TokenSecretEnvVar+"="+secret)
	}
}

func TestMigrationPrepareWritesOnlyRoleScopedReferences(t *testing.T) {
	preflight := NewMigrationPreflight(MigrationPreflightProbes{
		Runtime: func(_ context.Context) (RuntimePreflightTarget, error) {
			return RuntimePreflightTarget{Engine: "podman", Rootless: true, APIReachable: true}, nil
		},
		Image: func(context.Context) error { return nil }, Config: func(context.Context) error { return nil }, SplitTopology: func(context.Context) error { return nil }, DataDir: func(context.Context) error { return nil }, Registry: func(context.Context) error { return nil }, Env: func(context.Context) error { return nil }, Secrets: func(context.Context) error { return nil }, Ports: func(context.Context) error { return nil }, Network: func(context.Context) error { return nil }, Inventory: func(context.Context) error { return nil }, Disk: func(context.Context) error { return nil }, Credentials: func(context.Context) error { return nil },
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
