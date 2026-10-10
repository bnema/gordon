package domain_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

const guardContainer = "gordon-blog--web--x"

func guardConfig(binds ...domain.ContainerBind) *domain.ContainerConfig {
	return &domain.ContainerConfig{
		Name:        guardContainer,
		NetworkMode: "gordon-app-1",
		Labels:      map[string]string{domain.LabelApp: "blog"},
		Binds:       binds,
	}
}

func TestRuntimeGuard_NetworkModes(t *testing.T) {
	var guard domain.RuntimeGuard
	for _, mode := range []string{"host", "container:abc", "ns:/proc/1/ns/net"} {
		cfg := guardConfig()
		cfg.NetworkMode = mode
		err := guard.Check(cfg)
		require.ErrorIs(t, err, domain.ErrRuntimeGuard, mode)
		assert.NotContains(t, err.Error(), "/proc")
	}
	require.NoError(t, guard.Check(guardConfig()))
}

func TestRuntimeGuard_VolumeNames(t *testing.T) {
	var guard domain.RuntimeGuard
	for _, name := range []string{"/etc", "a/b", `a\b`} {
		cfg := guardConfig()
		cfg.Volumes = map[string]string{"/data": name}
		require.ErrorIs(t, guard.Check(cfg), domain.ErrRuntimeGuard, name)

		cfg = guardConfig()
		cfg.ReadOnlyVolumes = map[string]string{"/data": name}
		require.ErrorIs(t, guard.Check(cfg), domain.ErrRuntimeGuard, name)
	}
	cfg := guardConfig()
	cfg.Volumes = map[string]string{"/data": "gordon-blog--web--vol--data"}
	require.NoError(t, guard.Check(cfg))
}

func TestRuntimeGuard_ProtectedBinds(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	protectedChild := filepath.Join(dataDir, "registry")
	require.NoError(t, os.MkdirAll(protectedChild, 0o700))
	unrelated := filepath.Join(base, "public")
	require.NoError(t, os.MkdirAll(unrelated, 0o700))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(dataDir, link))

	guard := domain.RuntimeGuard{ProtectedPaths: []string{dataDir}}
	cases := map[string]string{
		"equal":    dataDir,
		"under":    protectedChild,
		"ancestor": base,
		"symlink":  link,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			err := guard.Check(guardConfig(domain.ContainerBind{Name: "conf", Source: source, Destination: "/etc/app", ReadOnly: true}))
			require.ErrorIs(t, err, domain.ErrRuntimeGuard)
			assert.Contains(t, err.Error(), `"conf"`)
			assert.NotContains(t, err.Error(), base)
		})
	}
	require.NoError(t, guard.Check(guardConfig(domain.ContainerBind{Name: "conf", Source: unrelated, Destination: "/etc/app", ReadOnly: true})))
}

func TestRuntimeGuard_OwnSecretBindException(t *testing.T) {
	dataDir := t.TempDir()
	secretDir := filepath.Join(dataDir, "secrets-run")
	guard := domain.RuntimeGuard{ProtectedPaths: []string{dataDir}, SecretFilesDir: secretDir}
	own := domain.ContainerBind{
		Name:        "secret-file-DB",
		Source:      filepath.Join(secretDir, "blog", guardContainer, "DB"),
		Destination: "/run/secrets/DB",
		ReadOnly:    true,
	}
	require.NoError(t, guard.Check(guardConfig(own)))

	cases := map[string]func(*domain.ContainerConfig){
		"read-write":   func(c *domain.ContainerConfig) { c.Binds[0].ReadOnly = false },
		"wrong dest":   func(c *domain.ContainerConfig) { c.Binds[0].Destination = "/run/secrets/OTHER" },
		"wrong app":    func(c *domain.ContainerConfig) { c.Labels[domain.LabelApp] = "other" },
		"no app label": func(c *domain.ContainerConfig) { c.Labels = nil },
		"wrong name":   func(c *domain.ContainerConfig) { c.Name = "gordon-blog--web--y" },
		"extra segment": func(c *domain.ContainerConfig) {
			c.Binds[0].Source = filepath.Join(secretDir, "blog", guardContainer, "sub", "DB")
		},
		"directory bind": func(c *domain.ContainerConfig) { c.Binds[0].Source = filepath.Join(secretDir, "blog", guardContainer) },
		"whole base":     func(c *domain.ContainerConfig) { c.Binds[0].Source = secretDir },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := guardConfig(own)
			mutate(cfg)
			err := guard.Check(cfg)
			require.ErrorIs(t, err, domain.ErrRuntimeGuard)
			assert.NotContains(t, err.Error(), dataDir)
		})
	}
}

func TestRuntimeGuard_ReadOnlyLogDirs(t *testing.T) {
	dataDir := t.TempDir()
	logs := filepath.Join(dataDir, "logs")
	require.NoError(t, os.MkdirAll(filepath.Join(logs, "containers"), 0o700))
	keys := filepath.Join(dataDir, "logs", "keys")
	secrets := filepath.Join(dataDir, "secrets-run")
	require.NoError(t, os.MkdirAll(secrets, 0o700))
	escape := filepath.Join(logs, "escape")
	require.NoError(t, os.Symlink(secrets, escape))
	guard := domain.RuntimeGuard{ProtectedPaths: []string{dataDir}, LogDirs: []string{logs}}
	bind := func(source string, readOnly bool) *domain.ContainerConfig {
		return guardConfig(domain.ContainerBind{Name: "logs", Source: source, Destination: "/var/log/gordon", ReadOnly: readOnly})
	}

	require.NoError(t, guard.Check(bind(logs, true)))
	require.NoError(t, guard.Check(bind(filepath.Join(logs, "containers"), true)))

	withProtected := domain.RuntimeGuard{ProtectedPaths: []string{dataDir, keys}, LogDirs: []string{logs}}
	refused := map[string]struct {
		guard domain.RuntimeGuard
		cfg   *domain.ContainerConfig
	}{
		"writable log dir":       {guard, bind(logs, false)},
		"writable log subdir":    {guard, bind(filepath.Join(logs, "containers"), false)},
		"data dir read-only":     {guard, bind(dataDir, true)},
		"sibling of logs":        {guard, bind(filepath.Join(dataDir, "registry"), true)},
		"symlink out of logs":    {guard, bind(escape, true)},
		"log dir with protected": {withProtected, bind(logs, true)},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, tc.guard.Check(tc.cfg), domain.ErrRuntimeGuard)
		})
	}
}
