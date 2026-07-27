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
	"time"

	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	outruntime "github.com/bnema/gordon/internal/adapters/out/grpc/runtime"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
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

func createRuntimeCommandClient(ctx context.Context, cfg RuntimeControlConfig) (out.RuntimeCommandClient, error) {
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
	return outruntime.NewOwnedClient(ctx, conn), nil
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
		if closeErr := closeOwnedRuntimeCommandClient(client); closeErr != nil {
			return nil, fmt.Errorf("runtime command client does not provide route drain acknowledgement (close failed: %v)", closeErr)
		}
		return nil, fmt.Errorf("runtime command client does not provide route drain acknowledgement")
	}
	return receiver, nil
}

// newRuntimeHandoffDialer dials only the validated host bind source paired
// with the replacement runtime's fixed component listener. The host endpoint
// is a private transient descriptor and is never copied into configuration,
// lifecycle commands, checkpoints, status, or errors.
func newRuntimeHandoffDialer(cfg RuntimeControlConfig) RuntimeHandoffDialer {
	return func(ctx context.Context, component ComponentLaunchComponent) (RuntimeHandoffClient, error) {
		endpoints := component.BootstrapEndpoints
		if component.Role != domain.ComponentRoleRuntime || len(component.PortPublishes) != 0 || !endpoints.valid() || endpoints.migrationID != componentMigrationID(component) {
			return nil, fmt.Errorf("replacement runtime bootstrap transport is invalid")
		}
		client, err := createPrivateBootstrapRuntimeCommandClient(ctx, cfg, "passthrough:///runtime-bootstrap", func(ctx context.Context) (net.Conn, error) {
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
			if closeErr := closeOwnedRuntimeCommandClient(client); closeErr != nil {
				return nil, fmt.Errorf("replacement runtime client lacks handoff protocol (close failed: %v)", closeErr)
			}
			return nil, fmt.Errorf("replacement runtime client lacks handoff protocol")
		}
		return handoff, nil
	}
}

func createPrivateRuntimeCommandClient(cfg RuntimeControlConfig, target string, dial func(context.Context) (net.Conn, error)) (out.RuntimeCommandClient, error) {
	creds, err := resolvePrivateRuntimeCredentials(cfg)
	if err != nil {
		return nil, err
	}
	return createPrivateRuntimeCommandClientWithCredentials(target, dial, creds)
}

func createPrivateBootstrapRuntimeCommandClient(ctx context.Context, cfg RuntimeControlConfig, target string, dial func(context.Context) (net.Conn, error)) (out.RuntimeCommandClient, error) {
	return createPrivateBootstrapRuntimeCommandClientWithRetry(ctx, cfg, target, dial, waitRuntimeHandoffRetry)
}

func createPrivateBootstrapRuntimeCommandClientWithRetry(ctx context.Context, cfg RuntimeControlConfig, target string, dial func(context.Context) (net.Conn, error), retry func(context.Context) error) (out.RuntimeCommandClient, error) {
	creds, err := resolvePrivateRuntimeCredentials(cfg)
	if err != nil {
		return nil, err
	}
	if err := waitForPrivateRuntimeTransport(ctx, dial, retry); err != nil {
		return nil, fmt.Errorf("wait for private runtime transport: %w", err)
	}
	return createPrivateRuntimeCommandClientWithCredentials(target, dial, creds)
}

func resolvePrivateRuntimeCredentials(cfg RuntimeControlConfig) (credentials.PerRPCCredentials, error) {
	token := runtimeControlToken(cfg)
	if token == "" {
		return nil, fmt.Errorf("private runtime authentication token is required")
	}
	creds, err := grpcauth.NewInsecureBearerTokenCredentials(token)
	if err != nil {
		return nil, fmt.Errorf("create private runtime credentials: %w", err)
	}
	return creds, nil
}

func createPrivateRuntimeCommandClientWithCredentials(target string, dial func(context.Context) (net.Conn, error), creds credentials.PerRPCCredentials) (out.RuntimeCommandClient, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dial(ctx) }),
		grpc.WithPerRPCCredentials(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("create private runtime client: %w", err)
	}
	// The launcher assumes ownership of private handoff clients returned by its
	// dialer. No operation context is attached here because successful handoffs
	// must survive the request that proved them; launcher Close is the owner.
	return outruntime.NewOwnedClient(context.Background(), conn), nil
}

