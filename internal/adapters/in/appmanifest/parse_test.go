package appmanifest_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/appmanifest"
	"github.com/bnema/gordon/internal/domain"
)

const validWeb = `
name = "blog"

[env]
APP_ENV = "production"

[services.web]
image = "registry.example.com/blog/web:1.4.2"

[services.web.readiness]
type = "http"
path = "/healthz"

[[services.web.http]]
host = "Blog.Example.COM."
port = 8080

[services.web.secrets]
DATABASE_URL = "database-url"
`

func TestParse_ValidWeb(t *testing.T) {
	spec, warnings, err := appmanifest.Parse([]byte(validWeb), "blog.toml")
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, "blog", spec.Name)
	assert.Equal(t, "production", spec.Env["APP_ENV"])
	require.Len(t, spec.Services, 1)
	svc := spec.Services[0]
	assert.Equal(t, "web", svc.Name)
	assert.Equal(t, "registry.example.com/blog/web:1.4.2", svc.Image)
	assert.Equal(t, "blog.example.com", svc.HTTP[0].Host)
	assert.Equal(t, "auto", svc.HTTP[0].TLS)
	assert.Equal(t, "http", svc.Readiness.Type)
}

func TestParse_SharedNetwork(t *testing.T) {
	doc := `
name = "media"
[services.web]
image = "registry.example.com/media/web:1"
[[network.shared]]
network = "backend"
services = ["web"]
aliases = ["media-web"]
`

	spec, warnings, err := appmanifest.Parse([]byte(doc), "media.toml")
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, spec.Networks, 1)
	assert.Equal(t, domain.AppSharedNetwork{
		Network: "backend", Services: []string{"web"}, Aliases: []string{"media-web"},
	}, spec.Networks[0])
}

func TestParse_FileNameMismatchIsWarning(t *testing.T) {
	_, warnings, err := appmanifest.Parse([]byte(validWeb), "other.toml")
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "other.toml")
}

func TestParse_MissingTagNormalizesToLatest(t *testing.T) {
	doc := `
name = "blog"
[services.web]
image = "registry.example.com/blog/web"
[[services.web.http]]
host = "blog.example.com"
port = 8080
`
	spec, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
	require.NoError(t, err)
	assert.Equal(t, "registry.example.com/blog/web:latest", spec.Services[0].Image)
}

func TestParse_UnknownFieldsRejected(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"top level", "name = \"blog\"\nbogus = 1\n[services.web]\nimage = \"img:1\"\n"},
		{"service", "name = \"blog\"\n[services.web]\nimage = \"img:1\"\nbogus = 1\n"},
		{"readiness", "name = \"blog\"\n[services.web]\nimage = \"img:1\"\n[services.web.readiness]\nbogus = 1\n"},
		{"http", "name = \"blog\"\n[services.web]\nimage = \"img:1\"\n[[services.web.http]]\nhost = \"blog.example.com\"\nport = 8080\nbogus = 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "blog.toml")
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
		})
	}
}

// TestParse_OldSyntaxRejected proves the retired array-of-tables schema
// and its name field are no longer accepted, with no compatibility shim.
func TestParse_OldSyntaxRejected(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			"legacy service array",
			"name = \"blog\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n",
			"[[service]] array-of-tables is a retired app shape",
		},
		{
			"legacy plural service array",
			"name = \"blog\"\n[[services]]\nname = \"web\"\nimage = \"img:1\"\n",
			"[[services]] array-of-tables is a retired app shape",
		},
		{
			"service name as field",
			"name = \"blog\"\n[services.web]\nname = \"web\"\nimage = \"img:1\"\n",
			"unknown TOML fields or tables: services.web.name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "blog.toml")
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestParse_RetiredArraySyntaxHintIsActionable proves the rejected
// [[service]] and [[services]] shapes carry the same stable keyed-schema
// remedy instead of leaking go-toml decoding wording.
func TestParse_RetiredArraySyntaxHintIsActionable(t *testing.T) {
	docs := map[string]string{
		"singular": "name = \"blog\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n",
		"plural":   "name = \"blog\"\n[[services]]\nname = \"web\"\nimage = \"img:1\"\n",
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.ErrorContains(t, err, "keyed schema")
			assert.ErrorContains(t, err, "[services.<name>]")
			assert.ErrorContains(t, err, "[services.web]")
		})
	}
}

