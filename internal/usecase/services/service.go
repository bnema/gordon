package services

import (
	"bufio"
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const defaultVolumePrefix = "gordon-service"

type Service struct {
	runtime           out.RuntimeStandaloneServiceManager
	secretProvider    out.SecretProvider
	sourceComponentID string
	instanceSecret    [sha256.Size]byte
	generation        atomic.Uint64
	now               func() time.Time
}

// NewService preserves monolith callers by adapting the broad local runtime to the narrow service port.
func NewService(runtime out.ContainerRuntime) *Service {
	return newService(NewLocalRuntimeStandaloneServiceManager(runtime), nil, "gordon-control", newStandaloneServiceInstanceSecret())
}

// NewServiceWithSecretProvider preserves monolith callers by adapting the broad local runtime to the narrow service port.
func NewServiceWithSecretProvider(runtime out.ContainerRuntime, secretProvider out.SecretProvider) *Service {
	return newService(NewLocalRuntimeStandaloneServiceManager(runtime), secretProvider, "gordon-control", newStandaloneServiceInstanceSecret())
}

// NewServiceWithVolumePrefix preserves monolith callers that configure managed-volume names.
func NewServiceWithVolumePrefix(runtime out.ContainerRuntime, volumePrefix string) *Service {
	return newService(newLocalRuntimeStandaloneServiceManager(runtime, volumePrefix), nil, "gordon-control", newStandaloneServiceInstanceSecret())
}

// NewServiceWithRuntimeStandaloneServiceManager creates the control-side service with a narrow runtime port.
func NewServiceWithRuntimeStandaloneServiceManager(runtime out.RuntimeStandaloneServiceManager) *Service {
	return newService(runtime, nil, "gordon-control", newStandaloneServiceInstanceSecret())
}

// NewServiceWithRuntimeStandaloneServiceManagerAndSecretProvider creates the control-side service with secret resolution.
func NewServiceWithRuntimeStandaloneServiceManagerAndSecretProvider(runtime out.RuntimeStandaloneServiceManager, secretProvider out.SecretProvider) *Service {
	return newService(runtime, secretProvider, "gordon-control", newStandaloneServiceInstanceSecret())
}

func newService(runtime out.RuntimeStandaloneServiceManager, secretProvider out.SecretProvider, sourceComponentID string, instanceSecret [sha256.Size]byte) *Service {
	if strings.TrimSpace(sourceComponentID) == "" {
		sourceComponentID = "gordon-control"
	}
	return &Service{
		runtime:           runtime,
		secretProvider:    secretProvider,
		sourceComponentID: sourceComponentID,
		instanceSecret:    instanceSecret,
		now:               func() time.Time { return time.Now().UTC() },
	}
}

func newStandaloneServiceInstanceSecret() [sha256.Size]byte {
	var instanceSecret [sha256.Size]byte
	if _, err := cryptorand.Read(instanceSecret[:]); err != nil {
		panic("standalone service command identity entropy unavailable")
	}
	return instanceSecret
}

func (s *Service) Reconcile(ctx context.Context, configured []domain.StandaloneService) error {
	if s.runtime == nil {
		return fmt.Errorf("runtime standalone service manager unavailable")
	}
	states, err := s.runtime.ListStandaloneServiceState(ctx)
	if err != nil {
		return fmt.Errorf("list standalone service state: %w", err)
	}
	existing := standaloneServiceStateByName(states)
	configuredNames := make(map[string]struct{}, len(configured))
	for _, service := range configured {
		configuredNames[service.Name] = struct{}{}
		if !service.Enabled {
			if len(existing[service.Name]) == 0 {
				continue
			}
			if err := s.remove(ctx, service.Name, "disabled", normalizeCleanup(service.Cleanup)); err != nil {
				return err
			}
			continue
		}
		if err := s.apply(ctx, service, existing[service.Name]); err != nil {
			return err
		}
	}
	removedNames := make([]string, 0)
	for name := range existing {
		if _, ok := configuredNames[name]; !ok {
			removedNames = append(removedNames, name)
		}
	}
	sort.Strings(removedNames)
	for _, name := range removedNames {
		if err := s.remove(ctx, name, "removed", normalizeCleanup(existing[name][0].Cleanup)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) apply(ctx context.Context, service domain.StandaloneService, existing []domain.RuntimeStandaloneServiceState) error {
	env, err := s.serviceEnv(ctx, service)
	if err != nil {
		return fmt.Errorf("resolve standalone service %q environment: %w", service.Name, err)
	}
	hash, err := serviceConfigHashWithEnv(service, env)
	if err != nil {
		return fmt.Errorf("hash standalone service %q config: %w", service.Name, err)
	}
	if !standaloneServiceNeedsApply(service, hash, existing) {
		return nil
	}
	result, err := s.runtime.ApplyStandaloneService(ctx, domain.ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: s.desiredIdentity("apply", service.Name+":"+hash),
		Service:                service.ForRuntimeApply(),
		ResolvedEnv:            env,
		ConfigHash:             hash,
	})
	if err != nil {
		return fmt.Errorf("apply standalone service %q: %w", service.Name, err)
	}
	if err := standaloneServiceCommandResultError(result); err != nil {
		return fmt.Errorf("apply standalone service %q: %w", service.Name, err)
	}
	return nil
}

func (s *Service) remove(ctx context.Context, name, reason string, cleanup domain.StandaloneServiceCleanup) error {
	result, err := s.runtime.RemoveStandaloneService(ctx, domain.RemoveStandaloneServiceCommand{
		RuntimeCommandIdentity: s.imperativeIdentity("remove", name),
		Name:                   name,
		Reason:                 reason,
		Cleanup:                normalizeCleanup(cleanup),
	})
	if err != nil {
		return fmt.Errorf("remove standalone service %q: %w", name, err)
	}
	if err := standaloneServiceCommandResultError(result); err != nil {
		return fmt.Errorf("remove standalone service %q: %w", name, err)
	}
	return nil
}

func standaloneServiceNeedsApply(service domain.StandaloneService, hash string, existing []domain.RuntimeStandaloneServiceState) bool {
	if len(existing) == 0 || len(existing) > 1 {
		return true
	}
	state := existing[0]
	if state.ConfigHash != hash || state.Status != domain.ContainerStatusRunning {
		return true
	}
	return service.Readiness.Type != "" && service.Readiness.Type != domain.StandaloneServiceReadinessNone
}

func standaloneServiceStateByName(states []domain.RuntimeStandaloneServiceState) map[string][]domain.RuntimeStandaloneServiceState {
	existing := make(map[string][]domain.RuntimeStandaloneServiceState)
	for _, state := range states {
		if strings.TrimSpace(state.Name) == "" {
			continue
		}
		existing[state.Name] = append(existing[state.Name], state)
	}
	return existing
}

func standaloneServiceCommandResultError(result domain.RuntimeCommandResult) error {
	switch result.Status {
	case domain.RuntimeCommandStatusSucceeded:
		return nil
	case domain.RuntimeCommandStatusFailed:
		return runtimeCommandOutcomeError("failed", result.Error)
	case domain.RuntimeCommandStatusDenied:
		return runtimeCommandOutcomeError("denied", result.Error)
	case domain.RuntimeCommandStatusPending, domain.RuntimeCommandStatusRunning:
		return fmt.Errorf("runtime command incomplete: %s", result.Status)
	default:
		return fmt.Errorf("runtime command invalid status")
	}
}

func runtimeCommandOutcomeError(outcome string, commandErr *domain.RuntimeCommandError) error {
	code := "runtime_command_" + outcome
	if commandErr != nil {
		if safeCode, ok := safeRuntimeCommandErrorCode(commandErr.Code); ok {
			code = safeCode
		}
	}
	return fmt.Errorf("runtime command %s (%s)", outcome, code)
}

func safeRuntimeCommandErrorCode(code string) (string, bool) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 128 {
		return "", false
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '_' && character != '-' && character != ':' {
			return "", false
		}
	}
	return code, true
}

func (s *Service) Status(ctx context.Context) ([]domain.StandaloneServiceStatus, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("runtime standalone service manager unavailable")
	}
	states, err := s.runtime.ListStandaloneServiceState(ctx)
	if err != nil {
		return nil, fmt.Errorf("list standalone service state: %w", err)
	}
	statuses := make([]domain.StandaloneServiceStatus, 0, len(states))
	for _, state := range states {
		statuses = append(statuses, domain.StandaloneServiceStatus{
			Name:          state.Name,
			ContainerID:   state.ContainerID,
			ContainerName: state.ContainerName,
			Status:        state.Status,
			ConfigHash:    state.ConfigHash,
		})
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Name != statuses[j].Name {
			return statuses[i].Name < statuses[j].Name
		}
		return statuses[i].ContainerID < statuses[j].ContainerID
	})
	return statuses, nil
}

