package docker

import (
	"bytes"
	"cmp"
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
	BoundingCaps nativePodmanBoundingCaps `json:"BoundingCaps"`
	HostConfig   struct {
		IDMappings *nativePodmanIDMappings `json:"IDMappings"`
	} `json:"HostConfig"`
}

type nativePodmanBoundingCaps struct {
	present      bool
	explicitNull bool
	duplicate    bool
}

func (caps *nativePodmanBoundingCaps) UnmarshalJSON(data []byte) error {
	if caps.present {
		caps.duplicate = true
		caps.explicitNull = false
		return nil
	}
	caps.present = true
	caps.explicitNull = bytes.Equal(data, []byte("null"))
	return nil
}

func (caps nativePodmanBoundingCaps) provesExplicitNull() bool {
	return caps.present && caps.explicitNull && !caps.duplicate
}

type nativePodmanIdentityResult struct {
	usernsMode              string
	boundingCapsNull        bool
	generationVolumeChownOK bool
}

const canonicalPodmanInspectedChownMode = "U,rprivate,nosuid,nodev,rbind"

type inspectBindSpec struct {
	source      string
	destination string
	hasChown    bool
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

func normalizeInspectedCapDrop(dropped, added []string, boundingCapsNull bool) []string {
	if !boundingCapsNull || len(added) != 0 || len(dropped) == 0 {
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
	if canonical == "CAP_ALL" || len(canonical) == len("CAP_") {
		return "", false
	}
	for index := len("CAP_"); index < len(canonical); index++ {
		character := canonical[index]
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return "", false
		}
	}
	return canonical, knownLinuxCapability(canonical)
}

func knownLinuxCapability(capabilityName string) bool {
	switch capabilityName {
	case "CAP_AUDIT_CONTROL", "CAP_AUDIT_READ", "CAP_AUDIT_WRITE",
		"CAP_BLOCK_SUSPEND", "CAP_BPF", "CAP_CHECKPOINT_RESTORE", "CAP_CHOWN",
		"CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID",
		"CAP_IPC_LOCK", "CAP_IPC_OWNER", "CAP_KILL", "CAP_LEASE", "CAP_LINUX_IMMUTABLE",
		"CAP_MAC_ADMIN", "CAP_MAC_OVERRIDE", "CAP_MKNOD", "CAP_NET_ADMIN",
		"CAP_NET_BIND_SERVICE", "CAP_NET_BROADCAST", "CAP_NET_RAW", "CAP_PERFMON",
		"CAP_SETFCAP", "CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_ADMIN",
		"CAP_SYS_BOOT", "CAP_SYS_CHROOT", "CAP_SYS_MODULE", "CAP_SYS_NICE", "CAP_SYS_PACCT",
		"CAP_SYS_PTRACE", "CAP_SYS_RAWIO", "CAP_SYS_RESOURCE", "CAP_SYS_TIME",
		"CAP_SYS_TTY_CONFIG", "CAP_SYSLOG", "CAP_WAKE_ALARM":
		return true
	default:
		return false
	}
}

func inspectNativePodmanIdentity(
	ctx context.Context,
	apiClient nativePodmanClient,
	inspected *domain.Container,
	binds []string,
) (nativePodmanIdentityResult, bool) {
	identity, ok := inspectedComponentIdentity(inspected)
	if !ok || !canonicalContainerID(inspected.ID) || !localUnixDaemon(apiClient.DaemonHost()) {
		return nativePodmanIdentityResult{}, false
	}
	native, ok := fetchNativePodmanInspect(ctx, apiClient, inspected.ID)
	if !ok || native.HostConfig.IDMappings == nil || !validNativeKeepIDMappings(*native.HostConfig.IDMappings, identity) {
		return nativePodmanIdentityResult{}, false
	}
	result := nativePodmanIdentityResult{
		usernsMode:       fmt.Sprintf("keep-id:uid=%d,gid=%d", identity.UID, identity.GID),
		boundingCapsNull: native.BoundingCaps.provesExplicitNull(),
	}
	if expectedName, ok := inspectedGenerationVolumeName(inspected); ok {
		_, mountOK := exactInspectedGenerationMount(inspected.VolumeMounts, expectedName)
		result.generationVolumeChownOK = mountOK && inspectNamedVolumeChownEvidence(expectedName, binds)
	}
	return result, true
}

func inspectedGenerationVolumeName(inspected *domain.Container) (string, bool) {
	if _, ok := inspectedComponentIdentity(inspected); !ok {
		return "", false
	}
	expected, ok := domain.MatchComponentGenerationVolume(inspected)
	if !ok || !canonicalInspectVolumeName(expected) {
		return "", false
	}
	return expected, true
}

func exactInspectedGenerationMount(mounts []domain.ContainerVolumeMount, expectedName string) (int, bool) {
	matched := -1
	for index, mountPoint := range mounts {
		if !inspectDestinationMayTarget(mountPoint.Destination, "/var/lib/gordon") {
			continue
		}
		if matched >= 0 || mountPoint.Type != "volume" || mountPoint.Name != expectedName ||
			mountPoint.Destination != "/var/lib/gordon" || mountPoint.ReadOnly || mountPoint.Mode != "" || len(mountPoint.Options) != 0 {
			return 0, false
		}
		matched = index
	}
	return matched, matched >= 0
}

func inspectNamedVolumeChownEvidence(expectedName string, binds []string) bool {
	matched := false
	for _, raw := range binds {
		if !inspectBindMayTarget(raw, "/var/lib/gordon") {
			continue
		}
		if matched {
			return false
		}
		matched = true

		spec, valid := parseInspectBindSpec(raw)
		if !valid || spec.source != expectedName || spec.destination != "/var/lib/gordon" || !spec.hasChown {
			return false
		}
	}
	return matched
}

func inspectBindMayTarget(raw, destination string) bool {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 {
		return false
	}
	return inspectDestinationMayTarget(parts[1], destination)
}

func inspectDestinationMayTarget(candidate, destination string) bool {
	if candidate == destination {
		return true
	}
	candidate = strings.TrimSpace(candidate)
	return candidate == destination || (path.IsAbs(candidate) && path.Clean(candidate) == destination)
}

func canonicalInspectVolumeName(name string) bool {
	if len(name) < 2 || !inspectVolumeNameInitial(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if !inspectVolumeNameInitial(character) && character != '_' && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

func inspectVolumeNameInitial(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
}

func canonicalInspectDestination(destination string) bool {
	return path.IsAbs(destination) && destination != "/" && !strings.Contains(destination, `\`) && path.Clean(destination) == destination
}

func parseInspectBindSpec(raw string) (inspectBindSpec, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || !canonicalInspectDestination(parts[1]) {
		return inspectBindSpec{}, false
	}
	if strings.TrimSpace(parts[0]) != parts[0] {
		return inspectBindSpec{}, false
	}

	spec := inspectBindSpec{source: parts[0], destination: parts[1]}
	if len(parts) == 2 {
		return spec, true
	}
	if parts[2] != canonicalPodmanInspectedChownMode {
		return inspectBindSpec{}, false
	}
	spec.hasChown = true
	return spec, true
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
	shared := apiClient.HTTPClient()
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return dial(dialCtx)
		},
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   shared.Timeout,
		Jar:       shared.Jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
		return cmp.Compare(left.ContainerID, right.ContainerID)
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
		return cmp.Compare(left.HostID, right.HostID)
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