// TestParse_NonArrayTablesKeepPlainDiagnostic proves the keyed-schema hint
// stays reserved for real array-of-tables shapes: a [service] table, a
// scalar service value, and a string array under services are not array
// tables, so they keep the plain unknown-field diagnostic instead of
// being described as [[service]]/[[services]].
func TestParse_NonArrayTablesKeepPlainDiagnostic(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			"service table",
			"name = \"blog\"\n[service]\nimage = \"img:1\"\n",
			"unknown TOML fields or tables: service",
		},
		{
			"service scalar",
			"name = \"blog\"\nservice = \"web\"\n",
			"unknown TOML fields or tables: service",
		},
		{
			// services is a keyed table, so an array value fails to decode.
			// Assert only that the diagnostic names the key, never go-toml's
			// internal wording.
			"services string array",
			"name = \"blog\"\nservices = [\"web\"]\n",
			"services",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "blog.toml")
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tc.wantErr))
			assert.NotContains(t, err.Error(), "array-of-tables is a retired app shape")
		})
	}
}

// TestParse_UnknownNestedFieldRejected proves strict decoding still
// reports the dotted path of an unknown table or field under a service.
func TestParse_UnknownNestedFieldRejected(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			"unknown protocol table",
			"name = \"blog\"\n[services.web]\nimage = \"img:1\"\n[[services.web.rcon]]\nport = 28016\n",
			"unknown TOML fields or tables: services.web.rcon",
		},
		{
			"unknown nested table",
			"name = \"blog\"\n[services.web]\nimage = \"img:1\"\n[services.web.extra]\nkey = \"value\"\n",
			"unknown TOML fields or tables: services.web.extra",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "blog.toml")
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestParse_QuotedDottedServiceKey proves a service name containing a
// dot must be quoted, decodes as one key, and is usable by nested tables.
func TestParse_QuotedDottedServiceKey(t *testing.T) {
	doc := `
name = "blog"
[services."web.api"]
image = "registry.example.com/blog/api:1"
[[services."web.api".http]]
host = "api.example.com"
port = 8080
`
	spec, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
	require.NoError(t, err)
	require.Len(t, spec.Services, 1)
	assert.Equal(t, "web.api", spec.Services[0].Name)
	require.Len(t, spec.Services[0].HTTP, 1)
	assert.Equal(t, "api.example.com", spec.Services[0].HTTP[0].Host)
}

// TestParse_UnquotedDottedKeyRejected proves an unquoted dotted key is
// read as nested tables, not as one service name, and is rejected.
func TestParse_UnquotedDottedKeyRejected(t *testing.T) {
	doc := `
name = "blog"
[services.web.api]
image = "registry.example.com/blog/api:1"
`
	_, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	assert.ErrorContains(t, err, "unknown TOML fields or tables: services.web.api")
}

// TestParse_DuplicateServiceTableRejected proves the TOML decoder rejects
// a repeated [services.<name>] table before domain validation runs.
func TestParse_DuplicateServiceTableRejected(t *testing.T) {
	doc := `
name = "blog"
[services.web]
image = "img:1"
[services.web]
image = "img:2"
`
	_, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	assert.ErrorContains(t, err, "already exists")
}

// TestParse_ServiceOrderDeterministic proves map keys are sorted before
// domain conversion so spec.Services order never depends on TOML order.
func TestParse_ServiceOrderDeterministic(t *testing.T) {
	doc := `
name = "blog"
[services.zeta]
image = "img:1"
[services.alpha]
image = "img:2"
[services.mid]
image = "img:3"
`
	for i := 0; i < 3; i++ {
		spec, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
		require.NoError(t, err)
		require.Len(t, spec.Services, 3)
		assert.Equal(t, []string{"alpha", "mid", "zeta"}, []string{
			spec.Services[0].Name, spec.Services[1].Name, spec.Services[2].Name,
		})
	}
}

