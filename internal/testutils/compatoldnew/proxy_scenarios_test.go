package compatoldnew

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedHTTPRouteImageMetadataSeparatesLabelAndExposePorts(t *testing.T) {
	require.NotEqual(t, managedHTTPRouteImagePort, managedHTTPRouteExposedPort)
	require.Contains(t, managedHTTPRouteDockerfile, "FROM "+managedHTTPRouteBaseImage)
	require.Contains(t, managedHTTPRouteDockerfile, "LABEL gordon.proxy.port=8080")
	require.Contains(t, managedHTTPRouteDockerfile, "EXPOSE 9090")
	require.Equal(t, "127.0.0.1::8080", managedHTTPRoutePublishAddress())
}

func TestManagedHTTPRouteSideRetryRemovesExactContainerBeforeRestart(t *testing.T) {
	var order []string
	containerActive := false
	resources := &managedProxyResources{
		containers: map[string]string{},
		cleanupCommand: func(_ context.Context, args ...string) error {
			require.Equal(t, []string{"rm", "-f", "gordon-managed.test"}, args)
			require.True(t, containerActive, "retry must remove the previous container")
			containerActive = false
			order = append(order, "remove-old")
			return nil
		},
	}
	attempts := 0
	attempt := func(_ context.Context, side, _, _, domain string, resources *managedProxyResources) (SideResult, error) {
		attempts++
		if containerActive {
			return SideResult{}, errors.New("container name conflict")
		}
		containerActive = true
		resources.containers[side] = "gordon-" + domain
		order = append(order, "start")
		if attempts == 1 {
			return SideResult{}, errors.New("listen tcp 127.0.0.1: address already in use")
		}
		return SideResult{Side: side}, nil
	}

	result, err := runManagedHTTPRouteSideWithAttempt(context.Background(), SideOld, "gordon", t.TempDir(), "managed.test", resources, attempt)

	require.NoError(t, err)
	require.Equal(t, SideOld, result.Side)
	require.Equal(t, []string{"start", "remove-old", "start"}, order)
	require.True(t, containerActive, "second attempt owns only its newly started container")
	require.Equal(t, "gordon-managed.test", resources.containers[SideOld])
}

func TestManagedProxyPullBaseImageRetriesOnlyTransientNetworkFailures(t *testing.T) {
	t.Run("retries transient network failure", func(t *testing.T) {
		attempts := 0
		resources := &managedProxyResources{command: func(_ context.Context, args ...string) error {
			attempts++
			require.Equal(t, []string{"pull", managedHTTPRouteBaseImage}, args)
			if attempts == 1 {
				return errTransientDockerNetwork
			}
			return nil
		}}

		require.NoError(t, resources.pullBaseImage(context.Background()))
		require.Equal(t, 2, attempts)
	})

	t.Run("does not retry non-transient failure", func(t *testing.T) {
		attempts := 0
		nonTransientErr := errors.New("image not found")
		resources := &managedProxyResources{command: func(_ context.Context, _ ...string) error {
			attempts++
			return nonTransientErr
		}}

		err := resources.pullBaseImage(context.Background())
		require.ErrorIs(t, err, nonTransientErr)
		require.Equal(t, 1, attempts)
	})
}

type zeroDowntimeDrainCleanupRunner struct {
	commands [][]string
	failures map[string]error
}

func (r *zeroDowntimeDrainCleanupRunner) command(_ context.Context, args ...string) error {
	r.commands = append(r.commands, args)
	return r.failures[args[len(args)-1]]
}

func TestZeroDowntimeDrainStateVolumesAreExactAndSideUnique(t *testing.T) {
	resources := newZeroDowntimeDrainResources("Run ID", "drain.test")
	require.Equal(t, "gordon-compat-zero-drain-state-run-id-old", resources.stateVolumeName(SideOld))
	require.Equal(t, "gordon-compat-zero-drain-state-run-id-new", resources.stateVolumeName(SideNew))
}

func TestZeroDowntimeDrainResponseParsers(t *testing.T) {
	instance, started := parseZeroDowntimeDrainSlowStart("INSTANCE:old\n", zeroDowntimeDrainStart)
	require.True(t, started)
	require.Equal(t, zeroDowntimeDrainOldInstance, instance)

	instance, started = parseZeroDowntimeDrainSlowStart("INSTANCE:unknown\n", zeroDowntimeDrainStart)
	require.False(t, started)
	require.Empty(t, instance)

	instance, completed := parseZeroDowntimeDrainResponse("INSTANCE:replacement\n" + zeroDowntimeDrainStart + zeroDowntimeDrainDone)
	require.True(t, completed)
	require.Equal(t, zeroDowntimeDrainReplacementInstance, instance)

	instance, routed := parseZeroDowntimeDrainFastResponse("INSTANCE:replacement\n")
	require.True(t, routed)
	require.Equal(t, zeroDowntimeDrainReplacementInstance, instance)

	_, routed = parseZeroDowntimeDrainFastResponse("INSTANCE:old\n")
	require.False(t, routed)
}

