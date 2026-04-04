package remote

import (
	"testing"
)

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
