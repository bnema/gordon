package services

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

type Config struct {
	Image      string                     `mapstructure:"image"`
	Containers map[string]ContainerConfig `mapstructure:"containers"`
	Enabled    *bool                      `mapstructure:"enabled"`
	Env        []string                   `mapstructure:"env"`
	EnvFile    string                     `mapstructure:"env_file"`
	Secrets    []SecretRefConfig          `mapstructure:"secrets"`
	Readiness  ReadinessConfig            `mapstructure:"readiness"`
	Cleanup    CleanupConfig              `mapstructure:"cleanup"`
	Ports      map[string]PortConfig      `mapstructure:"ports"`
	Volumes    []VolumeConfig             `mapstructure:"volumes"`
}

type ContainerConfig struct {
	Image     string            `mapstructure:"image"`
	Env       []string          `mapstructure:"env"`
	EnvFile   string            `mapstructure:"env_file"`
	Secrets   []SecretRefConfig `mapstructure:"secrets"`
	Readiness ReadinessConfig   `mapstructure:"readiness"`
	Volumes   []VolumeConfig    `mapstructure:"volumes"`
}

type PortConfig struct {
	ContainerName string                     `mapstructure:"container"`
	Container     int                        `mapstructure:"port"`
	Protocol      domain.ServicePortProtocol `mapstructure:"protocol"`
	TLS           bool                       `mapstructure:"tls"`
	Publish       string                     `mapstructure:"publish"`
	Private       bool                       `mapstructure:"private"`
	Public        bool                       `mapstructure:"public"`
	TrustedCIDRs  []string                   `mapstructure:"trusted_cidrs"`
}

type VolumeConfig struct {
	Source   string `mapstructure:"source"`
	Target   string `mapstructure:"target"`
	ReadOnly bool   `mapstructure:"read_only"`
}

type ResolvedVolumeMount struct {
	Source   string
	Target   string
	ReadOnly bool
	Managed  bool
}

type SecretRefConfig struct {
	Name string `mapstructure:"name"`
	Key  string `mapstructure:"key"`
}

type ReadinessConfig struct {
	Type     string `mapstructure:"type"`
	Path     string `mapstructure:"path"`
	Contains string `mapstructure:"contains"`
	Timeout  string `mapstructure:"timeout"`
}

type CleanupConfig struct {
	PreserveVolumes *bool `mapstructure:"preserve_volumes"`
	RemoveContainer *bool `mapstructure:"remove_container"`
}

func ToDomain(configs map[string]Config) ([]domain.StandaloneService, error) {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)

	services := make([]domain.StandaloneService, 0, len(configs))
	seenRuntimeNames := make(map[string]string, len(configs))
	for _, name := range names {
		svc, err := configs[name].ToDomain(name)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		runtimeName := serviceRuntimeIdentifier(name)
		if previous, ok := seenRuntimeNames[runtimeName]; ok {
			return nil, fmt.Errorf("service %q normalizes to same runtime identifier as %q", name, previous)
		}
		seenRuntimeNames[runtimeName] = name
		services = append(services, svc)
	}
	return services, nil
}

func (c Config) ToDomain(name string) (domain.StandaloneService, error) {
	for portName, port := range c.Ports {
		if port.Publish != "" {
			return domain.StandaloneService{}, fmt.Errorf("port %q uses removed publish setting; Gordon assigns routed backend ports automatically", portName)
		}
	}
	if err := c.Cleanup.validate(); err != nil {
		return domain.StandaloneService{}, err
	}
	readiness, err := c.readinessToDomain()
	if err != nil {
		return domain.StandaloneService{}, err
	}
	containers, err := containersToDomain(c.Containers)
	if err != nil {
		return domain.StandaloneService{}, err
	}
	svc := domain.StandaloneService{
		Name:       name,
		Image:      c.Image,
		Containers: containers,
		Enabled:    c.enabled(),
		Env:        append([]string(nil), c.Env...),
		EnvFile:    c.EnvFile,
		Secrets:    secretRefsToDomain(c.Secrets),
		Readiness:  readiness,
		Cleanup:    c.Cleanup.toDomain(),
		Ports:      portsToDomain(c.Ports),
		Volumes:    volumesToDomain(c.Volumes),
	}
	if err := svc.Validate(); err != nil {
		return domain.StandaloneService{}, err
	}
	return svc, nil
}

func (c Config) enabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c Config) readinessToDomain() (domain.StandaloneServiceReadiness, error) {
	return readinessConfigToDomain(c.Readiness)
}

