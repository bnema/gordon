package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestInspectNamedVolumeChownEvidenceAcceptsCanonicalPodmanOptionsInAnyOrder(t *testing.T) {
	for _, mode := range []string{
		"U,rprivate,nosuid,nodev,rbind",
		"U,nosuid,rprivate,nodev,rbind",
	} {
		assert.True(t, inspectNamedVolumeChownEvidence("gordon-runtime-migration-g1", []string{
			"gordon-runtime-migration-g1:/var/lib/gordon:" + mode,
		}))
	}
}

func TestInspectNamedVolumeChownEvidenceRejectsModeVariants(t *testing.T) {
	expected := "gordon-runtime-migration-g1"
	variants := map[string][]string{
		"missing":                 nil,
		"bare create mode":        {expected + ":/var/lib/gordon:U"},
		"subset":                  {expected + ":/var/lib/gordon:U,rprivate,nosuid,nodev"},
		"superset":                {expected + ":/var/lib/gordon:U,rprivate,nosuid,nodev,rbind,z"},
		"duplicate token":         {expected + ":/var/lib/gordon:U,rprivate,nosuid,nodev,rbind,rbind"},
		"lowercase alias":         {expected + ":/var/lib/gordon:u,rprivate,nosuid,nodev,rbind"},
		"extra access flag":       {expected + ":/var/lib/gordon:U,rprivate,nosuid,nodev,rbind,rw"},
		"bind source":             {"/srv/gordon:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind"},
		"other volume":            {"other-volume:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind"},
		"other destination":       {expected + ":/data:U,rprivate,nosuid,nodev,rbind"},
		"destination alias":       {expected + ":/var/lib/./gordon:U,rprivate,nosuid,nodev,rbind"},
		"duplicate bind":          {expected + ":/var/lib/gordon:U,rprivate,nosuid,nodev,rbind", expected + ":/var/lib/gordon:U,rprivate,nosuid,nodev,rbind"},
		"conflicting destination": {expected + ":/var/lib/gordon:U,rprivate,nosuid,nodev,rbind", expected + ":/var/lib/gordon:rw"},
	}
	for name, binds := range variants {
		t.Run(name, func(t *testing.T) {
			assert.False(t, inspectNamedVolumeChownEvidence(expected, binds))
		})
	}
}

func TestExactInspectedGenerationMountRequiresCanonicalGenerationVolume(t *testing.T) {
	tests := map[string]func(*domain.Container){
		"non-generation name": func(container *domain.Container) {
			container.Name = "gordon-runtime-other-g1"
		},
		"wrong volume": func(container *domain.Container) {
			container.VolumeMounts[0].Name = "other-volume"
		},
		"bind mount": func(container *domain.Container) {
			container.VolumeMounts[0].Type = string(mount.TypeBind)
		},
		"read only": func(container *domain.Container) {
			container.VolumeMounts[0].ReadOnly = true
		},
		"wrong destination": func(container *domain.Container) {
			container.VolumeMounts[0].Destination = "/data"
		},
		"existing mount option": func(container *domain.Container) {
			container.VolumeMounts[0].Options = []string{"delegated"}
		},
		"edge role": func(container *domain.Container) {
			container.Labels[domain.LabelComponentRole] = string(domain.ComponentRoleEdge)
			container.Name = "gordon-edge-migration-g1"
			container.VolumeMounts[0].Name = container.Name
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			inspected := nativeIdentityGenerationContainer()
			mutate(inspected)
			expectedName, ok := inspectedGenerationVolumeName(inspected)
			if !ok {
				assert.Empty(t, expectedName, "an unresolved generation volume must not yield a name")
				return
			}
			_, ok = exactInspectedGenerationMount(inspected.VolumeMounts, expectedName)
			assert.False(t, ok)
		})
	}
}

func nativeIdentityGenerationContainer() *domain.Container {
	return &domain.Container{
		ID:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Name: "gordon-runtime-migration-g1",
		Labels: map[string]string{
			domain.LabelComponent:            "true",
			domain.LabelComponentRole:        string(domain.ComponentRoleRuntime),
			domain.LabelComponentMigrationID: "migration",
			domain.LabelComponentGeneration:  "1",
		},
		VolumeMounts: []domain.ContainerVolumeMount{{
			Type: string(mount.TypeVolume), Name: "gordon-runtime-migration-g1", Destination: "/var/lib/gordon",
		}},
	}
}

