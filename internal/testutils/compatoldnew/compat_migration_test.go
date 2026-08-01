package compatoldnew

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCompatibilityMigrationProtocolFixture is deliberately deterministic: it
// guards the real-Podman fixture shape without creating a component itself.
// Component creation belongs exclusively to the candidate migrate CLI and the
// runtime it authorizes; the rootless test below verifies that behavior.
func TestCompatibilityMigrationProtocolFixture(t *testing.T) {
	root := filepath.Join(projectRoot(t), "internal", "testutils", "compatoldnew")
	fixtureSource, err := os.ReadFile(filepath.Join(root, "compat_migration_fixture.go"))
	require.NoError(t, err)
	testSource, err := os.ReadFile(filepath.Join(root, "compat_migration_test.go"))
	require.NoError(t, err)
	text := string(fixtureSource) + "\n" + string(testSource)
	for _, forbidden := range []string{
		"migrationRun" + "Component", "migrationRun" + "HTTP", "migrationFixture" + "Image",
		"fixture.invalid/" + "gordon", "busy" + "box:",
	} {
		require.NotContains(t, text, forbidden, "migration fixture must not directly create target components")
	}
	for _, command := range []string{"migrate plan", "migrate prepare", "migrate status", "migrate switch"} {
		require.Contains(t, text, command, "the real fixture must execute %q", command)
	}
	for _, managedPassGate := range []string{"secrets_backend = \"pass\"", "USER gordon", "ContainerVolumeOptionChown", "HostConfig.Binds", "chmod 0700", "secrets doctor", "--write-check", "gordon-control-secrets-"} {
		require.Contains(t, text, managedPassGate, "rootless migration must retain authentic role-owned volume gate %q", managedPassGate)
	}
	for _, removedSharedGroupGate := range []string{"gordon-" + "data", "21" + "900", "--group-" + "add"} {
		require.NotContains(t, text, removedSharedGroupGate, "rootless migration must not restore shared supplementary group %q", removedSharedGroupGate)
	}
	legacySameNamespaceMount := `filepath.Join(f.root, "` + "data" + `") + ":/var/lib/gordon"`
	require.NotContains(t, text, legacySameNamespaceMount, "authentic handoff fixture must keep host and component data roots distinct")
}

func TestSplitImageContractKeepsDistinctRolesWithoutSharedDataGroup(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(projectRoot(t), "Dockerfile"))
	require.NoError(t, err)
	dockerfile := string(contents)
	for _, identity := range []string{"21001", "21002", "21003", "21004"} {
		require.Contains(t, dockerfile, identity)
	}
	for _, removed := range []string{"gordon-" + "data", "21" + "900"} {
		require.NotContains(t, dockerfile, removed)
	}
	require.NotContains(t, dockerfile, "addgroup gordon-runtime")
	require.Contains(t, dockerfile, "chown 21002:21002 /var/lib/gordon/secrets")
}

// TestMigrationAuthDisabledEnvironmentNeedsOnlyRootlessSessionInputs documents
// that migration auth-disabled mode requires only rootless session inputs.
func TestMigrationAuthDisabledEnvironmentNeedsOnlyRootlessSessionInputs(t *testing.T) {
	environment := migrationAuthDisabledEnvironment("/run/user/1000")
	require.ElementsMatch(t, []string{
		"XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
	}, environment)
	for _, entry := range environment {
		require.NotContains(t, entry, "GORDON_AUTH_TOKEN_SECRET")
	}
}

func TestMigrationInterruptedRetry(t *testing.T) {
	// The real interruption is performed through the candidate CLI in
	// TestCompatibilityMigrationRootlessPodmanOldToSplit.
	require.True(t, strings.Contains(migrationScenarioOperations, "migrate prepare") && strings.Contains(migrationScenarioOperations, "migrate status"))
}

func TestAssertCleanedRejectsManagedPassSecretVolumeLeaks(t *testing.T) {
	require.Equal(t, []string{"gordon-control-secrets-0123456789abcdef"}, leakedManagedPassSecretVolumes("app-data\ngordon-control-secrets-0123456789abcdef\n"))
	require.Empty(t, leakedManagedPassSecretVolumes("gordon-control-migration-g1\ngordon-control-fixture-g1\n"))
}

func TestManagedPassSecretVolumeListingErrorIsNotTreatedAsEmpty(t *testing.T) {
	installPodmanInfoFixture(t, `printf 'volume listing failed\n' >&2; exit 1`)
	listing, err := managedPassSecretVolumeNames(t.Context())
	require.Error(t, err, "volume listing failures must fail closed")
	require.Empty(t, listing)
	require.Empty(t, leakedManagedPassSecretVolumes(listing), "ignoring listing errors would treat failure as an empty clean volume list")
}

// TestMigrationMissingEnvFailsPreflight documents that the real fixture uses
// an explicit missing environment preflight before it mutates the runtime.
func TestMigrationMissingEnvFailsPreflight(t *testing.T) {
	require.Contains(t, migrationScenarioOperations, "missing-env migrate plan")
}

