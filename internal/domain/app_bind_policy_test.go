package domain

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validPolicy(root, source string) AppBindPolicy {
	return AppBindPolicy{
		Name:            "config",
		Source:          source,
		AllowedApps:     []string{"blog"},
		AllowedServices: []string{"web"},
		Root:            root,
	}
}

func TestAppBindPolicy_Validate(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds")
	require.NoError(t, os.MkdirAll(source, 0o755))

	require.NoError(t, validPolicy(root, source).Validate())

	cases := []struct {
		name    string
		mutate  func(*AppBindPolicy)
		wantErr string
	}{
		{"empty name", func(p *AppBindPolicy) { p.Name = "" }, "policy name"},
		{"bad name", func(p *AppBindPolicy) { p.Name = "Bad" }, "policy name"},
		{"reserved name", func(p *AppBindPolicy) { p.Name = "a--b" }, "reserved separator"},
		{"relative source", func(p *AppBindPolicy) { p.Source = "binds" }, "must be absolute"},
		{"unclean source", func(p *AppBindPolicy) { p.Source = root + "/binds/../etc" }, "normalized clean path"},
		{"empty source", func(p *AppBindPolicy) { p.Source = "" }, "source must not be empty"},
		{"relative root", func(p *AppBindPolicy) { p.Root = "root" }, "must be absolute"},
		{"unclean root", func(p *AppBindPolicy) { p.Root = root + "/x/.." }, "normalized clean path"},
		{"empty allowed apps", func(p *AppBindPolicy) { p.AllowedApps = nil }, "allowed apps must not be empty"},
		{"empty allowed services", func(p *AppBindPolicy) { p.AllowedServices = nil }, "allowed services must not be empty"},
		{"bad allowed app", func(p *AppBindPolicy) { p.AllowedApps = []string{"Bad!"} }, "allowed app"},
		{"bad allowed service", func(p *AppBindPolicy) { p.AllowedServices = []string{"Bad!"} }, "allowed service"},
		{"duplicate allowed app", func(p *AppBindPolicy) { p.AllowedApps = []string{"blog", "blog"} }, "duplicated"},
		{"duplicate allowed service", func(p *AppBindPolicy) { p.AllowedServices = []string{"web", "web"} }, "duplicated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := validPolicy(root, source)
			tc.mutate(&policy)
			err := policy.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrBindPolicy)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestAppBindPolicy_ResolveAppBind(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds", "config")
	require.NoError(t, os.MkdirAll(source, 0o755))

	policy := validPolicy(root, source)
	resolved, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
	require.NoError(t, err)
	assert.Equal(t, "config", resolved.Name)
	assert.Equal(t, "/etc/app.conf", resolved.Destination)
	assert.False(t, resolved.ReadOnly)
	realSource, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	assert.Equal(t, realSource, resolved.Source)
}

func TestAppBindPolicy_ResolveAppBindReadOnlyNonWeakening(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "file.txt")
	require.NoError(t, os.WriteFile(source, []byte("data"), 0o644))

	cases := []struct {
		name         string
		policyRO     bool
		bindRO       bool
		wantReadOnly bool
	}{
		{"policy enforces read-only", true, false, true},
		{"bind requests read-only", false, true, true},
		{"both read-only", true, true, true},
		{"writable by default", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := validPolicy(root, source)
			policy.ReadOnly = tc.policyRO
			resolved, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf", ReadOnly: tc.bindRO})
			require.NoError(t, err)
			assert.Equal(t, tc.wantReadOnly, resolved.ReadOnly)
		})
	}
}

func TestAppBindPolicy_ResolveAppBindAllowlists(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds")
	require.NoError(t, os.MkdirAll(source, 0o755))
	policy := validPolicy(root, source)

	_, err := policy.ResolveAppBind("other", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
	require.ErrorIs(t, err, ErrBindPolicy)
	assert.Contains(t, err.Error(), "does not allow app")

	_, err = policy.ResolveAppBind("blog", "other", AppBind{Name: "config", Path: "/etc/app.conf"})
	require.ErrorIs(t, err, ErrBindPolicy)
	assert.Contains(t, err.Error(), "does not allow service")

	// Exact, not prefix, matching.
	_, err = policy.ResolveAppBind("blog-staging", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
	require.ErrorIs(t, err, ErrBindPolicy)
}

func TestAppBindPolicy_ResolveAppBindBindShape(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds")
	require.NoError(t, os.MkdirAll(source, 0o755))
	policy := validPolicy(root, source)

	_, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "Bad", Path: "/etc/app.conf"})
	require.ErrorIs(t, err, ErrInvalidAppSpec)

	_, err = policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "relative"})
	require.ErrorIs(t, err, ErrInvalidAppSpec)
}

