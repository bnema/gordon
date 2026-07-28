package app

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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

func TestPostHandoffRecoveryRejectsAlternateRuntimeEndpointSpellings(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", BootstrapRuntimeEndpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock"}
	dataDir := t.TempDir()
	endpoint, err := validatePostHandoffRuntimeEndpoint(checkpoint, dataDir)
	require.NoError(t, err)
	assert.Equal(t, "unix://"+filepath.Join(dataDir, "migration", "fixture", bootstrapRuntimeSocketName), endpoint)

	for _, alternate := range alternateRuntimeBootstrapEndpoints(checkpoint.MigrationID) {
		t.Run(alternate.name, func(t *testing.T) {
			checkpoint.BootstrapRuntimeEndpoint = alternate.value
			_, err := validatePostHandoffRuntimeEndpoint(checkpoint, dataDir)
			assert.Error(t, err, "recovery must reject %q", alternate.value)
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

func TestMigrationBootstrapTransportSeparatesHostDialAndComponentEndpoints(t *testing.T) {
	hostDataDir := t.TempDir()
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared}
	service := &MigrationService{}
	service.config.Server.DataDir = hostDataDir
	service.config.Server.Port = 8081
	require.NoError(t, service.setBootstrapListeners(&checkpoint))

	assert.Equal(t, "unix:///var/lib/gordon/migration/fixture/runtime-control.sock", checkpoint.BootstrapRuntimeEndpoint)
	assert.Equal(t, "127.0.0.1:18080", checkpoint.BootstrapEdgeProbeEndpoint)
	require.Equal(t, []MigrationPortBinding{{Role: "edge", HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8081, Protocol: "tcp"}}, checkpoint.PreparedPortBindings)
	require.NoError(t, validateCheckpoint(checkpoint))

	plan, err := NewComponentLaunchPlan(checkpoint)
	require.NoError(t, err)
	for _, component := range plan.Components {
		if component.Role == domain.ComponentRoleRuntime {
			assert.Equal(t, "unix:///var/lib/gordon/migration/fixture/runtime-control.sock", component.BootstrapEndpoints.componentEndpoint())
			assert.Equal(t, filepath.Join(hostDataDir, "migration", "fixture", bootstrapRuntimeSocketName), component.BootstrapEndpoints.hostDialPath())
		} else {
			assert.False(t, component.BootstrapEndpoints.valid(), "%s must not receive host runtime endpoint metadata", component.Role)
		}
		if component.Role == domain.ComponentRoleEdge {
			assert.Len(t, component.PortPublishes, 1)
			continue
		}
		assert.Empty(t, component.PortPublishes)
	}

	encoded, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), hostDataDir, "host dial paths are private transient descriptors")
}

func TestMigrationBootstrapUsesMonolithRegistryForOldServingProbe(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared}
	service := &MigrationService{}
	service.config.Server.Port = 8081
	service.config.Server.RegistryPort = 15000

	require.NoError(t, service.setBootstrapListeners(&checkpoint))
	assert.Equal(t, "127.0.0.1:15000", checkpoint.OldServingProbeEndpoint)
	assert.Equal(t, []MigrationPortBinding{
		{Role: "edge", HostIP: "127.0.0.1", HostPort: 8081, ContainerPort: 8081, Protocol: "tcp"},
		{Role: "edge", HostIP: "127.0.0.1", HostPort: 15000, ContainerPort: 15000, Protocol: "tcp"},
	}, checkpoint.PublicPortBindings)
}

func TestPrivateEdgeProbePortAvailabilityRejectsCollision(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	assert.Error(t, privateEdgeProbePortAvailable(listener.Addr().String()))
}

func TestMigrationBootstrapRuntimeTransportRejectsTraversalTCPAndOtherSockets(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared}
	for _, endpoint := range []string{
		"127.0.0.1:9444", "unix:///var/lib/gordon/migration/fixture/../runtime-control.sock",
		"unix:///var/lib/gordon/migration/fixture/podman.sock", "unix:///tmp/runtime-control.sock",
		"unix://host/var/lib/gordon/migration/fixture/runtime-control.sock",
	} {
		checkpoint.BootstrapRuntimeEndpoint = endpoint
		assert.Error(t, validateCheckpoint(checkpoint), endpoint)
	}
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

func TestRuntimeHandoffDialerUsesHostBindWhileComponentKeepsFixedPath(t *testing.T) {
	hostDataDir, err := os.MkdirTemp("/tmp", "gordon-bootstrap-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(hostDataDir)) })
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared}
	service := &MigrationService{}
	service.config.Server.DataDir = hostDataDir
	require.NoError(t, service.setBootstrapListeners(&checkpoint))
	plan, err := NewComponentLaunchPlan(checkpoint)
	require.NoError(t, err)
	runtimeComponent, ok := componentForRole(plan, domain.ComponentRoleRuntime)
	require.True(t, ok)

	hostSocket := filepath.Join(hostDataDir, "migration", "fixture", bootstrapRuntimeSocketName)
	require.NoError(t, os.MkdirAll(filepath.Dir(hostSocket), 0o700))
	listener, err := net.Listen("unix", hostSocket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	const token = "fixture-token"
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if values := metadata.ValueFromIncomingContext(ctx, "authorization"); len(values) != 1 || values[0] != "Bearer "+token {
			return nil, status.Error(codes.Unauthenticated, "runtime authentication failed")
		}
		return handler(ctx, req)
	}))
	runtimev1.RegisterRuntimeServiceServer(server, recoveryRuntimeHealthServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	target, err := newRuntimeHandoffDialer(RuntimeControlConfig{Token: token})(t.Context(), runtimeComponent)
	require.NoError(t, err)
	closer, ok := target.(interface{ Close() error })
	require.True(t, ok)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })
	require.NoError(t, target.PingRuntime(t.Context()))
	assert.Equal(t, filepath.Join(hostDataDir, "migration", "fixture", bootstrapRuntimeSocketName), runtimeComponent.BootstrapEndpoints.hostDialPath(), "handoff must dial the validated host bind source")
	assert.Equal(t, "unix:///var/lib/gordon/migration/fixture/runtime-control.sock", runtimeComponent.BootstrapEndpoints.componentEndpoint(), "generated runtime config keeps the component endpoint")
}

func TestRuntimeBootstrapDescriptorRejectsUncleanAndSymlinkHostRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migration"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "migration", "fixture")))

	for _, dataDir := range []string{filepath.Join(root, "safe", ".."), root} {
		checkpoint := MigrationCheckpoint{MigrationID: "fixture"}
		service := &MigrationService{}
		service.config.Server.DataDir = dataDir
		require.Error(t, service.setBootstrapListeners(&checkpoint), dataDir)
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
