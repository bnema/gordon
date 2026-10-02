package deployment

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// startedCandidate is one freshly started replacement generation and its
// observed loopback backend binds (container port -> host port).
type startedCandidate struct {
	Container *domain.Container
	TCPBinds  map[int]int
	UDPBinds  map[int]int
}

// createAndStart builds the runtime config and starts the replacement.
// Preflight already pulled and inspected the pinned runtime image.
// Every TCP-capable interface port is published on 127.0.0.1 ephemeral
// plus every UDP interface port on 127.0.0.1/udp ephemeral: readiness
// and the proxy dial these loopback binds rootless-first,
// never container IPs. The runtime keeps native restarts; Gordon
// reconciles intent at boot and in the monitor (accepted decision:
// native runtime restarts plus daemon reconciliation).
func (s *Service) createAndStart(ctx context.Context, app, revision string, p pinnedService, opID string, journal candidateJournal) (startedCandidate, error) {
	// Re-resolve binds from the current policy immediately before any
	// runtime mutation: a bind revoked since preflight must fail here,
	// before volume ownership or container creation.
	resolvedBinds, err := s.resolveServiceBinds(app, p.spec)
	if err != nil {
		return startedCandidate{}, err
	}
	// Re-resolve devices from the current policy under the same
	// fail-closed rule: revoked grants never reach the runtime.
	resolvedDevices, err := s.resolveServiceDevices(app, p.spec)
	if err != nil {
		return startedCandidate{}, err
	}
	env, err := s.serviceEnv(ctx, app, p)
	if err != nil {
		return startedCandidate{}, err
	}
	image := runtimeImageRef(p)
	// The incarnation network is verified or created before any
	// container exists, so a workload is only ever started on a
	// Gordon-owned network that no other app shares.
	appID, err := s.ensureIncarnationID(ctx, app)
	if err != nil {
		return startedCandidate{}, err
	}
	nets := s.resolveAppNetworks(appID, p.sharedNetworks)
	if err := s.ensureAppNetworks(ctx, app, appID, nets); err != nil {
		return startedCandidate{}, err
	}
	volumes := map[string]string{}
	readOnlyVolumes := map[string]string{}
	for _, vol := range p.spec.Volumes {
		runtimeName := domain.RuntimeVolumeName(app, p.spec.Name, vol.Name)
		// Durable ownership is reserved BEFORE the runtime volume
		// exists: a crash in between leaves a protected record, never
		// an unowned volume that prune could later adopt. The
		// incarnation ID is already validated above, so the labels
		// carry the same ID without another ownership load.
		if err := s.reserveVolumeOwnership(ctx, app, appID, p.spec.Name, vol.Name, runtimeName); err != nil {
			return startedCandidate{}, err
		}
		if err := s.deps.Runtime.CreateVolume(ctx, runtimeName, volumeProvenanceLabels(app, appID, p.spec.Name, revision)); err != nil {
			// CreateVolume is idempotent at the adapter; existence was
			// checked at preflight, so only real backend errors fail here.
			return startedCandidate{}, fmt.Errorf("deployment: create volume %q: %w", vol.Name, err)
		}
		// A declared read-only mount must reach the runtime as read-only:
		// a writable mount would let the service modify protected data.
		if vol.ReadOnly {
			readOnlyVolumes[vol.Path] = runtimeName
			continue
		}
		volumes[vol.Path] = runtimeName
	}
	config := &domain.ContainerConfig{
		Image:           image,
		Name:            domain.LogicalServiceIdentity(app, p.spec.Name) + "--" + shortOp(opID),
		Env:             env,
		Entrypoint:      append([]string(nil), p.spec.Command...),
		Volumes:         volumes,
		ReadOnlyVolumes: readOnlyVolumes,
		Binds:           resolvedBinds,
		CDIDevices:      resolvedDevices,
		Labels:          appLabels(app, p.spec.Name, revision),
		AutoRemove:      false,
		RestartPolicy:   domain.RestartPolicyAlways,
		PortPublishes:   backendPublishes(p.spec),
		NetworkMode:     nets.private,
		Hostname:        p.spec.Name,
		Aliases:         []string{p.spec.Name},
		MemoryLimit:     s.deps.Limits.MemoryBytes,
		NanoCPUs:        s.deps.Limits.NanoCPUs,
		PidsLimit:       s.deps.Limits.PidsLimit,
	}
	created, err := s.createContainer(ctx, p.name, config)
	if err != nil {
		return startedCandidate{}, err
	}
	// The candidate ID is durably journaled before the container is started
	// or joins a shared network, so an interrupted deployment can be cleaned
	// up instead of leaving an untracked running generation.
	if err := s.recordCandidate(ctx, app, p.spec.Name, created.ID, journal); err != nil {
		return startedCandidate{}, err
	}
	if err := s.connectSharedNetworks(ctx, created.ID, nets); err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return startedCandidate{Container: created}, err
	}
	if err := s.deps.Runtime.StartContainer(ctx, created.ID); err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return startedCandidate{Container: created}, fmt.Errorf("deployment: start container: %w", err)
	}
	binds, udpBinds, err := s.readBackendBinds(ctx, app, p.spec.Name, created.ID, backendPorts(p.spec))
	if err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return startedCandidate{Container: created}, err
	}
	return startedCandidate{Container: created, TCPBinds: binds, UDPBinds: udpBinds}, nil
}

