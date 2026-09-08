package appmanifest_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/appmanifest"
)

const validWeb = `
name = "blog"

[env]
APP_ENV = "production"

[[service]]
name = "web"
image = "registry.example.com/blog/web:1.4.2"

[service.readiness]
type = "http"
path = "/healthz"

[[service.http]]
host = "Blog.Example.COM."
port = 8080

[service.secrets]
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
	assert.Equal(t, "registry.example.com/blog/web:1.4.2", svc.Image)
	assert.Equal(t, 1, svc.Replicas)
	assert.Equal(t, "blog.example.com", svc.HTTP[0].Host)
	assert.Equal(t, "auto", svc.HTTP[0].TLS)
	assert.Equal(t, "http", svc.Readiness.Type)
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
[[service]]
name = "web"
image = "registry.example.com/blog/web"
[[service.http]]
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
		{"top level", "name = \"blog\"\nbogus = 1\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n"},
		{"service", "name = \"blog\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\nbogus = 1\n"},
		{"readiness", "name = \"blog\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n[service.readiness]\nbogus = 1\n"},
		{"http", "name = \"blog\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n[[service.http]]\nhost = \"blog.example.com\"\nport = 8080\nbogus = 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(tc.doc), "blog.toml")
			require.Error(t, err)
		})
	}
}

func TestParse_ServiceEnvRejected(t *testing.T) {
	doc := `
name = "blog"
[[service]]
name = "web"
image = "img:1"
[service.env]
FOO = "bar"
`
	_, _, err := appmanifest.Parse([]byte(doc), "blog.toml")
	require.ErrorContains(t, err, "[service.env]")
}

func TestParse_Names(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"bad app", "name = \"Blog!\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n", "app name"},
		{"double dash app", "name = \"a--b\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n", "--"},
		{"reserved app", "name = \"gordon\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n", "reserved"},
		{"no services", "name = \"blog\"\n", "at least one service"},
		{"dup service", "name = \"blog\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n[[service]]\nname = \"web\"\nimage = \"img:2\"\n", "duplicate service"},
		{"dup service case", "name = \"blog\"\n[[service]]\nname = \"Web\"\nimage = \"img:1\"\n", "must match"},
		{"normalized collision", "name = \"blog\"\n[[service]]\nname = \"a.b\"\nimage = \"img:1\"\n[[service]]\nname = \"a-b\"\nimage = \"img:2\"\n", "same runtime identifier"},
		{"replicas", "name = \"blog\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\nreplicas = 2\n", "replicas"},
		{"volume double dash", "name = \"blog\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n[[service.volume]]\nname = \"a--b\"\npath = \"/data\"\n", "--"},
		{"shared volume", "name = \"blog\"\n[[service]]\nname = \"a\"\nimage = \"img:1\"\n[[service.volume]]\nname = \"d\"\npath = \"/data\"\n[[service]]\nname = \"b\"\nimage = \"img:1\"\n[[service.volume]]\nname = \"d\"\npath = \"/data\"\n", "claimed by both"},
		{"env secret key collision", "name = \"blog\"\n[env]\nDB_PASSWORD = \"x\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n[service.secrets]\nDB_PASSWORD = \"db-password\"\n", "collides with a secret key"},
		{"secret ref in env", "name = \"blog\"\n[env]\nFOO = \"${pass:x}\"\n[[service]]\nname = \"web\"\nimage = \"img:1\"\n", "secret references"},
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
		{"bad type", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[service.readiness]\ntype = \"exec\"\n", "none|tcp|http|log"},
		{"log needs path", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[service.readiness]\ntype = \"log\"\ncontains = \"x\"\n", "path and contains"},
		{"http needs path", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.http]]\nhost = \"b.example.com\"\nport = 8080\n[service.readiness]\ntype = \"http\"\n", "http readiness requires path"},
		{"udp tcp rejected", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.udp]]\nentrypoint = \"udp\"\nport = 9000\npublish = \"0.0.0.0:9000\"\n[service.readiness]\ntype = \"tcp\"\n", "UDP-only"},
		{"multi tcp needs port", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.http]]\nhost = \"b.example.com\"\nport = 8080\n[[service.tcp]]\nentrypoint = \"tcp\"\nport = 9000\npublish = \"9000\"\n[service.readiness]\ntype = \"tcp\"\n", "readiness.port is required"},
		{"bad port ref", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.http]]\nhost = \"b.example.com\"\nport = 8080\n[service.readiness]\ntype = \"tcp\"\nport = 9999\n", "matches no declared container port"},
		{"bad timeout", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[service.readiness]\ntimeout = \"99h\"\n", "timeout must be within"},
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
		{"tcp needs entrypoint", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.tcp]]\nport = 9000\npublish = \"9000\"\n", "entrypoint is required"},
		{"publish hostname", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.tcp]]\nentrypoint = \"tcp\"\nport = 9000\npublish = \"example.com:9000\"\n", "literal IP"},
		{"rcon public needs cidrs", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.rcon]]\nentrypoint = \"tcp\"\nport = 28016\npublish = \"0.0.0.0:28016\"\npublic = true\n", "requires trusted_cidrs"},
		{"rcon private with cidrs", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.rcon]]\nentrypoint = \"tcp\"\nport = 28016\npublish = \"127.0.0.1:28016\"\ntrusted_cidrs = [\"10.0.0.0/8\"]\n", "must not set trusted_cidrs"},
		{"bad cidr", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.rcon]]\nentrypoint = \"tcp\"\nport = 28016\npublish = \"0.0.0.0:28016\"\npublic = true\ntrusted_cidrs = [\"nope\"]\n", "not a valid CIDR"},
		{"bad host", "name = \"b\"\n[[service]]\nname = \"w\"\nimage = \"i:1\"\n[[service.http]]\nhost = \"localhost\"\nport = 8080\n", "not a valid public hostname"},
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
[[service]]
name = "api"
image = "registry.example.com/shop/api:2.0.0"
[[service.http]]
host = "shop.example.com"
port = 8080
[[service.database]]
name = "main"
type = "postgres"
schedule = "daily"
[service.backup]
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
[[service]]
name = "server"
image = "registry.example.com/games/rust:2026.09"
[service.readiness]
type = "log"
path = "/data/logs/server.log"
contains = "Server startup complete"
timeout = "5m"
[[service.udp]]
entrypoint = "udp"
port = 28015
publish = "0.0.0.0:28015"
[[service.rcon]]
entrypoint = "tcp"
port = 28016
publish = "127.0.0.1:28016"
[[service.volume]]
name = "rust-data"
path = "/data"
`
	spec, _, err := appmanifest.Parse([]byte(game), "rust.toml")
	require.NoError(t, err)
	assert.Equal(t, "log", spec.Services[0].Readiness.Type)
	assert.False(t, spec.Services[0].RCON[0].Public)

	staging := `
name = "blog-staging"
[env]
APP_ENV = "staging"
[[service]]
name = "web"
image = "registry.example.com/blog/web:1.4.2-rc.1"
[[service.http]]
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
