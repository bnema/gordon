package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
)

func TestInitConfigRejectsLegacyPortsWithoutEntrypoints(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[server]\nport = 8088\ntls_port = 8443\n"), 0o600))

	_, _, err := initConfig(configPath)

	require.ErrorContains(t, err, "legacy server.port or server.tls_port configuration requires at least one [entrypoints] entry")
}

func TestInitConfigAcceptsLegacyPortsWithEntrypoint(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[server]\nport = 8088\n\n[entrypoints.edge]\naddress = \":443\"\nprotocol = \"smart_tcp\"\n"), 0o600))

	_, cfg, err := initConfig(configPath)

	require.NoError(t, err)
	require.Contains(t, cfg.EntryPoints, "edge")
}

func TestInitConfigLoadsExpandedHTTPPortWithTLS(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	config := `[services.app]
image = "app:latest"

[services.app.ports.secure_web]
port = 8443
protocol = "http"
tls = true
`
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))

	_, cfg, err := initConfig(configPath)
	require.NoError(t, err)
	services, err := servicecfg.ToDomain(cfg.Services)
	require.NoError(t, err)
	require.Len(t, services, 1)
	require.Equal(t, []domain.StandaloneServicePort{{
		Name: "secure_web", Container: 8443, Protocol: domain.ServicePortProtocolHTTP, TLS: true,
	}}, services[0].Ports)
}

func TestInitConfigLoadsMultiContainerServiceWithoutDeclaringInternalPorts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	config := `[services.shop.containers]
web = "shop-web:latest"
postgres = "postgres:18"
valkey = "valkey/valkey:latest"

[services.shop.ports]
web = "web:3000/http"
`
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))

	_, cfg, err := initConfig(configPath)
	require.NoError(t, err)
	services, err := servicecfg.ToDomain(cfg.Services)
	require.NoError(t, err)
	require.Len(t, services, 1)
	require.Equal(t, "shop", services[0].Name)
	require.Len(t, services[0].Containers, 3)
	require.Equal(t, "postgres", services[0].Containers[0].Name)
	require.Equal(t, "postgres:18", services[0].Containers[0].Image)
	require.Equal(t, "valkey", services[0].Containers[1].Name)
	require.Equal(t, "valkey/valkey:latest", services[0].Containers[1].Image)
	require.Equal(t, "web", services[0].Containers[2].Name)
	require.Equal(t, "shop-web:latest", services[0].Containers[2].Image)
	require.Equal(t, []domain.StandaloneServicePort{{
		Name: "web", ContainerName: "web", Container: 3000, Protocol: domain.ServicePortProtocolHTTP,
	}}, services[0].Ports)
}

func TestInitConfigRejectsRemovedServiceArraySyntax(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[[services]]\nname = \"app\"\nimage = \"app:latest\"\n"), 0o600))

	_, _, err := initConfig(configPath)

	require.ErrorContains(t, err, "[[services]] array syntax is no longer supported")
	require.ErrorContains(t, err, "[services.<name>]")
	require.ErrorContains(t, err, "docs/config/services.md")
}

func TestInitConfigLoadsKeyedServicesWithCompactPorts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	config := `[services.app]
image = "app:latest"
ports = { web = "8080/http", game = "9000/udp", rcon = { port = 9001, protocol = "tcp", private = true } }
`
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))

	_, cfg, err := initConfig(configPath)

	require.NoError(t, err)
	services, err := servicecfg.ToDomain(cfg.Services)
	require.NoError(t, err)
	require.Len(t, services, 1)
	require.Len(t, services[0].Ports, 3)
	require.Equal(t, "game", services[0].Ports[0].Name)
	require.Equal(t, 9000, services[0].Ports[0].Container)
	require.Equal(t, domain.ServicePortProtocolUDP, services[0].Ports[0].Protocol)
	require.Equal(t, "rcon", services[0].Ports[1].Name)
	require.True(t, services[0].Ports[1].Private)
	require.Equal(t, "web", services[0].Ports[2].Name)
	require.Equal(t, domain.ServicePortProtocolHTTP, services[0].Ports[2].Protocol)
}
