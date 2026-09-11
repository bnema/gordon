// Package appmanifest decodes standalone app TOML files into
// normalized domain.AppSpec values. It performs SHAPE decoding only
// (strict unknown fields, type mapping, defaults); all semantic rules
// live in domain.AppSpec.Validate. It never reads files, environment,
// or secrets beyond the manifest bytes given to it.
package appmanifest

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/bnema/gordon/internal/domain"
)

// rawManifest mirrors the frozen TOML schema for strict decoding.
type rawManifest struct {
	Name     string             `toml:"name"`
	Env      map[string]string  `toml:"env"`
	Services []rawService       `toml:"service"`
	Networks []rawSharedNetwork `toml:"network.shared"`
}

// rawService mirrors one [[service]] table.
type rawService struct {
	Name      string            `toml:"name"`
	Image     string            `toml:"image"`
	Command   []string          `toml:"command"`
	StopGrace string            `toml:"stop_grace"`
	Env       map[string]string `toml:"env"`
	Readiness rawReadiness      `toml:"readiness"`
	HTTP      []rawHTTP         `toml:"http"`
	TCP       []rawTCP          `toml:"tcp"`
	UDP       []rawUDP          `toml:"udp"`
	Secrets   map[string]string `toml:"secrets"`
	Volumes   []rawVolume       `toml:"volume"`
	Databases []rawDatabase     `toml:"database"`
	Backup    rawBackup         `toml:"backup"`
}

// rawReadiness mirrors [service.readiness].
type rawReadiness struct {
	Type     string `toml:"type"`
	Path     string `toml:"path"`
	Contains string `toml:"contains"`
	Port     int    `toml:"port"`
	Timeout  string `toml:"timeout"`
}

// rawHTTP mirrors [[service.http]].
type rawHTTP struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
	TLS  string `toml:"tls"`
}

// rawTCP mirrors [[service.tcp]].
type rawTCP struct {
	Entrypoint string `toml:"entrypoint"`
	Port       int    `toml:"port"`
	Publish    string `toml:"publish"`
}

// rawUDP mirrors [[service.udp]].
type rawUDP struct {
	Entrypoint string `toml:"entrypoint"`
	Port       int    `toml:"port"`
	Publish    string `toml:"publish"`
}

// rawVolume mirrors [[service.volume]].
type rawVolume struct {
	Name     string `toml:"name"`
	Path     string `toml:"path"`
	ReadOnly bool   `toml:"readonly"`
}

// rawDatabase mirrors [[service.database]].
type rawDatabase struct {
	Name     string `toml:"name"`
	Type     string `toml:"type"`
	Schedule string `toml:"schedule"`
}

// rawBackup mirrors [service.backup].
type rawBackup struct {
	Postgres []string `toml:"postgres"`
	Volume   []string `toml:"volume"`
}

// rawSharedNetwork mirrors [[network.shared]].
type rawSharedNetwork struct {
	Network  string   `toml:"network"`
	Services []string `toml:"services"`
	Aliases  []string `toml:"aliases"`
}

// Parse decodes manifest TOML bytes into a validated, normalized AppSpec.
// sourceName is used for mismatch warnings only; the name field is authoritative.
func Parse(data []byte, sourceName string) (domain.AppSpec, []string, error) {
	var raw rawManifest
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return domain.AppSpec{}, nil, fmt.Errorf("%w: %v", domain.ErrInvalidAppSpec, err)
	}
	spec, err := toDomain(raw)
	if err != nil {
		return domain.AppSpec{}, nil, err
	}
	var warnings []string
	if sourceName != "" && sourceName != spec.Name && sourceName != spec.Name+".toml" {
		warnings = append(warnings, fmt.Sprintf("file name %q does not match app name %q", sourceName, spec.Name))
	}
	if err := spec.Validate(); err != nil {
		return domain.AppSpec{}, warnings, err
	}
	return spec, warnings, nil
}

