package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusJSON(t *testing.T) {
	resetControlPlaneResolutionTestState(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/status", r.URL.Path)
		_, _ = w.Write([]byte(`{"routes":3,"registry_domain":"registry.example.com","registry_port":5000,"server_port":8080,"auto_route":true,"network_isolation":false,"container_status":{"app.example.com":"running"}}`))
	}))
	defer server.Close()

	originalConfigPath := configPath
	configPath = writeCLIConfig(t, `[control]
endpoint = "`+server.URL+`"
`)
	t.Cleanup(func() { configPath = originalConfigPath })

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})

	require.NoError(t, cmd.Execute())
	assert.JSONEq(t, `{
		"routes": 3,
		"registry_domain": "registry.example.com",
		"registry_port": 5000,
		"server_port": 8080,
		"auto_route": true,
		"network_isolation": false,
		"container_status": {"app.example.com":"running"}
	}`, out.String())
}
