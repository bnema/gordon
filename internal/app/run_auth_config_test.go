package app

import (
	"context"
	"strings"
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

func TestResolveSecretsBackend_RejectsMissingBackend(t *testing.T) {
	_, err := resolveSecretsBackend("")
	if err == nil {
		t.Fatal("expected error for missing secrets backend")
	}
	if !strings.Contains(err.Error(), "auth.secrets_backend is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveSecretsBackend_RejectsUnknownBackend(t *testing.T) {
	_, err := resolveSecretsBackend("vault")
	if err == nil {
		t.Fatal("expected error for unsupported secrets backend")
	}
	if !strings.Contains(err.Error(), `unsupported auth.secrets_backend "vault"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
