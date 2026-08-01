package compatoldnew

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/app"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	edgesnapshotgrpc "github.com/bnema/gordon/internal/adapters/in/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/adapters/in/grpc/interceptors"
	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/componentauth"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// TestCompatibilitySecurityComponentEnvMinimization is a deterministic,
// Docker-compatible launch-fixture gate. It validates the exact env-file
// contract before a role is started, so it does not require a local Podman
// socket and cannot accidentally expose fixture values in an artifact.
func TestCompatibilitySecurityComponentEnvMinimization(t *testing.T) {
	cfg := app.Config{}
	cfg.TLS.ACME.Enabled = true
	cfg.TLS.ACME.Challenge = string(domain.ACMEChallengeCloudflareDNS01)
	cfg.Backups.Volumes.Enabled = true
	cfg.Backups.Volumes.S3.Bucket = "fixture-bucket"
	manifest, err := app.BuildComponentEnvManifest(app.ComponentEnvManifestOptions{
		Config: cfg,
		Environment: map[string]string{
			"CLOUDFLARE_DNS_API_TOKEN":   "fixture-token-not-reported",
			"AWS_ACCESS_KEY_ID":          "fixture-access",
			"AWS_SECRET_ACCESS_KEY":      "fixture-secret-not-reported",
			"DOCKER_HOST":                "unix:///fixture/runtime.sock",
			"WORKLOAD_DATABASE_PASSWORD": "fixture-workload-secret-not-reported",
		},
	})
	require.NoError(t, err)
	assert.NotContains(t, manifest.KeysForRole(domain.ComponentRoleRuntime), "DOCKER_HOST")
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		assert.NotContains(t, manifest.KeysForRole(role), "DOCKER_HOST")
		assert.NotContains(t, manifest.KeysForRole(role), "WORKLOAD_DATABASE_PASSWORD")
	}
	assert.NotContains(t, manifest.KeysForRole(domain.ComponentRoleRuntime), "WORKLOAD_DATABASE_PASSWORD")
	assert.NotContains(t, manifest.RedactedSummary(), "fixture-token-not-reported")
}

// RunSecurityControlNoPodmanSocketAfterSplit verifies that ambient endpoint
// variables cannot grant a generated component runtime authority.
func RunSecurityControlNoPodmanSocketAfterSplit(artifactDir string) (Report, error) {
	manifest, err := securityComponentEnvManifest()
	if err != nil {
		return Report{}, err
	}
	controlKeys := manifest.KeysForRole(domain.ComponentRoleControl)
	runtimeKeys := manifest.KeysForRole(domain.ComponentRoleRuntime)
	return writeCurrentSecurityReport(artifactDir, map[string]bool{
		"controlHasNoRuntimeEndpoint": !containsString(controlKeys, "DOCKER_HOST"),
		"controlHasNoWorkloadSecret":  !containsString(controlKeys, "WORKLOAD_DATABASE_PASSWORD"),
		"runtimeHasNoAmbientEndpoint": !containsString(runtimeKeys, "DOCKER_HOST"),
	})
}

// RunSecurityUnsafeRuntimeRequestDenied exercises the production runtime policy
// manager with an untrusted, mutable image request. It must deny before an
// adapter can receive a write-capable runtime command.
func RunSecurityUnsafeRuntimeRequestDenied(artifactDir string) (Report, error) {
	manager := container.NewRuntimeStandaloneServicePolicyManager(nil, container.RuntimePolicy{
		Mode:                   container.RuntimePolicyModeEnforce,
		AllowedImageRegistries: []string{"registry.example.test"},
		RequireImageDigest:     true,
	})
	result, err := manager.ApplyStandaloneService(context.Background(), domain.ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "security-denied", IdempotencyKey: "security-denied", Generation: 1, SourceComponentID: "control"},
		Service:                domain.StandaloneService{Name: "unsafe", Image: "untrusted.example.test/unsafe:latest", Enabled: true},
		ConfigHash:             "security-policy",
	})
	if err != nil {
		return Report{}, err
	}
	return writeCurrentSecurityReport(artifactDir, map[string]bool{
		"adapterWasNotReached": result.Status == domain.RuntimeCommandStatusDenied,
		"policyDenied":         result.Error != nil && strings.HasPrefix(result.Error.Code, "runtime_policy_denied:"),
		"sanitizedError":       result.Error != nil && result.Error.Message == "runtime policy denied",
	})
}