func TestAppBindPolicy_ResolveAppBindRootBoundary(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "binds"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "binds"), 0o755))

	t.Run("source outside root", func(t *testing.T) {
		policy := validPolicy(root, filepath.Join(outside, "binds"))
		_, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.ErrorIs(t, err, ErrBindPolicy)
		assert.Contains(t, err.Error(), "outside root")
	})

	t.Run("sibling prefix is not inside", func(t *testing.T) {
		// root/../root-sibling must not be treated as a descendant of root.
		sibling := root + "-sibling"
		require.NoError(t, os.MkdirAll(sibling, 0o755))
		t.Cleanup(func() { _ = os.RemoveAll(sibling) })
		policy := validPolicy(root, sibling)
		_, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.ErrorIs(t, err, ErrBindPolicy)
		assert.Contains(t, err.Error(), "outside root")
	})

	t.Run("symlink escaping root is refused", func(t *testing.T) {
		link := filepath.Join(root, "escape")
		require.NoError(t, os.Symlink(filepath.Join(outside, "binds"), link))
		policy := validPolicy(root, link)
		_, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.ErrorIs(t, err, ErrBindPolicy)
		assert.Contains(t, err.Error(), "outside root")
	})

	t.Run("symlink inside root resolves", func(t *testing.T) {
		real := filepath.Join(root, "binds")
		link := filepath.Join(root, "link")
		require.NoError(t, os.Symlink(real, link))
		policy := validPolicy(root, link)
		resolved, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.NoError(t, err)
		realSource, err := filepath.EvalSymlinks(real)
		require.NoError(t, err)
		assert.Equal(t, realSource, resolved.Source)
	})

	t.Run("root itself is inside", func(t *testing.T) {
		policy := validPolicy(root, root)
		_, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.NoError(t, err)
	})

	t.Run("optional root is not required", func(t *testing.T) {
		policy := validPolicy("", filepath.Join(outside, "binds"))
		_, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.NoError(t, err)
	})
}

func TestAppBindPolicy_ResolveAppBindSourceTypes(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "binds"), 0o755))

	t.Run("missing source", func(t *testing.T) {
		policy := validPolicy(root, filepath.Join(root, "binds", "nope"))
		_, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.ErrorIs(t, err, ErrBindPolicy)
	})

	t.Run("fifo refused", func(t *testing.T) {
		fifo := filepath.Join(root, "binds", "fifo")
		require.NoError(t, syscall.Mkfifo(fifo, 0o600))
		policy := validPolicy(root, fifo)
		_, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.ErrorIs(t, err, ErrBindPolicy)
		assert.Contains(t, err.Error(), "regular file or directory")
	})

	t.Run("socket refused", func(t *testing.T) {
		socket := filepath.Join(root, "binds", "sock")
		listener, err := net.Listen("unix", socket)
		require.NoError(t, err)
		defer func() { _ = listener.Close() }()
		policy := validPolicy(root, socket)
		_, err = policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.ErrorIs(t, err, ErrBindPolicy)
		assert.Contains(t, err.Error(), "regular file or directory")
	})

	t.Run("regular file accepted", func(t *testing.T) {
		file := filepath.Join(root, "binds", "app.conf")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
		policy := validPolicy(root, file)
		resolved, err := policy.ResolveAppBind("blog", "web", AppBind{Name: "config", Path: "/etc/app.conf"})
		require.NoError(t, err)
		assert.Equal(t, file, resolved.Source)
	})
}

func TestBindSourceTypeAllowed(t *testing.T) {
	assert.True(t, bindSourceTypeAllowed(0))
	assert.True(t, bindSourceTypeAllowed(os.ModeDir))
	refused := []os.FileMode{
		os.ModeSymlink,
		os.ModeDevice,
		os.ModeCharDevice,
		os.ModeNamedPipe,
		os.ModeSocket,
		os.ModeIrregular,
	}
	for _, mode := range refused {
		assert.False(t, bindSourceTypeAllowed(mode), mode.String())
	}
}

func TestResolvedUnderRoot(t *testing.T) {
	assert.True(t, resolvedUnderRoot("/srv/binds", "/srv/binds"))
	assert.True(t, resolvedUnderRoot("/srv/binds", "/srv/binds/conf"))
	assert.False(t, resolvedUnderRoot("/srv/binds", "/srv/binds-sibling"))
	assert.False(t, resolvedUnderRoot("/srv/binds", "/srv/other"))
	assert.False(t, resolvedUnderRoot("/srv/binds", "/etc"))
}
