package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"

	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	outruntime "github.com/bnema/gordon/internal/adapters/out/grpc/runtime"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type RuntimeControlConfig struct {
	Endpoint      string `mapstructure:"endpoint"`
	ListenAddress string `mapstructure:"listen_address"`
	Token         string `mapstructure:"token"`
	TokenEnv      string `mapstructure:"token_env"`
	Insecure      bool   `mapstructure:"insecure"`
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

// newRuntimeHandoffDialer dials only the checkpointed private Gordon Unix
// socket of a prepared runtime. Unix uses local insecure transport with the
// required scoped component token; TCP remains TLS-only.
func newRuntimeHandoffDialer(cfg RuntimeControlConfig) RuntimeHandoffDialer {
	return func(ctx context.Context, component ComponentLaunchComponent) (RuntimeHandoffClient, error) {
		if !validBootstrapRuntimeEndpoint(component.BootstrapEndpoint, nil) || component.Role != domain.ComponentRoleRuntime || len(component.PortPublishes) != 0 {
			return nil, fmt.Errorf("replacement runtime bootstrap transport is invalid")
		}
		if runtimeControlToken(cfg) == "" {
			return nil, fmt.Errorf("replacement runtime authentication token is required")
		}
		target := cfg
		target.Endpoint = component.BootstrapEndpoint
		client, err := createRuntimeCommandClient(ctx, target)
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
