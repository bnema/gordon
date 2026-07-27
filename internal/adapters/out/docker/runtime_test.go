package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/oci/caps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntime_InspectImageEnv_RedactsValuesInDebugLog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/images/")
		assert.Contains(t, r.URL.Path, "/json")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Config": {
				"Env": ["API_KEY=super-secret-key", "DATABASE_URL=postgres://user:pass@example/db", "PORT=8080", "MALFORMED_SECRET_VALUE", "=empty-key-secret"]
			}
		}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	runtime := NewRuntimeWithClient(cli)

	var logs bytes.Buffer
	log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &logs})
	ctx := zerowrap.WithCtx(context.Background(), log)

	envVars, err := runtime.InspectImageEnv(ctx, "example/app:latest")
	require.NoError(t, err)
	assert.Equal(t, []string{"API_KEY=super-secret-key", "DATABASE_URL=postgres://user:pass@example/db", "PORT=8080", "MALFORMED_SECRET_VALUE", "=empty-key-secret"}, envVars)

	logOutput := logs.String()
	assert.Contains(t, logOutput, "env_keys")
	assert.Contains(t, logOutput, "API_KEY")
	assert.Contains(t, logOutput, "DATABASE_URL")
	assert.Contains(t, logOutput, "PORT")
	assert.Contains(t, logOutput, "[malformed]")
	assert.NotContains(t, logOutput, "super-secret-key")
	assert.NotContains(t, logOutput, "postgres://user:pass@example/db")
	assert.NotContains(t, logOutput, "MALFORMED_SECRET_VALUE")
	assert.NotContains(t, logOutput, "empty-key-secret")
	assert.NotContains(t, logOutput, "API_KEY=super-secret-key")
}

func TestParseVolumeOptionsSeparatesAccessFromEngineOptions(t *testing.T) {
	assert.Nil(t, parseVolumeOptions("ro"))
	assert.Nil(t, parseVolumeOptions("rw"))
	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, parseVolumeOptions("ro,U"))
	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, parseVolumeOptions("rw,U"))
}

func TestInspectVolumeOptionsRecoversPodmanNamedVolumeChownBind(t *testing.T) {
	mountPoint := container.MountPoint{
		Type:        mount.TypeVolume,
		Name:        "gordon-runtime-migration-g1",
		Source:      "/home/fixture/.local/share/containers/storage/volumes/gordon-runtime-migration-g1/_data",
		Destination: "/var/lib/gordon",
		Mode:        "",
		RW:          true,
	}

	options := inspectVolumeOptions(mountPoint, []string{"gordon-runtime-migration-g1:/var/lib/gordon:U"})

	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, options)
}