func (s *Service) desiredIdentity(kind, hash string) domain.RuntimeCommandIdentity {
	return s.identity(kind + ":" + hash)
}

func (s *Service) imperativeIdentity(kind, subject string) domain.RuntimeCommandIdentity {
	cleanSubject := strings.TrimSpace(subject)
	if cleanSubject == "" {
		cleanSubject = "all"
	}
	generation := s.generation.Add(1)
	return s.identityWithGeneration(fmt.Sprintf("%s:%s:%d", kind, cleanSubject, generation), generation)
}

func (s *Service) identity(key string) domain.RuntimeCommandIdentity {
	return s.identityWithGeneration(key, s.generation.Add(1))
}

func (s *Service) identityWithGeneration(key string, generation uint64) domain.RuntimeCommandIdentity {
	namespacedKey := s.keyedDigest([]byte(key))
	return domain.RuntimeCommandIdentity{
		ID:                domain.RuntimeCommandID("standalone-service:" + namespacedKey),
		IdempotencyKey:    namespacedKey,
		Generation:        generation,
		SourceComponentID: s.sourceComponentID,
		RequestedAt:       s.now().UTC(),
	}
}

func (s *Service) keyedDigest(payload []byte) string {
	digest := hmac.New(sha256.New, s.instanceSecret[:])
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil))
}