func TestRuntimeInspectTrustsNativeBoundingCapsNullDespiteCompatibleSubset(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	compatCapabilities := []string{"chown", "cap_setuid"}
	runtime, requests := newNativeInspectTestRuntime(t, containerID, compatCapabilities, nil, http.StatusOK,
		rawNativeSecurityInspect(21002, `"BoundingCaps":null,"EffectiveCaps":null`))

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

func TestRuntimeInspectBoundingCapsRejectsInvalidCompatibleCapabilities(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := map[string]struct {
		dropped []string
		added   []string
	}{
		"drop absent":        {},
		"capability added":   {dropped: []string{"chown"}, added: []string{"setuid"}},
		"duplicate aliases":  {dropped: []string{"chown", "CAP_CHOWN"}},
		"literal ALL":        {dropped: []string{"ALL"}},
		"unknown capability": {dropped: []string{"CAP_NOT_REAL"}},
		"malformed name":     {dropped: []string{"CAP_NET-BIND-SERVICE"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runtime, _ := newNativeInspectTestRuntime(t, containerID, test.dropped, test.added, http.StatusOK, validNativeSecurityInspect(21002))

			inspected, err := runtime.InspectContainer(t.Context(), containerID)

			require.NoError(t, err)
			assert.Nil(t, inspected.CapDrop)
			assert.Equal(t, "keep-id:uid=21002,gid=21002", inspected.UsernsMode)
		})
	}
}

func TestRuntimeInspectBoundingCapsFailsClosedUnlessExplicitlyNull(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := map[string]any{
		"field absent":               validNativeIDMappings(21002),
		"non-null observed set":      rawNativeSecurityInspect(21002, `"BoundingCaps":["CAP_CHOWN","CAP_SETUID"],"EffectiveCaps":null`),
		"empty array undocumented":   nativeSecurityInspectWithCaps(21002, []string{}),
		"malformed string":           nativeSecurityInspectWithCaps(21002, "null"),
		"duplicate field":            rawNativeSecurityInspect(21002, `"BoundingCaps":["CAP_CHOWN"],"BoundingCaps":null,"EffectiveCaps":null`),
		"EffectiveCaps is not proof": nativeSecurityInspectWithEffectiveCapsOnly(21002),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			runtime, requests := newNativeInspectTestRuntime(t, containerID, []string{"chown"}, nil, http.StatusOK, body)

			inspected, err := runtime.InspectContainer(t.Context(), containerID)

			require.NoError(t, err)
			assert.Nil(t, inspected.CapDrop)
			assert.Equal(t, "keep-id:uid=21002,gid=21002", inspected.UsernsMode)
			assert.Len(t, *requests, 2)
		})
	}
}

func TestRuntimeInspectMalformedNativeJSONFailsBothProofs(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	runtime, requests := newNativeInspectTestRuntime(t, containerID, []string{"chown"}, nil, http.StatusOK,
		nativeRawInspect(`{"BoundingCaps":null`))

	inspected, err := runtime.InspectContainer(t.Context(), containerID)

	require.NoError(t, err)
	assert.Nil(t, inspected.CapDrop)
	assert.Equal(t, "private", inspected.UsernsMode)
	assert.Len(t, *requests, 2)
}

