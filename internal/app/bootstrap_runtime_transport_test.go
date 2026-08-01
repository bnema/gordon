package app

import (
	"context"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bnema/gordon/internal/domain"
)

type alternateRuntimeEndpoint struct {
	name  string
	value string
}

func alternateRuntimeBootstrapEndpoints(migrationID string) []alternateRuntimeEndpoint {
	canonicalPath := "/var/lib/gordon/migration/" + migrationID + "/runtime-control.sock"
	return []alternateRuntimeEndpoint{
		{name: "leading whitespace", value: " unix://" + canonicalPath},
		{name: "trailing whitespace", value: "unix://" + canonicalPath + " "},
		{name: "single scheme slash", value: "unix:" + canonicalPath},
		{name: "extra scheme slash", value: "unix:///" + canonicalPath},
		{name: "host authority", value: "unix://localhost" + canonicalPath},
		{name: "userinfo authority", value: "unix://user@" + canonicalPath},
		{name: "query", value: "unix://" + canonicalPath + "?mode=private"},
		{name: "empty query", value: "unix://" + canonicalPath + "?"},
		{name: "fragment", value: "unix://" + canonicalPath + "#socket"},
		{name: "empty fragment", value: "unix://" + canonicalPath + "#"},
		{name: "noncanonical scheme", value: "UNIX://" + canonicalPath},
		{name: "duplicate separator", value: "unix:///var/lib/gordon//migration/" + migrationID + "/runtime-control.sock"},
		{name: "dot segment", value: "unix:///var/lib/gordon/./migration/" + migrationID + "/runtime-control.sock"},
		{name: "encoded path byte", value: "unix://" + canonicalPath[:len(canonicalPath)-1] + "%6b"},
		{name: "encoded separator", value: "unix:///var/lib/gordon/migration/" + migrationID + "%2Fruntime-control.sock"},
		{name: "encoded dot", value: "unix:///var/lib/gordon/migration/" + migrationID + "/%2e/runtime-control.sock"},
		{name: "encoded dot traversal", value: "unix:///var/lib/gordon/migration/other/%2e%2e/" + migrationID + "/runtime-control.sock"},
	}
}

func TestRuntimeBootstrapSocketPathAcceptsOnlyCanonicalUnixEndpoint(t *testing.T) {
	canonical := "unix:///var/lib/gordon/migration/fixture/runtime-control.sock"
	path, ok := runtimeBootstrapSocketPath(canonical, componentDataDirectory)
	require.True(t, ok)
	assert.Equal(t, "/var/lib/gordon/migration/fixture/runtime-control.sock", path)

	for _, endpoint := range alternateRuntimeBootstrapEndpoints("fixture") {
		t.Run(endpoint.name, func(t *testing.T) {
			_, ok := runtimeBootstrapSocketPath(endpoint.value, componentDataDirectory)
			assert.False(t, ok, "%q must not be accepted as an alternate spelling", endpoint.value)
		})
	}
}

func TestRuntimeBootstrapDescriptorStoresOnlyCanonicalRootAndIdentity(t *testing.T) {
	hostDataDir := t.TempDir()
	endpoints, err := newRuntimeBootstrapEndpoints("unix:///var/lib/gordon/migration/fixture/runtime-control.sock", hostDataDir, "fixture")
	require.NoError(t, err)

	descriptorType := reflect.TypeFor[RuntimeBootstrapEndpoints]()
	fields := make([]string, 0, descriptorType.NumField())
	for field := range descriptorType.Fields() {
		fields = append(fields, field.Name)
	}
	assert.ElementsMatch(t, []string{"hostDataRootValue", "migrationID"}, fields)
	assert.Equal(t, "unix:///var/lib/gordon/migration/fixture/runtime-control.sock", endpoints.componentEndpoint())
	assert.Equal(t, filepath.Join(hostDataDir, "migration", "fixture", bootstrapRuntimeSocketName), endpoints.hostDialPath())
}

