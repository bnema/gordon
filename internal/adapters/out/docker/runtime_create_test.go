package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntime_CreateContainerPublishesExplicitUDPPort(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image: "rust:latest",
		Name:  "gordon-rust",
		PortPublishes: []domain.ContainerPortPublish{
			{
				HostIP:        "127.0.0.1",
				HostPort:      38015,
				ContainerPort: 28015,
				Protocol:      domain.NetworkProtocolUDP,
			},
		},
	})

	exposedPorts, ok := createBody["ExposedPorts"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, exposedPorts, "28015/udp")

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	portBindings, ok := hostConfig["PortBindings"].(map[string]any)
	require.True(t, ok)
	bindings, ok := portBindings["28015/udp"].([]any)
	require.True(t, ok)
	require.Len(t, bindings, 1)
	binding, ok := bindings[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", binding["HostIp"])
	assert.Equal(t, "38015", binding["HostPort"])
}

func TestRuntime_CreateContainerKeepsLegacyPortsRandomLoopbackTCP(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image: "nginx:latest",
		Name:  "gordon-app.example.com",
		Ports: []int{8080},
	})

	exposedPorts, ok := createBody["ExposedPorts"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, exposedPorts, "8080/tcp")

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	portBindings, ok := hostConfig["PortBindings"].(map[string]any)
	require.True(t, ok)
	bindings, ok := portBindings["8080/tcp"].([]any)
	require.True(t, ok)
	require.Len(t, bindings, 1)
	binding, ok := bindings[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", binding["HostIp"])
	assert.Equal(t, "0", binding["HostPort"])
}

func TestRuntime_CreateContainerAppliesRestartPolicy(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image:         "nginx:latest",
		Name:          "gordon-app.example.com",
		RestartPolicy: domain.RestartPolicyAlways,
	})

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	restartPolicy, ok := hostConfig["RestartPolicy"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, domain.RestartPolicyAlways, restartPolicy["Name"])
}

func createTestContainer(t *testing.T, config *domain.ContainerConfig) map[string]any {
	t.Helper()

	var createBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1.41/containers/create":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&createBody))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"abc123","Warnings":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.41/containers/abc123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"abc123",
				"Name":"/gordon-app.example.com",
				"Image":"sha256:image",
				"Created":"2026-05-05T00:00:00Z",
				"Config":{"Image":"nginx:latest","Labels":{}},
				"State":{"Status":"created","ExitCode":0},
				"NetworkSettings":{"Ports":{}}
			}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.New(client.WithHost("tcp://"+host), client.WithAPIVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)

	runtime := NewRuntimeWithClient(cli)
	created, err := runtime.CreateContainer(context.Background(), config)
	require.NoError(t, err)
	assert.Equal(t, "abc123", created.ID)
	require.NotNil(t, createBody)

	return createBody
}

// TestRuntime_CreateContainerMountsDeclaredReadOnlyVolumes proves a mount
// declared read-only reaches the real Docker create request as read-only.
func TestRuntime_CreateContainerMountsDeclaredReadOnlyVolumes(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image:           "nginx:latest",
		Name:            "gordon-app",
		Volumes:         map[string]string{"/data": "gordon-vol-data"},
		ReadOnlyVolumes: map[string]string{"/config": "gordon-vol-config"},
	})

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	binds, ok := hostConfig["Binds"].([]any)
	require.True(t, ok)
	assert.Contains(t, binds, "gordon-vol-data:/data")
	assert.Contains(t, binds, "gordon-vol-config:/config:ro")
}

// TestRuntime_CreateContainerAppliesNetworkAndResourceLimits proves the
// isolation contract reaches the real Docker create request: the container
// joins the named private network and carries the configured memory, CPU,
// and PID limits.
func TestRuntime_CreateContainerAppliesNetworkAndResourceLimits(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image:       "nginx:latest",
		Name:        "gordon-app",
		NetworkMode: "gordon-app-abc123",
		Aliases:     []string{"web"},
		Hostname:    "web",
		MemoryLimit: 512 << 20,
		NanoCPUs:    1_500_000_000,
		PidsLimit:   256,
	})

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gordon-app-abc123", hostConfig["NetworkMode"])
	assert.Equal(t, float64(512<<20), hostConfig["Memory"])
	assert.Equal(t, float64(1_500_000_000), hostConfig["NanoCpus"])
	assert.Equal(t, float64(256), hostConfig["PidsLimit"])

	networking, ok := createBody["NetworkingConfig"].(map[string]any)
	require.True(t, ok)
	endpoints, ok := networking["EndpointsConfig"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, endpoints, "gordon-app-abc123")
}

