package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeAdapterContractCreateContainer(t *testing.T) {
	createBody := createTestContainer(t, &domain.ContainerConfig{
		Image:           "nginx:latest",
		Name:            "gordon-app.example.com",
		Hostname:        "app.example.com",
		Env:             []string{"GORDON_ROUTE=app.example.com"},
		Ports:           []int{8080},
		Labels:          map[string]string{domain.LabelDomain: "app.example.com", domain.LabelManaged: "true"},
		Volumes:         map[string]string{"/data": "gordon-app-data"},
		ReadOnlyVolumes: map[string]string{"/config": "gordon-app-config"},
		NetworkMode:     "gordon-app-net",
		Aliases:         []string{"app.example.com"},
		RestartPolicy:   domain.RestartPolicyAlways,
		MemoryLimit:     256 << 20,
		NanoCPUs:        750_000_000,
		PidsLimit:       128,
		ReadOnlyRootFS:  true,
		CapDrop:         []string{"ALL"},
		CapAdd:          []string{"NET_BIND_SERVICE"},
	})

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"no-new-privileges:true"}, hostConfig["SecurityOpt"])
	assert.ElementsMatch(t, []any{"gordon-app-data:/data", "gordon-app-config:/config:ro"}, hostConfig["Binds"])
	assert.Equal(t, "gordon-app-net", hostConfig["NetworkMode"])
	assert.Equal(t, []any{"ALL"}, hostConfig["CapDrop"])
	assert.Equal(t, []any{"CAP_NET_BIND_SERVICE"}, hostConfig["CapAdd"])
	assert.Equal(t, true, hostConfig["ReadonlyRootfs"])

	portBindings, ok := hostConfig["PortBindings"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, portBindings, "8080/tcp")

	restartPolicy, ok := hostConfig["RestartPolicy"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, domain.RestartPolicyAlways, restartPolicy["Name"])

	assert.InDelta(t, float64(256<<20), hostConfig["Memory"], 0)
	assert.InDelta(t, float64(750_000_000), hostConfig["NanoCpus"], 0)
	assert.InDelta(t, float64(128), hostConfig["PidsLimit"], 0)

	labels, ok := createBody["Labels"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "app.example.com", labels[domain.LabelDomain])
	assert.Equal(t, "true", labels[domain.LabelManaged])

	networkingConfig, ok := createBody["NetworkingConfig"].(map[string]any)
	require.True(t, ok)
	endpoints, ok := networkingConfig["EndpointsConfig"].(map[string]any)
	require.True(t, ok)
	endpoint, ok := endpoints["gordon-app-net"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"app.example.com"}, endpoint["Aliases"])
}
