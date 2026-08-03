package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/app"
)

func TestSplitModeControlPlaneResolution(t *testing.T) {
	t.Run("control endpoint targets remote without local kernel", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		var localCalls int
		originalNewLocalKernelQuiet := newLocalKernelQuiet
		newLocalKernelQuiet = func(string) (*app.Kernel, error) {
			localCalls++
			return nil, errors.New("local kernel should not be initialized")
		}
		t.Cleanup(func() { newLocalKernelQuiet = originalNewLocalKernelQuiet })

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/admin/status", r.URL.Path)
			_, _ = w.Write([]byte(`{"routes":2,"registry_domain":"registry.test","registry_port":5000,"server_port":8080,"auto_route":true,"network_isolation":true,"container_status":{"app.test":"running"}}`))
		}))
		defer server.Close()

		configPath := writeCLIConfig(t, `[control]
endpoint = "`+server.URL+`"
insecure_tls = true
`)

		handle, err := resolveControlPlane(configPath)
		require.NoError(t, err)
		require.True(t, handle.isRemote)
		require.Zero(t, localCalls)

		status, err := handle.plane.GetStatus(context.Background())
		require.NoError(t, err)
		require.Equal(t, 2, status.Routes)
		require.Equal(t, "running", status.ContainerStatus["app.test"])
		require.Zero(t, localCalls)
	})

	t.Run("explicit remote takes precedence over control endpoint", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		controlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("control endpoint should not be used when explicit remote is set")
		}))
		defer controlServer.Close()
		explicitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"routes":7,"registry_domain":"explicit.test","registry_port":5000,"server_port":8080,"auto_route":false,"network_isolation":false,"container_status":{}}`))
		}))
		defer explicitServer.Close()

		configPath := writeCLIConfig(t, `[control]
endpoint = "`+controlServer.URL+`"
`)
		remoteFlag = explicitServer.URL

		handle, err := resolveControlPlane(configPath)
		require.NoError(t, err)
		require.True(t, handle.isRemote)

		status, err := handle.plane.GetStatus(context.Background())
		require.NoError(t, err)
		require.Equal(t, 7, status.Routes)
	})

	t.Run("control endpoint credentials honor CLI precedence", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/auth/token":
				_, password, ok := r.BasicAuth()
				require.True(t, ok, "expected token exchange to use Basic auth")
				require.Equal(t, "flag-token", password)
				_, _ = w.Write([]byte(`{"token":"ephemeral-token","expires_in":60}`))
			case "/admin/status":
				require.Equal(t, "Bearer ephemeral-token", r.Header.Get("Authorization"))
				_, _ = w.Write([]byte(`{"routes":5,"registry_domain":"auth.test","registry_port":5000,"server_port":8080,"auto_route":false,"network_isolation":false,"container_status":{}}`))
			default:
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
		}))
		defer server.Close()

		t.Setenv("GORDON_TOKEN", "env-token")
		tokenFlag = "flag-token"
		configPath := writeCLIConfig(t, `[control]
endpoint = "`+server.URL+`"
token = "config-token"
`)

		handle, err := resolveControlPlane(configPath)
		require.NoError(t, err)
		require.True(t, handle.isRemote)

		status, err := handle.plane.GetStatus(context.Background())
		require.NoError(t, err)
		require.Equal(t, 5, status.Routes)
	})

	t.Run("control endpoint token env falls back after config token", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		t.Setenv("CONTROL_TOKEN", "token-from-custom-env")
		controlCfg := app.ControlConfig{TokenEnv: "CONTROL_TOKEN"}
		token, insecureTLS := resolveConfiguredControlPlaneAuth(controlCfg)

		require.Equal(t, "token-from-custom-env", token)
		require.False(t, insecureTLS)
	})

	t.Run("control endpoint insecure TLS honors CLI precedence", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		t.Setenv("GORDON_INSECURE", "true")
		_, insecureTLS := resolveConfiguredControlPlaneAuth(app.ControlConfig{})
		require.True(t, insecureTLS)
	})

	t.Run("control endpoint takes precedence over active remote fallback", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		activeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("active remote fallback should not be used when control endpoint is configured")
		}))
		defer activeServer.Close()
		require.NoError(t, remote.SaveRemotes("", &remote.ClientConfig{
			Active: "active",
			Remotes: map[string]remote.RemoteEntry{
				"active": {URL: activeServer.URL},
			},
		}))

		controlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/admin/status", r.URL.Path)
			_, _ = w.Write([]byte(`{"routes":6,"registry_domain":"control.test","registry_port":5000,"server_port":8080,"auto_route":false,"network_isolation":true,"container_status":{}}`))
		}))
		defer controlServer.Close()

		handle, err := resolveControlPlane(writeCLIConfig(t, `[control]