func TestPrivateEdgeProbePortAvailabilityRejectsCollision(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	assert.Error(t, privateEdgeProbePortAvailable(listener.Addr().String()))
}

func TestComponentLaunchPlanRejectsTCPBootstrapAndUnsafePreparedPublish(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, BootstrapRuntimeEndpoint: "127.0.0.1:23456"}
	_, err := NewComponentLaunchPlan(checkpoint)
	require.Error(t, err)

	checkpoint.BootstrapRuntimeEndpoint = "unix:///var/lib/gordon/migration/fixture/runtime-control.sock"
	checkpoint.BootstrapEdgeProbeEndpoint = "127.0.0.1:23456"
	for _, binding := range []MigrationPortBinding{
		{Role: "runtime", HostIP: "127.0.0.1", HostPort: 23456, ContainerPort: 9444, Protocol: "tcp"},
		{Role: "edge", HostIP: "0.0.0.0", HostPort: 23456, ContainerPort: 9444, Protocol: "tcp"},
		{Role: "edge", HostIP: "127.0.0.1", HostPort: 23456, ContainerPort: 9444, Protocol: "udp"},
		{Role: "edge", HostIP: "127.0.0.1", HostPort: 23456, ContainerPort: 23456, Protocol: "tcp"},
	} {
		checkpoint.PreparedPortBindings = []MigrationPortBinding{binding}
		_, err = NewComponentLaunchPlan(checkpoint)
		require.Error(t, err, "%+v", binding)
	}
}

func TestRuntimeComponentLauncherClosesFailedPrivateHandoffClient(t *testing.T) {
	oldRuntime := &handoffRuntime{}
	failedTarget := &closingHandoffRuntime{handoffRuntime: handoffRuntime{probeErrors: []error{status.Error(codes.PermissionDenied, "invalid runtime")}}}
	component := ComponentLaunchComponent{
		Role:        domain.ComponentRoleRuntime,
		ComponentID: "gordon-runtime-fixture-g1",
		Labels: map[string]string{
			domain.LabelComponentVersion:     "v2",
			domain.LabelComponentGeneration:  "1",
			domain.LabelComponentMigrationID: "fixture",
		},
	}
	launcher, err := NewRuntimeComponentLauncherWithHandoff(oldRuntime, func(context.Context, ComponentLaunchComponent) (RuntimeHandoffClient, error) {
		return failedTarget, nil
	})
	require.NoError(t, err)

	require.Error(t, launcher.TransferRuntimeCommandChannel(t.Context(), component))
	assert.True(t, failedTarget.closed, "a rejected bootstrap connection must stop its gRPC goroutines")
}

type closingHandoffRuntime struct {
	handoffRuntime
	closed bool
}

func (r *closingHandoffRuntime) Close() error {
	r.closed = true
	return nil
}

func TestRuntimeHandoffDialerRejectsWrongRoleTokenAndMissingDescriptor(t *testing.T) {
	dial := newRuntimeHandoffDialer(RuntimeControlConfig{Token: "fixture-token"})
	component := ComponentLaunchComponent{Role: domain.ComponentRoleEdge}
	_, err := dial(t.Context(), component)
	require.Error(t, err)

	component.Role = domain.ComponentRoleRuntime
	_, err = dial(t.Context(), component)
	require.Error(t, err)

	missingToken := newRuntimeHandoffDialer(RuntimeControlConfig{})
	_, err = missingToken(t.Context(), component)
	require.Error(t, err)
}

func TestRuntimeBootstrapSocketPathUsesConfiguredMigrationDirectory(t *testing.T) {
	root := t.TempDir()
	endpoint := "unix://" + filepath.Join(root, "migration", "fixture", bootstrapRuntimeSocketName)
	path, ok := runtimeBootstrapSocketPath(endpoint, root)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(root, "migration", "fixture", bootstrapRuntimeSocketName), path)
}