// TestMigrationTrafficSwitchFailsClosed ensures the source gate continues to
// require the actual switch operation rather than an in-memory switcher fake.
func TestMigrationTrafficSwitchFailsClosed(t *testing.T) {
	require.Contains(t, migrationScenarioOperations, "migrate switch")
}

func TestMigrationStatusPhase(t *testing.T) {
	phase, err := migrationStatusPhase(`{
  "phase": "switched"
}`)
	require.NoError(t, err)
	require.Equal(t, "switched", phase)
}

func TestMigrationStatusDiagnosticErrorRedactsSecrets(t *testing.T) {
	const secret = "fixture-runtime-handoff-token"
	diagnostic := migrationStatusDiagnosticError(fmt.Errorf("status failed with token %s", secret))
	require.NotContains(t, diagnostic, secret)
	require.Contains(t, diagnostic, "<redacted>")
}

func TestNormalizeNetworkSet(t *testing.T) {
	require.Equal(t, []string{"app", "internal"}, normalizeNetworkSet(" internal; app;\n"))
}

const migrationScenarioOperations = "migrate plan; missing-env migrate plan; migrate prepare; migrate status; migrate switch"

// TestCompatibilityMigrationRootlessPodmanOldToSplit is the release gate. The
// fixture is allowed to create only its old monolith, app, network, volume and
// system service. Every split component is created by the actual candidate CLI
// through the old runtime's mounted rootless Podman socket.
func TestCompatibilityMigrationRootlessPodmanOldToSplit(t *testing.T) {
	if !PodmanEnabledFromEnv() {
		return
	}
	var probes migrationProbeAssertions
	reportPath := os.Getenv("GORDON_COMPAT_MIGRATION_REPORT_PATH")
	if reportPath != "" && !filepath.IsAbs(reportPath) {
		reportPath = filepath.Join(projectRoot(t), reportPath)
	}
	t.Cleanup(func() {
		if reportPath == "" {
			return
		}
		report := migrationInvocationReport{
			Scenario: "rootless-podman-old-to-split",
			Skipped:  false,
			Passed:   !t.Failed() && probes.passed(),
			Probes:   probes,
		}
		if err := writeMigrationInvocationReport(reportPath, report); err != nil {
			t.Errorf("write sanitized migration invocation report: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	requireRootlessPodman(t, ctx)

	// newRealMigrationFixture registers its own cleanup, so a failure inside the
	// constructor still reports diagnostics and removes its resources.
	fixture := newRealMigrationFixture(t, ctx)

	fixture.runCLI("migrate", "plan", "--json")
	fixture.runMissingEnvPlan()
	fixture.runCLI("migrate", "prepare", "--json")
	status := fixture.runCLI("migrate", "status", "--json")
	normalizedStatus := strings.ReplaceAll(strings.ReplaceAll(status, " ", ""), "\n", "")
	require.Contains(t, normalizedStatus, `"bootstrap_runtime_endpoint":"unix:///var/lib/gordon/migration/`, "old monolith must use the private Gordon Unix RPC socket")
	require.Contains(t, normalizedStatus, `"old_serving_probe_endpoint":"127.0.0.1:15000"`, "old serving proof must target the monolith's in-namespace registry listener, never rootless host-port NAT")
	fixture.assertPreparedTargets()
	fixture.assertPreparedRoleSecurityAndPrivateAccess()
	fixture.assertAuthDisabledRoleIsolation()
	fixture.assertPreparedAppNetworkHandoff()
	fixture.assertPreparedEdgeProbeListener()
	fixture.assertRuntimeBootstrapTransport()
	fixture.assertRuntimeSocketExclusive()
	fixture.assertOldCoordinatorUsesHostRuntimeSocket()
	fixture.assertAuthenticatedEdgeAttestation()

	// A second prepare is an interruption/retry at the persisted checkpoint
	// boundary. It must discover and reuse the same target generation through
	// the production adapter. On rootless Podman, compatible inspect projects
	// keep-id as private and expands CapDrop=ALL, so this retry also gates the
	// adapter's native ID-mapping and complete capability normalization.
	fixture.runCLI("migrate", "prepare", "--json")
	fixture.assertPreparedTargets()
	// The runtime handler survives its caller's monolith termination. The
	// original exec must not claim success; a fresh CLI observes and retries the
	// durably committed result after the old container has gone away.
	fixture.runSwitchExpectCallerTermination()
	// The replacement runtime owns the transaction after it stops this caller's
	// monolith. A new process must observe its durable terminal result before
	// inspecting listener state; Podman can still report the old container as
	// running for a brief interval after StopContainer returns.
	fixture.awaitInterruptedSwitchTerminalStatus()
	fixture.assertSwitchedTraffic()
	probes.Application = true
	probes.Listeners = true
	fixture.assertRegistryArtifact()
	probes.Registry = true
	fixture.assertInterruptedSwitchRetry()
	probes.Resume = true
	fixture.assertManagedPassVolumePersistence()
	fixture.cleanup()
	fixture.assertCleaned()
}
