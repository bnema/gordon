package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const runtimeStandaloneServiceCompletedResultLimit = 256

type localRuntimeStandaloneServiceManager struct {
	runtime      out.ContainerRuntime
	volumePrefix string
	now          func() time.Time

	mu             sync.Mutex
	completed      map[string]domain.RuntimeCommandResult
	completedOrder []string
	inFlight       map[string]*standaloneServiceInFlight
}

type standaloneServiceInFlight struct {
	done   chan struct{}
	result domain.RuntimeCommandResult
}

// NewLocalRuntimeStandaloneServiceManager creates the monolith implementation of the narrow standalone-service runtime port.
func NewLocalRuntimeStandaloneServiceManager(runtime out.ContainerRuntime) out.RuntimeStandaloneServiceManager {
	return newLocalRuntimeStandaloneServiceManager(runtime, defaultVolumePrefix)
}

func newLocalRuntimeStandaloneServiceManager(runtime out.ContainerRuntime, volumePrefix string) *localRuntimeStandaloneServiceManager {
	if volumePrefix == "" {
		volumePrefix = defaultVolumePrefix
	}
	return &localRuntimeStandaloneServiceManager{
		runtime:      runtime,
		volumePrefix: volumePrefix,
		now:          time.Now,
		completed:    make(map[string]domain.RuntimeCommandResult),
		inFlight:     make(map[string]*standaloneServiceInFlight),
	}
}

// ApplyStandaloneService realizes one enabled standalone service. Runtime failures are represented
// by a sanitized result so callers cannot transport resolved environment values or runtime details.
func (m *localRuntimeStandaloneServiceManager) ApplyStandaloneService(ctx context.Context, command domain.ApplyStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return m.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	return m.runOnce(ctx, command.RuntimeCommandIdentity, "apply_standalone_service", func() error {
		if m.runtime == nil {
			return errors.New("runtime standalone service manager not configured")
		}
		containers, err := m.runtime.ListContainers(ctx, true)
		if err != nil {
			return fmt.Errorf("list standalone service containers: %w", err)
		}
		existing := managedServiceContainers(containers)[command.Service.Name]
		return m.apply(ctx, command.Service, command.ConfigHash, command.ResolvedEnv, existing)
	})
}