// TestRuntime_CreateContainerTranslatesBindMounts proves ephemeral host binds
// reach the Docker create request as TypeBind mounts in deterministic
// destination order, read-only preserved, alongside named-volume Binds.
func TestRuntime_CreateContainerTranslatesBindMounts(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image:   "nginx:latest",
		Name:    "gordon-app",
		Volumes: map[string]string{"/data": "gordon-vol-data"},
		Binds: []domain.ContainerBind{
			{Name: "rw", Source: "/srv/binds/rw", Destination: "/etc/app.conf"},
			{Name: "ro", Source: "/srv/binds/ro", Destination: "/var/lib/data", ReadOnly: true},
		},
	})

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)

	// Existing named-volume Binds are preserved alongside explicit mounts.
	binds, ok := hostConfig["Binds"].([]any)
	require.True(t, ok)
	assert.Contains(t, binds, "gordon-vol-data:/data")

	mounts, ok := hostConfig["Mounts"].([]any)
	require.True(t, ok)
	require.Len(t, mounts, 2)

	first, ok := mounts[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "bind", first["Type"])
	assert.Equal(t, "/srv/binds/rw", first["Source"])
	assert.Equal(t, "/etc/app.conf", first["Target"])
	assert.NotContains(t, first, "ReadOnly")

	second, ok := mounts[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "bind", second["Type"])
	assert.Equal(t, "/srv/binds/ro", second["Source"])
	assert.Equal(t, "/var/lib/data", second["Target"])
	assert.Equal(t, true, second["ReadOnly"])
}

func TestBuildBindMounts(t *testing.T) {
	t.Run("sorts by destination", func(t *testing.T) {
		mounts, err := buildBindMounts(&domain.ContainerConfig{Binds: []domain.ContainerBind{
			{Name: "z", Source: "/srv/binds/z", Destination: "/z"},
			{Name: "a", Source: "/srv/binds/a", Destination: "/a"},
		}})
		require.NoError(t, err)
		require.Len(t, mounts, 2)
		assert.Equal(t, "/a", mounts[0].Target)
		assert.Equal(t, "/z", mounts[1].Target)
	})

	t.Run("invalid paths rejected without echoing source", func(t *testing.T) {
		cases := []struct {
			name string
			bind domain.ContainerBind
		}{
			{"relative source", domain.ContainerBind{Name: "b", Source: "srv/binds", Destination: "/etc/app.conf"}},
			{"empty source", domain.ContainerBind{Name: "b", Destination: "/etc/app.conf"}},
			{"unclean source", domain.ContainerBind{Name: "b", Source: "/srv/../binds", Destination: "/etc/app.conf"}},
			{"relative destination", domain.ContainerBind{Name: "b", Source: "/srv/binds", Destination: "etc/app.conf"}},
			{"sensitive destination", domain.ContainerBind{Name: "b", Source: "/srv/binds", Destination: "/dev"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := buildBindMounts(&domain.ContainerConfig{Binds: []domain.ContainerBind{tc.bind}})
				require.Error(t, err)
				if tc.bind.Source != "" {
					assert.NotContains(t, err.Error(), tc.bind.Source, "errors must never echo the host source")
				}
			})
		}
	})

	t.Run("duplicate destination rejected", func(t *testing.T) {
		_, err := buildBindMounts(&domain.ContainerConfig{Binds: []domain.ContainerBind{
			{Name: "one", Source: "/srv/binds/one", Destination: "/etc/app.conf"},
			{Name: "two", Source: "/srv/binds/two", Destination: "/etc/app.conf"},
		}})
		require.Error(t, err)
	})
}
