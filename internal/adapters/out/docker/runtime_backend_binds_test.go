package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// backendBindsRuntime builds a docker runtime whose container inspection
// reports the given port mappings.
func backendBindsRuntime(t *testing.T, portsJSON string) *Runtime {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/abc123/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"abc123","NetworkSettings":{"Ports":` + portsJSON + `}}`))
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.New(client.WithHost("tcp://"+host), client.WithAPIVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	return NewRuntimeWithClient(cli)
}

// TestRuntime_GetContainerBackendBindsRejectsMalformedMappings proves only
// an exact single loopback binding is accepted: a wildcard, non-loopback,
// empty, or duplicated mapping fails closed.
func TestRuntime_GetContainerBackendBindsRejectsMalformedMappings(t *testing.T) {
	cases := []struct {
		name  string
		ports string
	}{
		{"wildcard", `{"9000/tcp":[{"HostIp":"0.0.0.0","HostPort":"32771"}]}`},
		{"non-loopback", `{"9000/tcp":[{"HostIp":"192.168.1.5","HostPort":"32771"}]}`},
		{"empty host ip", `{"9000/tcp":[{"HostIp":"","HostPort":"32771"}]}`},
		{"multiple bindings", `{"9000/tcp":[{"HostIp":"127.0.0.1","HostPort":"32771"},{"HostIp":"127.0.0.1","HostPort":"32772"}]}`},
		{"invalid host port", `{"9000/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := backendBindsRuntime(t, tc.ports)
			_, err := runtime.GetContainerBackendBinds(context.Background(), "abc123", []domain.ContainerBackendPort{
				{ContainerPort: 9000, Protocol: domain.NetworkProtocolTCP},
			})
			require.Error(t, err)
		})
	}
}

// TestRuntime_GetContainerBackendBindsResolvesTCPAndUDPTogether proves one
// inspection resolves both a TCP and a UDP bind, including the same
// container port number on both protocols (distinct sockets).
func TestRuntime_GetContainerBackendBindsResolvesTCPAndUDPTogether(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/abc123/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"abc123",
				"NetworkSettings":{"Ports":{
					"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"32770"}],
					"9000/tcp":[{"HostIp":"127.0.0.1","HostPort":"32771"}],
					"9000/udp":[{"HostIp":"127.0.0.1","HostPort":"32772"}]
				}}
			}`))
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.New(client.WithHost("tcp://"+host), client.WithAPIVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)

	runtime := NewRuntimeWithClient(cli)
	binds, err := runtime.GetContainerBackendBinds(context.Background(), "abc123", []domain.ContainerBackendPort{
		{ContainerPort: 5432, Protocol: domain.NetworkProtocolTCP},
		{ContainerPort: 9000, Protocol: domain.NetworkProtocolTCP},
		{ContainerPort: 9000, Protocol: domain.NetworkProtocolUDP},
	})
	require.NoError(t, err)
	require.Len(t, binds, 3)
	assert.Equal(t, domain.ContainerBackendBind{ContainerPort: 5432, HostPort: 32770, Protocol: domain.NetworkProtocolTCP}, binds[0])
	assert.Equal(t, domain.ContainerBackendBind{ContainerPort: 9000, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}, binds[1])
	assert.Equal(t, domain.ContainerBackendBind{ContainerPort: 9000, HostPort: 32772, Protocol: domain.NetworkProtocolUDP}, binds[2])
}

// TestRuntime_GetContainerBackendBindsFailsWhenProtocolMissing proves a
// missing protocol-specific mapping fails closed instead of falling
// back to the other protocol's bind.
func TestRuntime_GetContainerBackendBindsFailsWhenProtocolMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/abc123/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"abc123",
				"NetworkSettings":{"Ports":{
					"9000/tcp":[{"HostIp":"127.0.0.1","HostPort":"32771"}]
				}}
			}`))
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.New(client.WithHost("tcp://"+host), client.WithAPIVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)

	runtime := NewRuntimeWithClient(cli)
	_, err = runtime.GetContainerBackendBinds(context.Background(), "abc123", []domain.ContainerBackendPort{
		{ContainerPort: 9000, Protocol: domain.NetworkProtocolUDP},
	})
	require.ErrorContains(t, err, "9000/udp")
}