func TestRuntimeInspectNativeIDMappingsFailClosed(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	valid := validNativeIDMappings(21002)
	tooManyEntries := make([]string, maxNativePodmanIDMapEntries+1)
	for index := range tooManyEntries {
		tooManyEntries[index] = "0:1:1"
	}
	tests := map[string]struct {
		status int
		body   any
	}{
		"native unavailable":          {status: http.StatusNotFound, body: map[string]any{"message": "not found"}},
		"Docker response":             {status: http.StatusOK, body: map[string]any{"Id": containerID, "HostConfig": map[string]any{}}},
		"forged null with wrong role": {status: http.StatusOK, body: validNativeSecurityInspect(21001)},
		"missing gid map":             {status: http.StatusOK, body: nativeMappingsBody(nativeMapEntries(21002), nil)},
		"numeric object alias":        {status: http.StatusOK, body: map[string]any{"HostConfig": map[string]any{"IDMappings": map[string]any{"UidMap": []map[string]any{{"ContainerID": 0, "HostID": 1, "Size": 21002}}, "GidMap": nativeMapEntries(21002)}}}},
		"wrong singleton host":        {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:1000:1")},
		"overlapping host ranges":     {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 2, "21003:21002:44534")},
		"duplicate container range":   {status: http.StatusOK, body: mutateNativeMapping(valid, "GidMap", 2, "21002:21003:44534")},
		"container gap":               {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 2, "21004:21003:44534")},
		"host gap":                    {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 2, "21003:21004:44534")},
		"incoherent uid and gid":      {status: http.StatusOK, body: mutateNativeMapping(valid, "GidMap", 2, "21003:21003:44533")},
		"non singleton role":          {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:0:2")},
		"zero size":                   {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:0:0")},
		"leading zero alias":          {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "021002:0:1")},
		"positive sign":               {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "+21002:0:1")},
		"negative sign":               {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "-21002:0:1")},
		"ASCII whitespace":            {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, " 21002:0:1")},
		"Unicode decimal alias":       {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "２１００２:0:1")},
		"missing field":               {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:0")},
		"extra field":                 {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:0:1:0")},
		"overflow":                    {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "18446744073709551616:0:1")},
		"Linux ID overflow":           {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "4294967296:0:1")},
		"too many entries":            {status: http.StatusOK, body: nativeMappingsBody(tooManyEntries, nativeMapEntries(21002))},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runtime, _ := newNativeInspectTestRuntime(t, containerID, []string{"chown"}, nil, test.status, test.body)
			inspected, err := runtime.InspectContainer(t.Context(), containerID)
			require.NoError(t, err)
			assert.Equal(t, "private", inspected.UsernsMode)
			assert.Nil(t, inspected.CapDrop)
		})
	}
}

func TestRuntimeInspectNativeProofDoesNotLeakResponseValues(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := validNativeIDMappings(21002)
	body["private_value"] = "native-secret-marker"
	runtime, _ := newNativeInspectTestRuntime(t, containerID, []string{"chown"}, nil, http.StatusOK, body)
	var logs bytes.Buffer
	logger := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &logs})

	_, err := runtime.InspectContainer(zerowrap.WithCtx(t.Context(), logger), containerID)

	require.NoError(t, err)
	assert.NotContains(t, logs.String(), "native-secret-marker")
	assert.NotContains(t, logs.String(), "/run/user/fixture/podman.sock")
}

func TestRuntimeInspectDoesNotExtendNativeAuthority(t *testing.T) {
	const invalidID = "../../private/socket"
	runtime, requests := newNativeInspectTestRuntime(t, invalidID, []string{"chown"}, nil, http.StatusOK, validNativeIDMappings(21002))

	inspected, err := runtime.InspectContainer(t.Context(), "safe-name")

	require.NoError(t, err)
	assert.Equal(t, "private", inspected.UsernsMode)
	require.Len(t, *requests, 1)
	assert.NotContains(t, (*requests)[0], "libpod")
}

func TestNativePodmanHTTPClientDoesNotMutateSharedClient(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var nativeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(request.URL.Path, "/v1.41/containers/"):
			_, _ = w.Write([]byte(`{
				"Id":"` + containerID + `",
				"Name":"/gordon-control-fixture-g1",
				"Image":"sha256:fixture",
				"Created":"2026-05-05T00:00:00Z",
				"Config":{"Image":"gordon:fixture","User":"21002:21002","Labels":{"gordon.component":"true","gordon.component.role":"control"}},
				"HostConfig":{"UsernsMode":"private","CapDrop":["chown"],"SecurityOpt":["no-new-privileges:true"]},
				"State":{"Status":"running","ExitCode":0},
				"NetworkSettings":{"Ports":{}}
			}`))
		case strings.HasPrefix(request.URL.Path, "/v4.0.0/libpod/containers/"):
			nativeCalls.Add(1)
			if err := json.NewEncoder(w).Encode(validNativeSecurityInspect(21002)); err != nil {
				t.Errorf("encode native inspect: %v", err)
			}
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	shared := &http.Client{Transport: transport}
	apiClient, err := client.NewClientWithOpts(
		client.WithHost("unix:///run/user/fixture/podman.sock"),
		client.WithVersion("1.41"),
		client.WithHTTPClient(shared),
	)
	require.NoError(t, err)
	baselineTransport := shared.Transport
	baselineRedirect := shared.CheckRedirect
	runtime := NewRuntimeWithClient(apiClient)

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			_, inspectErr := runtime.InspectContainer(t.Context(), containerID)
			assert.NoError(t, inspectErr)
		}()
	}
	wg.Wait()

	assert.Same(t, baselineTransport, shared.Transport)
	assertSharedCheckRedirectUnchanged(t, baselineRedirect, shared.CheckRedirect)
	assert.GreaterOrEqual(t, nativeCalls.Load(), int32(workers))
}