func serviceConfigHash(svc domain.StandaloneService) (string, error) {
	return NewService(nil).serviceConfigHash(context.Background(), svc)
}

func (s *Service) serviceConfigHash(ctx context.Context, svc domain.StandaloneService) (string, error) {
	resolvedEnv, err := s.serviceEnv(ctx, svc)
	if err != nil {
		return "", err
	}
	return serviceConfigHashWithEnv(svc, resolvedEnv)
}

func serviceConfigHashWithEnv(svc domain.StandaloneService, resolvedEnv []string) (string, error) {
	payload := struct {
		Image       string
		ResolvedEnv []string
		Readiness   domain.StandaloneServiceReadiness
		Ports       []domain.StandaloneServicePort
		Volumes     []domain.StandaloneServiceVolume
		Cleanup     domain.StandaloneServiceCleanup
	}{svc.Image, append([]string(nil), resolvedEnv...), svc.Readiness, append([]domain.StandaloneServicePort(nil), svc.Ports...), append([]domain.StandaloneServiceVolume(nil), svc.Volumes...), normalizeCleanup(svc.Cleanup)}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) serviceEnv(ctx context.Context, svc domain.StandaloneService) ([]string, error) {
	envMap := make(map[string]string)
	if svc.EnvFile != "" {
		fileEnv, err := loadEnvFile(ctx, svc.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("load standalone service %q env file: %w", svc.Name, err)
		}
		maps.Copy(envMap, fileEnv)
	}
	for _, entry := range svc.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("parse standalone service %q env entry", svc.Name)
		}
		envMap[key] = value
	}
	if len(svc.Secrets) > 0 && s.secretProvider == nil {
		return nil, fmt.Errorf("resolve standalone service %q secrets: secret provider is not configured", svc.Name)
	}
	for _, secret := range svc.Secrets {
		value, err := s.secretProvider.GetSecret(ctx, serviceSecretPath(svc.Name, secret.Name))
		if err != nil {
			return nil, fmt.Errorf("resolve standalone service %q secret %q: %w", svc.Name, secret.Name, err)
		}
		envMap[secret.Key] = value
	}
	return envMapToList(envMap), nil
}

func loadEnvFile(ctx context.Context, path string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid env file line")
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func serviceSecretPath(serviceName, secretName string) string {
	if strings.Contains(secretName, ":") {
		return secretName
	}
	return "service/" + serviceName + "/" + secretName
}

func envMapToList(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func managedServiceContainers(containers []*domain.Container) map[string][]*domain.Container {
	managed := make(map[string][]*domain.Container)
	for _, container := range containers {
		if container == nil || container.Labels[domain.LabelService] != "true" {
			continue
		}
		name := container.Labels[domain.LabelServiceName]
		if name == "" {
			continue
		}
		managed[name] = append(managed[name], container)
	}
	return managed
}

func managedVolumeSet(labels map[string]string) map[string]struct{} {
	values := strings.Split(labels[domain.LabelServiceManagedVolumes], ",")
	managed := make(map[string]struct{}, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		managed[name] = struct{}{}
	}
	return managed
}

func cleanupFromLabels(labels map[string]string) domain.StandaloneServiceCleanup {
	cleanup := domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true}
	if value, ok := labels[domain.LabelServiceCleanupPreserveVolumes]; ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cleanup.PreserveVolumes = parsed
		}
	}
	if value, ok := labels[domain.LabelServiceCleanupRemoveContainer]; ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cleanup.RemoveContainer = parsed
		}
	}
	return cleanup
}