// RemoveStandaloneService stops/removes the named managed service according to the command cleanup policy.
func (m *localRuntimeStandaloneServiceManager) RemoveStandaloneService(ctx context.Context, command domain.RemoveStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return m.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	return m.runOnce(ctx, command.RuntimeCommandIdentity, "remove_standalone_service", func() error {
		if m.runtime == nil {
			return errors.New("runtime standalone service manager not configured")
		}
		containers, err := m.runtime.ListContainers(ctx, true)
		if err != nil {
			return fmt.Errorf("list standalone service containers: %w", err)
		}
		cleanup := normalizeCleanup(command.Cleanup)
		for _, container := range managedServiceContainers(containers)[command.Name] {
			if err := m.cleanupContainer(ctx, command.Name, command.Reason, cleanup, container); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListStandaloneServiceState returns the stable, sanitized state that is safe to cross the runtime boundary.
func (m *localRuntimeStandaloneServiceManager) ListStandaloneServiceState(ctx context.Context) ([]domain.RuntimeStandaloneServiceState, error) {
	if m.runtime == nil {
		return nil, errors.New("runtime standalone service manager not configured")
	}
	containers, err := m.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil, errors.New("list standalone service state failed")
	}
	states := make([]domain.RuntimeStandaloneServiceState, 0)
	for _, container := range containers {
		if container == nil || container.Labels[domain.LabelService] != "true" {
			continue
		}
		name := container.Labels[domain.LabelServiceName]
		if name == "" {
			continue
		}
		states = append(states, domain.RuntimeStandaloneServiceState{
			Name:          name,
			ContainerID:   container.ID,
			ContainerName: container.Name,
			Status:        containerStatus(container),
			ConfigHash:    container.Labels[domain.LabelServiceConfigHash],
			Cleanup:       cleanupFromLabels(container.Labels),
		})
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].Name != states[j].Name {
			return states[i].Name < states[j].Name
		}
		if states[i].ContainerID != states[j].ContainerID {
			return states[i].ContainerID < states[j].ContainerID
		}
		return states[i].ContainerName < states[j].ContainerName
	})
	return states, nil
}

func (m *localRuntimeStandaloneServiceManager) apply(ctx context.Context, service domain.StandaloneService, hash string, env []string, existing []*domain.Container) error {
	cleanup := normalizeCleanup(service.Cleanup)
	if len(existing) == 0 {
		return m.createAndStart(ctx, service, hash, env)
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
		for _, container := range existing {
			if containerStatus(container) == domain.ContainerStatusRunning {
				if err := m.runtime.StopContainer(ctx, container.ID); err != nil {
					return fmt.Errorf("stop stale standalone service %q container: %w", service.Name, err)
				}
			}
			if cleanup.RemoveContainer {
				if err := m.runtime.RemoveContainer(ctx, container.ID, true); err != nil {
					return fmt.Errorf("remove stale standalone service %q container: %w", service.Name, err)
				}
			}
		}
		return m.createAndStart(ctx, service, hash, env)
	}
	for _, duplicate := range existing[1:] {
		if err := m.cleanupContainer(ctx, service.Name, "duplicate", cleanup, duplicate); err != nil {
			return err
		}
	}
	if containerStatus(current) != domain.ContainerStatusRunning {
		if err := m.runtime.StartContainer(ctx, current.ID); err != nil {
			return fmt.Errorf("start standalone service %q container: %w", service.Name, err)
		}
	}
	return m.waitReadiness(ctx, current.ID, service)
}

func (m *localRuntimeStandaloneServiceManager) createAndStart(ctx context.Context, service domain.StandaloneService, hash string, env []string) error {
	config, err := m.containerConfig(ctx, service, hash, env)
	if err != nil {
		return err
	}
	container, err := m.runtime.CreateContainer(ctx, config)
	if err != nil {
		return fmt.Errorf("create standalone service %q container: %w", service.Name, err)
	}
	if err := m.runtime.StartContainer(ctx, container.ID); err != nil {
		return fmt.Errorf("start standalone service %q container: %w", service.Name, err)
	}
	return m.waitReadiness(ctx, container.ID, service)
}

func (m *localRuntimeStandaloneServiceManager) containerConfig(ctx context.Context, service domain.StandaloneService, hash string, env []string) (*domain.ContainerConfig, error) {
	var imageVolumes []string
	if len(service.Volumes) == 0 {
		var err error
		imageVolumes, err = m.runtime.InspectImageVolumes(ctx, service.Image)
		if err != nil {
			return nil, fmt.Errorf("inspect standalone service %q image volumes: %w", service.Name, err)
		}
	}
	mounts := ResolveVolumeMounts(m.volumePrefix, service.Name, service.Volumes, imageVolumes)
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
	publishes, err := portPublishes(service)
	if err != nil {
		return nil, err
	}
	return &domain.ContainerConfig{
		Image:           service.Image,
		Name:            serviceContainerName(service.Name),
		Env:             append([]string(nil), env...),
		PortPublishes:   publishes,
		Labels:          serviceLabels(service.Name, hash, normalizeCleanup(service.Cleanup), managedVolumes),
		AutoRemove:      false,
		RestartPolicy:   domain.RestartPolicyAlways,
		Volumes:         emptyToNil(volumes),
		ReadOnlyVolumes: emptyToNil(readOnlyVolumes),
	}, nil
}

func (m *localRuntimeStandaloneServiceManager) cleanupContainer(ctx context.Context, name, reason string, cleanup domain.StandaloneServiceCleanup, container *domain.Container) error {
	if containerStatus(container) == domain.ContainerStatusRunning {
		if err := m.runtime.StopContainer(ctx, container.ID); err != nil {
			return fmt.Errorf("stop %s standalone service %q container: %w", reason, name, err)
		}
	}
	if cleanup.RemoveContainer {
		if err := m.runtime.RemoveContainer(ctx, container.ID, true); err != nil {
			return fmt.Errorf("remove %s standalone service %q container: %w", reason, name, err)
		}
	}
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
		if err := m.runtime.RemoveVolume(ctx, mount.Name, true); err != nil {
			return fmt.Errorf("remove standalone service %q volume %q: %w", name, mount.Name, err)
		}
	}
	return nil
}

