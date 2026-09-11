package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func validSpec() domain.AppSpec {
	return domain.AppSpec{
		Name: "blog",
		Env:  map[string]string{"APP_ENV": "production"},
		Services: []domain.AppService{
			{
				Name:      "web",
				Image:     "registry.example.com/blog/web:1.4.2",
				StopGrace: 10 * time.Second,
				Readiness: domain.AppReadiness{Type: "http", Path: "/healthz", Timeout: 30 * time.Second},
				HTTP:      []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
				Secrets:   map[string]string{"DATABASE_URL": "database-url"},
			},
		},
	}
}

func TestAppSpec_ValidateOK(t *testing.T) {
	require.NoError(t, validSpec().Validate())
}

func TestAppSpec_ValidateErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*domain.AppSpec)
		wantErr string
	}{
		{"bad app", func(s *domain.AppSpec) { s.Name = "Bad!" }, "app name"},
		{"no services", func(s *domain.AppSpec) { s.Services = nil }, "at least one service"},
		{"no image", func(s *domain.AppSpec) { s.Services[0].Image = "" }, "requires an image"},
		{"env newline", func(s *domain.AppSpec) { s.Env["X"] = "a\nb" }, "newlines"},
		{"env secret ref", func(s *domain.AppSpec) { s.Env["X"] = "${sops:y}" }, "secret references"},
		{"http bad tls", func(s *domain.AppSpec) { s.Services[0].HTTP[0].TLS = "sometimes" }, "auto|always|never"},
		{"http readiness authority", func(s *domain.AppSpec) { s.Services[0].Readiness.Path = "@127.0.0.1:9000/private" }, "origin-form path"},
		{"http readiness absolute URL", func(s *domain.AppSpec) { s.Services[0].Readiness.Path = "http://127.0.0.1/private" }, "origin-form path"},
		{"http readiness scheme relative", func(s *domain.AppSpec) { s.Services[0].Readiness.Path = "//127.0.0.1/private" }, "origin-form path"},
		{"network unknown service", func(s *domain.AppSpec) {
			s.Networks = []domain.AppSharedNetwork{{Network: "n", Services: []string{"ghost"}}}
		}, "unknown service"},
		{"network bad alias", func(s *domain.AppSpec) {
			s.Networks = []domain.AppSharedNetwork{{Network: "n", Services: []string{"web"}, Aliases: []string{"Bad_Alias!"}}}
		}, "DNS label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			tc.mutate(&spec)
			err := spec.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestAppIdentityHelpers(t *testing.T) {
	assert.Equal(t, "gordon-blog--web", domain.LogicalServiceIdentity("blog", "web"))
	assert.Equal(t, "gordon-blog--a-b", domain.LogicalServiceIdentity("blog", "a.b"))
	assert.Equal(t,
		"gordon-blog--db--vol--pgdata",
		domain.RuntimeVolumeName("blog", "db", "pgdata"))
	assert.Equal(t,
		"gordon/apps/blog/web/database-url",
		domain.AppSecretPath("blog", "web", "database-url"))
	assert.Equal(t,
		"gordon/apps/app-uuid-1/web/database-url",
		domain.AppSecretPathForID("app-uuid-1", "blog", "web", "database-url"))
	assert.Equal(t,
		"gordon/apps/blog/web/database-url",
		domain.AppSecretPathForID("", "blog", "web", "database-url"))
	assert.Equal(t, "blog.example.com", domain.CanonicalHTTPHost("Blog.Example.COM."))
}

func TestParsePublish(t *testing.T) {
	host, port, err := domain.ParsePublish("127.0.0.1:8080")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	assert.Equal(t, 8080, port)

	host, port, err = domain.ParsePublish("8080")
	require.NoError(t, err)
	assert.Empty(t, host)
	assert.Equal(t, 8080, port)

	_, _, err = domain.ParsePublish("example.com:8080")
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)

	_, _, err = domain.ParsePublish("8080:9090:extra")
	require.Error(t, err)

	_, _, err = domain.ParsePublish("")
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)

	_, _, err = domain.ParsePublish("0")
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
}

func TestDiffAppSpec_DeterministicAndRedacted(t *testing.T) {
	before := validSpec()
	after := validSpec()
	after.Services = append(after.Services, domain.AppService{
		Name: "worker", Image: "img:1",
		StopGrace: 10 * time.Second,
		Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second},
	})
	after.Services[0].Image = "registry.example.com/blog/web:1.5.0"
	after.Services[0].Secrets = map[string]string{"DATABASE_URL": "database-url-v2"}
	after.Env["NEW_KEY"] = "v"

	diff := domain.DiffAppSpec(after, before)
	assert.Equal(t, []string{"service/worker"}, diff.Added)
	assert.Empty(t, diff.Removed)
	assert.Contains(t, diff.Changed, "service/web/image")
	assert.Contains(t, diff.Changed, "service/web/secret/database-url-v2")
	assert.Contains(t, diff.Changed, "service/web/secret/database-url")
	assert.Contains(t, diff.Changed, "env")
	// Sorted output.
	assert.IsIncreasing(t, diff.Changed)
	// No secret values leak: only names/paths.
	for _, entry := range append(append(diff.Added, diff.Removed...), diff.Changed...) {
		assert.NotContains(t, entry, "s3cr3t")
	}

	// Identical specs produce an empty diff.
	empty := domain.DiffAppSpec(before, validSpec())
	assert.Empty(t, empty.Added)
	assert.Empty(t, empty.Removed)
	assert.Empty(t, empty.Changed)

	// Removed service.
	removed := domain.DiffAppSpec(domain.AppSpec{Name: "x"}, before)
	assert.Equal(t, []string{"service/web"}, removed.Removed)
}

func TestDiffAppSpec_ErrorsWrapped(t *testing.T) {
	assert.True(t, errors.Is(domain.AppSpec{}.Validate(), domain.ErrInvalidAppSpec))
}
