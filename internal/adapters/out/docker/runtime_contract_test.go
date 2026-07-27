package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestContainerDomainCarriesExplicitVolumeOptions(t *testing.T) {
	configField, ok := reflect.TypeFor[domain.ContainerConfig]().FieldByName("VolumeOptions")
	require.True(t, ok, "container create config must transport mount options separately from volume names")
	assert.Equal(t, reflect.TypeFor[map[string][]string](), configField.Type)

	mountField, ok := reflect.TypeFor[domain.ContainerVolumeMount]().FieldByName("Options")
	require.True(t, ok, "authoritative inspect mounts must expose parsed options")
	assert.Equal(t, reflect.TypeFor[[]string](), mountField.Type)
}

func TestContainerDomainHasNoSharedSupplementaryGroupTransport(t *testing.T) {
	_, configHasGroupAdd := reflect.TypeFor[domain.ContainerConfig]().FieldByName("GroupAdd")
	assert.False(t, configHasGroupAdd)
	_, inspectedHasGroupAdd := reflect.TypeFor[domain.Container]().FieldByName("GroupAdd")
	assert.False(t, inspectedHasGroupAdd)
}

func TestFixedComponentProcessIdentities(t *testing.T) {
	tests := []struct {
		role domain.ComponentRole
		uid  int
		gid  int
		user string
	}{
		{role: domain.ComponentRoleRuntime, uid: 21001, gid: 21001, user: "21001:21001"},
		{role: domain.ComponentRoleControl, uid: 21002, gid: 21002, user: "21002:21002"},
		{role: domain.ComponentRoleEdge, uid: 21003, gid: 21003, user: "21003:21003"},
		{role: domain.ComponentRoleRegistry, uid: 21004, gid: 21004, user: "21004:21004"},
	}
	for _, test := range tests {
		identity, ok := domain.FixedComponentProcessIdentity(test.role)
		require.True(t, ok)
		assert.Equal(t, test.uid, identity.UID)
		assert.Equal(t, test.gid, identity.GID)
		assert.Equal(t, test.user, identity.User)
	}
	_, ok := domain.FixedComponentProcessIdentity(domain.ComponentRole("monolith"))
	assert.False(t, ok)
}

func TestRuntimeAdapterSerializesExplicitVolumeOptionsWithoutChangingSourceName(t *testing.T) {
	config := &domain.ContainerConfig{
		Volumes:       map[string]string{"/var/lib/gordon": "gordon-runtime-fixture-g1"},
		VolumeOptions: map[string][]string{"/var/lib/gordon": {domain.ContainerVolumeOptionChown}},
	}

	assert.Equal(t, []string{"gordon-runtime-fixture-g1:/var/lib/gordon:U"}, buildVolumeBinds(config, zerowrap.FromCtx(t.Context())))
	assert.Equal(t, "gordon-runtime-fixture-g1", config.Volumes["/var/lib/gordon"], "mount options must never be encoded into the volume name")
}

func TestRuntimeRejectsPodmanVolumeOwnershipOptionBeforeDockerCreate(t *testing.T) {
	for _, test := range []struct {
		name    string
		version string
		info    string
	}{
		{name: "Docker", version: `{"Components":[{"Name":"Docker Engine"}]}`, info: `{"Rootless":true,"SecurityOptions":["name=rootless"]}`},
		{name: "rootful Podman", version: `{"Components":[{"Name":"Podman Engine"}]}`, info: `{"Rootless":false,"SecurityOptions":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			created := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/version":
					_, _ = w.Write([]byte(test.version))
				case "/v1.41/info":
					_, _ = w.Write([]byte(test.info))
				case "/v1.41/containers/create":
					created = true
					_, _ = w.Write([]byte(`{"Id":"unexpected"}`))
				case "/v1.41/containers/unexpected/json":
					_, _ = w.Write([]byte(`{"Id":"unexpected","Config":{},"State":{},"NetworkSettings":{}}`))
				default:
					http.NotFound(w, request)
				}
			}))
			defer server.Close()

			host := strings.TrimPrefix(server.URL, "http://")
			cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
			require.NoError(t, err)
			_, err = NewRuntimeWithClient(cli).CreateContainer(t.Context(), &domain.ContainerConfig{
				Image: "gordon:fixture", Name: "gordon-runtime-fixture-g1",
				Volumes:       map[string]string{"/var/lib/gordon": "gordon-runtime-fixture-g1"},
				VolumeOptions: map[string][]string{"/var/lib/gordon": {domain.ContainerVolumeOptionChown}},
			})
			require.Error(t, err)
			assert.False(t, created, "unsupported engines must never receive Podman's U option")
		})
	}
}

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

func TestRuntimeAdapterContractInspectIdentitySecurityAndMounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/v1.41/containers/component-fixture/json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Id":"component-fixture",
			"Name":"/gordon-runtime-generation-7",
			"Image":"sha256:fixture-image",
			"Created":"2026-05-05T00:00:00Z",
			"Config":{"Image":"gordon@sha256:fixture","User":"21001:21001","Labels":{"gordon.component.role":"runtime"}},
			"HostConfig":{
				"UsernsMode":"keep-id",
				"CapDrop":["ALL"],
				"CapAdd":["NET_BIND_SERVICE"],
				"SecurityOpt":["no-new-privileges:true"]
			},
			"State":{"Status":"running","ExitCode":0},
			"Mounts":[{
				"Type":"volume","Name":"gordon-runtime-data-generation-7",
				"Source":"/rootless-storage/volumes/runtime/_data",
				"Destination":"/var/lib/gordon","Driver":"local","Mode":"U",
				"RW":true,"Propagation":"rprivate"
			}],
			"NetworkSettings":{"Ports":{}}
		}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)

	inspected, err := NewRuntimeWithClient(cli).InspectContainer(context.Background(), "component-fixture")
	require.NoError(t, err)
	assert.Equal(t, "21001:21001", inspected.User)
	assert.Equal(t, "keep-id", inspected.UsernsMode)
	assert.Equal(t, []string{"ALL"}, inspected.CapDrop)
	assert.Equal(t, []string{"NET_BIND_SERVICE"}, inspected.CapAdd)
	assert.True(t, inspected.NoNewPrivileges)
	require.Len(t, inspected.VolumeMounts, 1)
	assert.Equal(t, domain.ContainerVolumeMount{
		Name:        "gordon-runtime-data-generation-7",
		Type:        "volume",
		Source:      "/rootless-storage/volumes/runtime/_data",
		Destination: "/var/lib/gordon",
		Driver:      "local",
		Mode:        "U",
		Propagation: "rprivate",
		Options:     []string{domain.ContainerVolumeOptionChown},
		ReadOnly:    false,
	}, inspected.VolumeMounts[0])
}
