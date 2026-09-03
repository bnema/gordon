package services

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const defaultVolumePrefix = "gordon-service"

type Service struct {
	runtime        out.ContainerRuntime
	secretProvider out.SecretProvider
	volumePrefix   string
}

func NewService(runtime out.ContainerRuntime) *Service {
	return &Service{runtime: runtime, volumePrefix: defaultVolumePrefix}
}

func NewServiceWithSecretProvider(runtime out.ContainerRuntime, secretProvider out.SecretProvider) *Service {
	return &Service{runtime: runtime, secretProvider: secretProvider, volumePrefix: defaultVolumePrefix}
}

func NewServiceWithVolumePrefix(runtime out.ContainerRuntime, volumePrefix string) *Service {
	if volumePrefix == "" {
		volumePrefix = defaultVolumePrefix
	}
	return &Service{runtime: runtime, volumePrefix: volumePrefix}
}

func (s *Service) Reconcile(ctx context.Context, configured []domain.StandaloneService) error {
	workloads, networks := normalizeServiceWorkloads(configured)
	if err := s.ensureServiceNetworks(ctx, networks); err != nil {
		return err
	}
	containers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("list standalone service containers: %w", err)
	}
	existing := managedServiceContainers(containers)
	configuredNames := make(map[string]struct{}, len(workloads))
	for _, svc := range workloads {
		key := serviceWorkloadKey(svc.Name, svc.ContainerName)
		configuredNames[key] = struct{}{}
		if err := s.reconcileOne(ctx, svc, existing[key]); err != nil {
			return err
		}
	}
	for name, containers := range existing {
		if _, ok := configuredNames[name]; ok {
			continue
		}
		if err := s.stopRemoved(ctx, name, containers); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ResolvePorts(ctx context.Context, configured []domain.StandaloneService) ([]domain.ServicePortBackend, error) {
	containers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list service containers for port resolution: %w", err)
	}
	existing := managedServiceContainers(containers)
	backends := make([]domain.ServicePortBackend, 0)
	for _, svc := range configured {
		if !svc.Enabled {
			continue
		}
		for _, port := range svc.Ports {
			component := port.ContainerName
			candidates := existing[serviceWorkloadKey(svc.Name, component)]
			container := runningServiceContainer(candidates)
			if container == nil {
				return nil, fmt.Errorf("service %q port %q has no running container", svc.Name, port.Name)
			}
			hostPort, err := s.runtime.GetContainerPublishedPort(ctx, container.ID, port.Container, port.Protocol.NetworkProtocol())
			if err != nil {
				return nil, fmt.Errorf("resolve service %q port %q: %w", svc.Name, port.Name, err)
			}
			backends = append(backends, domain.ServicePortBackend{
				Service: svc.Name, PortName: port.Name, Host: "127.0.0.1", Port: hostPort, Protocol: port.Protocol, TLS: port.TLS,
			})
		}
	}
	sort.Slice(backends, func(i, j int) bool {
		if backends[i].Service == backends[j].Service {
			return backends[i].PortName < backends[j].PortName
		}
		return backends[i].Service < backends[j].Service
	})
	return backends, nil
}

func runningServiceContainer(containers []*domain.Container) *domain.Container {
	for _, container := range containers {
		if containerStatus(container) == domain.ContainerStatusRunning {
			return container
		}
	}
	return nil
}