func securityComponentEnvManifest() (*app.ComponentEnvManifest, error) {
	cfg := app.Config{}
	cfg.TLS.ACME.Enabled = true
	cfg.TLS.ACME.Challenge = string(domain.ACMEChallengeCloudflareDNS01)
	cfg.Backups.Volumes.Enabled = true
	cfg.Backups.Volumes.S3.Bucket = "fixture-bucket"
	return app.BuildComponentEnvManifest(app.ComponentEnvManifestOptions{
		Config: cfg,
		Environment: map[string]string{
			"CLOUDFLARE_DNS_API_TOKEN":   "fixture-token-not-reported",
			"AWS_ACCESS_KEY_ID":          "fixture-access",
			"AWS_SECRET_ACCESS_KEY":      "fixture-secret-not-reported",
			"DOCKER_HOST":                "unix:///fixture/runtime.sock",
			"WORKLOAD_DATABASE_PASSWORD": "fixture-workload-secret-not-reported",
		},
	})
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

type securityAuthCase string

const (
	securityMissingToken          securityAuthCase = "missing"
	securityWrongComponentToken   securityAuthCase = "wrong_component"
	securityWrongScopeToken       securityAuthCase = "wrong_scope"
	securityUnknownComponentToken                  = "invalid-component-token"
)

// RunSecurityComponentAuth exercises the actual control EdgeService transport
// with a fresh on-disk component-token store. Artifacts intentionally contain
// only contract booleans, never credentials, endpoints, or component IDs.
func RunSecurityComponentAuth(ctx context.Context, artifactDir string, authCase securityAuthCase) (Report, error) {
	fixture, err := newSecurityControlFixture(ctx)
	if err != nil {
		return Report{}, err
	}
	defer fixture.close()

	denied, valid, err := fixture.exercise(ctx, authCase)
	if err != nil {
		return Report{}, err
	}
	return writeCurrentSecurityReport(artifactDir, map[string]bool{
		"rejected":      denied,
		"validAccepted": valid,
	})
}

type securityControlFixture struct {
	root       string
	listener   net.Listener
	server     *grpc.Server
	hub        *edgesnapshot.SnapshotHub
	trafficHub *edgesnapshot.TrafficGraphHub
	valid      string
	limited    string
}

func newSecurityControlFixture(ctx context.Context) (*securityControlFixture, error) {
	root, err := os.MkdirTemp("", "gordon-compat-security-tokens-*")
	if err != nil {
		return nil, fmt.Errorf("security control token directory: %w", err)
	}
	store, err := tokenstore.NewUnsafeStore(root, securityLog())
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("security control token store: %w", err)
	}
	service := componentauth.NewService(store, securityLog(), componentauth.Config{})
	valid, err := service.CreateToken(ctx, componentauth.CreateRequest{Name: "edge", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{
		domain.ComponentScopeRoutesWatch,
		domain.ComponentScopeTrafficWatch,
		domain.ComponentScopeEdgeDrain,
	}})
	if err != nil {
		return nil, fmt.Errorf("security control valid token: %w", err)
	}
	limited, err := service.CreateToken(ctx, componentauth.CreateRequest{Name: "edge", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{domain.ComponentScopeEdgeDrain}})
	if err != nil {
		return nil, fmt.Errorf("security control limited token: %w", err)
	}
	hub := edgesnapshot.NewSnapshotHub()
	entry, err := domain.NewReadyRouteTargetEntry("app.example.test", "edge-target.internal", 8080, "http", domain.RouteTargetProtocolHTTP1, 1)
	if err != nil {
		return nil, err
	}
	if err := hub.Publish(domain.RouteTargetSnapshot{Generation: 1, Entries: []domain.RouteTargetEntry{entry}}); err != nil {
		return nil, err
	}
	trafficHub := edgesnapshot.NewTrafficGraphHub()
	if err := trafficHub.Publish(domain.TrafficGraphSnapshot{Generation: 1}); err != nil {
		return nil, fmt.Errorf("security control initial traffic graph: %w", err)
	}
	server := grpc.NewServer(
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(service, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(service, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())),
	)
	edgev1.RegisterEdgeServiceServer(server, edgesnapshotgrpc.NewServerWithTrafficGraphSource(hub, trafficHub))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() { _ = server.Serve(listener) }()
	return &securityControlFixture{root: root, listener: listener, server: server, hub: hub, trafficHub: trafficHub, valid: valid.Token, limited: limited.Token}, nil
}