// runtimeImageRef resolves the exact runtime image reference of one pinned
// service: the image preflight resolved, else the manifest reference pinned to
// its digest.
func runtimeImageRef(p pinnedService) string {
	if p.runtimeImage != "" {
		return p.runtimeImage
	}
	if p.digest == "" {
		return p.spec.Image
	}
	return stripImageTag(p.spec.Image) + "@" + p.digest
}

// recordCandidate durably journals a freshly created candidate before it is
// started. A candidate whose ID cannot be recorded must not keep running:
// recovery could never find it, so it is removed and the failure surfaced.
func (s *Service) recordCandidate(ctx context.Context, app, service, containerID string, journal candidateJournal) error {
	if journal == nil {
		return nil
	}
	if err := journal(ctx, containerID); err != nil {
		retired := s.retireContainer(ctx, app, retireOptions{Service: service, Force: true}, containerID)
		if !retired.Gone {
			return fmt.Errorf("%w (candidate cleanup: %s)", err, cleanupDetail(retired))
		}
		return err
	}
	return nil
}

func (s *Service) createContainer(ctx context.Context, service string, config *domain.ContainerConfig) (*domain.Container, error) {
	created, err := s.deps.Runtime.CreateContainer(ctx, config)
	if err == nil {
		return created, nil
	}
	// The engine-unsupported sentinel survives redaction so callers can
	// map it to the structured runtime-unsupported envelope. It carries
	// no host inventory (engine family/version only), and the device
	// gate runs only for device-bearing creates, so this branch also
	// covers services that declare both binds and devices.
	if errors.Is(err, domain.ErrRuntimeUnsupported) {
		return nil, fmt.Errorf("deployment: create container for service %q with administrative devices: %w", service, domain.ErrRuntimeUnsupported)
	}
	if len(config.Binds) > 0 {
		// A CreateContainer failure is a runtime error, not a bind policy
		// violation: policy was already enforced by resolveServiceBinds
		// before this call. Runtime errors may embed the resolved host
		// path, so redact the whole cause instead of mislabelling it and
		// keep it out of operation journals and API/CLI responses.
		return nil, fmt.Errorf("deployment: create container for service %q with administrative mounts: runtime error redacted", service)
	}
	if len(config.CDIDevices) > 0 {
		// Same redaction rule for device-bearing creates: runtime errors
		// may embed resolved CDI IDs, which are host inventory. Policy
		// was already enforced by resolveServiceDevices before this call.
		return nil, fmt.Errorf("deployment: create container for service %q with administrative devices: runtime error redacted", service)
	}
	return nil, fmt.Errorf("deployment: create container: %w", err)
}

