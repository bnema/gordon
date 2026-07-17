package compatoldnew

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
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
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

type securityAuthCase string

const (
	securityMissingToken          securityAuthCase = "missing"
	securityWrongComponentToken   securityAuthCase = "wrong_component"
	securityWrongScopeToken       securityAuthCase = "wrong_scope"
	securityUnknownComponentToken                  = "gordon_component.unknown-component.unknown-secret"
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
	root     string
	listener net.Listener
	server   *grpc.Server
	hub      *edgesnapshot.SnapshotHub
	valid    string
	limited  string
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
	valid, err := service.CreateToken(ctx, componentauth.CreateRequest{Name: "edge", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{domain.ComponentScopeRoutesWatch}})
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
	server := grpc.NewServer(
		grpc.UnaryInterceptor(interceptors.ComponentAuthUnaryInterceptor(service, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())),
		grpc.StreamInterceptor(interceptors.ComponentAuthStreamInterceptor(service, edgesnapshotgrpc.MethodScopes(), edgesnapshotgrpc.MethodRoles())),
	)
	edgev1.RegisterEdgeServiceServer(server, edgesnapshotgrpc.NewServer(hub))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() { _ = server.Serve(listener) }()
	return &securityControlFixture{root: root, listener: listener, server: server, hub: hub, valid: valid.Token, limited: limited.Token}, nil
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
// container with only its edge config mounted. A real authenticated snapshot
// stream drives a request through the public proxy before the container's
// mounts, environment, and named file descriptors are checked.
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
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\nCOPY gordon /gordon\nENTRYPOINT [\"/gordon\"]\n"), 0o600); err != nil {
		return Report{}, err
	}
	if err := securityCommand(ctx, root, "docker", "build", "--tag", image, "."); err != nil {
		return Report{}, fmt.Errorf("build candidate edge container: %w", err)
	}
	if err := securityCommand(ctx, repoRoot, "docker", "run", "--detach", "--rm", "--network", "host", "--name", name, "--mount", "type=bind,source="+configPath+",target=/edge.toml,readonly", image, "serve", "--role", "edge", "--config", "/edge.toml"); err != nil {
		return Report{}, fmt.Errorf("start candidate edge container: %w", err)
	}

	proxyWorks, err := securityProxyWorks(ctx, port)
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

func securityContainerIsolation(ctx context.Context, repoRoot, name string) (bool, bool, bool, error) {
	mounts, err := securityCommandOutput(ctx, repoRoot, "docker", "inspect", "--format", "{{range .Mounts}}{{println .Source}}{{end}}", name)
	if err != nil {
		return false, false, false, err
	}
	env, err := securityCommandOutput(ctx, repoRoot, "docker", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", name)
	if err != nil {
		return false, false, false, err
	}
	pidText, err := securityCommandOutput(ctx, repoRoot, "docker", "inspect", "--format", "{{.State.Pid}}", name)
	if err != nil {
		return false, false, false, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(pidText))
	if err != nil || pid < 1 {
		return false, false, false, fmt.Errorf("candidate edge container did not expose a process PID")
	}
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return false, false, false, fmt.Errorf("inspect candidate edge file descriptors: %w", err)
	}
	noSocketFD := true
	for _, fd := range fds {
		target, linkErr := os.Readlink(filepath.Join(fdDir, fd.Name()))
		if linkErr == nil && securitySocketReference(target) {
			noSocketFD = false
		}
	}
	return !securitySocketReference(mounts), !securitySocketReference(env), noSocketFD, nil
}

func securitySocketReference(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "docker.sock") || strings.Contains(value, "podman.sock") || strings.Contains(value, "docker_host=") || strings.Contains(value, "podman_host=")
}

func securityBuildCandidate(ctx context.Context, repoRoot, output string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", output, "./main.go") // #nosec G204 -- fixed candidate build command.
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build failed")
	}
	return nil
}

func securityCommand(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed compatibility harness commands.
	cmd.Dir = dir
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed", name)
	}
	return nil
}

func securityCommandOutput(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed compatibility harness commands.
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