func securityLog() zerowrap.Logger {
	return zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard})
}

func (f *securityControlFixture) close() {
	f.server.Stop()
	_ = f.listener.Close()
	_ = os.RemoveAll(f.root)
}

func (f *securityControlFixture) exercise(ctx context.Context, authCase securityAuthCase) (bool, bool, error) {
	connection, err := grpc.NewClient(f.listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, false, err
	}
	defer connection.Close()
	api := edgev1.NewEdgeServiceClient(connection)

	var rejectedErr error
	switch authCase {
	case securityMissingToken:
		stream, callErr := api.WatchRouteSnapshots(ctx, &edgev1.WatchRouteSnapshotsRequest{})
		if callErr == nil {
			_, rejectedErr = stream.Recv()
		} else {
			rejectedErr = callErr
		}
	case securityWrongComponentToken:
		credentials, credentialErr := grpcauth.NewInsecureBearerTokenCredentials(securityUnknownComponentToken)
		if credentialErr != nil {
			return false, false, credentialErr
		}
		unknownConnection, dialErr := grpc.NewClient(f.listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(credentials))
		if dialErr != nil {
			return false, false, dialErr
		}
		stream, callErr := edgev1.NewEdgeServiceClient(unknownConnection).WatchRouteSnapshots(ctx, &edgev1.WatchRouteSnapshotsRequest{})
		if callErr == nil {
			_, rejectedErr = stream.Recv()
		} else {
			rejectedErr = callErr
		}
		_ = unknownConnection.Close()
	case securityWrongScopeToken:
		credentials, credentialErr := grpcauth.NewInsecureBearerTokenCredentials(f.limited)
		if credentialErr != nil {
			return false, false, credentialErr
		}
		limitedConnection, dialErr := grpc.NewClient(f.listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(credentials))
		if dialErr != nil {
			return false, false, dialErr
		}
		stream, callErr := edgev1.NewEdgeServiceClient(limitedConnection).WatchRouteSnapshots(ctx, &edgev1.WatchRouteSnapshotsRequest{})
		if callErr == nil {
			_, rejectedErr = stream.Recv()
		} else {
			rejectedErr = callErr
		}
		_ = limitedConnection.Close()
	default:
		return false, false, fmt.Errorf("unknown security auth case")
	}
	denied := status.Code(rejectedErr) == codes.Unauthenticated || status.Code(rejectedErr) == codes.PermissionDenied
	if authCase == securityWrongComponentToken {
		denied = status.Code(rejectedErr) == codes.Unauthenticated
	}

	credentials, err := grpcauth.NewInsecureBearerTokenCredentials(f.valid)
	if err != nil {
		return false, false, err
	}
	validConnection, err := grpc.NewClient(f.listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(credentials))
	if err != nil {
		return false, false, err
	}
	defer validConnection.Close()
	stream, err := edgev1.NewEdgeServiceClient(validConnection).WatchRouteSnapshots(ctx, &edgev1.WatchRouteSnapshotsRequest{})
	if err != nil {
		return denied, false, nil
	}
	message, err := stream.Recv()
	return denied, err == nil && message.GetGeneration() == 1, nil
}