endpoint = "`+controlServer.URL+`"
`))
		require.NoError(t, err)
		require.True(t, handle.isRemote)

		status, err := handle.plane.GetStatus(context.Background())
		require.NoError(t, err)
		require.Equal(t, 6, status.Routes)
	})

	t.Run("environment control endpoint targets remote", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		var localCalls int
		originalNewLocalKernelQuiet := newLocalKernelQuiet
		newLocalKernelQuiet = func(string) (*app.Kernel, error) {
			localCalls++
			return nil, errors.New("local kernel should not be initialized")
		}
		t.Cleanup(func() { newLocalKernelQuiet = originalNewLocalKernelQuiet })

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/admin/status", r.URL.Path)
			_, _ = w.Write([]byte(`{"routes":4,"registry_domain":"env.test","registry_port":5000,"server_port":8080,"auto_route":false,"network_isolation":true,"container_status":{}}`))
		}))
		defer server.Close()
		t.Setenv("GORDON_CONTROL_ENDPOINT", server.URL)

		handle, err := resolveControlPlane(writeCLIConfig(t, ``))
		require.NoError(t, err)
		require.True(t, handle.isRemote)
		require.Zero(t, localCalls)

		status, err := handle.plane.GetStatus(context.Background())
		require.NoError(t, err)
		require.Equal(t, 4, status.Routes)
		require.Zero(t, localCalls)
	})

	t.Run("route commands use control endpoint instead of local services", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		var listCalled bool
		var statusCalled bool
		var addCalled bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/admin/routes" && r.URL.RawQuery == "":
				listCalled = true
				_, _ = w.Write([]byte(`{"routes":[{"domain":"app.test","image":"registry.test/app:latest"}]}`))
			case r.Method == http.MethodGet && r.URL.Path == "/admin/routes" && r.URL.Query().Get("detailed") == "true":
				statusCalled = true
				_, _ = w.Write([]byte(`{"routes":[{"domain":"app.test","image":"registry.test/app:latest","container_status":"running"}]}`))
			case r.Method == http.MethodGet && r.URL.Path == "/admin/health":
				_, _ = w.Write([]byte(`{"health":{}}`))
			case r.Method == http.MethodPost && r.URL.Path == "/admin/routes":
				addCalled = true
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"status":"created"}`))
			default:
				t.Fatalf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
		}))
		defer server.Close()

		cfgPath := writeCLIConfig(t, `[control]
endpoint = "`+server.URL+`"
`)
		sections, err := collectRoutesListSections(context.Background(), cfgPath, routesListDeps{})
		require.NoError(t, err)
		require.Len(t, sections, 1)
		require.Equal(t, "remote", sections[0].Kind)
		require.Equal(t, "control", sections[0].Name)
		require.True(t, listCalled)

		statusSections, err := collectRoutesStatusSections(context.Background(), cfgPath, routesStatusDeps{})
		require.NoError(t, err)
		require.Len(t, statusSections, 1)
		require.Equal(t, "remote", statusSections[0].Kind)
		require.Equal(t, "control", statusSections[0].Name)
		require.True(t, statusCalled)

		originalConfigPath := configPath
		configPath = cfgPath
		t.Cleanup(func() { configPath = originalConfigPath })
		cmd := newRoutesAddCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"api.test", "registry.test/api:latest"})
		require.NoError(t, cmd.Execute())
		require.True(t, addCalled)
	})

	t.Run("route inference uses control endpoint before matching saved remote", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		savedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("saved remote inference should not be probed when control endpoint is configured")
		}))
		defer savedServer.Close()
		require.NoError(t, remote.SaveRemotes("", &remote.ClientConfig{
			Remotes: map[string]remote.RemoteEntry{
				"saved": {URL: savedServer.URL},
			},
		}))

		controlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/admin/routes/app.test", r.URL.Path)
			_, _ = w.Write([]byte(`{"domain":"app.test","image":"control.test/app:latest"}`))
		}))
		defer controlServer.Close()

		originalConfigPath := configPath
		configPath = writeCLIConfig(t, `[control]
endpoint = "`+controlServer.URL+`"
`)
		t.Cleanup(func() { configPath = originalConfigPath })

		handle, err := resolveControlPlaneForRouteDomain(context.Background(), "app.test")
		require.NoError(t, err)
		require.True(t, handle.isRemote)

		route, err := handle.plane.GetRoute(context.Background(), "app.test")
		require.NoError(t, err)
		require.Equal(t, "control.test/app:latest", route.Image)
	})

	t.Run("routes add local mode tolerates omitted auth secrets backend", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "gordon.toml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(`[server]
gordon_domain = "gordon.local"
data_dir = "`+filepath.Join(tmpDir, "data")+`"

[auth]
enabled = false
`), 0o600))

		originalConfigPath := configPath
		configPath = cfgPath
		t.Cleanup(func() { configPath = originalConfigPath })

		cmd := newRoutesAddCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"local.test", "registry.test/local:latest"})

		require.NoError(t, cmd.Execute())
		require.Contains(t, out.String(), "Route configured")
	})

	t.Run("no remote and no control endpoint uses local kernel", func(t *testing.T) {
		resetControlPlaneResolutionTestState(t)

		var localCalls int
		originalNewLocalKernelQuiet := newLocalKernelQuiet
		newLocalKernelQuiet = func(string) (*app.Kernel, error) {
			localCalls++
			return &app.Kernel{}, nil
		}
		t.Cleanup(func() { newLocalKernelQuiet = originalNewLocalKernelQuiet })

		handle, err := resolveControlPlane(writeCLIConfig(t, ``))
		require.NoError(t, err)
		require.False(t, handle.isRemote)
		require.Equal(t, 1, localCalls)
	})
}

func resetControlPlaneResolutionTestState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	t.Setenv("GORDON_INSECURE", "")
	t.Setenv("GORDON_CONTROL_ENDPOINT", "")
	remoteFlag = ""
	tokenFlag = ""
	insecureTLSFlag = false
}

func writeCLIConfig(t *testing.T, extra string) string {
	t.Helper()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "gordon.toml")
	contents := `[server]
gordon_domain = "gordon.local"
data_dir = "` + filepath.Join(tmpDir, "data") + `"

[auth]
enabled = false
secrets_backend = "unsafe"

` + extra
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}