func serviceLabels(name, hash string, cleanup domain.StandaloneServiceCleanup, managedVolumes []string) map[string]string {
	sort.Strings(managedVolumes)
	return map[string]string{
		domain.LabelManaged:                       "true",
		domain.LabelService:                       "true",
		domain.LabelServiceName:                   name,
		domain.LabelServiceConfigHash:             hash,
		domain.LabelServiceManagedVolumes:         strings.Join(managedVolumes, ","),
		domain.LabelServiceCleanupPreserveVolumes: strconv.FormatBool(cleanup.PreserveVolumes),
		domain.LabelServiceCleanupRemoveContainer: strconv.FormatBool(cleanup.RemoveContainer),
	}
}

func portPublishes(svc domain.StandaloneService) ([]domain.ContainerPortPublish, error) {
	publishes := make([]domain.ContainerPortPublish, 0, len(svc.Ports))
	for _, port := range svc.Ports {
		if port.Publish == "" {
			continue
		}
		host, hostPort, err := net.SplitHostPort(port.Publish)
		if err != nil {
			return nil, fmt.Errorf("parse standalone service %q port %q publish: %w", svc.Name, port.Name, err)
		}
		hostPortNumber, err := strconv.Atoi(hostPort)
		if err != nil {
			return nil, fmt.Errorf("parse standalone service %q port %q publish port: %w", svc.Name, port.Name, err)
		}
		publishes = append(publishes, domain.ContainerPortPublish{HostIP: host, HostPort: hostPortNumber, ContainerPort: port.Container, Protocol: port.Protocol})
	}
	return publishes, nil
}

func waitTCPReadiness(ctx context.Context, svc domain.StandaloneService) error {
	address, err := tcpReadinessAddress(svc)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("standalone service %q tcp readiness timed out: %w", svc.Name, err)
		}
		dialer := net.Dialer{Timeout: 100 * time.Millisecond}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("standalone service %q tcp readiness timed out: %w", svc.Name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func tcpReadinessAddress(svc domain.StandaloneService) (string, error) {
	for _, port := range svc.Ports {
		if port.Protocol != domain.NetworkProtocolTCP || port.Publish == "" {
			continue
		}
		host, hostPort, err := net.SplitHostPort(port.Publish)
		if err != nil {
			return "", fmt.Errorf("parse standalone service %q tcp readiness port %q publish: %w", svc.Name, port.Name, err)
		}
		switch host {
		case "::":
			host = "::1"
		case "0.0.0.0", "":
			host = "127.0.0.1"
		}
		return net.JoinHostPort(host, hostPort), nil
	}
	return "", fmt.Errorf("standalone service %q tcp readiness requires a published tcp port", svc.Name)
}

func logReadinessTimeoutError(serviceName string, timeoutErr, lastErr error) error {
	if lastErr != nil {
		return fmt.Errorf("standalone service %q log readiness timed out: %w; last read error: %w", serviceName, timeoutErr, lastErr)
	}
	return fmt.Errorf("standalone service %q log readiness timed out: %w", serviceName, timeoutErr)
}

func normalizeCleanup(cleanup domain.StandaloneServiceCleanup) domain.StandaloneServiceCleanup {
	if !cleanup.PreserveVolumes && !cleanup.RemoveContainer {
		return domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true}
	}
	return cleanup
}

func containerStatus(container *domain.Container) domain.ContainerStatus {
	status := strings.ToLower(container.Status)
	if strings.Contains(status, string(domain.ContainerStatusRunning)) {
		return domain.ContainerStatusRunning
	}
	if strings.Contains(status, string(domain.ContainerStatusExited)) {
		return domain.ContainerStatusExited
	}
	if strings.Contains(status, string(domain.ContainerStatusCreated)) {
		return domain.ContainerStatusCreated
	}
	if strings.Contains(status, string(domain.ContainerStatusPaused)) {
		return domain.ContainerStatusPaused
	}
	if strings.Contains(status, string(domain.ContainerStatusStopped)) {
		return domain.ContainerStatusStopped
	}
	return domain.ContainerStatusUnknown
}

func serviceContainerName(name string) string {
	return "gordon-service-" + strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(name)
}

func emptyToNil(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return values
}
