package app

import (
	"context"
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/zerowrap"
)

func TestBuildAuthConfig_UsesConfigEnabledFlag(t *testing.T) {
	t.Setenv(TokenSecretEnvVar, "test-token-secret")

	cfg := Config{}
	cfg.Auth.Enabled = false
	cfg.Auth.Type = "token"

	authCfg, err := buildAuthConfig(context.Background(), cfg, domain.AuthTypeToken, domain.SecretsBackendUnsafe, t.TempDir(), zerowrap.Default())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if authCfg.Enabled {
		t.Fatalf("expected auth config enabled=false, got true")
	}
}
