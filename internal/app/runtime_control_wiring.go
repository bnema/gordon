package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	grpcauth "github.com/bnema/gordon/internal/adapters/out/grpc/auth"
	outruntime "github.com/bnema/gordon/internal/adapters/out/grpc/runtime"
	"github.com/bnema/gordon/internal/boundaries/out"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type RuntimeControlConfig struct {
	Endpoint string `mapstructure:"endpoint"`
	Token    string `mapstructure:"token"`
	TokenEnv string `mapstructure:"token_env"`
}

func createRuntimeCommandClient(_ context.Context, cfg RuntimeControlConfig) (out.RuntimeCommandClient, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, nil
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12}))}
	if token := runtimeControlToken(cfg); token != "" {
		creds, err := grpcauth.NewBearerTokenCredentials(token)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithPerRPCCredentials(creds))
	}
	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("create runtime command client: %w", err)
	}
	return outruntime.NewClient(conn), nil
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