func TestZeroDowntimeDrainArtifactOrderingFieldsAreSafeAndStable(t *testing.T) {
	artifact := zeroDowntimeDrainArtifact(zeroDowntimeDrainObservation{
		ReplacementRoutedDuringStabilization: true,
		OldSurvivedRefreshUntilRelease:       true,
	})
	encoded, err := json.Marshal(artifact.RawValue())
	require.NoError(t, err)
	require.JSONEq(t, `{
		"marker_observed": false,
		"old_response_from_old": false,
		"fresh_response_from_replacement": false,
		"replacement_routed_during_stabilization": true,
		"old_survived_refresh_until_release": true,
		"target_changed": false,
		"deploy_succeeded": false,
		"deploy_blocked_until_response_release": false,
		"deploy_returned_before_response_release": false,
		"old_target_continuously_running": false,
		"drain_duration_bucket": ""
	}`, string(encoded))
	require.NotContains(t, string(encoded), "container_id")
	require.NotContains(t, string(encoded), "proxy_port")
}

func TestZeroDowntimeDrainOrderingContract(t *testing.T) {
	base := zeroDowntimeDrainObservation{
		MarkerObserved:                       true,
		OldResponseFromOld:                   true,
		FreshResponseFromReplacement:         true,
		ReplacementRoutedDuringStabilization: true,
		OldSurvivedRefreshUntilRelease:       true,
		TargetChanged:                        true,
		DeploySucceeded:                      true,
		DeployBlockedUntilResponseRelease:    true,
	}
	require.True(t, base.satisfiesOrderingContract())

	base.ReplacementRoutedDuringStabilization = false
	require.False(t, base.satisfiesOrderingContract(), "the provider refresh must route to the replacement during stabilization")
	base.ReplacementRoutedDuringStabilization = true

	base.OldSurvivedRefreshUntilRelease = false
	require.False(t, base.satisfiesOrderingContract(), "the old target must survive the refresh until release")
	base.OldSurvivedRefreshUntilRelease = true

	base.DeployBlockedUntilResponseRelease = false
	base.DeployReturnedBeforeResponseRelease = true
	base.OldTargetContinuouslyRunning = true
	require.True(t, base.satisfiesOrderingContract())

	base.OldTargetContinuouslyRunning = false
	require.False(t, base.satisfiesOrderingContract(), "an asynchronous deploy must retain the old target until release")
}

func TestZeroDowntimeDrainCleanupAttemptsEveryTrackedResourceAfterFailures(t *testing.T) {
	containerCleanupErr := errors.New("remove containers failed")
	sourceCleanupErr := errors.New("remove source failed")
	oldCleanupErr := errors.New("remove old registry tag failed")
	volumeCleanupErr := errors.New("remove old state volume failed")
	runner := &zeroDowntimeDrainCleanupRunner{failures: map[string]error{
		"gordon-compat-zero-drain:test":                   sourceCleanupErr,
		"localhost:41001/gordon-compat-drain-test:latest": oldCleanupErr,
		"gordon-compat-zero-drain-state-test-old":         volumeCleanupErr,
	}}
	resources := &zeroDowntimeDrainResources{
		imageTags: []string{
			"gordon-compat-zero-drain:test",
			"localhost:41001/gordon-compat-drain-test:latest",
			"localhost:41002/gordon-compat-drain-test:latest",
		},
		volumeNames:           []string{"gordon-compat-zero-drain-state-test-old", "gordon-compat-zero-drain-state-test-new"},
		cleanupCommand:        runner.command,
		cleanupContainersFunc: func(context.Context) error { return containerCleanupErr },
	}

	err := resources.cleanup(context.Background())

	require.ErrorIs(t, err, containerCleanupErr)
	require.ErrorIs(t, err, sourceCleanupErr)
	require.ErrorIs(t, err, oldCleanupErr)
	require.ErrorIs(t, err, volumeCleanupErr)
	require.Equal(t, [][]string{
		{"image", "rm", "-f", "gordon-compat-zero-drain:test"},
		{"image", "rm", "-f", "localhost:41001/gordon-compat-drain-test:latest"},
		{"image", "rm", "-f", "localhost:41002/gordon-compat-drain-test:latest"},
		{"volume", "rm", "-f", "gordon-compat-zero-drain-state-test-old"},
		{"volume", "rm", "-f", "gordon-compat-zero-drain-state-test-new"},
	}, runner.commands)
}

func TestManagedProxyCleanupJoinsPrimaryErrorAndAttemptsAllOwnedResources(t *testing.T) {
	primaryErr := errors.New("primary scenario failure")
	oldCleanupErr := errors.New("remove old failed")
	newCleanupErr := errors.New("remove new failed")
	imageCleanupErr := errors.New("remove image failed")
	var commands [][]string
	resources := &managedProxyResources{
		imageTag: "managed-image:test",
		containers: map[string]string{
			SideOld: "managed-old",
			SideNew: "managed-new",
		},
		cleanupCommand: func(_ context.Context, args ...string) error {
			commands = append(commands, args)
			switch args[len(args)-1] {
			case "managed-old":
				return oldCleanupErr
			case "managed-new":
				return newCleanupErr
			case "managed-image:test":
				return imageCleanupErr
			default:
				t.Fatalf("unexpected cleanup command: %v", args)
				return nil
			}
		},
	}

	cleanupErr := resources.cleanup(context.Background())
	err := joinManagedProxyCleanupError(primaryErr, cleanupErr)

	require.ErrorIs(t, err, primaryErr)
	require.ErrorIs(t, err, oldCleanupErr)
	require.ErrorIs(t, err, newCleanupErr)
	require.ErrorIs(t, err, imageCleanupErr)
	require.Equal(t, [][]string{
		{"rm", "-f", "managed-old"},
		{"rm", "-f", "managed-new"},
		{"image", "rm", "-f", "managed-image:test"},
	}, commands)
}