func TestParse_ServiceEnvRejected(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			"bare service name",
			"name = \"blog\"\n[services.web]\nimage = \"img:1\"\n[services.web.env]\nFOO = \"bar\"\n",
			"[services.web.env]",
		},
		{
			"dotted service name is quoted",
			"name = \"blog\"\n[services.\"web.api\"]\nimage = \"img:1\"\n[services.\"web.api\".env]\nFOO = \"bar\"\n",
			`[services."web.api".env]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "blog.toml")
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestParse_Names(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"bad app", "name = \"Blog!\"\n[services.web]\nimage = \"img:1\"\n", "app name"},
		{"double dash app", "name = \"a--b\"\n[services.web]\nimage = \"img:1\"\n", "--"},
		{"reserved app", "name = \"gordon\"\n[services.web]\nimage = \"img:1\"\n", "reserved"},
		{"no services", "name = \"blog\"\n", "at least one service"},
		{"dup service", "name = \"blog\"\n[services.web]\nimage = \"img:1\"\n[services.web]\nimage = \"img:2\"\n", "already exists"},
		{"dup service case", "name = \"blog\"\n[services.Web]\nimage = \"img:1\"\n", "must match"},
		{"normalized collision", "name = \"blog\"\n[services.\"a.b\"]\nimage = \"img:1\"\n[services.\"a-b\"]\nimage = \"img:2\"\n", "same runtime identifier"},
		{"replicas rejected", "name = \"blog\"\n[services.web]\nimage = \"img:1\"\nreplicas = 2\n", "unknown TOML fields or tables: services.web.replicas"},
		{"volume double dash", "name = \"blog\"\n[services.web]\nimage = \"img:1\"\n[[services.web.volume]]\nname = \"a--b\"\npath = \"/data\"\n", "--"},
		{"shared volume", "name = \"blog\"\n[services.a]\nimage = \"img:1\"\n[[services.a.volume]]\nname = \"d\"\npath = \"/data\"\n[services.b]\nimage = \"img:1\"\n[[services.b.volume]]\nname = \"d\"\npath = \"/data\"\n", "claimed by both"},
		{"env secret key collision", "name = \"blog\"\n[env]\nDB_PASSWORD = \"x\"\n[services.web]\nimage = \"img:1\"\n[services.web.secrets]\nDB_PASSWORD = \"db-password\"\n", "collides with a secret key"},
		{"secret ref in env", "name = \"blog\"\n[env]\nFOO = \"${pass:x}\"\n[services.web]\nimage = \"img:1\"\n", "secret references"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "blog.toml")
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestParse_Readiness(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"bad type", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[services.w.readiness]\ntype = \"exec\"\n", "none|tcp|http|log"},
		{"log needs path", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[services.w.readiness]\ntype = \"log\"\ncontains = \"x\"\n", "path and contains"},
		{"http needs path", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[[services.w.http]]\nhost = \"b.example.com\"\nport = 8080\n[services.w.readiness]\ntype = \"http\"\n", "http readiness requires path"},
		{"udp tcp rejected", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[[services.w.udp]]\nentrypoint = \"udp\"\nport = 9000\npublish = \"0.0.0.0:9000\"\n[services.w.readiness]\ntype = \"tcp\"\n", "UDP-only"},
		{"multi tcp needs port", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[[services.w.http]]\nhost = \"b.example.com\"\nport = 8080\n[[services.w.tcp]]\nentrypoint = \"tcp\"\nport = 9000\npublish = \"9000\"\n[services.w.readiness]\ntype = \"tcp\"\n", "readiness.port is required"},
		{"bad port ref", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[[services.w.http]]\nhost = \"b.example.com\"\nport = 8080\n[services.w.readiness]\ntype = \"tcp\"\nport = 9999\n", "matches no declared container port"},
		{"bad timeout", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[services.w.readiness]\ntimeout = \"99h\"\n", "timeout must be within"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "b.toml")
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestParse_Interfaces(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"tcp needs entrypoint", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[[services.w.tcp]]\nport = 9000\npublish = \"9000\"\n", "entrypoint is required"},
		{"publish hostname", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[[services.w.tcp]]\nentrypoint = \"tcp\"\nport = 9000\npublish = \"example.com:9000\"\n", "literal IP"},
		{"rcon table rejected", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[[services.w.rcon]]\nentrypoint = \"tcp\"\nport = 28016\npublish = \"0.0.0.0:28016\"\npublic = true\n", "unknown TOML fields or tables: services.w.rcon"},
		{"bad host", "name = \"b\"\n[services.w]\nimage = \"i:1\"\n[[services.w.http]]\nhost = \"localhost\"\nport = 8080\n", "not a valid public hostname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "b.toml")
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestParse_Backups(t *testing.T) {
	doc := `
name = "shop"
[services.api]
image = "registry.example.com/shop/api:2.0.0"
[[services.api.http]]
host = "shop.example.com"
port = 8080
[[services.api.database]]
name = "main"
type = "postgres"
schedule = "daily"
[services.api.backup]
postgres = ["main"]
`
	spec, _, err := appmanifest.Parse([]byte(doc), "shop.toml")
	require.NoError(t, err)
	assert.Equal(t, "daily", spec.Services[0].Databases[0].Schedule)

	badRef := strings.Replace(doc, "postgres = [\"main\"]", "postgres = [\"ghost\"]", 1)
	_, _, err = appmanifest.Parse([]byte(badRef), "shop.toml")
	require.ErrorContains(t, err, "unknown database")

	badSchedule := strings.Replace(doc, "schedule = \"daily\"", "schedule = \"minutely\"", 1)
	_, _, err = appmanifest.Parse([]byte(badSchedule), "shop.toml")
	require.ErrorContains(t, err, "schedule must be")

	badEngine := strings.Replace(doc, "type = \"postgres\"", "type = \"mysql\"", 1)
	_, _, err = appmanifest.Parse([]byte(badEngine), "shop.toml")
	require.ErrorContains(t, err, "must be postgres")
}

func TestParse_GameAndStagingExamples(t *testing.T) {
	game := `
name = "rust"
[services.server]
image = "registry.example.com/games/rust:2026.09"
[services.server.readiness]
type = "log"
path = "/data/logs/server.log"
contains = "Server startup complete"
timeout = "5m"
[[services.server.udp]]
entrypoint = "udp"
port = 28015
publish = "0.0.0.0:28015"
[[services.server.tcp]]
entrypoint = "tcp"
port = 28016
publish = "127.0.0.1:28016"
[[services.server.volume]]
name = "rust-data"
path = "/data"
`
	spec, _, err := appmanifest.Parse([]byte(game), "rust.toml")
	require.NoError(t, err)
	assert.Equal(t, "log", spec.Services[0].Readiness.Type)
	require.Len(t, spec.Services[0].TCP, 1)
	assert.Equal(t, 28016, spec.Services[0].TCP[0].Port)

	staging := `
name = "blog-staging"
[env]
APP_ENV = "staging"
[services.web]
image = "registry.example.com/blog/web:1.4.2-rc.1"
[[services.web.http]]
host = "staging-blog.example.com"
port = 8080
`
	spec, _, err = appmanifest.Parse([]byte(staging), "blog-staging.toml")
	require.NoError(t, err)
	assert.Equal(t, "blog-staging", spec.Name)
}

func TestParse_NoFileEnvSecretReads(t *testing.T) {
	// Parser input is bytes only; there is no path parameter that could
	// trigger file, env, or secret reads beyond the manifest input.
	spec, _, err := appmanifest.Parse([]byte(validWeb), "")
	require.NoError(t, err)
	assert.Equal(t, "blog", spec.Name)
}

func TestParse_Binds(t *testing.T) {
	doc := `
name = "blog"
[services.web]
image = "registry.example.com/blog/web:1.4.2"
[[services.web.bind]]
name = "config"
path = "/etc/app.conf"
readonly = true
[[services.web.bind]]
name = "data"
path = "/var/lib/app"
`
	spec, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
	require.NoError(t, err)
	require.Len(t, spec.Services[0].Binds, 2)
	assert.Equal(t, domain.AppBind{Name: "config", Path: "/etc/app.conf", ReadOnly: true}, spec.Services[0].Binds[0])
	assert.Equal(t, domain.AppBind{Name: "data", Path: "/var/lib/app"}, spec.Services[0].Binds[1])
}

func TestParse_BindDestinationRejected(t *testing.T) {
	doc := `
name = "blog"
[services.web]
image = "registry.example.com/blog/web:1.4.2"
[[services.web.bind]]
name = "dev"
path = "/dev"
`
	_, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	assert.Contains(t, err.Error(), "sensitive container path")
}

func TestParse_Devices(t *testing.T) {
	doc := `
name = "demo"
[services.worker]
image = "registry.example.com/demo/worker:1"
devices = ["test_gpu"]
`
	spec, _, err := appmanifest.Parse([]byte(doc), "demo.toml")
	require.NoError(t, err)
	require.Len(t, spec.Services, 1)
	assert.Equal(t, []string{"test_gpu"}, spec.Services[0].Devices)
}

func TestParse_DevicesWrongType(t *testing.T) {
	doc := `
name = "demo"
[services.worker]
image = "registry.example.com/demo/worker:1"
devices = "test_gpu"
`
	_, _, err := appmanifest.Parse([]byte(doc), "demo.toml")
	require.Error(t, err)
}

func TestParse_DevicesMalformedRejected(t *testing.T) {
	doc := `
name = "demo"
[services.worker]
image = "registry.example.com/demo/worker:1"
devices = ["Bad Name!"]
`
	_, _, err := appmanifest.Parse([]byte(doc), "demo.toml")
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
}

func TestParse_DevicesDuplicateRejected(t *testing.T) {
	doc := `
name = "demo"
[services.worker]
image = "registry.example.com/demo/worker:1"
devices = ["test_gpu", "test_gpu"]
`
	_, _, err := appmanifest.Parse([]byte(doc), "demo.toml")
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	assert.Contains(t, err.Error(), "duplicate device")
}

func TestParse_UnknownGPUFieldRejected(t *testing.T) {
	doc := `
name = "demo"
[services.worker]
image = "registry.example.com/demo/worker:1"
gpus = "all"
`
	_, _, err := appmanifest.Parse([]byte(doc), "demo.toml")
	require.Error(t, err)
}
