package compatoldnew

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScenarioDefinitions(t *testing.T) {
	expected := map[Surface]struct {
		scenarios []Scenario
		names     []string
	}{
		SurfaceConfig: {ConfigScenarios(), []string{
			"config/minimal-load",
			"config/realistic-load",
			"config/legacy-registry-domain-keys",
			"config/invalid-error",
			"config/env-override-precedence",
			"config/server-settings",
			"config/auth-settings",
			"config/api-rate-limits",
			"config/deploy-settings",
			"config/network-isolation",
			"config/volume-settings",
			"config/auto-route-preview",
			"config/routes-save-load",
			"config/external-routes",
			"config/attachments",
			"config/public-tls-acme",
			"config/dns-settings",
			"config/entrypoints-traffic-graph",
			"config/standalone-network-services",
			"config/logging",
			"config/telemetry",
			"config/backups",
			"config/images",
			"config/container-security-defaults",
			"config/reload-preserves-critical-fields",
			"config/backup-canonical-save",
		}},
		SurfaceCLI: {CLIScenarios(), []string{
			"cli/config-show-json",
			"cli/routes-list-json",
			"cli/routes-add-remove",
			"cli/status-text",
			"cli/status-json",
			"cli/networks-list-json",
			"cli/logs",
		}},
		SurfaceAPI: {HTTPScenarios(), []string{
			"api/auth-missing-invalid",
			"api/route-list-detail",
			"api/route-add-update-remove",
			"api/reload",
			"api/health-status",
			"api/request-too-large",
			"api/forbidden-permission",
		}},
		SurfaceRegistry: {RegistryScenarios(), []string{
			"registry/v2-ping",
			"registry/auth-challenge",
			"registry/push-image",
			"registry/pull-image",
			"registry/tag-list",
			"registry/upload-too-large",
			"registry/invalid-name-reference",
			"registry/image-push-event",
		}},
		SurfaceProxy: {ProxyScenarios(), []string{
			"proxy/managed-http-route",
			"proxy/unknown-host",
			"proxy/external-route",
			"proxy/h2c-backend",
			"proxy/registry-domain-routing",
			"proxy/body-size-limit",
			"proxy/zero-downtime-drain",
			"proxy/access-log-emitted",
		}},
		SurfaceRuntime: {RuntimeScenarios(), []string{
			"runtime/deploy-image-proxy-port-label",
			"runtime/exposed-port-fallback",
			"runtime/env-file",
			"runtime/secrets",
			"runtime/volume",
			"runtime/attachment",
			"runtime/network-group",
			"runtime/readiness-failure",
			"runtime/route-removal-cleanup",
			"runtime/startup-recovery",
		}},
		SurfaceMigration: {MigrationScenarios(), []string{
			"migration/monolith-to-split-preflight",
			"migration/component-startup-health",
			"migration/no-unsafe-traffic-switch",
			"migration/env-transfer",
			"migration/interrupted-retry",
		}},
		SurfaceSecurity: {SecurityScenarios(), []string{
			"security/edge-no-podman-socket",
			"security/registry-no-podman-socket",
			"security/control-no-podman-socket-after-split",
			"security/missing-component-token-rejected",
			"security/wrong-component-token-rejected",
			"security/wrong-scope-component-token-rejected",
			"security/unsafe-runtime-request-denied",
		}},
	}

	implemented := implementedScenarioNames()
	seen := make(map[string]struct{})
	for surface, group := range expected {
		requireScenarioNames(t, surface, group.scenarios, group.names)
		for _, scenario := range group.scenarios {
			require.NotEmpty(t, scenario.SpecSection, scenario.Name)
			require.Equal(t, surface, scenario.Surface, scenario.Name)
			if _, isImplemented := implemented[scenario.Name]; isImplemented {
				require.Equal(t, ScenarioStatusImplemented, scenario.Status, scenario.Name)
				require.Empty(t, scenario.BlockReason, scenario.Name)
			} else {
				require.Equal(t, ScenarioStatusPending, scenario.Status, scenario.Name)
				require.NotEmpty(t, scenario.BlockReason, scenario.Name)
			}

			_, duplicate := seen[scenario.Name]
			require.False(t, duplicate, "duplicate scenario name %q", scenario.Name)
			seen[scenario.Name] = struct{}{}
		}
	}

	require.Len(t, AllScenarios(), len(seen))
}

func TestImplementedScenarioAllowlistIsExact(t *testing.T) {
	expected := map[string]struct{}{
		"cli/config-show-json":                          {},
		"cli/routes-list-json":                          {},
		"api/auth-missing-invalid":                      {},
		"api/route-list-detail":                         {},
		"api/route-add-update-remove":                   {},
		"proxy/managed-http-route":                      {},
		"proxy/external-route":                          {},
		"proxy/zero-downtime-drain":                     {},
		"security/edge-no-podman-socket":                {},
		"security/missing-component-token-rejected":     {},
		"security/wrong-component-token-rejected":       {},
		"security/wrong-scope-component-token-rejected": {},
	}
	require.Equal(t, expected, implementedScenarioNames())
}