// The application-level bootstrap barrier is the sole owner of transport
// readiness. It retries only connectability failures; validation and permission
// failures retain a terminal status and fail closed.
type privateRuntimeTransportErrorCategory string

const (
	privateRuntimeTransportInvalidShape       privateRuntimeTransportErrorCategory = "invalid_shape"
	privateRuntimeTransportSymlinkAncestor    privateRuntimeTransportErrorCategory = "symlink_ancestor"
	privateRuntimeTransportInvalidNode        privateRuntimeTransportErrorCategory = "invalid_node"
	privateRuntimeTransportInspectionFailure  privateRuntimeTransportErrorCategory = "inspection_failure"
	privateRuntimeTransportConnectPermission  privateRuntimeTransportErrorCategory = "connect_permission"
	privateRuntimeTransportConnectUnavailable privateRuntimeTransportErrorCategory = "connect_unavailable"
	privateRuntimeTransportUnvalidatedFailure privateRuntimeTransportErrorCategory = "unvalidated_failure"
)

var errPrivateRuntimeTransportUnavailable = errors.New("private runtime transport is unavailable [category=" + string(privateRuntimeTransportConnectUnavailable) + "]")

func waitForPrivateRuntimeTransport(ctx context.Context, dial func(context.Context) (net.Conn, error), retry func(context.Context) error) error {
	for {
		connection, err := dial(ctx)
		if err == nil {
			if closeErr := connection.Close(); closeErr != nil {
				return status.Error(codes.Internal, "private runtime readiness probe cleanup failed")
			}
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.DeadlineExceeded) && isTransientPrivateRuntimeTransportError(err) {
				return boundedPrivateRuntimeTransportUnavailable()
			}
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				return context.DeadlineExceeded
			}
			return context.Canceled
		}
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if !isTransientPrivateRuntimeTransportError(err) {
			if category, ok := privateRuntimeTransportValidationCategory(err); ok {
				return privateRuntimeTransportValidationError(category)
			}
			if errors.Is(err, os.ErrPermission) {
				return privateRuntimeTransportValidationError(privateRuntimeTransportConnectPermission)
			}
			return privateRuntimeTransportValidationError(privateRuntimeTransportUnvalidatedFailure)
		}
		if retryErr := retry(ctx); retryErr != nil {
			return canonicalPrivateRuntimeRetryError(retryErr)
		}
	}
}

func isTransientPrivateRuntimeTransportError(err error) bool {
	return errors.Is(err, errPrivateRuntimeTransportUnavailable)
}

func canonicalPrivateRuntimeRetryError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return boundedPrivateRuntimeTransportUnavailable()
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return privateRuntimeTransportValidationError(privateRuntimeTransportUnvalidatedFailure)
	}
}

type boundedPrivateRuntimeTransportError struct{}

func (boundedPrivateRuntimeTransportError) Error() string {
	return errPrivateRuntimeTransportUnavailable.Error()
}

func (boundedPrivateRuntimeTransportError) Unwrap() []error {
	return []error{errPrivateRuntimeTransportUnavailable, context.DeadlineExceeded}
}

func boundedPrivateRuntimeTransportUnavailable() error {
	return boundedPrivateRuntimeTransportError{}
}

