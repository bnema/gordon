package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
	"github.com/bnema/gordon/internal/usecase/traffic"
)

func TestGeneratedRuntimePolicyKeepsHostInstallationIdentityAcrossGenerations(t *testing.T) {
	firstRoot := filepath.Join(t.TempDir(), "installation")
	secondRoot := filepath.Join(t.TempDir(), "installation")
	generatedVolume := func(t *testing.T, root, generation string) string {
		t.Helper()
		cfg := Config{}
		cfg.Server.DataDir = root
		files, err := WriteComponentConfigManifests(cfg, filepath.Join(root, "migration", "config", "fixture", generation))
		require.NoError(t, err)
		_, generated, err := initConfig(componentConfigReferences(componentConfigPaths(files))[domain.ComponentRoleRuntime])
		require.NoError(t, err)
		policy := runtimeRolePolicy(generated, nil)
		return policy.ManagedControlSecretsVolume
	}

	firstGeneration := generatedVolume(t, firstRoot, "1")
	assert.Equal(t, firstGeneration, generatedVolume(t, firstRoot, "2"))
	assert.NotEqual(t, firstGeneration, generatedVolume(t, secondRoot, "1"))
	assert.Regexp(t, `^gordon-control-secrets-[0-9a-f]{16}$`, firstGeneration)
}