func TestInspectVolumeOptionsRejectsUntrustedChownBindEvidence(t *testing.T) {
	mountPoint := container.MountPoint{
		Type:        mount.TypeVolume,
		Name:        "gordon-runtime-migration-g1",
		Source:      "/home/fixture/.local/share/containers/storage/volumes/gordon-runtime-migration-g1/_data",
		Destination: "/var/lib/gordon",
		Mode:        "",
		RW:          true,
	}
	tests := map[string]struct {
		mount container.MountPoint
		binds []string
	}{
		"missing":                     {mount: mountPoint},
		"malformed":                   {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:U:extra"}},
		"duplicate":                   {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:U", "gordon-runtime-migration-g1:/var/lib/gordon:U"}},
		"conflicting":                 {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:U", "gordon-runtime-migration-g1:/var/lib/gordon:rw"}},
		"malformed duplicate":         {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:U", "gordon-runtime-migration-g1:/var/lib/gordon:rw:extra"}},
		"alias duplicate":             {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:U", "gordon-runtime-migration-g1:/var/lib/other/../gordon:rw"}},
		"bind source":                 {mount: mountPoint, binds: []string{"/srv/gordon:/var/lib/gordon:U"}},
		"source traversal":            {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1/../other:/var/lib/gordon:U"}},
		"unknown option":              {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:U,z"}},
		"destination mismatch":        {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/other:U"}},
		"destination dot alias":       {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/./gordon:U"}},
		"destination traversal alias": {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/other/../gordon:U"}},
		"destination slash alias":     {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon/:U"}},
		"source mismatch":             {mount: mountPoint, binds: []string{"other-volume:/var/lib/gordon:U"}},
		"read only":                   {mount: mountPoint, binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:ro,U"}},
		"mount destination alias": {
			mount: func() container.MountPoint {
				aliased := mountPoint
				aliased.Destination = "/var/lib/./gordon"
				return aliased
			}(),
			binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:U"},
		},
		"mount conflict": {
			mount: func() container.MountPoint {
				conflicting := mountPoint
				conflicting.Mode = "U,U"
				return conflicting
			}(),
			binds: []string{"gordon-runtime-migration-g1:/var/lib/gordon:U"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, inspectVolumeOptions(test.mount, test.binds))
		})
	}
}

func TestInspectVolumeOptionsRequiresBindEvidenceForMountModeChown(t *testing.T) {
	mountPoint := container.MountPoint{Type: mount.TypeVolume, Name: "app-data", Destination: "/data", Mode: "U", RW: true}

	assert.Nil(t, inspectVolumeOptions(mountPoint, nil))
	assert.Nil(t, inspectVolumeOptions(mountPoint, []string{"app-data:/data:rw"}))
}

func TestInspectVolumeOptionsReconcilesEachMountWithoutDroppingOtherOptions(t *testing.T) {
	binds := []string{
		"app-data:/data:U",
		"config-data:/config:ro",
		"unrelated:/other:U:malformed",
	}
	owned := container.MountPoint{Type: mount.TypeVolume, Name: "app-data", Destination: "/data", Mode: "delegated", RW: true}
	unrelated := container.MountPoint{Type: mount.TypeBind, Source: "/host/config", Destination: "/config", Mode: "ro,z", RW: false}

	assert.Equal(t, []string{"delegated", domain.ContainerVolumeOptionChown}, inspectVolumeOptions(owned, binds))
	assert.Equal(t, []string{"z"}, inspectVolumeOptions(unrelated, binds))
}

func TestInspectVolumeOptionsIgnoresMalformedChownEvidenceForAnotherDestination(t *testing.T) {
	mountPoint := container.MountPoint{Type: mount.TypeVolume, Name: "app-data", Destination: "/data", RW: true}
	binds := []string{"app-data:/data:U", "other:/other:U:malformed"}

	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, inspectVolumeOptions(mountPoint, binds))
}

func TestInspectVolumeOptionsLeavesDockerMountWithoutChownUnchanged(t *testing.T) {
	mountPoint := container.MountPoint{Type: mount.TypeVolume, Name: "app-data", Destination: "/data", Mode: "rw", RW: true}

	assert.Nil(t, inspectVolumeOptions(mountPoint, []string{"app-data:/data:rw"}))
	assert.Nil(t, inspectVolumeOptions(mountPoint, nil))
}

func TestBuildVolumeBindsReturnsVolumeOptionValidationErrors(t *testing.T) {
	config := &domain.ContainerConfig{
		Volumes:       map[string]string{"/var/lib/gordon": "gordon-runtime-fixture-g1"},
		VolumeOptions: map[string][]string{"/var/lib/gordon": {"z"}},
	}

	binds, err := buildVolumeBinds(config, zerowrap.FromCtx(t.Context()))

	require.Error(t, err)
	assert.Nil(t, binds)
}

func TestRuntimeVolumeOptionsRequireCanonicalSingletonChown(t *testing.T) {
	engines := []struct {
		name            string
		version         string
		info            string
		canonicalCreate bool
	}{
		{name: "rootless Podman", version: `{"Components":[{"Name":"Podman Engine"}]}`, info: `{"Rootless":true,"SecurityOptions":["name=rootless"]}`, canonicalCreate: true},
		{name: "Docker", version: `{"Components":[{"Name":"Docker Engine"}]}`, info: `{"Rootless":true,"SecurityOptions":["name=rootless"]}`},
		{name: "rootful Podman", version: `{"Components":[{"Name":"Podman Engine"}]}`, info: `{"Rootless":false,"SecurityOptions":[]}`},
	}
	invalid := []struct {
		name    string
		options []string
	}{
		{name: "comma containing", options: []string{"U,ro"}},
		{name: "composite comma duplicate", options: []string{"U,U"}},
		{name: "empty token", options: []string{""}},
		{name: "empty entry", options: []string{}},
		{name: "duplicate", options: []string{"U", "U"}},
		{name: "mixed", options: []string{"U", "ro"}},
		{name: "read only access flag", options: []string{"ro"}},
		{name: "writable access flag", options: []string{"rw"}},
		{name: "unknown", options: []string{"z"}},
		{name: "whitespace", options: []string{" U "}},
		{name: "wrong case", options: []string{"u"}},
	}

	for _, engine := range engines {
		t.Run(engine.name+"/canonical", func(t *testing.T) {
			created, probed, binds, err := createContainerWithVolumeOptions(t, engine.version, engine.info, []string{domain.ContainerVolumeOptionChown}, false)
			assert.True(t, probed)
			assert.Equal(t, engine.canonicalCreate, created)
			if engine.canonicalCreate {
				require.NoError(t, err)
				assert.Equal(t, []string{"gordon-runtime-fixture-g1:/var/lib/gordon:U"}, binds)
				return
			}
			require.Error(t, err)
		})
		for _, malformed := range invalid {
			t.Run(engine.name+"/"+malformed.name, func(t *testing.T) {
				created, probed, _, err := createContainerWithVolumeOptions(t, engine.version, engine.info, malformed.options, false)
				require.Error(t, err)
				assert.False(t, probed, "invalid tokens must be rejected before engine preflight")
				assert.False(t, created, "invalid tokens must never reach container serialization")
			})
		}
	}
}

func TestRuntimeRejectsChownOptionOnReadOnlyMount(t *testing.T) {
	created, probed, _, err := createContainerWithVolumeOptions(
		t,
		`{"Components":[{"Name":"Podman Engine"}]}`,
		`{"Rootless":true,"SecurityOptions":["name=rootless"]}`,
		[]string{domain.ContainerVolumeOptionChown},
		true,
	)
	require.Error(t, err)
	assert.False(t, probed, "U is valid only on a writable generation volume")
	assert.False(t, created, "read-only mounts must never serialize U")
}

func createContainerWithVolumeOptions(t *testing.T, version, info string, options []string, readOnly bool) (bool, bool, []string, error) {
	t.Helper()
	var created atomic.Bool
	var probed atomic.Bool
	var binds []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/version":
			probed.Store(true)
			_, _ = w.Write([]byte(version))
		case "/v1.41/info":
			probed.Store(true)
			_, _ = w.Write([]byte(info))
		case "/v1.41/containers/create":
			created.Store(true)
			var payload struct {
				HostConfig struct {
					Binds []string
				}
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			binds = payload.HostConfig.Binds
			_, _ = w.Write([]byte(`{"Id":"created"}`))
		case "/v1.41/containers/created/json":
			_, _ = w.Write([]byte(`{"Id":"created","Name":"/gordon-runtime-fixture-g1","Image":"sha256:fixture","Created":"2026-05-05T00:00:00Z","Config":{"Image":"gordon:fixture"},"State":{"Status":"created","ExitCode":0},"NetworkSettings":{"Ports":{}}}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	apiClient, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	volumes := map[string]string{"/var/lib/gordon": "gordon-runtime-fixture-g1"}
	var readOnlyVolumes map[string]string
	if readOnly {
		readOnlyVolumes = volumes
		volumes = nil
	}
	_, err = NewRuntimeWithClient(apiClient).CreateContainer(t.Context(), &domain.ContainerConfig{
		Image: "gordon:fixture", Name: "gordon-runtime-fixture-g1",
		Volumes:         volumes,
		ReadOnlyVolumes: readOnlyVolumes,
		VolumeOptions:   map[string][]string{"/var/lib/gordon": options},
	})
	return created.Load(), probed.Load(), binds, err
}

func TestRuntimeInspectNormalizesAuthoritativePodmanIdentity(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	allCapabilities := caps.GetAllCapabilities()
	compatCapabilities := slices.Clone(allCapabilities)
	for index := range compatCapabilities {
		compatCapabilities[index] = strings.ToLower(strings.TrimPrefix(compatCapabilities[index], "CAP_"))
	}
	runtime, requests := newNativeInspectTestRuntime(t, containerID, compatCapabilities, nil, http.StatusOK, validNativeIDMappings(21002))

	inspected, err := runtime.InspectContainer(t.Context(), containerID)

	require.NoError(t, err)
	assert.Equal(t, []string{"ALL"}, inspected.CapDrop)
	assert.Empty(t, inspected.CapAdd)
	assert.Equal(t, "keep-id:uid=21002,gid=21002", inspected.UsernsMode)
	assert.Equal(t, []string{
		"/v1.41/containers/" + containerID + "/json",
		"/v4.0.0/libpod/containers/" + containerID + "/json",
	}, *requests)
}

func TestRuntimeInspectCapabilityNormalizationFailsClosed(t *testing.T) {
	allCapabilities := []string{"CAP_CHOWN", "CAP_SETUID", "CAP_SETGID", "CAP_NET_BIND_SERVICE"}
	canonicalEquivalent := []string{"chown", "cap_setuid", "SetGid", "net_bind_service"}
	assert.Equal(t, []string{"ALL"}, normalizeInspectedCapDropAgainst(canonicalEquivalent, nil, allCapabilities))

	tests := map[string]struct {
		dropped []string
		added   []string
	}{
		"literal all": {dropped: []string{"ALL"}},
		"subset":      {dropped: slices.Clone(allCapabilities[:len(allCapabilities)-1])},
		"duplicate":   {dropped: append(slices.Clone(allCapabilities), allCapabilities[0])},
		"unknown":     {dropped: append(slices.Clone(allCapabilities[:len(allCapabilities)-1]), "CAP_NOT_REAL")},
		"superset":    {dropped: append(slices.Clone(allCapabilities), "CAP_NOT_REAL")},
		"cap add":     {dropped: slices.Clone(allCapabilities), added: []string{"CAP_CHOWN"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assert.NotEqual(t, []string{"ALL"}, normalizeInspectedCapDropAgainst(test.dropped, test.added, allCapabilities))
		})
	}
}

func TestRuntimeInspectNativeIDMappingsFailClosed(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	valid := validNativeIDMappings(21002)
	tests := map[string]struct {
		status int
		body   any
	}{
		"native unavailable": {status: http.StatusNotFound, body: map[string]any{"message": "not found"}},
		"Docker response":    {status: http.StatusOK, body: map[string]any{"Id": containerID, "HostConfig": map[string]any{}}},
		"wrong role":         {status: http.StatusOK, body: validNativeIDMappings(21001)},
		"missing gid map": {status: http.StatusOK, body: map[string]any{
			"HostConfig": map[string]any{"IDMappings": map[string]any{"UidMap": nativeMapEntries(21002)}},
		}},
		"overlapping host ranges":   {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 2, "HostID", uint64(100001))},
		"duplicate container range": {status: http.StatusOK, body: mutateNativeMapping(valid, "GidMap", 2, "ContainerID", uint64(21002))},
		"non singleton role":        {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "Size", uint64(2))},
		"incoherent maps":           {status: http.StatusOK, body: mutateNativeMapping(valid, "GidMap", 2, "Size", uint64(44532))},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runtime, _ := newNativeInspectTestRuntime(t, containerID, caps.GetAllCapabilities(), nil, test.status, test.body)
			inspected, err := runtime.InspectContainer(t.Context(), containerID)
			require.NoError(t, err)
			assert.Equal(t, "private", inspected.UsernsMode)
		})
	}
}

func TestRuntimeInspectNativeProofDoesNotLeakResponseValues(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := validNativeIDMappings(21002)
	body["private_value"] = "native-secret-marker"
	runtime, _ := newNativeInspectTestRuntime(t, containerID, caps.GetAllCapabilities(), nil, http.StatusOK, body)
	var logs bytes.Buffer
	logger := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &logs})

	_, err := runtime.InspectContainer(zerowrap.WithCtx(t.Context(), logger), containerID)

	require.NoError(t, err)
	assert.NotContains(t, logs.String(), "native-secret-marker")
	assert.NotContains(t, logs.String(), "/run/user/fixture/podman.sock")
}

func TestRuntimeInspectDoesNotExtendNativeAuthority(t *testing.T) {
	const invalidID = "../../private/socket"
	runtime, requests := newNativeInspectTestRuntime(t, invalidID, caps.GetAllCapabilities(), nil, http.StatusOK, validNativeIDMappings(21002))

	inspected, err := runtime.InspectContainer(t.Context(), "safe-name")

	require.NoError(t, err)
	assert.Equal(t, "private", inspected.UsernsMode)
	require.Len(t, *requests, 1)
	assert.NotContains(t, (*requests)[0], "libpod")
}

func newNativeInspectTestRuntime(t *testing.T, responseID string, capDrop, capAdd []string, nativeStatus int, nativeBody any) (*Runtime, *[]string) {
	t.Helper()
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(request.URL.Path, "/v1.41/containers/"):
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"Id": responseID, "Name": "/gordon-control-fixture-g1", "Image": "sha256:fixture", "Created": "2026-05-05T00:00:00Z",
				"Config":     map[string]any{"Image": "gordon:fixture", "User": "21002:21002", "Labels": map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "control"}},
				"HostConfig": map[string]any{"UsernsMode": "private", "CapDrop": capDrop, "CapAdd": capAdd, "SecurityOpt": []string{"no-new-privileges:true"}},
				"State":      map[string]any{"Status": "running", "ExitCode": 0}, "NetworkSettings": map[string]any{"Ports": map[string]any{}},
			}))
		case strings.HasPrefix(request.URL.Path, "/v4.0.0/libpod/containers/"):
			w.WriteHeader(nativeStatus)
			require.NoError(t, json.NewEncoder(w).Encode(nativeBody))
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	apiClient, err := client.NewClientWithOpts(
		client.WithHost("unix:///run/user/fixture/podman.sock"),
		client.WithVersion("1.41"),
		client.WithHTTPClient(&http.Client{Transport: transport}),
	)
	require.NoError(t, err)
	return NewRuntimeWithClient(apiClient), &requests
}

func validNativeIDMappings(roleID uint64) map[string]any {
	return map[string]any{"HostConfig": map[string]any{"IDMappings": map[string]any{
		"UidMap": nativeMapEntries(roleID), "GidMap": nativeMapEntries(roleID),
	}}}
}

func nativeMapEntries(roleID uint64) []map[string]any {
	return []map[string]any{
		{"ContainerID": uint64(0), "HostID": uint64(100000), "Size": roleID},
		{"ContainerID": roleID, "HostID": uint64(1000), "Size": uint64(1)},
		{"ContainerID": roleID + 1, "HostID": 100000 + roleID, "Size": uint64(65535) - roleID},
	}
}

func mutateNativeMapping(source map[string]any, mapName string, index int, field string, value uint64) map[string]any {
	data, _ := json.Marshal(source)
	var cloned map[string]any
	_ = json.Unmarshal(data, &cloned)
	hostConfig := cloned["HostConfig"].(map[string]any)
	mappings := hostConfig["IDMappings"].(map[string]any)
	entries := mappings[mapName].([]any)
	entries[index].(map[string]any)[field] = value
	return cloned
}

func TestWaitForVolumeArchiveContainerIgnoresNilErrorBeforeStatus(t *testing.T) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	errCh <- nil
	statusCh <- container.WaitResponse{StatusCode: 7}

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.NoError(t, err)
	assert.Equal(t, int64(7), statusCode)
}

func TestWaitForVolumeArchiveContainerHandlesClosedErrorChannelBeforeStatus(t *testing.T) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error)
	close(errCh)
	statusCh <- container.WaitResponse{StatusCode: 3}

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.NoError(t, err)
	assert.Equal(t, int64(3), statusCode)
}

func TestWaitForVolumeArchiveContainerReturnsErrorChannelError(t *testing.T) {
	statusCh := make(chan container.WaitResponse)
	errCh := make(chan error, 1)
	wantErr := errors.New("wait failed")
	errCh <- wantErr

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, int64(0), statusCode)
}

func TestWaitForVolumeArchiveContainerErrorsWhenStatusChannelCloses(t *testing.T) {
	statusCh := make(chan container.WaitResponse)
	errCh := make(chan error)
	close(statusCh)
	close(errCh)

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait channel closed")
	assert.Equal(t, int64(0), statusCode)
}