func (m *localRuntimeStandaloneServiceManager) waitReadiness(ctx context.Context, containerID string, service domain.StandaloneService) error {
	readiness := service.Readiness
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
		return waitTCPReadiness(readyCtx, service)
	case domain.StandaloneServiceReadinessLog:
		return m.waitLogReadiness(readyCtx, containerID, service)
	default:
		return fmt.Errorf("unsupported standalone service %q readiness type %q", service.Name, readiness.Type)
	}
}

func (m *localRuntimeStandaloneServiceManager) waitLogReadiness(ctx context.Context, containerID string, service domain.StandaloneService) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return logReadinessTimeoutError(service.Name, err, lastErr)
		}
		reader, err := m.runtime.CopyFromContainer(ctx, containerID, service.Readiness.Path)
		if err == nil {
			content, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil {
				err = readErr
			} else if closeErr != nil {
				err = closeErr
			} else if strings.Contains(string(content), service.Readiness.Contains) {
				return nil
			}
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return logReadinessTimeoutError(service.Name, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func (m *localRuntimeStandaloneServiceManager) runOnce(ctx context.Context, identity domain.RuntimeCommandIdentity, kind string, operation func() error) (domain.RuntimeCommandResult, error) {
	key := identity.DedupeKey(kind)
	m.mu.Lock()
	if result, ok := m.completed[key]; ok {
		m.mu.Unlock()
		return result, nil
	}
	if inFlight, ok := m.inFlight[key]; ok {
		m.mu.Unlock()
		select {
		case <-inFlight.done:
			return inFlight.result, nil
		case <-ctx.Done():
			return m.failedResult(identity, ctx.Err()), nil
		}
	}
	inFlight := &standaloneServiceInFlight{done: make(chan struct{})}
	m.inFlight[key] = inFlight
	m.mu.Unlock()

	result := m.baseResult(identity)
	if err := operation(); err != nil {
		result.CompletedAt = m.now()
		result.Status = domain.RuntimeCommandStatusFailed
		result.Error = sanitizeStandaloneServiceRuntimeError(err)
	} else {
		result.CompletedAt = m.now()
		result.Status = domain.RuntimeCommandStatusSucceeded
	}

	m.mu.Lock()
	m.rememberCompletedLocked(key, result)
	delete(m.inFlight, key)
	inFlight.result = result
	close(inFlight.done)
	m.mu.Unlock()
	return result, nil
}

func (m *localRuntimeStandaloneServiceManager) baseResult(identity domain.RuntimeCommandIdentity) domain.RuntimeCommandResult {
	return domain.RuntimeCommandResult{CommandID: identity.ID, IdempotencyKey: identity.IdempotencyKey, Generation: identity.Generation, Status: domain.RuntimeCommandStatusRunning, StartedAt: m.now()}
}

func (m *localRuntimeStandaloneServiceManager) failedResult(identity domain.RuntimeCommandIdentity, err error) domain.RuntimeCommandResult {
	result := m.baseResult(identity)
	result.CompletedAt = result.StartedAt
	result.Status = domain.RuntimeCommandStatusFailed
	result.Error = sanitizeStandaloneServiceRuntimeError(err)
	return result
}

func (m *localRuntimeStandaloneServiceManager) rememberCompletedLocked(key string, result domain.RuntimeCommandResult) {
	if _, exists := m.completed[key]; !exists {
		m.completedOrder = append(m.completedOrder, key)
	}
	m.completed[key] = result
	for len(m.completedOrder) > runtimeStandaloneServiceCompletedResultLimit {
		oldest := m.completedOrder[0]
		copy(m.completedOrder, m.completedOrder[1:])
		m.completedOrder = m.completedOrder[:len(m.completedOrder)-1]
		delete(m.completed, oldest)
	}
}

func sanitizeStandaloneServiceRuntimeError(err error) *domain.RuntimeCommandError {
	code := "runtime_command_failed"
	message := "runtime command failed"
	retryable := false
	switch {
	case errors.Is(err, context.Canceled):
		code, message = "context_canceled", "context canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code, message, retryable = "context_deadline_exceeded", "context deadline exceeded", true
	case errors.Is(err, domain.ErrInvalidRuntimeCommand):
		code, message = "invalid_runtime_command", "invalid runtime command"
	}
	return &domain.RuntimeCommandError{Code: code, Message: message, Retryable: retryable}
}

var _ out.RuntimeStandaloneServiceManager = (*localRuntimeStandaloneServiceManager)(nil)
