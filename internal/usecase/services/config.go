package services

import (
	"fmt"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

type Config struct {
	Name      string            `mapstructure:"name"`
	Image     string            `mapstructure:"image"`
	Enabled   bool              `mapstructure:"enabled"`
	Env       []string          `mapstructure:"env"`
	EnvFile   string            `mapstructure:"env_file"`
	Secrets   []SecretRefConfig `mapstructure:"secrets"`
	Readiness ReadinessConfig   `mapstructure:"readiness"`
	Cleanup   CleanupConfig     `mapstructure:"cleanup"`
	Ports     []PortConfig      `mapstructure:"ports"`
	Volumes   []VolumeConfig    `mapstructure:"volumes"`
}

type PortConfig struct {
	Name         string                 `mapstructure:"name"`
	Container    int                    `mapstructure:"container"`
	Protocol     domain.NetworkProtocol `mapstructure:"protocol"`
	Publish      string                 `mapstructure:"publish"`
	Private      bool                   `mapstructure:"private"`
	TrustedCIDRs []string               `mapstructure:"trusted_cidrs"`
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

func ToDomain(configs []Config) ([]domain.StandaloneService, error) {
	services := make([]domain.StandaloneService, 0, len(configs))
	seenNames := make(map[string]struct{}, len(configs))
	for i, cfg := range configs {
		svc, err := cfg.ToDomain()
		if err != nil {
			return nil, fmt.Errorf("service config %d: %w", i, err)
		}
		name := strings.TrimSpace(svc.Name)
		if _, ok := seenNames[name]; ok {
			return nil, fmt.Errorf("service config %d: duplicate service name %q", i, name)
		}
		seenNames[name] = struct{}{}
		services = append(services, svc)
	}
	return services, nil
}

func (c Config) ToDomain() (domain.StandaloneService, error) {
	readiness, err := c.readinessToDomain()
	if err != nil {
		return domain.StandaloneService{}, err
	}
	svc := domain.StandaloneService{
		Name:      c.Name,
		Image:     c.Image,
		Enabled:   c.Enabled,
		Env:       append([]string(nil), c.Env...),
		EnvFile:   c.EnvFile,
		Secrets:   secretRefsToDomain(c.Secrets),
		Readiness: readiness,
		Cleanup:   c.Cleanup.toDomain(),
		Ports:     portsToDomain(c.Ports),
		Volumes:   volumesToDomain(c.Volumes),
	}
	if err := svc.Validate(); err != nil {
		return domain.StandaloneService{}, err
	}
	return svc, nil
}

func (c Config) readinessToDomain() (domain.StandaloneServiceReadiness, error) {
	readinessType := c.Readiness.Type
	if readinessType == "" {
		readinessType = domain.StandaloneServiceReadinessNone
	}
	readiness := domain.StandaloneServiceReadiness{Type: readinessType, Path: c.Readiness.Path, Contains: c.Readiness.Contains}
	if c.Readiness.Timeout != "" {
		timeout, err := time.ParseDuration(c.Readiness.Timeout)
		if err != nil {
			return domain.StandaloneServiceReadiness{}, fmt.Errorf("readiness timeout %q is invalid: %w", c.Readiness.Timeout, err)
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

func portsToDomain(configs []PortConfig) []domain.StandaloneServicePort {
	ports := make([]domain.StandaloneServicePort, 0, len(configs))
	for _, cfg := range configs {
		ports = append(ports, domain.StandaloneServicePort{
			Name:         cfg.Name,
			Container:    cfg.Container,
			Protocol:     cfg.Protocol,
			Publish:      cfg.Publish,
			Private:      cfg.Private,
			TrustedCIDRs: append([]string(nil), cfg.TrustedCIDRs...),
		})
	}
	return ports
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
