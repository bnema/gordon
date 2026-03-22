package app

import (
	"context"
	"strings"
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
)

func TestBuildAuthConfig_UsesConfigEnabledFlag(t *testing.T) {
	t.Setenv(TokenSecretEnvVar, "test-token-secret-that-is-at-least-32-bytes-long")

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

func TestResolveAuthType_RejectsPassword(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Type = "password"
	_, err := resolveAuthType(cfg)
	if err == nil {
		t.Fatal("expected error for auth.type=password")
	}
	if !strings.Contains(err.Error(), "unsupported auth.type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveAuthType_AcceptsToken(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Type = "token"
	authType, err := resolveAuthType(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authType != domain.AuthTypeToken {
		t.Fatalf("expected AuthTypeToken, got %v", authType)
	}
}

func TestResolveAuthType_AcceptsEmpty(t *testing.T) {
	cfg := Config{}
	authType, err := resolveAuthType(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authType != domain.AuthTypeToken {
		t.Fatalf("expected AuthTypeToken, got %v", authType)
	}
}

func TestLoadConfig_DoesNotDefaultSecretsBackendToUnsafe(t *testing.T) {
	v := viper.New()

	err := loadConfig(v, "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if got := v.GetString("auth.secrets_backend"); got != "" {
		t.Fatalf("expected empty default for auth.secrets_backend, got %q", got)
	}
}
