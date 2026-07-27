package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	outruntime "github.com/bnema/gordon/internal/adapters/out/grpc/runtime"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type RuntimeControlConfig struct {
	Endpoint                    string `mapstructure:"endpoint"`
	ListenAddress               string `mapstructure:"listen_address"`
	Token                       string `mapstructure:"token"`
	TokenEnv                    string `mapstructure:"token_env"`
	RegistryStorageRoot         string `mapstructure:"registry_storage_root"`
	MigrationStateRoot          string `mapstructure:"migration_state_root"`
	ManagedControlSecretsVolume string `mapstructure:"managed_control_secrets_volume"`
	Insecure                    bool   `mapstructure:"insecure"`
}

func createRuntimeCommandClient(_ context.Context, cfg RuntimeControlConfig) (out.RuntimeCommandClient, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, nil
	}
	unixPath, unixEndpoint := runtimeUnixEndpoint(endpoint)
	if strings.HasPrefix(strings.ToLower(endpoint), "unix:") && !unixEndpoint {
		return nil, fmt.Errorf("runtime Unix endpoint must be the generated migration socket")
	}
	transportCredentials := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	newBearerCredentials := grpcauth.NewBearerTokenCredentials
	target := endpoint
	if unixEndpoint {
		if runtimeControlToken(cfg) == "" {
			return nil, fmt.Errorf("unix runtime transport requires a scoped component token")
		}
		transportCredentials = insecure.NewCredentials()
		newBearerCredentials = grpcauth.NewInsecureBearerTokenCredentials
		target = "passthrough:///runtime-control"
	} else if cfg.Insecure {
		// Existing non-migration deployments may explicitly opt in to plaintext
		// TCP. Generated migration role configs never set this; their Unix
		// bootstrap is authenticated by a scoped component token.
		transportCredentials = insecure.NewCredentials()
		newBearerCredentials = grpcauth.NewInsecureBearerTokenCredentials
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(transportCredentials)}
	if unixEndpoint {
		opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", unixPath)
		}))
	}
	if token := runtimeControlToken(cfg); token != "" {
		creds, err := newBearerCredentials(token)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithPerRPCCredentials(creds))
	}
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("create runtime command client: %w", err)
	}
	return outruntime.NewClient(conn), nil
}

// createPostHandoffRuntimeCommandClient is intentionally separate from the
// normal runtime client. Post-handoff recovery runs on the host, where the
// checkpoint's component-visible socket has already been translated and
// validated against this configured data directory. It will never accept a
// generic host or engine Unix endpoint.
func createPostHandoffRuntimeCommandClient(_ context.Context, cfg RuntimeControlConfig, dataDir string) (out.RuntimeCommandClient, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	path, ok := postHandoffRuntimeUnixSocket(endpoint, dataDir)
	if !ok {
		return nil, fmt.Errorf("post-handoff runtime transport is invalid")
	}
	return createPrivateRuntimeCommandClient(cfg, "passthrough:///post-handoff-runtime", func(ctx context.Context) (net.Conn, error) {
		return dialValidatedRuntimeSocket(ctx, path)
	})
}

// postHandoffRuntimeUnixSocket accepts precisely a currently-existing Gordon
// migration socket rooted in the host's configured data directory. Lstat is
// deliberate: recovery must not follow a replacement symlink to an engine
// socket or an arbitrary host service after checkpoint validation.
func postHandoffRuntimeUnixSocket(endpoint, dataDir string) (string, bool) {
	path, ok := runtimeBootstrapSocketPath(endpoint, resolveDataDir(dataDir))
	if !ok {
		return "", false
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return "", false
	}
	return path, true
}

// createRuntimeStateSubscriber exposes only the narrow actual-state stream to
// control orchestration. The control role never receives a runtime socket or a
// container adapter.
// createRuntimeRouteDrainAckReceiver exposes only the opaque route-drain relay
// used by control after it validates an edge acknowledgement.
func createRuntimeRouteDrainAckReceiver(ctx context.Context, cfg RuntimeControlConfig) (out.RouteDrainAckReceiver, error) {
	client, err := createRuntimeCommandClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("runtime.endpoint is required for route drain relay")
	}
	receiver, ok := client.(out.RouteDrainAckReceiver)
	if !ok {
		return nil, fmt.Errorf("runtime command client does not provide route drain acknowledgement")
	}
	return receiver, nil
}