func readinessConfigToDomain(config ReadinessConfig) (domain.StandaloneServiceReadiness, error) {
	readinessType := config.Type
	if readinessType == "" {
		readinessType = domain.StandaloneServiceReadinessNone
	}
	readiness := domain.StandaloneServiceReadiness{Type: readinessType, Path: config.Path, Contains: config.Contains}
	if config.Timeout != "" {
		timeout, err := time.ParseDuration(config.Timeout)
		if err != nil {
			return domain.StandaloneServiceReadiness{}, fmt.Errorf("readiness timeout %q is invalid: %w", config.Timeout, err)
		}
		if timeout <= 0 {
			return domain.StandaloneServiceReadiness{}, fmt.Errorf("readiness timeout must be positive when set")
		}
		readiness.Timeout = timeout
		readiness.TimeoutSet = true
	}
	return readiness, nil
}

func (c CleanupConfig) toDomain() domain.StandaloneServiceCleanup {
	cleanup := domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true}
	if c.PreserveVolumes != nil {
		cleanup.PreserveVolumes = *c.PreserveVolumes
	}
	if c.RemoveContainer != nil {
		cleanup.RemoveContainer = *c.RemoveContainer
	}
	return cleanup
}

func (c CleanupConfig) validate() error {
	if c.PreserveVolumes == nil || c.RemoveContainer == nil {
		return nil
	}
	if !*c.PreserveVolumes && !*c.RemoveContainer {
		return fmt.Errorf("cleanup cannot set preserve_volumes=false with remove_container=false")
	}
	return nil
}

func portsToDomain(configs map[string]PortConfig) []domain.StandaloneServicePort {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)

	ports := make([]domain.StandaloneServicePort, 0, len(configs))
	for _, name := range names {
		cfg := configs[name]
		protocol := cfg.Protocol
		if protocol == "" {
			protocol = domain.ServicePortProtocolTCP
		}
		ports = append(ports, domain.StandaloneServicePort{
			Name:          name,
			ContainerName: cfg.ContainerName,
			Container:     cfg.Container,
			Protocol:      protocol,
			TLS:           cfg.TLS,
			Publish:       cfg.Publish,
			Private:       cfg.Private,
			Public:        cfg.Public,
			TrustedCIDRs:  append([]string(nil), cfg.TrustedCIDRs...),
		})
	}
	return ports
}

func containersToDomain(configs map[string]ContainerConfig) ([]domain.StandaloneServiceContainer, error) {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)

	containers := make([]domain.StandaloneServiceContainer, 0, len(configs))
	for _, name := range names {
		cfg := configs[name]
		readiness, err := readinessConfigToDomain(cfg.Readiness)
		if err != nil {
			return nil, fmt.Errorf("container %q: %w", name, err)
		}
		containers = append(containers, domain.StandaloneServiceContainer{
			Name: name, Image: cfg.Image, Env: append([]string(nil), cfg.Env...), EnvFile: cfg.EnvFile,
			Secrets: secretRefsToDomain(cfg.Secrets), Readiness: readiness, Volumes: volumesToDomain(cfg.Volumes),
		})
	}
	return containers, nil
}

func volumesToDomain(configs []VolumeConfig) []domain.StandaloneServiceVolume {
	volumes := make([]domain.StandaloneServiceVolume, 0, len(configs))
	for _, cfg := range configs {
		volumes = append(volumes, domain.StandaloneServiceVolume{Source: cfg.Source, Target: cfg.Target, ReadOnly: cfg.ReadOnly})
	}
	return volumes
}

func secretRefsToDomain(configs []SecretRefConfig) []domain.StandaloneServiceSecretRef {
	secrets := make([]domain.StandaloneServiceSecretRef, 0, len(configs))
	for _, cfg := range configs {
		secrets = append(secrets, domain.StandaloneServiceSecretRef{Name: cfg.Name, Key: cfg.Key})
	}
	return secrets
}

func ResolveVolumeMounts(prefix, serviceName string, explicitVolumes []domain.StandaloneServiceVolume, imageVolumePaths []string) []ResolvedVolumeMount {
	if len(explicitVolumes) > 0 {
		mounts := make([]ResolvedVolumeMount, 0, len(explicitVolumes))
		for _, volume := range explicitVolumes {
			mounts = append(mounts, ResolvedVolumeMount{Source: volume.Source, Target: volume.Target, ReadOnly: volume.ReadOnly})
		}
		return mounts
	}

	mounts := make([]ResolvedVolumeMount, 0, len(imageVolumePaths))
	for _, path := range imageVolumePaths {
		mounts = append(mounts, ResolvedVolumeMount{Source: ManagedServiceVolumeName(prefix, serviceName, path), Target: path, Managed: true})
	}
	return mounts
}

func ManagedServiceVolumeName(prefix, serviceName, volumePath string) string {
	return fmt.Sprintf("%s-%s-%s",
		prefix,
		strings.ReplaceAll(serviceName, ".", "-"),
		strings.ReplaceAll(strings.Trim(volumePath, "/"), "/", "-"))
}

func serviceRuntimeIdentifier(name string) string {
	return strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(name)
}
