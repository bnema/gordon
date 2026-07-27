package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/docker/docker/client"
	"github.com/moby/sys/capability"

	"github.com/bnema/gordon/internal/domain"
)

const (
	nativePodmanInspectPrefix          = "/v4.0.0/libpod/containers/"
	nativePodmanInspectSuffix          = "/json"
	maxNativePodmanInspectBytes        = 1 << 20
	maxNativePodmanIDMapEntries        = 64
	maxLinuxIDExclusive         uint64 = 1 << 32
)

type nativePodmanClient interface {
	DaemonHost() string
	Dialer() func(context.Context) (net.Conn, error)
	HTTPClient() *http.Client
}

type nativePodmanInspect struct {
	HostConfig struct {
		IDMappings *nativePodmanIDMappings `json:"IDMappings"`
	} `json:"HostConfig"`
}

type nativePodmanIDMappings struct {
	UIDMap []string `json:"UidMap"`
	GIDMap []string `json:"GidMap"`
}

type nativePodmanIDMap struct {
	ContainerID uint64 `json:"ContainerID"`
	HostID      uint64 `json:"HostID"`
	Size        uint64 `json:"Size"`
}

func normalizeInspectedCapDrop(dropped, added []string) []string {
	return normalizeInspectedCapDropWithSource(dropped, added, supportedKernelCapabilities)
}

func normalizeInspectedCapDropWithSource(
	dropped, added []string,
	source func() ([]string, bool),
) []string {
	allCapabilities, ok := source()
	if !ok {
		return nil
	}
	return normalizeInspectedCapDropAgainst(dropped, added, allCapabilities)
}

// supportedKernelCapabilities returns only capabilities supported by the
// running Linux kernel. On non-Linux systems capability.ListSupported reports
// that capability discovery is unsupported, so normalization fails closed.
func supportedKernelCapabilities() ([]string, bool) {
	supported, err := capability.ListSupported()
	if err != nil || len(supported) == 0 {
		return nil, false
	}
	capabilities := make([]string, len(supported))
	for index, known := range supported {
		capabilities[index] = known.String()
	}
	return capabilities, true
}

func normalizeInspectedCapDropAgainst(dropped, added, allCapabilities []string) []string {
	if len(added) != 0 || len(dropped) == 0 || len(allCapabilities) == 0 || len(dropped) != len(allCapabilities) {
		return nil
	}
	expected := make(map[string]struct{}, len(allCapabilities))
	for _, capabilityName := range allCapabilities {
		canonical, ok := canonicalCapability(capabilityName)
		if !ok {
			return nil
		}
		expected[canonical] = struct{}{}
	}
	if len(expected) != len(allCapabilities) {
		return nil
	}
	seen := make(map[string]struct{}, len(dropped))
	for _, capabilityName := range dropped {
		canonical, ok := canonicalCapability(capabilityName)
		if !ok {
			return nil
		}
		if _, duplicate := seen[canonical]; duplicate {
			return nil
		}
		if _, known := expected[canonical]; !known {
			return nil
		}
		seen[canonical] = struct{}{}
	}
	return []string{"ALL"}
}

func canonicalCapability(capabilityName string) (string, bool) {
	if capabilityName == "" || strings.TrimSpace(capabilityName) != capabilityName {
		return "", false
	}
	canonical := strings.ToUpper(capabilityName)
	if !strings.HasPrefix(canonical, "CAP_") {
		canonical = "CAP_" + canonical
	}
	return canonical, canonical != "CAP_ALL"
}

func inspectNativePodmanKeepID(
	ctx context.Context,
	apiClient nativePodmanClient,
	inspected *domain.Container,
) (string, bool) {
	identity, ok := inspectedComponentIdentity(inspected)
	if !ok || !canonicalContainerID(inspected.ID) || !localUnixDaemon(apiClient.DaemonHost()) {
		return "", false
	}
	native, ok := fetchNativePodmanInspect(ctx, apiClient, inspected.ID)
	if !ok || native.HostConfig.IDMappings == nil || !validNativeKeepIDMappings(*native.HostConfig.IDMappings, identity) {
		return "", false
	}
	return fmt.Sprintf("keep-id:uid=%d,gid=%d", identity.UID, identity.GID), true
}

func fetchNativePodmanInspect(
	ctx context.Context,
	apiClient nativePodmanClient,
	containerID string,
) (nativePodmanInspect, bool) {
	endpoint := "http://localhost" + nativePodmanInspectPrefix + url.PathEscape(containerID) + nativePodmanInspectSuffix
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nativePodmanInspect{}, false
	}
	request.Header.Set("Accept", "application/json")

	httpClient, transport := nativePodmanHTTPClient(apiClient)
	defer transport.CloseIdleConnections()
	response, err := httpClient.Do(request)
	if err != nil {
		return nativePodmanInspect{}, false
	}
	defer response.Body.Close()
	if !validNativePodmanResponse(response) {
		return nativePodmanInspect{}, false
	}
	return decodeNativePodmanInspect(response.Body)
}

func nativePodmanHTTPClient(apiClient nativePodmanClient) (*http.Client, *http.Transport) {
	dial := apiClient.Dialer()
	httpClient := apiClient.HTTPClient()
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return dial(dialCtx)
		},
	}
	httpClient.Transport = transport
	return httpClient, transport
}

func validNativePodmanResponse(response *http.Response) bool {
	return response.StatusCode == http.StatusOK && response.ContentLength <= maxNativePodmanInspectBytes &&
		response.Header.Get("Content-Encoding") == "" && jsonContentType(response.Header.Get("Content-Type"))
}