// newRuntimeHandoffDialer dials only the validated host bind source paired
// with the replacement runtime's fixed component listener. The host endpoint
// is a private transient descriptor and is never copied into configuration,
// lifecycle commands, checkpoints, status, or errors.
func newRuntimeHandoffDialer(cfg RuntimeControlConfig) RuntimeHandoffDialer {
	return func(_ context.Context, component ComponentLaunchComponent) (RuntimeHandoffClient, error) {
		endpoints := component.BootstrapEndpoints
		if component.Role != domain.ComponentRoleRuntime || len(component.PortPublishes) != 0 || !endpoints.valid() || endpoints.migrationID != componentMigrationID(component) {
			return nil, fmt.Errorf("replacement runtime bootstrap transport is invalid")
		}
		client, err := createPrivateBootstrapRuntimeCommandClient(cfg, "passthrough:///runtime-bootstrap", func(ctx context.Context) (net.Conn, error) {
			if !endpoints.valid() {
				return nil, status.Error(codes.PermissionDenied, "replacement runtime transport is invalid")
			}
			return dialValidatedRuntimeSocket(ctx, endpoints.hostDialPath())
		})
		if err != nil {
			return nil, err
		}
		handoff, ok := client.(RuntimeHandoffClient)
		if !ok {
			return nil, fmt.Errorf("replacement runtime client lacks handoff protocol")
		}
		return handoff, nil
	}
}

func createPrivateRuntimeCommandClient(cfg RuntimeControlConfig, target string, dial func(context.Context) (net.Conn, error)) (out.RuntimeCommandClient, error) {
	return createPrivateRuntimeCommandClientWithOptions(cfg, target, dial)
}

func createPrivateBootstrapRuntimeCommandClient(cfg RuntimeControlConfig, target string, dial func(context.Context) (net.Conn, error)) (out.RuntimeCommandClient, error) {
	return createPrivateRuntimeCommandClientWithOptions(cfg, target, dial,
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  25 * time.Millisecond,
				Multiplier: 1.4,
				Jitter:     0.2,
				MaxDelay:   100 * time.Millisecond,
			},
			MinConnectTimeout: 100 * time.Millisecond,
		}),
	)
}

func createPrivateRuntimeCommandClientWithOptions(cfg RuntimeControlConfig, target string, dial func(context.Context) (net.Conn, error), extraOptions ...grpc.DialOption) (out.RuntimeCommandClient, error) {
	token := runtimeControlToken(cfg)
	if token == "" {
		return nil, fmt.Errorf("private runtime authentication token is required")
	}
	creds, err := grpcauth.NewInsecureBearerTokenCredentials(token)
	if err != nil {
		return nil, fmt.Errorf("create private runtime credentials: %w", err)
	}
	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dial(ctx) }),
		grpc.WithPerRPCCredentials(creds),
	}
	dialOptions = append(dialOptions, extraOptions...)
	conn, err := grpc.NewClient(target, dialOptions...)
	if err != nil {
		return nil, fmt.Errorf("create private runtime client: %w", err)
	}
	return &privateRuntimeCommandClient{Client: outruntime.NewClient(conn), conn: conn}, nil
}

type privateRuntimeCommandClient struct {
	*outruntime.Client
	conn *grpc.ClientConn
}

func (c *privateRuntimeCommandClient) Close() error {
	return c.conn.Close()
}

// WaitForReady retries ordinary picker errors but deliberately fails gRPC
// status errors. Keep only ENOENT and ECONNREFUSED transient; validation and
// permission failures retain a terminal status and fail closed.
var errPrivateRuntimeTransportUnavailable = errors.New("private runtime transport is unavailable")

func dialValidatedRuntimeSocket(ctx context.Context, path string) (net.Conn, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != bootstrapRuntimeSocketName || pathContainsSymlink(filepath.Dir(path)) {
		return nil, status.Error(codes.PermissionDenied, "private runtime transport is invalid")
	}
	info, err := os.Lstat(path)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0) {
		return nil, status.Error(codes.PermissionDenied, "private runtime transport is invalid")
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, errPrivateRuntimeTransportUnavailable
	}
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "private runtime transport is invalid")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err == nil {
		return connection, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return nil, errPrivateRuntimeTransportUnavailable
	}
	return nil, status.Error(codes.PermissionDenied, "private runtime transport is invalid")
}

func runtimeUnixEndpoint(endpoint string) (string, bool) {
	return runtimeBootstrapSocketPath(endpoint, componentDataDirectory)
}

func createRuntimeStateSubscriber(ctx context.Context, cfg RuntimeControlConfig) (out.RuntimeStateSubscriber, error) {
	client, err := createRuntimeCommandClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("runtime.endpoint is required for control route snapshots")
	}
	subscriber, ok := client.(out.RuntimeStateSubscriber)
	if !ok {
		return nil, fmt.Errorf("runtime command client does not provide actual-state subscription")
	}
	return subscriber, nil
}

func runtimeControlToken(cfg RuntimeControlConfig) string {
	if token := strings.TrimSpace(cfg.Token); token != "" {
		return token
	}
	if envKey := strings.TrimSpace(cfg.TokenEnv); envKey != "" {
		return strings.TrimSpace(os.Getenv(envKey))
	}
	return ""
}
