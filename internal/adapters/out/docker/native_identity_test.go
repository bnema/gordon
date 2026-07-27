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
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeInspectNormalizesAuthoritativePodmanIdentity(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	allCapabilities, ok := supportedKernelCapabilities()
	require.True(t, ok)
	require.NotEmpty(t, allCapabilities)
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

func TestRuntimeInspectCapabilityNormalizationUsesKnownCapabilitiesWhenEffectiveSetIsEmpty(t *testing.T) {
	knownCapabilities, ok := supportedKernelCapabilities()
	require.True(t, ok)
	require.NotEmpty(t, knownCapabilities)
	currentEffectiveCapabilities := []string{}
	require.Empty(t, currentEffectiveCapabilities, "models a rootless Gordon process without effective capabilities")

	compatCapabilities := slices.Clone(knownCapabilities)
	for index := range compatCapabilities {
		compatCapabilities[index] = strings.ToLower(strings.TrimPrefix(compatCapabilities[index], "CAP_"))
	}
	assert.Equal(t, []string{"ALL"}, normalizeInspectedCapDrop(compatCapabilities, nil))
	assert.Nil(t, normalizeInspectedCapDrop(currentEffectiveCapabilities, nil))
}

func TestRuntimeInspectCapabilityNormalizationFailsClosedWhenKernelDiscoveryIsUnavailable(t *testing.T) {
	unavailable := func() ([]string, bool) { return nil, false }

	assert.Nil(t, normalizeInspectedCapDropWithSource([]string{"CAP_CHOWN"}, nil, unavailable))
}

func TestRuntimeInspectCapabilityNormalizationFailsClosed(t *testing.T) {
	allCapabilities := []string{"CAP_CHOWN", "CAP_SETUID", "CAP_SETGID", "CAP_NET_BIND_SERVICE"}
	canonicalEquivalent := []string{"chown", "cap_setuid", "SetGid", "net_bind_service"}
	assert.Equal(t, []string{"ALL"}, normalizeInspectedCapDropAgainst(canonicalEquivalent, nil, allCapabilities))

	tests := map[string]struct {
		dropped []string
		added   []string
		known   []string
	}{
		"empty dropped":      {dropped: nil, known: allCapabilities},
		"empty known":        {dropped: nil, known: nil},
		"literal all":        {dropped: []string{"ALL"}, known: allCapabilities},
		"subset":             {dropped: slices.Clone(allCapabilities[:len(allCapabilities)-1]), known: allCapabilities},
		"duplicate":          {dropped: append(slices.Clone(allCapabilities), allCapabilities[0]), known: allCapabilities},
		"unknown":            {dropped: append(slices.Clone(allCapabilities[:len(allCapabilities)-1]), "CAP_NOT_REAL"), known: allCapabilities},
		"superset":           {dropped: append(slices.Clone(allCapabilities), "CAP_NOT_REAL"), known: allCapabilities},
		"cap add":            {dropped: slices.Clone(allCapabilities), added: []string{"CAP_CHOWN"}, known: allCapabilities},
		"duplicate known":    {dropped: slices.Clone(allCapabilities), known: append(slices.Clone(allCapabilities), allCapabilities[0])},
		"noncanonical known": {dropped: slices.Clone(allCapabilities), known: []string{"CAP_CHOWN", " CAP_SETUID", "CAP_SETGID", "CAP_NET_BIND_SERVICE"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assert.NotEqual(t, []string{"ALL"}, normalizeInspectedCapDropAgainst(test.dropped, test.added, test.known))
		})
	}
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
		"native unavailable":        {status: http.StatusNotFound, body: map[string]any{"message": "not found"}},
		"Docker response":           {status: http.StatusOK, body: map[string]any{"Id": containerID, "HostConfig": map[string]any{}}},
		"wrong role":                {status: http.StatusOK, body: validNativeIDMappings(21001)},
		"missing gid map":           {status: http.StatusOK, body: nativeMappingsBody(nativeMapEntries(21002), nil)},
		"numeric object alias":      {status: http.StatusOK, body: map[string]any{"HostConfig": map[string]any{"IDMappings": map[string]any{"UidMap": []map[string]any{{"ContainerID": 0, "HostID": 1, "Size": 21002}}, "GidMap": nativeMapEntries(21002)}}}},
		"wrong singleton host":      {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:1000:1")},
		"overlapping host ranges":   {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 2, "21003:21002:44534")},
		"duplicate container range": {status: http.StatusOK, body: mutateNativeMapping(valid, "GidMap", 2, "21002:21003:44534")},
		"container gap":             {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 2, "21004:21003:44534")},
		"host gap":                  {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 2, "21003:21004:44534")},
		"incoherent uid and gid":    {status: http.StatusOK, body: mutateNativeMapping(valid, "GidMap", 2, "21003:21003:44533")},
		"non singleton role":        {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:0:2")},
		"zero size":                 {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:0:0")},
		"leading zero alias":        {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "021002:0:1")},
		"positive sign":             {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "+21002:0:1")},
		"negative sign":             {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "-21002:0:1")},
		"ASCII whitespace":          {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, " 21002:0:1")},
		"Unicode decimal alias":     {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "２１００２:0:1")},
		"missing field":             {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:0")},
		"extra field":               {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "21002:0:1:0")},
		"overflow":                  {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "18446744073709551616:0:1")},
		"Linux ID overflow":         {status: http.StatusOK, body: mutateNativeMapping(valid, "UidMap", 1, "4294967296:0:1")},
		"too many entries":          {status: http.StatusOK, body: nativeMappingsBody(tooManyEntries, nativeMapEntries(21002))},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runtime, _ := newNativeInspectTestRuntime(t, containerID, mustSupportedKernelCapabilities(t), nil, test.status, test.body)
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
	runtime, _ := newNativeInspectTestRuntime(t, containerID, mustSupportedKernelCapabilities(t), nil, http.StatusOK, body)
	var logs bytes.Buffer
	logger := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &logs})

	_, err := runtime.InspectContainer(zerowrap.WithCtx(t.Context(), logger), containerID)

	require.NoError(t, err)
	assert.NotContains(t, logs.String(), "native-secret-marker")
	assert.NotContains(t, logs.String(), "/run/user/fixture/podman.sock")
}

func TestRuntimeInspectDoesNotExtendNativeAuthority(t *testing.T) {
	const invalidID = "../../private/socket"
	runtime, requests := newNativeInspectTestRuntime(t, invalidID, mustSupportedKernelCapabilities(t), nil, http.StatusOK, validNativeIDMappings(21002))

	inspected, err := runtime.InspectContainer(t.Context(), "safe-name")

	require.NoError(t, err)
	assert.Equal(t, "private", inspected.UsernsMode)
	require.Len(t, *requests, 1)
	assert.NotContains(t, (*requests)[0], "libpod")
}

func mustSupportedKernelCapabilities(t *testing.T) []string {
	t.Helper()
	capabilities, ok := supportedKernelCapabilities()
	require.True(t, ok)
	return capabilities
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