// RunSecurityEdgeNoPodmanSocket starts the candidate edge in an isolated Docker
// container with only its edge config mounted. Real authenticated route and
// traffic streams drive its public proxy and health endpoint before the
// container's mounts, environment, and named file descriptors are checked.
func RunSecurityEdgeNoPodmanSocket(ctx context.Context, repoRoot, artifactDir string) (Report, error) {
	if runtime.GOOS != "linux" {
		return Report{}, fmt.Errorf("security edge isolation requires Linux /proc inspection")
	}
	if repoRoot == "" || artifactDir == "" {
		return Report{}, fmt.Errorf("security edge isolation requires repository and artifact directories")
	}
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		return Report{}, err
	}

	backendHost, err := securityNonLoopbackIPv4()
	if err != nil {
		return Report{}, err
	}
	backend, err := net.Listen("tcp", net.JoinHostPort(backendHost, "0"))
	if err != nil {
		return Report{}, fmt.Errorf("security edge backend listener: %w", err)
	}
	defer backend.Close()
	backendServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("snapshot-proxy-ok")) })}
	go func() { _ = backendServer.Serve(backend) }()
	defer func() { _ = backendServer.Close() }()

	control, err := newSecurityControlFixture(ctx)
	if err != nil {
		return Report{}, err
	}
	defer control.close()
	if err := publishSecurityBackendSnapshot(control, backend.Addr().(*net.TCPAddr)); err != nil {
		return Report{}, err
	}

	port, err := securityFreeLoopbackPort()
	if err != nil {
		return Report{}, err
	}
	root, err := os.MkdirTemp("", "gordon-compat-security-edge-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(root)
	binary := filepath.Join(root, "gordon")
	if err := securityBuildCandidate(ctx, repoRoot, binary); err != nil {
		return Report{}, fmt.Errorf("build candidate security edge: %w", err)
	}
	if _, err := securityBuildFDInspector(ctx, root); err != nil {
		return Report{}, fmt.Errorf("build security fd inspector: %w", err)
	}
	configPath := filepath.Join(root, "edge.toml")
	if err := os.WriteFile(configPath, []byte(securityEdgeConfig(control.listener.Addr().String(), control.valid, port)), 0o600); err != nil {
		return Report{}, err
	}
	name := "gordon-compat-security-edge-" + sanitizePart(RunID("edge-isolation"))
	image := "gordon-compat-security-edge:" + sanitizePart(RunID("edge-image"))
	defer func() {
		_ = securityCommand(context.Background(), repoRoot, "docker", "rm", "--force", name)
		_ = securityCommand(context.Background(), repoRoot, "docker", "image", "rm", "--force", image)
	}()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(securityImageDockerfile()), 0o600); err != nil {
		return Report{}, err
	}
	if err := securityCommand(ctx, root, "docker", "build", "--tag", image, "."); err != nil {
		return Report{}, fmt.Errorf("build candidate edge container: %w", err)
	}
	if err := securityFDInspectorNegativeControl(ctx, repoRoot, image); err != nil {
		return Report{}, err
	}
	if err := securityCommand(ctx, repoRoot, "docker", "run", "--detach", "--rm", "--network", "host", "--name", name, "--mount", "type=bind,source="+configPath+",target=/edge.toml,readonly", image, "serve", "--role", "edge", "--config", "/edge.toml"); err != nil {
		return Report{}, fmt.Errorf("start candidate edge container: %w", err)
	}

	proxyWorks, err := securityProxyWorks(ctx, port)
	if err != nil {
		return Report{}, err
	}
	healthWorks, err := securityHealthWorks(ctx, port)
	if err != nil {
		return Report{}, err
	}
	noSocketMount, noSocketEnv, noSocketFD, err := securityContainerIsolation(ctx, repoRoot, name)
	if err != nil {
		return Report{}, err
	}
	return writeCurrentSecurityReport(artifactDir, map[string]bool{
		"noSocketEnvironment":    noSocketEnv,
		"noSocketFileDescriptor": noSocketFD,
		"noSocketMount":          noSocketMount,
		"proxyWorksFromSnapshot": proxyWorks,
		"healthWorksFromStreams": healthWorks,
	})
}

func publishSecurityBackendSnapshot(control *securityControlFixture, backend *net.TCPAddr) error {
	entry, err := domain.NewReadyRouteTargetEntry("app.example.test", backend.IP.String(), backend.Port, "http", domain.RouteTargetProtocolHTTP1, 2)
	if err != nil {
		return err
	}
	return control.hub.Publish(domain.RouteTargetSnapshot{Generation: 2, Entries: []domain.RouteTargetEntry{entry}})
}

