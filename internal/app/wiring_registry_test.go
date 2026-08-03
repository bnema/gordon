package app

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

type registryRoleTestPublisher struct {
	events chan domain.ComponentEventEnvelope
}

func (p registryRoleTestPublisher) PublishComponentEvent(_ context.Context, event domain.ComponentEventEnvelope) error {
	p.events <- event
	return nil
}

func TestRegistryRoleServesOCIHealthWithoutRuntimeGraph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.toml")
	require.NoError(t, os.WriteFile(path, []byte("[storage]\ndata_dir = \""+filepath.Join(t.TempDir(), "data")+"\"\n[listen]\naddress = \"127.0.0.1:0\"\n[listen.tls]\nmode = \"disabled\"\n[control]\nevent_endpoint = \"control:9092\"\nevent_token = \"token\"\n"), 0600))
	ready := make(chan net.Listener, 1)
	published := make(chan domain.ComponentEventEnvelope, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runRegistryWithDependencies(ctx, path, registryRoleDependencies{listen: func(network, address string) (net.Listener, error) {
			listener, err := net.Listen(network, address)
			if err == nil {
				ready <- listener
			}
			return listener, err
		}, dialEvents: func(context.Context, RegistryControlConfig) (out.ComponentEventPublisher, func(), error) {
			return registryRoleTestPublisher{published}, func() {}, nil
		}, newServer: productionRegistryRoleDependencies().newServer})
	}()
	listener := <-ready
	response, err := http.Get("http://" + listener.Addr().String() + "/healthz")
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.NoError(t, response.Body.Close())
	response, err = http.Get("http://" + listener.Addr().String() + "/v2/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	cancel()
	require.Eventually(t, func() bool {
		select {
		case err := <-done:
			require.NoError(t, err)
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
}
