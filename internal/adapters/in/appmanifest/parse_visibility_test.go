package appmanifest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/appmanifest"
	"github.com/bnema/gordon/internal/domain"
)

// TestParse_VisibilityNormalization proves absent visibility becomes
// explicit public, an internal interface keeps its TLS absent, and a
// public interface still defaults an absent TLS to auto.
func TestParse_VisibilityNormalization(t *testing.T) {
	t.Run("absent visibility stays public with auto tls", func(t *testing.T) {
		doc := `
name = "blog"
[services.web]
image = "registry.example.com/blog/web:1.0.0"
[[services.web.http]]
host = "blog.example.com"
port = 8080
`
		spec, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
		require.NoError(t, err)
		require.Len(t, spec.Services[0].HTTP, 1)
		iface := spec.Services[0].HTTP[0]
		assert.Equal(t, domain.AppVisibilityPublic, iface.Visibility)
		assert.Equal(t, domain.AppTLSAuto, iface.TLS)
	})

	t.Run("explicit internal keeps tls absent and has no host", func(t *testing.T) {
		doc := `
name = "blog"
[services.api]
image = "registry.example.com/blog/api:1.0.0"
[services.api.readiness]
type = "http"
path = "/healthz"
[[services.api.http]]
visibility = "internal"
port = 8080
`
		spec, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
		require.NoError(t, err)
		require.Len(t, spec.Services[0].HTTP, 1)
		iface := spec.Services[0].HTTP[0]
		assert.Equal(t, domain.AppVisibilityInternal, iface.Visibility)
		assert.Empty(t, iface.Host)
		assert.Empty(t, iface.TLS, "an internal interface must not inherit the public auto tls default")
		assert.True(t, spec.Services[0].InternallyOnlyPort(8080))
	})

	t.Run("explicit public normalizes to auto tls", func(t *testing.T) {
		doc := `
name = "blog"
[services.web]
image = "registry.example.com/blog/web:1.0.0"
[[services.web.http]]
host = "blog.example.com"
port = 8080
visibility = "public"
`
		spec, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
		require.NoError(t, err)
		assert.Equal(t, domain.AppVisibilityPublic, spec.Services[0].HTTP[0].Visibility)
		assert.Equal(t, domain.AppTLSAuto, spec.Services[0].HTTP[0].TLS)
	})
}

// TestParse_VisibilityRejections proves invalid combinations fail at
// validation with the sentinel error.
func TestParse_VisibilityRejections(t *testing.T) {
	tests := map[string]string{
		"internal forbids host": `
name = "blog"
[services.api]
image = "registry.example.com/blog/api:1.0.0"
[[services.api.http]]
host = "blog.example.com"
port = 8080
visibility = "internal"
`,
		"internal forbids tls": `
name = "blog"
[services.api]
image = "registry.example.com/blog/api:1.0.0"
[[services.api.http]]
port = 8080
visibility = "internal"
tls = "auto"
`,
		"internal forbids explicitly empty tls": `
name = "blog"
[services.api]
image = "registry.example.com/blog/api:1.0.0"
[[services.api.http]]
port = 8080
visibility = "internal"
tls = ""
`,
		"unknown visibility value": `
name = "blog"
[services.web]
image = "registry.example.com/blog/web:1.0.0"
[[services.web.http]]
host = "blog.example.com"
port = 8080
visibility = "vpn"
`,
		"internal port also published as tcp": `
name = "blog"
[services.api]
image = "registry.example.com/blog/api:1.0.0"
[[services.api.http]]
port = 8080
visibility = "internal"
[[services.api.tcp]]
port = 8080
`,
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
		})
	}
}

// TestParse_VisibilityUnknownFieldStillRejected proves the parser stays
// strict: a near-miss key is still a decode error.
func TestParse_VisibilityUnknownFieldStillRejected(t *testing.T) {
	doc := `
name = "blog"
[services.web]
image = "registry.example.com/blog/web:1.0.0"
[[services.web.http]]
host = "blog.example.com"
port = 8080
visibilities = "internal"
`
	_, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
}
