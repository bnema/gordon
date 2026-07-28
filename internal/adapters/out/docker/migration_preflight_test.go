package docker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeProbeRuntimeEnvironmentRecognizesRootlessPodman6(t *testing.T) {
	server := preflightFixtureServer(t, `{"Components":[{"Name":"Podman Engine","Version":"6.0.1"}]}`, `{"Rootless":true,"SecurityOptions":["name=rootless"],"DockerRootDir":"/tmp"}`)
	defer server.Close()

	report, err := newTestRuntime(t, server).ProbeRuntimeEnvironment(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "podman", report.Engine)
	assert.True(t, report.Rootless)
	assert.True(t, report.APIReachable)
	assert.True(t, report.ImageAvailable)
	assert.True(t, report.ImagePullable)
	assert.True(t, report.NetworkFeasible)
	assert.NotZero(t, report.DiskAvailable)
}

func TestRuntimeProbeRuntimeEnvironmentFailsClosedForNonAuthoritativePodmanClaims(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		info     string
		engine   string
		rootless bool
	}{
		{name: "docker", version: `{"Components":[{"Name":"Engine","Version":"28.0"}]}`, info: `{"Rootless":true,"SecurityOptions":["name=rootless"],"DockerRootDir":"/tmp"}`, engine: "docker", rootless: true},
		{name: "spoofed info name", version: `{"Components":[{"Name":"Engine","Version":"28.0"}]}`, info: `{"Name":"podman","SecurityOptions":["name=rootless"],"DockerRootDir":"/tmp"}`, engine: "docker", rootless: true},
		{name: "missing components", version: `{"Version":"6.0.1","Os":"linux","Arch":"amd64"}`, info: `{"Name":"podman","Rootless":true,"DockerRootDir":"/tmp"}`, engine: "docker", rootless: true},
		{name: "conflicting components", version: `{"Components":[{"Name":"Podman Engine"},{"Name":"Docker Engine"}]}`, info: `{"Rootless":true,"DockerRootDir":"/tmp"}`, engine: "docker", rootless: true},
		{name: "rootless conflict", version: `{"Components":[{"Name":"Podman Engine"}]}`, info: `{"Rootless":false,"SecurityOptions":["name=rootless"],"DockerRootDir":"/tmp"}`, engine: "podman", rootless: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := preflightFixtureServer(t, tc.version, tc.info)
			defer server.Close()
			report, err := newTestRuntime(t, server).ProbeRuntimeEnvironment(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.engine, report.Engine)
			assert.Equal(t, tc.rootless, report.Rootless)
		})
	}
}

func TestRuntimeProbeEnvironmentRootlessPodmanService(t *testing.T) {
	if os.Getenv("GORDON_COMPAT_PODMAN") != "1" {
		t.Skip("set GORDON_COMPAT_PODMAN=1 to run the rootless Podman service gate")
	}
	podman, err := exec.LookPath("podman")
	require.NoError(t, err)
	socket := filepath.Join(t.TempDir(), "podman.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	service := exec.CommandContext(ctx, podman, "system", "service", "--time=0", "unix://"+socket)
	require.NoError(t, service.Start())
	t.Cleanup(func() {
		if service.Process != nil {
			_ = service.Process.Kill()
		}
		_ = service.Wait()
	})
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			require.NoError(t, ctx.Err(), "rootless Podman service did not create its socket")
		case <-time.After(20 * time.Millisecond):
		}
	}
	runtime, err := NewRuntimeWithSocket(socket)
	require.NoError(t, err)
	report, err := runtime.ProbeRuntimeEnvironment(ctx)
	require.NoError(t, err)
	assert.Equal(t, "podman", report.Engine)
	assert.True(t, report.Rootless)
}

func TestRuntimeProbePublicListenersAcceptsOnlyManagedMonolithAndRejectsRaces(t *testing.T) {
	port := freeFixturePort(t)
	managed := fmt.Sprintf(`[{"Id":"managed","Names":["/gordon-monolith"],"State":"running","Labels":{"gordon.managed":"true"},"Ports":[{"PublicPort":%d,"Type":"tcp"}]}]`, port)
	unrelated := fmt.Sprintf(`[{"Id":"other","Names":["/other"],"State":"running","Labels":{},"Ports":[{"PublicPort":%d,"Type":"tcp"}]}]`, port)

	for _, tc := range []struct {
		name      string
		responses []string
		want      bool
	}{
		{name: "managed monolith", responses: []string{managed}, want: false},
		{name: "managed route is not monolith", responses: []string{fmt.Sprintf(`[{"Id":"route","Names":["/gordon-app-example-test"],"State":"running","Labels":{"gordon.managed":"true","gordon.route":"app.example.test"},"Ports":[{"PublicPort":%d,"Type":"tcp"}]}]`, port)}, want: false},
		{name: "unrelated container", responses: []string{unrelated}, want: false},
		{name: "container bind race", responses: []string{"[]", unrelated}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1.41/containers/json" {
					t.Fatalf("unexpected Docker-compatible request %s", r.URL.Path)
				}
				i := int(calls.Add(1) - 1)
				if i >= len(tc.responses) {
					i = len(tc.responses) - 1
				}
				_, _ = w.Write([]byte(tc.responses[i]))
			}))
			defer server.Close()

			available, err := newTestRuntime(t, server).ProbePublicListeners(context.Background(), []int{port})
			require.NoError(t, err)
			assert.Equal(t, []bool{tc.want}, available)
		})
	}
}

func freeFixturePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func preflightFixtureServer(t *testing.T, version, info string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_ping":
			w.WriteHeader(http.StatusOK)
		case "/version":
			_, _ = w.Write([]byte(version))
		case "/v1.41/info":
			_, _ = w.Write([]byte(info))
		case "/v1.41/images/json", "/v1.41/networks":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected Docker-compatible request %s", r.URL.Path)
		}
	}))
}