func waitRuntimeHandoffRetry(ctx context.Context) error {
	timer := time.NewTimer(runtimeHandoffRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func dialValidatedRuntimeSocket(ctx context.Context, path string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return dialValidatedRuntimeSocketWithDialer(ctx, path, dialer.DialContext)
}

// dialValidatedRuntimeSocketWithDialer classifies connectivity errors only
// after the canonical path and socket inode have been validated. This keeps
// startup retries from widening authority to a missing or replaced node.
func dialValidatedRuntimeSocketWithDialer(ctx context.Context, path string, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if err := validatePrivateRuntimeSocketPath(path, os.Lstat); err != nil {
		return nil, err
	}
	connection, err := dial(ctx, "unix", path)
	if err == nil {
		return connection, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	switch classifyValidatedRuntimeConnectError(err) {
	case validatedRuntimeConnectCanceled:
		return nil, context.Canceled
	case validatedRuntimeConnectDeadline:
		return nil, context.DeadlineExceeded
	case validatedRuntimeConnectRetryable:
		return nil, errPrivateRuntimeTransportUnavailable
	}
	return nil, privateRuntimeTransportValidationError(privateRuntimeTransportConnectPermission)
}

// validatePrivateRuntimeSocketPath maps each Lstat result to a value-free
// category. It deliberately discards path, ownership, and mode details.
func validatePrivateRuntimeSocketPath(path string, lstat func(string) (os.FileInfo, error)) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != bootstrapRuntimeSocketName {
		return privateRuntimeTransportValidationError(privateRuntimeTransportInvalidShape)
	}
	switch inspectRuntimeSocketAncestors(filepath.Dir(path), lstat) {
	case runtimeSocketAncestorsValid:
	case runtimeSocketAncestorSymlink:
		return privateRuntimeTransportValidationError(privateRuntimeTransportSymlinkAncestor)
	case runtimeSocketAncestorMissing, runtimeSocketAncestorInspectionFailure, runtimeSocketAncestorInvalidPath:
		return privateRuntimeTransportValidationError(privateRuntimeTransportInspectionFailure)
	}
	info, err := lstat(path)
	if err != nil {
		return privateRuntimeTransportValidationError(privateRuntimeTransportInspectionFailure)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return privateRuntimeTransportValidationError(privateRuntimeTransportInvalidNode)
	}
	return nil
}

type privateRuntimeTransportValidationStatus struct {
	category privateRuntimeTransportErrorCategory
}

func (e *privateRuntimeTransportValidationStatus) Error() string {
	return e.GRPCStatus().Err().Error()
}

func (e *privateRuntimeTransportValidationStatus) GRPCStatus() *status.Status {
	return status.New(codes.PermissionDenied, fmt.Sprintf("private runtime transport is invalid [category=%s]", e.category))
}

func privateRuntimeTransportValidationError(category privateRuntimeTransportErrorCategory) error {
	return &privateRuntimeTransportValidationStatus{category: canonicalPrivateRuntimeTransportCategory(category)}
}

func privateRuntimeTransportValidationCategory(err error) (privateRuntimeTransportErrorCategory, bool) {
	validation, ok := errors.AsType[*privateRuntimeTransportValidationStatus](err)
	if !ok || validation == nil {
		return "", false
	}
	return canonicalPrivateRuntimeTransportCategory(validation.category), true
}

func canonicalPrivateRuntimeTransportCategory(category privateRuntimeTransportErrorCategory) privateRuntimeTransportErrorCategory {
	switch category {
	case privateRuntimeTransportInvalidShape,
		privateRuntimeTransportSymlinkAncestor,
		privateRuntimeTransportInvalidNode,
		privateRuntimeTransportInspectionFailure,
		privateRuntimeTransportConnectPermission,
		privateRuntimeTransportConnectUnavailable,
		privateRuntimeTransportUnvalidatedFailure:
		return category
	default:
		return privateRuntimeTransportUnvalidatedFailure
	}
}

type validatedRuntimeConnectErrorCategory uint8

const (
	validatedRuntimeConnectRetryable validatedRuntimeConnectErrorCategory = iota
	validatedRuntimeConnectPermission
	validatedRuntimeConnectCanceled
	validatedRuntimeConnectDeadline
)

// classifyValidatedRuntimeConnectError intentionally returns only a category:
// caller-controlled path and syscall details must not escape the trust check.
func classifyValidatedRuntimeConnectError(err error) validatedRuntimeConnectErrorCategory {
	switch {
	case errors.Is(err, context.Canceled):
		return validatedRuntimeConnectCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return validatedRuntimeConnectDeadline
	case errors.Is(err, os.ErrPermission):
		return validatedRuntimeConnectPermission
	default:
		return validatedRuntimeConnectRetryable
	}
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
		if closeErr := closeOwnedRuntimeCommandClient(client); closeErr != nil {
			return nil, fmt.Errorf("runtime command client does not provide actual-state subscription (close failed: %v)", closeErr)
		}
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