func implementedScenarioNames() map[string]struct{} {
	implemented := make(map[string]struct{})
	for _, scenario := range AllScenarios() {
		if scenario.Status == ScenarioStatusImplemented {
			implemented[scenario.Name] = struct{}{}
		}
	}
	return implemented
}

func TestImplementedScenarioFilteringIsExplicitAndPendingIsFailSafe(t *testing.T) {
	implemented := implementedScenario("cli/config-show-json", SurfaceCLI, "6.2 CLI compatibility", false)
	pending := pendingScenario("migration/not-ready", SurfaceMigration, "6.4 migration", false, "requires migration harness")
	unknown := Scenario{Name: "unknown", Surface: SurfaceCLI}

	require.Equal(t, ScenarioStatusImplemented, implemented.Status)
	require.Empty(t, implemented.BlockReason)
	require.False(t, mustSkip(t, implemented))
	require.True(t, mustSkip(t, pending))
	reason, skip := unknown.SkipReason()
	require.True(t, skip)
	require.Contains(t, reason, "not implemented")

	selected, err := SelectImplementedScenarios([]Scenario{implemented, pending}, []string{implemented.Name})
	require.NoError(t, err)
	require.Equal(t, []Scenario{implemented}, selected)
	_, err = SelectImplementedScenarios([]Scenario{implemented, pending}, []string{pending.Name})
	require.Error(t, err)
}

func mustSkip(t *testing.T, scenario Scenario) bool {
	t.Helper()
	_, skip := scenario.SkipReason()
	return skip
}

func TestScenarioPodmanRequirements(t *testing.T) {
	for _, scenario := range RegistryScenarios() {
		require.True(t, scenario.PodmanRequired, scenario.Name)
	}
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == managedHTTPRouteScenarioName || scenario.Name == externalRouteScenarioName || scenario.Name == zeroDowntimeDrainScenarioName {
			require.False(t, scenario.PodmanRequired, scenario.Name)
			continue
		}
		require.True(t, scenario.PodmanRequired, scenario.Name)
	}
	for _, scenario := range RuntimeScenarios() {
		require.True(t, scenario.PodmanRequired, scenario.Name)
	}

	podmanByName := make(map[string]bool)
	for _, scenario := range append(CLIScenarios(), SecurityScenarios()...) {
		podmanByName[scenario.Name] = scenario.PodmanRequired
	}
	require.True(t, podmanByName["cli/networks-list-json"])
	require.True(t, podmanByName["cli/logs"])
	require.False(t, podmanByName["security/edge-no-podman-socket"])
	require.False(t, podmanByName["security/missing-component-token-rejected"])
	require.False(t, podmanByName["security/wrong-component-token-rejected"])

	t.Setenv(EnvCompatPodman, "")
	podmanScenario := RegistryScenarios()[0]
	reason, skip := podmanScenario.SkipReason()
	require.True(t, skip)
	require.Contains(t, reason, EnvCompatPodman)

	t.Setenv(EnvCompatPodman, "1")
	reason, skip = podmanScenario.SkipReason()
	require.True(t, skip)
	require.Equal(t, podmanScenario.BlockReason, reason)
}

func TestPendingProxyScenariosDoNotSilentlyPass(t *testing.T) {
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == managedHTTPRouteScenarioName || scenario.Name == externalRouteScenarioName || scenario.Name == zeroDowntimeDrainScenarioName {
			continue
		}
		require.Equal(t, ScenarioStatusPending, scenario.Status, scenario.Name)
		require.NotEmpty(t, scenario.BlockReason, scenario.Name)

		reason, skip := scenario.SkipReason()
		require.True(t, skip, scenario.Name)
		require.NotEmpty(t, reason, scenario.Name)
	}
}

func TestMigrationAndSecurityScenariosDoNotSilentlyPass(t *testing.T) {
	for _, scenario := range MigrationScenarios() {
		require.Equal(t, ScenarioStatusPending, scenario.Status, scenario.Name)
		require.NotEmpty(t, scenario.BlockReason, scenario.Name)
	}
	for _, scenario := range SecurityScenarios() {
		if scenario.Status == ScenarioStatusImplemented {
			continue
		}
		require.Equal(t, ScenarioStatusPending, scenario.Status, scenario.Name)
		require.NotEmpty(t, scenario.BlockReason, scenario.Name)

		reason, skip := scenario.SkipReason()
		require.True(t, skip, scenario.Name)
		require.NotEmpty(t, reason, scenario.Name)
	}
}

func requireScenarioNames(t *testing.T, surface Surface, scenarios []Scenario, names []string) {
	t.Helper()
	actual := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		actual = append(actual, scenario.Name)
	}
	require.ElementsMatch(t, names, actual, "surface %s", surface)
}