func TestWriteComponentConfigManifestsScopesRolesAndPermissions(t *testing.T) {
	cfg := Config{}
	cfg.Server.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Server.Port = 8080
	cfg.Server.RegistryPort = 5000
	cfg.Auth.TokenSecret = "control-token-reference"
	cfg.Auth.SecretsBackend = "unsafe"
	cfg.Runtime.Token = "private-runtime-handoff-token"
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	require.Len(t, files, 4)
	byRole := componentConfigReferences(componentConfigPaths(files))
	require.Contains(t, byRole, domain.ComponentRoleControl)
	require.Contains(t, byRole, domain.ComponentRoleRuntime)
	require.Contains(t, byRole, domain.ComponentRoleRegistry)
	require.Contains(t, byRole, domain.ComponentRoleEdge)
	for _, file := range files {
		info, statErr := os.Stat(file.Path)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	edge, err := os.ReadFile(byRole[domain.ComponentRoleEdge])
	require.NoError(t, err)
	assert.NotContains(t, string(edge), "control-token-reference")
	assert.Contains(t, string(edge), "[edge]")
	assert.Contains(t, string(edge), "token_env")
	control, err := os.ReadFile(byRole[domain.ComponentRoleControl])
	require.NoError(t, err)
	assert.Contains(t, string(control), "endpoint = 'unix:///var/lib/gordon/migration/fixture/runtime-control.sock'")
	assert.NotContains(t, string(control), "127.0.0.1:19444")
	runtime, err := os.ReadFile(byRole[domain.ComponentRoleRuntime])
	require.NoError(t, err)
	assert.Contains(t, string(runtime), "token_env = 'GORDON_COMPONENT_RUNTIME_TOKEN'")
	assert.Contains(t, string(runtime), "[auth]")
	assert.Contains(t, string(runtime), "secrets_backend = 'unsafe'", "runtime must initialize the component-token validator before binding its Unix socket")
	registry, err := os.ReadFile(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err)
	assert.NotContains(t, string(registry), "control-token-reference")
	assert.Contains(t, string(registry), "[storage]")
	assert.Contains(t, string(registry), "event_token_env")
	for _, file := range files {
		assert.Equal(t, ".toml", filepath.Ext(file.Path), "role configs must be consumable by role TOML decoders")
	}
}

func TestComponentControlConfigOwnsSafeRoutingContractWithoutAuthorityLeaks(t *testing.T) {
	cfg := Config{}
	cfg.Server.DataDir = "/host/private-data"
	cfg.Server.Port = 18080
	cfg.Server.RegistryPort = 15000
	cfg.Server.GordonDomain = "app.example.test"
	cfg.Server.RegistryDomain = "registry.example.test"
	cfg.Server.LegacyRegistryDomains = []string{"old-registry.example.test"}
	cfg.Server.TLSCertFile = "/host/private-cert.pem"
	cfg.Server.TLSKeyFile = "/host/private-key.pem"
	cfg.Control.EdgeAlias = "gordon-edge"
	cfg.Control.RegistryAlias = "gordon-registry"
	cfg.Control.RegistryPort = 15000
	cfg.Control.DrainRegistrationTimeout = "7s"
	cfg.Runtime.Endpoint = "unix:///host/podman.sock"
	cfg.Runtime.Token = "runtime-token"
	cfg.Auth.TokenSecret = "auth-token-reference"
	cfg.Auth.Username = "admin"
	cfg.Services = []servicecfg.Config{{Name: "metrics", Image: "example.test/metrics:latest", Enabled: true, Env: []string{"PASSWORD=never-copy"}, EnvFile: "/host/service.env", Secrets: []servicecfg.SecretRefConfig{{Name: "api", Key: "token"}}, Ports: []servicecfg.PortConfig{{Name: "http", Container: 9090, Protocol: domain.NetworkProtocolTCP, Publish: "198.51.100.10:9090"}}}}
	cfg.EntryPoints = map[string]traffic.EntryPointConfig{"metrics": {Address: ":9000", Protocol: domain.EntryPointProtocolTCP}}
	cfg.Traffic.TCP.Routers = []traffic.RouterConfig{{Name: "metrics", EntryPoint: "metrics", Service: "metrics:http"}}
	cfg.NetworkServices = []traffic.NetworkServiceConfig{{Name: "dns", Ports: []traffic.PortConfig{{Name: "dns", Container: 53, Protocol: domain.NetworkProtocolUDP}}}}

	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"), ComponentConfigOptions{ExternalRoutes: map[string]any{"public.example.test": "198.51.100.10:8443"}})
	require.NoError(t, err)
	control, err := os.ReadFile(componentConfigReferences(componentConfigPaths(files))[domain.ComponentRoleControl])
	require.NoError(t, err)
	contents := string(control)
	for _, required := range []string{"gordon_domain = 'app.example.test'", "registry_domain = 'registry.example.test'", "legacy_registry_domains = ['old-registry.example.test']", "edge_alias = 'gordon-edge'", "registry_alias = 'gordon-registry'", "drain_registration_timeout = '7s'", "[external_routes]", "public.example.test", "[entrypoints.metrics]", "[traffic.tcp]", "[[network_services]]", "[[services]]"} {
		assert.Contains(t, contents, required)
	}
	for _, forbidden := range []string{"/host/private-key.pem", "/host/private-cert.pem", "/host/podman.sock", "runtime-token", "auth-token-reference", "PASSWORD=never-copy", "/host/service.env", "token'"} {
		assert.NotContains(t, contents, forbidden)
	}
}

func TestGeneratedControlConfigPublishesInitialRegistryForwardingTarget(t *testing.T) {
	cfg := Config{}
	cfg.Server.RegistryDomain = "registry.example.test"
	cfg.Server.RegistryPort = 15000
	cfg.Control.RegistryAlias = "gordon-registry"
	cfg.Control.RegistryPort = 15000
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	v, controlCfg, err := initConfig(componentConfigReferences(componentConfigPaths(files))[domain.ComponentRoleControl])
	require.NoError(t, err)
	options, err := controlProducerOptions(v, controlCfg)
	require.NoError(t, err)
	require.NotNil(t, options.Registry)
	assert.Equal(t, edgesnapshot.RegistryTarget{Domain: "registry.example.test", Alias: "gordon-registry", Port: 15000, Scheme: "http", Protocol: domain.RouteTargetProtocolHTTP1}, *options.Registry)
}

func TestGeneratedControlRegistryTargetMatchesRegistryComponentListener(t *testing.T) {
	cfg := Config{}
	cfg.Server.RegistryDomain = "registry.example.test"
	cfg.Server.RegistryPort = 15000
	// The legacy control default must not override the port actually bound by
	// the generated registry role.
	cfg.Control.RegistryAlias = "gordon-registry"
	cfg.Control.RegistryPort = 5000
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	byRole := componentConfigReferences(componentConfigPaths(files))

	v, controlCfg, err := initConfig(byRole[domain.ComponentRoleControl])
	require.NoError(t, err)
	options, err := controlProducerOptions(v, controlCfg)
	require.NoError(t, err)
	require.NotNil(t, options.Registry)
	assert.Equal(t, 15000, options.Registry.Port)

	registryCfg, err := initRegistryConfig(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0:15000", registryCfg.Listen.Address)
}

func TestWriteComponentConfigManifestsRejectsUnsafeExternalRoutes(t *testing.T) {
	_, err := WriteComponentConfigManifests(Config{}, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"), ComponentConfigOptions{ExternalRoutes: map[string]any{"blocked.example.test": "127.0.0.1:8080"}})
	require.ErrorIs(t, err, domain.ErrSSRFBlocked)
}

func TestComponentConfigUsesEnvReferencesForRegistryForwardCredential(t *testing.T) {
	files, err := WriteComponentConfigManifests(Config{}, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	byRole := componentConfigReferences(componentConfigPaths(files))
	for _, role := range []domain.ComponentRole{domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		contents, readErr := os.ReadFile(byRole[role])
		require.NoError(t, readErr)
		assert.Contains(t, string(contents), "token_env = '"+registryForwardTokenEnvVar+"'")
		assert.NotContains(t, string(contents), migrationRegistryForwardToken("private-runtime-handoff-token"))
	}
}

func TestComponentConfigSeparatesPreparedProbeAndFinalPublicEdge(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port, cfg.Server.RegistryPort = 18080, 15000
	cfg.Runtime.Token = "private-runtime-handoff-token"
	finalBinding := MigrationPortBinding{Role: "edge", HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 18080, Protocol: "tcp"}
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"), ComponentConfigOptions{FinalEdgeBinding: &finalBinding})
	require.NoError(t, err)
	preparedPath := componentConfigReferences(componentConfigPaths(files))[domain.ComponentRoleEdge]
	prepared, err := os.ReadFile(preparedPath)
	require.NoError(t, err)
	final, err := os.ReadFile(filepath.Join(filepath.Dir(preparedPath), "edge-final.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(prepared), "migration_probe_enabled = true")
	assert.Contains(t, string(prepared), "migration_hairpin_enabled = false")
	assert.Contains(t, string(prepared), "migration_probe_token_env = 'GORDON_MIGRATION_PROBE_TOKEN'")
	assert.NotContains(t, string(prepared), migrationProbeToken(cfg.Runtime.Token))
	assert.Contains(t, string(final), "migration_probe_enabled = false")
	assert.Contains(t, string(final), "migration_hairpin_enabled = true")
	assert.Contains(t, string(final), "published_host_ip = '127.0.0.1'")
	assert.NotContains(t, string(final), "migration_probe_token_env")
	assert.NotContains(t, string(final), migrationProbeToken(cfg.Runtime.Token))
	finalConfig, err := initEdgeConfig(filepath.Join(filepath.Dir(preparedPath), "edge-final.toml"))
	require.NoError(t, err)
	assert.False(t, finalConfig.Edge.MigrationProbeEnabled)
	assert.True(t, finalConfig.Edge.MigrationHairpinEnabled)
	assert.Equal(t, "127.0.0.1", finalConfig.Edge.PublishedHostIP)
}

func TestComponentConfigDisablesHairpinForPublicFinalBinding(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port = 18080
	binding := MigrationPortBinding{Role: "edge", HostIP: "0.0.0.0", HostPort: 18080, ContainerPort: 18080, Protocol: "tcp"}
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"), ComponentConfigOptions{FinalEdgeBinding: &binding})
	require.NoError(t, err)
	preparedPath := componentConfigReferences(componentConfigPaths(files))[domain.ComponentRoleEdge]
	final, err := os.ReadFile(filepath.Join(filepath.Dir(preparedPath), "edge-final.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(final), "published_host_ip = '0.0.0.0'")
	assert.Contains(t, string(final), "migration_hairpin_enabled = false")
}

func TestComponentConfigManifestsUseStrictRoleSchemas(t *testing.T) {
	cfg := Config{}
	cfg.Server.DataDir = t.TempDir()
	cfg.Server.Port = 18080
	cfg.Server.RegistryPort = 15000
	cfg.Server.MaxBlobChunkSize = "95MB"
	cfg.Server.MaxBlobSize = "1GB"
	cfg.Control.Endpoint = "gordon-control:9443"
	cfg.Control.InsecureTLS = true
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	byRole := componentConfigReferences(componentConfigPaths(files))
	_, err = initEdgeConfig(byRole[domain.ComponentRoleEdge])
	require.NoError(t, err, "edge config must be accepted by its strict decoder")
	_, err = initRegistryConfig(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err, "registry config must be accepted by its strict decoder")
}

func TestComponentConfigManifestsNormalizeSparseServingLimits(t *testing.T) {
	files, err := WriteComponentConfigManifests(Config{}, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	byRole := componentConfigReferences(componentConfigPaths(files))

	registry, err := initRegistryConfig(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err)
	assert.Equal(t, "95MB", registry.Limits.MaxBlobChunkSize)
	assert.Equal(t, "1GB", registry.Limits.MaxBlobSize)

	edge, err := initEdgeConfig(byRole[domain.ComponentRoleEdge])
	require.NoError(t, err)
	assert.Equal(t, "512MB", edge.Edge.MaxProxyBodySize)
	assert.Equal(t, "1GB", edge.Edge.MaxProxyResponseSize)
}

func TestComponentConfigManifestsPreserveExplicitServingLimits(t *testing.T) {
	cfg := Config{}
	cfg.Server.MaxBlobChunkSize = "7MB"
	cfg.Server.MaxBlobSize = "11MB"
	cfg.Server.MaxProxyBodySize = "13MB"
	cfg.Server.MaxProxyResponseSize = "17MB"
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	byRole := componentConfigReferences(componentConfigPaths(files))

	registry, err := initRegistryConfig(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err)
	assert.Equal(t, cfg.Server.MaxBlobChunkSize, registry.Limits.MaxBlobChunkSize)
	assert.Equal(t, cfg.Server.MaxBlobSize, registry.Limits.MaxBlobSize)
	edge, err := initEdgeConfig(byRole[domain.ComponentRoleEdge])
	require.NoError(t, err)
	assert.Equal(t, cfg.Server.MaxProxyBodySize, edge.Edge.MaxProxyBodySize)
	assert.Equal(t, cfg.Server.MaxProxyResponseSize, edge.Edge.MaxProxyResponseSize)
}

func TestComponentConfigManifestsRejectInvalidServingLimitsBeforeLaunch(t *testing.T) {
	cases := []func(*Config){
		func(cfg *Config) { cfg.Server.MaxBlobChunkSize = "not-a-size" },
		func(cfg *Config) { cfg.Server.MaxBlobChunkSize, cfg.Server.MaxBlobSize = "2GB", "1GB" },
		func(cfg *Config) { cfg.Server.MaxProxyBodySize = "not-a-size" },
	}
	for _, configure := range cases {
		cfg := Config{}
		configure(&cfg)
		_, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
		require.Error(t, err)
	}
}

func TestComponentConfigUsesPrivateControlBindAndInternalAlias(t *testing.T) {
	cfg := Config{}
	cfg.Control.ListenAddress = "127.0.0.1:9443"
	cfg.Control.Endpoint = "control.example.test:9443"
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	byRole := componentConfigReferences(componentConfigPaths(files))
	control, err := os.ReadFile(byRole[domain.ComponentRoleControl])
	require.NoError(t, err)
	assert.Contains(t, string(control), "listen_address = '0.0.0.0:9443'")
	assert.NotContains(t, string(control), cfg.Control.ListenAddress)
	for _, role := range []domain.ComponentRole{domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		contents, readErr := os.ReadFile(byRole[role])
		require.NoError(t, readErr)
		assert.Contains(t, string(contents), "gordon-control:9443")
		assert.NotContains(t, string(contents), cfg.Control.Endpoint)
	}
}

func TestWriteComponentConfigManifestsRejectsInvalidControlPort(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "127.0.0.1:not-a-port", "not-an-address"} {
		t.Run(address, func(t *testing.T) {
			cfg := Config{}
			cfg.Control.ListenAddress = address
			_, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
			require.Error(t, err)
			assert.ErrorContains(t, err, "control listen address")
		})
	}
}

func TestComponentConfigUsesInternalRuntimeAliasInsteadOfHostBootstrap(t *testing.T) {
	cfg := Config{}
	cfg.Runtime.Endpoint = "127.0.0.1:19444"
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	control, err := os.ReadFile(componentConfigReferences(componentConfigPaths(files))[domain.ComponentRoleControl])
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(control), "endpoint = 'unix:///var/lib/gordon/migration/fixture/runtime-control.sock'"))
	assert.NotContains(t, string(control), cfg.Runtime.Endpoint)
}

func componentConfigPaths(files []ComponentConfigFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}