func (s *Service) Status(ctx context.Context) ([]domain.StandaloneServiceStatus, error) {
	containers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list standalone service containers: %w", err)
	}
	statuses := make([]domain.StandaloneServiceStatus, 0)
	for _, container := range containers {
		if container.Labels[domain.LabelService] != "true" {
			continue
		}
		statuses = append(statuses, domain.StandaloneServiceStatus{
			Name:          container.Labels[domain.LabelServiceName],
			ComponentName: container.Labels[domain.LabelServiceContainer],
			ContainerID:   container.ID,
			ContainerName: container.Name,
			Status:        containerStatus(container),
			ConfigHash:    container.Labels[domain.LabelServiceConfigHash],
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses, nil
}

func (s *Service) reconcileOne(ctx context.Context, svc domain.StandaloneService, existing []*domain.Container) error {
	cleanup := normalizeCleanup(svc.Cleanup)
	if !svc.Enabled {
		return s.stopDisabled(ctx, svc.Name, cleanup, existing)
	}
	env, err := s.serviceEnv(ctx, svc)
	if err != nil {
		return fmt.Errorf("resolve standalone service %q environment: %w", svc.Name, err)
	}
	hash, err := serviceConfigHashWithEnv(svc, env)
	if err != nil {
		return fmt.Errorf("hash standalone service %q config: %w", svc.Name, err)
	}
	if len(existing) == 0 {
		return s.createAndStart(ctx, svc, hash, env)
	}
	sort.SliceStable(existing, func(i, j int) bool {
		leftRunning := containerStatus(existing[i]) == domain.ContainerStatusRunning
		rightRunning := containerStatus(existing[j]) == domain.ContainerStatusRunning
		if leftRunning != rightRunning {
			return leftRunning
		}
		return existing[i].ID < existing[j].ID
	})
	current := existing[0]
	if current.Labels[domain.LabelServiceConfigHash] != hash {
		if err := s.recreate(ctx, svc, cleanup, existing, hash, env); err != nil {
			return err
		}
		return nil
	}
	for _, duplicate := range existing[1:] {
		if err := s.cleanupContainer(ctx, svc.Name, "duplicate", cleanup, duplicate); err != nil {
			return err
		}
	}
	if containerStatus(current) != domain.ContainerStatusRunning {
		if err := s.runtime.StartContainer(ctx, current.ID); err != nil {
			return fmt.Errorf("start standalone service %q container: %w", svc.Name, err)
		}
	}
	if svc.Readiness.Type == domain.StandaloneServiceReadinessTCP {
		if err := s.resolveTCPReadinessPort(ctx, current.ID, &svc); err != nil {
			return err
		}
	}
	if err := s.waitReadiness(ctx, current.ID, svc); err != nil {
		return err
	}
	return nil
}

func (s *Service) recreate(ctx context.Context, svc domain.StandaloneService, cleanup domain.StandaloneServiceCleanup, existing []*domain.Container, hash string, env []string) error {
	for _, container := range existing {
		if containerStatus(container) == domain.ContainerStatusRunning {
			if err := s.runtime.StopContainer(ctx, container.ID); err != nil {
				return fmt.Errorf("stop stale standalone service %q container: %w", svc.Name, err)
			}
		}
		if cleanup.RemoveContainer {
			if err := s.runtime.RemoveContainer(ctx, container.ID, true); err != nil {
				return fmt.Errorf("remove stale standalone service %q container: %w", svc.Name, err)
			}
		}
	}
	return s.createAndStart(ctx, svc, hash, env)
}

func (s *Service) stopDisabled(ctx context.Context, name string, cleanup domain.StandaloneServiceCleanup, existing []*domain.Container) error {
	for _, container := range existing {
		if err := s.cleanupContainer(ctx, name, "disabled", cleanup, container); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) stopRemoved(ctx context.Context, name string, existing []*domain.Container) error {
	for _, container := range existing {
		cleanup := cleanupFromLabels(container.Labels)
		if err := s.cleanupContainer(ctx, name, "removed", cleanup, container); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) cleanupContainer(ctx context.Context, name, reason string, cleanup domain.StandaloneServiceCleanup, container *domain.Container) error {
	if containerStatus(container) == domain.ContainerStatusRunning {
		if err := s.runtime.StopContainer(ctx, container.ID); err != nil {
			return fmt.Errorf("stop %s standalone service %q container: %w", reason, name, err)
		}
	}
	if cleanup.RemoveContainer {
		if err := s.runtime.RemoveContainer(ctx, container.ID, true); err != nil {
			return fmt.Errorf("remove %s standalone service %q container: %w", reason, name, err)
		}
	}
	if err := s.removeManagedVolumes(ctx, name, cleanup, container); err != nil {
		return err
	}
	return nil
}

func (s *Service) removeManagedVolumes(ctx context.Context, name string, cleanup domain.StandaloneServiceCleanup, container *domain.Container) error {
	if cleanup.PreserveVolumes {
		return nil
	}
	managed := managedVolumeSet(container.Labels)
	for _, mount := range container.VolumeMounts {
		if mount.Type != "volume" || mount.Name == "" {
			continue
		}
		if _, ok := managed[mount.Name]; !ok {
			continue
		}
		if err := s.runtime.RemoveVolume(ctx, mount.Name, true); err != nil {
			return fmt.Errorf("remove standalone service %q volume %q: %w", name, mount.Name, err)
		}
	}
	return nil
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

func (s *Service) createAndStart(ctx context.Context, svc domain.StandaloneService, hash string, env []string) error {
	config, err := s.containerConfig(ctx, svc, hash, env)
	if err != nil {
		return err
	}
	container, err := s.runtime.CreateContainer(ctx, config)
	if err != nil {
		return fmt.Errorf("create standalone service %q container: %w", svc.Name, err)
	}
	if err := s.runtime.StartContainer(ctx, container.ID); err != nil {
		return fmt.Errorf("start standalone service %q container: %w", svc.Name, err)
	}
	if svc.Readiness.Type == domain.StandaloneServiceReadinessTCP {
		if err := s.resolveTCPReadinessPort(ctx, container.ID, &svc); err != nil {
			return err
		}
	}
	if err := s.waitReadiness(ctx, container.ID, svc); err != nil {
		return err
	}
	return nil
}

func (s *Service) resolveTCPReadinessPort(ctx context.Context, containerID string, svc *domain.StandaloneService) error {
	for i := range svc.Ports {
		port := &svc.Ports[i]
		if port.Protocol.NetworkProtocol() != domain.NetworkProtocolTCP {
			continue
		}
		hostPort, err := s.runtime.GetContainerPublishedPort(ctx, containerID, port.Container, domain.NetworkProtocolTCP)
		if err != nil {
			return fmt.Errorf("resolve service %q readiness port %q: %w", svc.Name, port.Name, err)
		}
		port.Publish = net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort))
		return nil
	}
	return fmt.Errorf("standalone service %q tcp readiness requires a declared TCP port", svc.Name)
}

func (s *Service) containerConfig(ctx context.Context, svc domain.StandaloneService, hash string, env []string) (*domain.ContainerConfig, error) {
	var imageVolumes []string
	if len(svc.Volumes) == 0 {
		var err error
		imageVolumes, err = s.runtime.InspectImageVolumes(ctx, svc.Image)
		if err != nil {
			return nil, fmt.Errorf("inspect standalone service %q image volumes: %w", svc.Name, err)
		}
	}
	mounts := ResolveVolumeMounts(s.volumePrefix, serviceWorkloadKey(svc.Name, svc.ContainerName), svc.Volumes, imageVolumes)
	volumes := make(map[string]string)
	readOnlyVolumes := make(map[string]string)
	managedVolumes := make([]string, 0)
	for _, mount := range mounts {
		if mount.Managed {
			managedVolumes = append(managedVolumes, mount.Source)
		}
		if mount.ReadOnly {
			readOnlyVolumes[mount.Target] = mount.Source
			continue
		}
		volumes[mount.Target] = mount.Source
	}
	publishes, err := portPublishes(svc)
	if err != nil {
		return nil, err
	}
	return &domain.ContainerConfig{
		Image:           svc.Image,
		Name:            serviceContainerName(svc.Name, svc.ContainerName),
		Env:             env,
		PortPublishes:   publishes,
		Labels:          serviceLabels(svc.Name, svc.ContainerName, hash, normalizeCleanup(svc.Cleanup), managedVolumes),
		AutoRemove:      false,
		RestartPolicy:   domain.RestartPolicyAlways,
		Volumes:         emptyToNil(volumes),
		ReadOnlyVolumes: emptyToNil(readOnlyVolumes),
		NetworkMode:     svc.NetworkName,
		Aliases:         componentAliases(svc.ContainerName),
	}, nil
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
		key := serviceWorkloadKey(name, container.Labels[domain.LabelServiceContainer])
		managed[key] = append(managed[key], container)
	}
	return managed
}

func serviceLabels(name, component, hash string, cleanup domain.StandaloneServiceCleanup, managedVolumes []string) map[string]string {
	sort.Strings(managedVolumes)
	return map[string]string{
		domain.LabelManaged:                       "true",
		domain.LabelService:                       "true",
		domain.LabelServiceName:                   name,
		domain.LabelServiceContainer:              component,
		domain.LabelServiceConfigHash:             hash,
		domain.LabelServiceManagedVolumes:         strings.Join(managedVolumes, ","),
		domain.LabelServiceCleanupPreserveVolumes: strconv.FormatBool(cleanup.PreserveVolumes),
		domain.LabelServiceCleanupRemoveContainer: strconv.FormatBool(cleanup.RemoveContainer),
	}
}

func portPublishes(svc domain.StandaloneService) ([]domain.ContainerPortPublish, error) {
	publishes := make([]domain.ContainerPortPublish, 0, len(svc.Ports))
	for _, port := range svc.Ports {
		publishes = append(publishes, domain.ContainerPortPublish{
			HostIP: "127.0.0.1", HostPort: 0, ContainerPort: port.Container, Protocol: port.Protocol.NetworkProtocol(),
		})
	}
	return publishes, nil
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

func (s *Service) waitReadiness(ctx context.Context, containerID string, svc domain.StandaloneService) error {
	readiness := svc.Readiness
	if readiness.Type == "" || readiness.Type == domain.StandaloneServiceReadinessNone {
		return nil
	}
	readyCtx := ctx
	cancel := func() {}
	if readiness.Timeout > 0 {
		readyCtx, cancel = context.WithTimeout(ctx, readiness.Timeout)
	}
	defer cancel()
	switch readiness.Type {
	case domain.StandaloneServiceReadinessTCP:
		return waitTCPReadiness(readyCtx, svc)
	case domain.StandaloneServiceReadinessLog:
		return s.waitLogReadiness(readyCtx, containerID, svc)
	default:
		return fmt.Errorf("unsupported standalone service %q readiness type %q", svc.Name, readiness.Type)
	}
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
		if port.Protocol.NetworkProtocol() != domain.NetworkProtocolTCP || port.Publish == "" {
			continue
		}
		host, hostPort, err := net.SplitHostPort(port.Publish)
		if err != nil {
			return "", fmt.Errorf("parse standalone service %q tcp readiness publish: %w", svc.Name, err)
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

func (s *Service) waitLogReadiness(ctx context.Context, containerID string, svc domain.StandaloneService) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return logReadinessTimeoutError(svc.Name, err, lastErr)
		}
		found, err := s.logContains(ctx, containerID, svc.Readiness.Path, svc.Readiness.Contains)
		if err == nil && found {
			return nil
		}
		if errors.Is(err, domain.ErrReadinessLogSizeExceeded) {
			return err
		}
		if err != nil {
			lastErr = err
		}
		// Continue polling until timeout; many services create the log after start.
		select {
		case <-ctx.Done():
			return logReadinessTimeoutError(svc.Name, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func logReadinessTimeoutError(serviceName string, timeoutErr, lastErr error) error {
	if lastErr != nil {
		return fmt.Errorf("standalone service %q log readiness timed out: %w; last read error: %w", serviceName, timeoutErr, lastErr)
	}
	return fmt.Errorf("standalone service %q log readiness timed out: %w", serviceName, timeoutErr)
}

const maxReadinessLogSize = 1 << 20 // 1 MiB

func (s *Service) logContains(ctx context.Context, containerID, path, contains string) (bool, error) {
	reader, err := s.runtime.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return false, err
	}
	defer reader.Close()
	if contains == "" {
		return true, nil
	}

	needle := []byte(contains)
	buffer := make([]byte, 32*1024)
	carry := make([]byte, 0, len(needle)-1)
	remaining := maxReadinessLogSize
	for remaining > 0 {
		readSize := min(len(buffer), remaining)
		n, readErr := reader.Read(buffer[:readSize])
		if n > 0 {
			window := append(carry, buffer[:n]...)
			if bytes.Contains(window, needle) {
				return true, nil
			}
			overlap := min(len(needle)-1, len(window))
			carry = append(carry[:0], window[len(window)-overlap:]...)
			remaining -= n
		}
		if errors.Is(readErr, io.EOF) {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
	}

	var extra [1]byte
	if _, err := reader.Read(extra[:]); errors.Is(err, io.EOF) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return false, fmt.Errorf("%w: limit is %d bytes", domain.ErrReadinessLogSizeExceeded, maxReadinessLogSize)
}

func normalizeServiceWorkloads(configured []domain.StandaloneService) ([]domain.StandaloneService, map[string]string) {
	workloads := make([]domain.StandaloneService, 0, len(configured))
	networks := make(map[string]string)
	for _, svc := range configured {
		if len(svc.Containers) == 0 {
			workloads = append(workloads, svc)
			continue
		}
		networkName := serviceNetworkName(svc.Name)
		if svc.Enabled {
			networks[svc.Name] = networkName
		}
		for _, component := range svc.Containers {
			workload := svc
			workload.Containers = nil
			workload.Image = component.Image
			workload.ContainerName = component.Name
			workload.NetworkName = networkName
			workload.Env = append(append([]string(nil), svc.Env...), component.Env...)
			if component.EnvFile != "" {
				workload.EnvFile = component.EnvFile
			}
			workload.Secrets = append(append([]domain.StandaloneServiceSecretRef(nil), svc.Secrets...), component.Secrets...)
			if component.Readiness.Type != "" {
				workload.Readiness = component.Readiness
			}
			workload.Volumes = append([]domain.StandaloneServiceVolume(nil), component.Volumes...)
			workload.Ports = portsForComponent(svc.Ports, component.Name)
			workloads = append(workloads, workload)
		}
	}
	return workloads, networks
}

func portsForComponent(ports []domain.StandaloneServicePort, component string) []domain.StandaloneServicePort {
	result := make([]domain.StandaloneServicePort, 0)
	for _, port := range ports {
		if port.ContainerName != component {
			continue
		}
		port.ContainerName = ""
		result = append(result, port)
	}
	return result
}

func (s *Service) ensureServiceNetworks(ctx context.Context, networks map[string]string) error {
	if len(networks) == 0 {
		return nil
	}
	existing, err := s.runtime.ListNetworks(ctx)
	if err != nil {
		return fmt.Errorf("list service networks: %w", err)
	}
	byName := make(map[string]struct{}, len(existing))
	for _, network := range existing {
		byName[network.Name] = struct{}{}
	}
	for serviceName, networkName := range networks {
		if _, ok := byName[networkName]; ok {
			continue
		}
		if err := s.runtime.CreateNetwork(ctx, networkName, domain.NetworkConfig{
			Driver: "bridge", Labels: map[string]string{domain.LabelManaged: "true", domain.LabelServiceName: serviceName},
		}); err != nil {
			return fmt.Errorf("create private network for service %q: %w", serviceName, err)
		}
	}
	return nil
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

func serviceContainerName(name string, components ...string) string {
	base := "gordon-service-" + sanitizeServiceRuntimeName(name)
	if len(components) == 0 || components[0] == "" {
		return base
	}
	return base + "-" + sanitizeServiceRuntimeName(components[0])
}

func serviceNetworkName(name string) string {
	return "gordon-service-" + sanitizeServiceRuntimeName(name)
}

func sanitizeServiceRuntimeName(name string) string {
	return strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(name)
}

func serviceWorkloadKey(name, component string) string {
	if component == "" {
		return name
	}
	return name + "/" + component
}

func componentAliases(component string) []string {
	if component == "" {
		return nil
	}
	return []string{component}
}

func emptyToNil(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return values
}