// toDomain maps raw TOML onto domain types with normalization and defaults.
func toDomain(raw rawManifest) (domain.AppSpec, error) {
	spec := domain.AppSpec{
		Name:     raw.Name,
		Env:      map[string]string{},
		Services: make([]domain.AppService, 0, len(raw.Services)),
		Networks: make([]domain.AppSharedNetwork, 0, len(raw.Networks)),
	}
	for key, value := range raw.Env {
		spec.Env[key] = value
	}
	for i := range raw.Services {
		svc, err := toDomainService(raw.Services[i])
		if err != nil {
			return domain.AppSpec{}, err
		}
		spec.Services = append(spec.Services, svc)
	}
	for _, net := range raw.Networks {
		spec.Networks = append(spec.Networks, domain.AppSharedNetwork{
			Network:  net.Network,
			Services: append([]string(nil), net.Services...),
			Aliases:  append([]string(nil), net.Aliases...),
		})
	}
	return spec, nil
}

// toDomainService maps one raw service with defaults.
func toDomainService(raw rawService) (domain.AppService, error) {
	svc := domain.AppService{
		Name:    raw.Name,
		Image:   normalizeImage(raw.Image),
		Command: append([]string(nil), raw.Command...),
		Secrets: map[string]string{},
		Backup: domain.AppBackup{
			Postgres: append([]string(nil), raw.Backup.Postgres...),
			Volume:   append([]string(nil), raw.Backup.Volume...),
		},
	}
	stopGrace := domain.AppDefaultStopGrace
	if raw.StopGrace != "" {
		parsed, err := time.ParseDuration(raw.StopGrace)
		if err != nil {
			return domain.AppService{}, fmt.Errorf("%w: service %q stop_grace %q is invalid: %v", domain.ErrInvalidAppSpec, raw.Name, raw.StopGrace, err)
		}
		stopGrace = parsed
	}
	svc.StopGrace = stopGrace
	readiness, err := toDomainReadiness(raw.Name, raw.Readiness)
	if err != nil {
		return domain.AppService{}, err
	}
	svc.Readiness = readiness
	for _, h := range raw.HTTP {
		tls := h.TLS
		if tls == "" {
			tls = domain.AppTLSAuto
		}
		svc.HTTP = append(svc.HTTP, domain.AppHTTPInterface{
			Host: domain.CanonicalHTTPHost(h.Host),
			Port: h.Port,
			TLS:  tls,
		})
	}
	for _, t := range raw.TCP {
		svc.TCP = append(svc.TCP, domain.AppTCPInterface{
			Entrypoint: t.Entrypoint,
			Port:       t.Port,
			Publish:    t.Publish,
		})
	}
	for _, u := range raw.UDP {
		svc.UDP = append(svc.UDP, domain.AppUDPInterface{
			Entrypoint: u.Entrypoint,
			Port:       u.Port,
			Publish:    u.Publish,
		})
	}
	for key, value := range raw.Secrets {
		svc.Secrets[key] = value
	}
	for _, v := range raw.Volumes {
		svc.Volumes = append(svc.Volumes, domain.AppVolume{
			Name:     v.Name,
			Path:     v.Path,
			ReadOnly: v.ReadOnly,
		})
	}
	for _, db := range raw.Databases {
		svc.Databases = append(svc.Databases, domain.AppDatabase{
			Name:     db.Name,
			Type:     db.Type,
			Schedule: db.Schedule,
		})
	}
	if len(raw.Env) > 0 {
		return domain.AppService{}, fmt.Errorf("%w: service %q [service.env] is not allowed, use secrets", domain.ErrInvalidAppSpec, raw.Name)
	}
	return svc, nil
}

// toDomainReadiness maps readiness with defaults.
func toDomainReadiness(service string, raw rawReadiness) (domain.AppReadiness, error) {
	readiness := domain.AppReadiness{
		Type:     raw.Type,
		Path:     raw.Path,
		Contains: raw.Contains,
		Port:     raw.Port,
		Timeout:  domain.AppDefaultReadinessTimeout,
	}
	if readiness.Type == "" {
		readiness.Type = domain.AppReadinessNone
	}
	if raw.Timeout != "" {
		parsed, err := time.ParseDuration(raw.Timeout)
		if err != nil {
			return domain.AppReadiness{}, fmt.Errorf("%w: service %q readiness timeout %q is invalid: %v", domain.ErrInvalidAppSpec, service, raw.Timeout, err)
		}
		readiness.Timeout = parsed
	}
	return readiness, nil
}

// normalizeImage records a missing tag as explicit :latest.
func normalizeImage(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return image
	}
	if strings.Contains(image, "@") {
		return image
	}
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon <= lastSlash {
		return image + ":latest"
	}
	return image
}