func assertSharedCheckRedirectUnchanged(
	t *testing.T,
	baseline, current func(*http.Request, []*http.Request) error,
) {
	t.Helper()
	if baseline == nil {
		assert.Nil(t, current)
		return
	}
	require.NotNil(t, current)
	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	require.NoError(t, err)
	assert.Equal(t, baseline(req, nil), current(req, nil))
}

func newNativeInspectTestRuntime(t *testing.T, responseID string, capDrop, capAdd []string, nativeStatus int, nativeBody any) (*Runtime, *[]string) {
	t.Helper()
	var requests []string
	var requestsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestsMu.Lock()
		requests = append(requests, request.URL.Path)
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(request.URL.Path, "/v1.41/containers/"):
			if err := json.NewEncoder(w).Encode(map[string]any{
				"Id": responseID, "Name": "/gordon-control-fixture-g1", "Image": "sha256:fixture", "Created": "2026-05-05T00:00:00Z",
				"Config":     map[string]any{"Image": "gordon:fixture", "User": "21002:21002", "Labels": map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: "control"}},
				"HostConfig": map[string]any{"UsernsMode": "private", "CapDrop": capDrop, "CapAdd": capAdd, "SecurityOpt": []string{"no-new-privileges:true"}},
				"State":      map[string]any{"Status": "running", "ExitCode": 0}, "NetworkSettings": map[string]any{"Ports": map[string]any{}},
			}); err != nil {
				t.Errorf("encode compatible inspect: %v", err)
			}
		case strings.HasPrefix(request.URL.Path, "/v4.0.0/libpod/containers/"):
			w.WriteHeader(nativeStatus)
			if raw, ok := nativeBody.(nativeRawInspect); ok {
				if _, err := w.Write([]byte(raw)); err != nil {
					t.Errorf("write native inspect: %v", err)
				}
				return
			}
			if err := json.NewEncoder(w).Encode(nativeBody); err != nil {
				t.Errorf("encode native inspect: %v", err)
			}
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

type nativeRawInspect string

func rawNativeSecurityInspect(roleID uint64, fields string) nativeRawInspect {
	return nativeRawInspect(fmt.Sprintf(
		`{%s,"HostConfig":{"IDMappings":{"UidMap":["0:1:%d","%d:0:1","%d:%d:%d"],"GidMap":["0:1:%d","%d:0:1","%d:%d:%d"]}}}`,
		fields,
		roleID, roleID, roleID+1, roleID+1, uint64(65536)-roleID,
		roleID, roleID, roleID+1, roleID+1, uint64(65536)-roleID,
	))
}

func validNativeSecurityInspect(roleID uint64) map[string]any {
	body := validNativeIDMappings(roleID)
	body["BoundingCaps"] = nil
	body["EffectiveCaps"] = nil
	return body
}

func nativeSecurityInspectWithCaps(roleID uint64, boundingCaps any) map[string]any {
	body := validNativeIDMappings(roleID)
	body["BoundingCaps"] = boundingCaps
	body["EffectiveCaps"] = nil
	return body
}

func nativeSecurityInspectWithEffectiveCapsOnly(roleID uint64) map[string]any {
	body := validNativeIDMappings(roleID)
	body["EffectiveCaps"] = []string{"CAP_CHOWN"}
	return body
}

func validNativeIDMappings(roleID uint64) map[string]any {
	entries := nativeMapEntries(roleID)
	return nativeMappingsBody(entries, slices.Clone(entries))
}

func nativeMappingsBody(uidMap, gidMap []string) map[string]any {
	return map[string]any{"HostConfig": map[string]any{"IDMappings": map[string]any{
		"UidMap": uidMap, "GidMap": gidMap,
	}}}
}

func nativeMapEntries(roleID uint64) []string {
	return []string{
		fmt.Sprintf("0:1:%d", roleID),
		fmt.Sprintf("%d:0:1", roleID),
		fmt.Sprintf("%d:%d:%d", roleID+1, roleID+1, uint64(65536)-roleID),
	}
}

func mutateNativeMapping(source map[string]any, mapName string, index int, value string) map[string]any {
	data, _ := json.Marshal(source)
	var cloned map[string]any
	_ = json.Unmarshal(data, &cloned)
	hostConfig := cloned["HostConfig"].(map[string]any)
	mappings := hostConfig["IDMappings"].(map[string]any)
	entries := mappings[mapName].([]any)
	entries[index] = value
	return cloned
}
