package compatoldnew

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedHTTPRouteImageMetadataSeparatesLabelAndExposePorts(t *testing.T) {
	require.NotEqual(t, managedHTTPRouteImagePort, managedHTTPRouteExposedPort)
	require.Contains(t, managedHTTPRouteDockerfile, "LABEL gordon.proxy.port=8080")
	require.Contains(t, managedHTTPRouteDockerfile, "EXPOSE 9090")
	require.Equal(t, "127.0.0.1::8080", managedHTTPRoutePublishAddress())
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