// reserveVolumeOwnership durably records one app-owned volume as
// attached BEFORE the runtime volume is created, so a crash between the
// two leaves a protected record instead of an unowned volume. The
// caller passes the validated incarnation ID from ensureIncarnationID,
// which already assigned it, so no reload is needed for the labels.
func (s *Service) reserveVolumeOwnership(ctx context.Context, app, appID, service, volumeName, runtimeName string) error {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load ownership: %w", err)
	}
	if ownership.App == "" {
		ownership.App = app
	}
	if ownership.ID == "" {
		ownership.ID = appID
	}
	entry := domain.AppOwnedVolume{
		Name:        volumeName,
		Service:     service,
		RuntimeName: runtimeName,
		State:       domain.AppResourceAttached,
	}
	replaced := false
	for i, existing := range ownership.Volumes {
		if existing.RuntimeName == runtimeName {
			ownership.Volumes[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		ownership.Volumes = append(ownership.Volumes, entry)
	}
	if err := s.deps.State.SaveOwnership(ctx, ownership); err != nil {
		return fmt.Errorf("deployment: reserve volume ownership: %w", err)
	}
	return nil
}

// volumeProvenanceLabels is the label set stamped on an app-created
// volume. Ownership is never inferred from the volume name, so the
// labels must carry the full provenance: app, incarnation UUID, and
// service. The managed marker is stamped here as well as merged by the
// adapter, so provenance never depends on adapter behaviour alone.
func volumeProvenanceLabels(app, appID, service, revision string) map[string]string {
	labels := map[string]string{
		domain.LabelManaged:     "true",
		domain.LabelApp:         app,
		domain.LabelAppService:  service,
		domain.LabelAppRevision: revision,
	}
	if appID != "" {
		labels[domain.LabelAppID] = appID
	}
	return labels
}

// appLabels stamps engine ownership on every created container.
func appLabels(app, service, revision string) map[string]string {
	return map[string]string{
		domain.LabelManaged:     "true",
		domain.LabelApp:         app,
		domain.LabelAppService:  service,
		domain.LabelAppRevision: revision,
	}
}

// shortOp keeps container names short but unique per op.
func shortOp(opID string) string {
	trimmed := strings.TrimPrefix(opID, "op-")
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	if trimmed == "" {
		return "op"
	}
	return trimmed
}

// serviceEnv resolves the service environment: the captured revision's
// app-wide public env on every service, overlaid by that service's own
// public env (the service value wins for the same key), plus its secret
// values (memory only, UUID-keyed paths). Keys are emitted sorted and
// deterministic. Overlapping public/secret keys are invalid with no
// precedence; preflight rejects them before any mutation.
func (s *Service) serviceEnv(ctx context.Context, app string, p pinnedService) ([]string, error) {
	spec := p.spec
	envMap := make(map[string]string, len(p.appEnv)+len(spec.Env)+len(spec.Secrets))
	maps.Copy(envMap, p.appEnv)
	maps.Copy(envMap, spec.Env)
	keys := make([]string, 0, len(spec.Secrets))
	for envKey := range spec.Secrets {
		keys = append(keys, envKey)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return sortedEnv(envMap), nil
	}
	id, err := s.appSecretID(ctx, app)
	if err != nil {
		return nil, err
	}
	for _, envKey := range keys {
		if _, overlap := envMap[envKey]; overlap {
			return nil, fmt.Errorf("deployment: env key %q collides with a secret key in service %q: %w", envKey, spec.Name, domain.ErrInvalidAppSpec)
		}
		path := domain.AppSecretPathForID(id, app, spec.Name, spec.Secrets[envKey])
		value, err := s.deps.Secrets.GetSecret(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("deployment: secret %s (env %s) missing: %w", path, envKey, domain.ErrAppSecretMissing)
		}
		envMap[envKey] = value
	}
	return sortedEnv(envMap), nil
}

// sortedEnv renders a KEY=value list in sorted key order. Values stay in
// memory for container creation only; nothing is persisted or logged.
func sortedEnv(envMap map[string]string) []string {
	if len(envMap) == 0 {
		return nil
	}
	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+envMap[key])
	}
	return env
}

// pullImage fetches the pinned image from the installation registry.
// External refs pull anonymously; installation-registry refs use the
// internal credentials. A missing registry config disables pulling
// (tests and pre-pulled environments).
func (s *Service) pullImage(ctx context.Context, image string) (string, error) {
	if err := s.validateImageSource(image, ""); err != nil {
		return "", err
	}
	if s.deps.Registry.Domain == "" {
		return image, nil
	}
	if !s.deps.ImagePolicy.IsInstallationImage(image) {
		if err := s.deps.Runtime.PullImage(ctx, image); err != nil {
			return "", fmt.Errorf("deployment: pull image %q: %w", image, err)
		}
		return image, nil
	}
	_, remainder, ok := strings.Cut(image, "/")
	if !ok || !strings.Contains(remainder, "@sha256:") {
		return "", fmt.Errorf("deployment: installation image must include an exact sha256 digest: %w", domain.ErrAppImageNotAllowed)
	}
	pullImage := s.deps.Registry.PullAddress + "/" + remainder
	request := domain.ImagePullRequest{Reference: pullImage, Username: s.deps.Registry.Username, Password: s.deps.Registry.Password, Transport: domain.ImagePullTransportHTTP}
	if err := s.deps.Runtime.PullImageWithOptions(ctx, request); err != nil {
		return "", fmt.Errorf("deployment: pull installation image %q via configured local HTTP transport %q: %w", image, s.deps.Registry.PullAddress, err)
	}
	digest := remainder[strings.LastIndex(remainder, "@")+1:]
	if err := s.deps.Runtime.VerifyImageDigest(ctx, pullImage, digest); err != nil {
		return "", fmt.Errorf("deployment: verify pulled installation image %q: %w", image, err)
	}
	return pullImage, nil
}

// stripImageTag removes any tag or digest suffix from an image reference.
// colon is a registry port (localhost:15500/e2e/web:v1 strips to
// localhost:15500/e2e/web).
func stripImageTag(ref string) string {
	if base, _, ok := strings.Cut(ref, "@"); ok {
		return base
	}
	lastSlash := strings.LastIndex(ref, "/")
	if idx := strings.LastIndex(ref, ":"); idx > lastSlash {
		return ref[:idx]
	}
	return ref
}