func securityEdgeConfig(controlAddress, token string, port int) string {
	return fmt.Sprintf(`[control]
endpoint = %q
token = %q
insecure_tls = true
[edge]
listen_address = "127.0.0.1:%d"
trusted_proxy_cidrs = ["127.0.0.0/8"]
[edge.tls]
mode = "external"
`, controlAddress, token, port)
}

func securityFreeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func securityNonLoopbackIPv4() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr == nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("security edge isolation requires a non-loopback IPv4 address for host-network proxy verification")
}

func securityProxyWorks(ctx context.Context, port int) (bool, error) {
	client := &http.Client{Timeout: time.Second}
	address := "http://127.0.0.1:" + strconv.Itoa(port)
	lastStatus := 0
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/", nil)
		if err != nil {
			return false, err
		}
		request.Host = "app.example.test"
		response, err := client.Do(request)
		if err == nil {
			lastStatus = response.StatusCode
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && string(body) == "snapshot-proxy-ok" {
				return true, nil
			}
		}
		if ctx.Err() != nil {
			return false, fmt.Errorf("candidate edge did not proxy a snapshot route before timeout (last HTTP status %d)", lastStatus)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func securityHealthWorks(ctx context.Context, port int) (bool, error) {
	client := &http.Client{Timeout: time.Second}
	address := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	lastStatus := 0
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return false, err
		}
		response, err := client.Do(request)
		if err == nil {
			lastStatus = response.StatusCode
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				return true, nil
			}
		}
		if ctx.Err() != nil {
			return false, fmt.Errorf("candidate edge did not become healthy from authenticated streams before timeout (last HTTP status %d)", lastStatus)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func securityContainerIsolation(ctx context.Context, repoRoot, name string) (bool, bool, bool, error) {
	mounts, err := securityCommandOutput(ctx, repoRoot, "docker", "inspect", "--format", "{{range .Mounts}}{{println .Source}}{{end}}", name)
	if err != nil {
		return false, false, false, err
	}
	env, err := securityCommandOutput(ctx, repoRoot, "docker", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", name)
	if err != nil {
		return false, false, false, err
	}
	inspection, err := securityInspectContainerFDs(ctx, repoRoot, name)
	if err != nil {
		return false, false, false, err
	}
	return !securitySocketReference(mounts), !securitySocketReference(env), !inspection.AuthorityDetected, nil
}

func securitySocketReference(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "docker") || strings.Contains(value, "podman") || strings.Contains(value, "containerd") || strings.Contains(value, "cri-dockerd") || strings.Contains(value, "crio") || strings.Contains(value, "/cri.sock") || strings.Contains(value, "/cri/")
}

func securityCommand(ctx context.Context, dir, name string, args ...string) error {
	cmd, err := newIsolatedCommand(ctx, name, args, nil, nil, false)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", name, err)
	}
	cmd.Dir = dir
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed", name)
	}
	return nil
}

func securityCommandOutput(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd, err := newIsolatedCommand(ctx, name, args, nil, nil, false)
	if err != nil {
		return "", fmt.Errorf("prepare %s: %w", name, err)
	}
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s failed", name)
	}
	return string(output), nil
}

func writeCurrentSecurityReport(artifactDir string, observation map[string]bool) (Report, error) {
	expectedValues := make(map[string]bool, len(observation))
	for key := range observation {
		expectedValues[key] = true
	}
	expected := NewRuntimeArtifact("security contract", expectedValues, LevelSecurityNegative)
	actual := NewRuntimeArtifact("security contract", observation, LevelSecurityNegative)
	return CompareSideResultsWithMetadata(
		SideResult{Side: SideOld, Artifact: expected},
		SideResult{Side: SideNew, Artifact: actual},
		nil,
		artifactDir,
		ReportMetadata{RerunCommand: "GORDON_COMPAT_RUN_REAL=1 make compat-harness-security"},
	)
}
