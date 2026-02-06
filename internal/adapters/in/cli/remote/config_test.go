package remote

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRemote_InsecureTLSFromClientConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := []byte(`
[client]
mode = "remote"
remote = "https://gordon.example.com"
insecure_tls = true
`)
	path := DefaultClientConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	url, _, insecure, isRemote := ResolveRemote("", "", false)
	if !isRemote {
		t.Fatalf("expected remote mode")
	}
	if url != "https://gordon.example.com" {
		t.Fatalf("unexpected url: %s", url)
	}
	if !insecure {
		t.Fatalf("expected insecure TLS true from client config")
	}
}

func TestResolveInsecureTLSForRemote_FromRemoteEntry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	remotes := &ClientConfig{
		Active: "prod",
		Remotes: map[string]RemoteEntry{
			"prod": {
				URL:         "https://gordon.example.com",
				InsecureTLS: true,
			},
		},
	}
	if err := SaveRemotes("", remotes); err != nil {
		t.Fatalf("save remotes: %v", err)
	}

	if !ResolveInsecureTLSForRemote(false, "prod") {
		t.Fatalf("expected insecure TLS true from remote entry")
	}
}

func TestResolveInsecureTLSForRemote_DefaultSecure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// With no flag, no env, and no config, TLS should be secure by default
	if ResolveInsecureTLSForRemote(false, "") {
		t.Fatalf("expected insecure TLS false by default")
	}
}

func TestResolveInsecureTLSForRemote_EnvAndFlagPrecedence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_INSECURE", "true")

	// env=true should enable when flag is false
	if !ResolveInsecureTLSForRemote(false, "") {
		t.Fatalf("expected insecure TLS true from env")
	}

	// flag=true should always enable
	if !ResolveInsecureTLSForRemote(true, "") {
		t.Fatalf("expected insecure TLS true from flag")
	}
}