func decodeNativePodmanInspect(body io.Reader) (nativePodmanInspect, bool) {
	limited := &io.LimitedReader{R: body, N: maxNativePodmanInspectBytes + 1}
	var native nativePodmanInspect
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&native); err != nil || limited.N == 0 {
		return nativePodmanInspect{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || limited.N == 0 {
		return nativePodmanInspect{}, false
	}
	return native, true
}

func inspectedComponentIdentity(inspected *domain.Container) (domain.ComponentProcessIdentity, bool) {
	if inspected == nil || inspected.Labels[domain.LabelComponent] != "true" {
		return domain.ComponentProcessIdentity{}, false
	}
	return domain.FixedComponentProcessIdentity(domain.ComponentRole(inspected.Labels[domain.LabelComponentRole]))
}

func canonicalContainerID(containerID string) bool {
	if len(containerID) != 64 {
		return false
	}
	for _, character := range containerID {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func localUnixDaemon(daemonHost string) bool {
	parsed, err := client.ParseHostURL(daemonHost)
	return err == nil && parsed.Scheme == "unix" && parsed.Host != "" && path.IsAbs(parsed.Host) && path.Clean(parsed.Host) == parsed.Host
}

func jsonContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

func validNativeKeepIDMappings(mappings nativePodmanIDMappings, identity domain.ComponentProcessIdentity) bool {
	if identity.UID <= 0 || identity.GID <= 0 {
		return false
	}
	uid, ok := parseAndValidateNativeIDMap(mappings.UIDMap, uint64(identity.UID))
	if !ok {
		return false
	}
	gid, ok := parseAndValidateNativeIDMap(mappings.GIDMap, uint64(identity.GID))
	if !ok || len(uid) != len(gid) {
		return false
	}
	for index := range uid {
		if uid[index].ContainerID != gid[index].ContainerID || uid[index].Size != gid[index].Size {
			return false
		}
	}
	return true
}

func parseAndValidateNativeIDMap(encoded []string, roleID uint64) ([]nativePodmanIDMap, bool) {
	if len(encoded) < 3 || len(encoded) > maxNativePodmanIDMapEntries {
		return nil, false
	}
	mappings := make([]nativePodmanIDMap, len(encoded))
	for index, entry := range encoded {
		mapping, ok := parseNativeIDMapEntry(entry)
		if !ok {
			return nil, false
		}
		mappings[index] = mapping
	}
	return validateNativeIDMap(mappings, roleID)
}

func parseNativeIDMapEntry(entry string) (nativePodmanIDMap, bool) {
	fields := strings.Split(entry, ":")
	if len(fields) != 3 {
		return nativePodmanIDMap{}, false
	}
	containerID, ok := parseCanonicalNativeID(fields[0])
	if !ok {
		return nativePodmanIDMap{}, false
	}
	hostID, ok := parseCanonicalNativeID(fields[1])
	if !ok {
		return nativePodmanIDMap{}, false
	}
	size, ok := parseCanonicalNativeID(fields[2])
	if !ok || size == 0 {
		return nativePodmanIDMap{}, false
	}
	return nativePodmanIDMap{ContainerID: containerID, HostID: hostID, Size: size}, true
}

func parseCanonicalNativeID(encoded string) (uint64, bool) {
	if encoded == "" {
		return 0, false
	}
	for index := range len(encoded) {
		if encoded[index] < '0' || encoded[index] > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(encoded, 10, 64)
	return value, err == nil && strconv.FormatUint(value, 10) == encoded
}

func validateNativeIDMap(mappings []nativePodmanIDMap, roleID uint64) ([]nativePodmanIDMap, bool) {
	sorted := slices.Clone(mappings)
	slices.SortFunc(sorted, func(left, right nativePodmanIDMap) int {
		return intCompare(left.ContainerID, right.ContainerID)
	})
	roleEntries := 0
	var containerEnd uint64
	for _, mapping := range sorted {
		if !validNativeIDMapRange(mapping) || mapping.ContainerID != containerEnd || !coherentNativeKeepIDRange(mapping, roleID) {
			return nil, false
		}
		containerEnd = mapping.ContainerID + mapping.Size
		if mapping.ContainerID == roleID {
			roleEntries++
		}
	}
	if roleEntries != 1 || containerEnd <= roleID+1 || !nativeHostRangesCoherent(sorted, containerEnd) {
		return nil, false
	}
	return sorted, true
}

func coherentNativeKeepIDRange(mapping nativePodmanIDMap, roleID uint64) bool {
	rangeEnd := mapping.ContainerID + mapping.Size
	switch {
	case rangeEnd <= roleID:
		return mapping.HostID == mapping.ContainerID+1
	case mapping.ContainerID == roleID:
		return mapping.HostID == 0 && mapping.Size == 1
	case mapping.ContainerID > roleID:
		return mapping.HostID == mapping.ContainerID
	default:
		return false
	}
}

func validNativeIDMapRange(mapping nativePodmanIDMap) bool {
	return mapping.Size > 0 && mapping.ContainerID < maxLinuxIDExclusive && mapping.HostID < maxLinuxIDExclusive &&
		mapping.Size <= maxLinuxIDExclusive-mapping.ContainerID && mapping.Size <= maxLinuxIDExclusive-mapping.HostID
}

func nativeHostRangesCoherent(mappings []nativePodmanIDMap, expectedEnd uint64) bool {
	byHost := slices.Clone(mappings)
	slices.SortFunc(byHost, func(left, right nativePodmanIDMap) int {
		return intCompare(left.HostID, right.HostID)
	})
	var hostEnd uint64
	for _, mapping := range byHost {
		if mapping.HostID != hostEnd {
			return false
		}
		hostEnd = mapping.HostID + mapping.Size
	}
	return hostEnd == expectedEnd
}

func intCompare(left, right uint64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
