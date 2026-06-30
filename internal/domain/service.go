package domain

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	StandaloneServiceReadinessNone = "none"
	StandaloneServiceReadinessTCP  = "tcp"
	StandaloneServiceReadinessLog  = "log"
)

type StandaloneService struct {
	Name      string
	Image     string
	Enabled   bool
	Env       []string
	EnvFile   string
	Secrets   []StandaloneServiceSecretRef
	Readiness StandaloneServiceReadiness
	Cleanup   StandaloneServiceCleanup
	Ports     []StandaloneServicePort
	Volumes   []StandaloneServiceVolume
}

type StandaloneServiceStatus struct {
	Name          string
	ContainerID   string
	ContainerName string
	Status        ContainerStatus
	ConfigHash    string
}

type StandaloneServicePort struct {
	Name         string
	Container    int
	Protocol     NetworkProtocol
	Publish      string
	Private      bool
	Public       bool
	TrustedCIDRs []string
}

type StandaloneServiceVolume struct {
	Source   string
	Target   string
	ReadOnly bool
}

type StandaloneServiceSecretRef struct {
	Name string
	Key  string
}

type StandaloneServiceReadiness struct {
	Type       string
	Path       string
	Contains   string
	Timeout    time.Duration
	TimeoutSet bool
}

type StandaloneServiceCleanup struct {
	PreserveVolumes bool
	RemoveContainer bool
}

func (s StandaloneService) WithDefaults() StandaloneService {
	if s.Readiness.Type == "" {
		s.Readiness.Type = StandaloneServiceReadinessNone
	}
	s.Cleanup = s.Cleanup.WithDefaults()
	return s
}

func (c StandaloneServiceCleanup) WithDefaults() StandaloneServiceCleanup {
	if !c.PreserveVolumes && !c.RemoveContainer {
		return StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true}
	}
	return c
}

func (s StandaloneService) Validate() error {
	s = s.WithDefaults()
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("standalone service name is required")
	}
	if s.Enabled && strings.TrimSpace(s.Image) == "" {
		return fmt.Errorf("standalone service %q image is required when enabled", s.Name)
	}
	if s.EnvFile != "" && strings.TrimSpace(s.EnvFile) == "" {
		return fmt.Errorf("standalone service %q env_file must be non-empty when set", s.Name)
	}
	if err := validateStandaloneServicePorts(s); err != nil {
		return err
	}
	if err := validateStandaloneServiceVolumes(s); err != nil {
		return err
	}
	if err := validateStandaloneServiceSecrets(s); err != nil {
		return err
	}
	if err := validateStandaloneServiceReadiness(s); err != nil {
		return err
	}
	return nil
}

func validateStandaloneServicePorts(s StandaloneService) error {
	seen := make(map[string]struct{}, len(s.Ports))
	for i, port := range s.Ports {
		name := strings.TrimSpace(port.Name)
		if name == "" {
			return fmt.Errorf("standalone service %q port %d name is required", s.Name, i)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("standalone service %q duplicate port name %q", s.Name, name)
		}
		seen[name] = struct{}{}
		if port.Container <= 0 || port.Container > 65535 {
			return fmt.Errorf("standalone service %q port %q container port must be 1-65535", s.Name, name)
		}
		if port.Protocol != NetworkProtocolTCP && port.Protocol != NetworkProtocolUDP {
			return fmt.Errorf("standalone service %q port %q protocol must be tcp or udp", s.Name, name)
		}
		if port.Private && port.Public {
			return fmt.Errorf("standalone service %q port %q cannot be both private and public", s.Name, name)
		}
		if err := validateStandaloneServicePublish(s.Name, name, port.Publish); err != nil {
			return err
		}
		for _, cidr := range port.TrustedCIDRs {
			if strings.TrimSpace(cidr) == "" {
				return fmt.Errorf("standalone service %q port %q trusted_cidrs must not contain empty values", s.Name, name)
			}
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("standalone service %q port %q trusted_cidr %q is invalid: %w", s.Name, name, cidr, err)
			}
		}
	}
	return nil
}

func validateStandaloneServicePublish(serviceName, portName, publish string) error {
	if publish == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(publish)
	if err != nil {
		return fmt.Errorf("standalone service %q port %q publish address %q is invalid: %w", serviceName, portName, publish, err)
	}
	if strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return fmt.Errorf("standalone service %q port %q publish address %q must include host and port", serviceName, portName, publish)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("standalone service %q port %q publish address %q must include a valid port", serviceName, portName, publish)
	}
	return nil
}

func validateStandaloneServiceVolumes(s StandaloneService) error {
	seenTargets := make(map[string]struct{}, len(s.Volumes))
	for i, volume := range s.Volumes {
		target := strings.TrimSpace(volume.Target)
		if target == "" {
			return fmt.Errorf("standalone service %q volume %d target is required", s.Name, i)
		}
		if !filepath.IsAbs(target) {
			return fmt.Errorf("standalone service %q volume %d target %q must be an absolute container path", s.Name, i, volume.Target)
		}
		if _, ok := seenTargets[target]; ok {
			return fmt.Errorf("standalone service %q duplicate volume target %q", s.Name, target)
		}
		seenTargets[target] = struct{}{}
	}
	return nil
}

func validateStandaloneServiceSecrets(s StandaloneService) error {
	for i, secret := range s.Secrets {
		if strings.TrimSpace(secret.Name) == "" {
			return fmt.Errorf("standalone service %q secret %d name is required", s.Name, i)
		}
		if strings.TrimSpace(secret.Key) == "" {
			return fmt.Errorf("standalone service %q secret %d key is required", s.Name, i)
		}
	}
	return nil
}

func validateStandaloneServiceReadiness(s StandaloneService) error {
	readinessType := s.Readiness.Type
	if readinessType == "" {
		readinessType = StandaloneServiceReadinessNone
	}
	if s.Readiness.TimeoutSet && s.Readiness.Timeout <= 0 {
		return fmt.Errorf("standalone service %q readiness timeout must be positive when set", s.Name)
	}
	switch readinessType {
	case StandaloneServiceReadinessNone, StandaloneServiceReadinessTCP:
		return nil
	case StandaloneServiceReadinessLog:
		if strings.TrimSpace(s.Readiness.Path) == "" {
			return fmt.Errorf("standalone service %q log readiness path is required", s.Name)
		}
		if strings.TrimSpace(s.Readiness.Contains) == "" {
			return fmt.Errorf("standalone service %q log readiness contains is required", s.Name)
		}
		return nil
	default:
		return fmt.Errorf("standalone service %q readiness type must be none, tcp, or log", s.Name)
	}
}
