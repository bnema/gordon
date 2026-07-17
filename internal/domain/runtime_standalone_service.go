package domain

import (
	"fmt"
	"strings"
)

// ApplyStandaloneServiceCommand asks the runtime to realize one enabled standalone service.
// Service contains runtime configuration only; ResolvedEnv is the sole field allowed to carry
// resolved environment values across the component boundary.
type ApplyStandaloneServiceCommand struct {
	RuntimeCommandIdentity
	Service     StandaloneService
	ResolvedEnv []string
	ConfigHash  string
}

// Validate checks the apply command identity, service, and sanitized runtime payload.
func (c ApplyStandaloneServiceCommand) Validate() error {
	if err := c.RuntimeCommandIdentity.Validate(); err != nil {
		return err
	}
	if !c.Service.Enabled {
		return fmt.Errorf("%w: standalone service must be enabled for apply", ErrInvalidRuntimeCommand)
	}
	if len(c.Service.Env) != 0 || strings.TrimSpace(c.Service.EnvFile) != "" || len(c.Service.Secrets) != 0 {
		return fmt.Errorf("%w: standalone service contains unresolved environment references", ErrInvalidRuntimeCommand)
	}
	if strings.TrimSpace(c.ConfigHash) == "" {
		return fmt.Errorf("%w: standalone service config hash is required", ErrInvalidRuntimeCommand)
	}
	if err := c.Service.Validate(); err != nil {
		return fmt.Errorf("%w: standalone service is invalid: %v", ErrInvalidRuntimeCommand, err)
	}
	return nil
}

// RemoveStandaloneServiceCommand asks the runtime to remove one standalone service.
type RemoveStandaloneServiceCommand struct {
	RuntimeCommandIdentity
	Name    string
	Reason  string
	Cleanup StandaloneServiceCleanup
}

// Validate checks the remove command identity and service name.
func (c RemoveStandaloneServiceCommand) Validate() error {
	if err := c.RuntimeCommandIdentity.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("%w: standalone service name is required", ErrInvalidRuntimeCommand)
	}
	return nil
}

// RuntimeStandaloneServiceState is sanitized standalone service state published by the runtime.
type RuntimeStandaloneServiceState struct {
	Name          string
	ContainerID   string
	ContainerName string
	Status        ContainerStatus
	ConfigHash    string
	Cleanup       StandaloneServiceCleanup
}

// ForRuntimeApply returns the runtime configuration without environment sources or secret references.
func (s StandaloneService) ForRuntimeApply() StandaloneService {
	runtimeService := s
	runtimeService.Env = nil
	runtimeService.EnvFile = ""
	runtimeService.Secrets = nil
	runtimeService.Ports = cloneStandaloneServicePorts(s.Ports)
	runtimeService.Volumes = append([]StandaloneServiceVolume(nil), s.Volumes...)
	return runtimeService
}

func cloneStandaloneServicePorts(ports []StandaloneServicePort) []StandaloneServicePort {
	cloned := append([]StandaloneServicePort(nil), ports...)
	for i := range cloned {
		cloned[i].TrustedCIDRs = append([]string(nil), ports[i].TrustedCIDRs...)
	}
	return cloned
}
