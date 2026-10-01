// Package appmanifest decodes standalone app TOML files into
// normalized domain.AppSpec values. It performs SHAPE decoding only
// (strict unknown fields, type mapping, defaults); all semantic rules
// live in domain.AppSpec.Validate. It never reads files, environment,
// or secrets beyond the manifest bytes given to it.
package appmanifest

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/bnema/gordon/internal/domain"
)

// rawManifest mirrors the frozen TOML schema for strict decoding.
// Services is keyed by service name: [services.<name>].
type rawManifest struct {
	Name      string                `toml:"name"`
	Env       map[string]string     `toml:"env"`
	Services  map[string]rawService `toml:"services"`
	Network   rawNetwork            `toml:"network"`
	Telemetry rawTelemetry          `toml:"telemetry"`
}

// rawTelemetry mirrors [telemetry] and [services.<name>.telemetry].
// A nil Logs means "inherit": service inherits the app, the app
// inherits the default (export on).
type rawTelemetry struct {
	Logs *bool `toml:"logs"`
}

// rawNetwork mirrors [network] and its [[network.shared]] children.
type rawNetwork struct {
	Shared []rawSharedNetwork `toml:"shared"`
}

// rawService mirrors one [services.<name>] table. The service name is
// the table key, so there is no name field inside the table.
type rawService struct {
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
	Binds     []rawBind         `toml:"bind"`
	// Devices lists logical device names from `devices = [...]`.
	Devices   []string      `toml:"devices"`
	Databases []rawDatabase `toml:"database"`
	Backup    rawBackup     `toml:"backup"`
	Telemetry rawTelemetry  `toml:"telemetry"`
}

// rawReadiness mirrors [services.<name>.readiness].
type rawReadiness struct {
	Type     string `toml:"type"`
	Path     string `toml:"path"`
	Contains string `toml:"contains"`
	Port     int    `toml:"port"`
	Timeout  string `toml:"timeout"`
}

// rawHTTP mirrors [[services.<name>.http]].
type rawHTTP struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
	// TLS uses presence tracking because internal interfaces reject every
	// explicit value, including an empty string.
	TLS *string `toml:"tls"`
	// Visibility is empty when unset; the parser normalizes it to public.
	Visibility string `toml:"visibility"`
}

// rawTCP mirrors [[services.<name>.tcp]].
type rawTCP struct {
	Entrypoint string `toml:"entrypoint"`
	Port       int    `toml:"port"`
	Publish    string `toml:"publish"`
}

// rawUDP mirrors [[services.<name>.udp]].
type rawUDP struct {
	Entrypoint string `toml:"entrypoint"`
	Port       int    `toml:"port"`
	Publish    string `toml:"publish"`
}

// rawVolume mirrors [[services.<name>.volume]].
type rawVolume struct {
	Name     string `toml:"name"`
	Path     string `toml:"path"`
	ReadOnly bool   `toml:"readonly"`
}

// rawBind mirrors [[services.<name>.bind]].
type rawBind struct {
	Name     string `toml:"name"`
	Path     string `toml:"path"`
	ReadOnly bool   `toml:"readonly"`
}

// rawDatabase mirrors [[services.<name>.database]].
type rawDatabase struct {
	Name     string `toml:"name"`
	Type     string `toml:"type"`
	Schedule string `toml:"schedule"`
}

// rawBackup mirrors [services.<name>.backup].
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
		if hint := legacyKeyedSchemaHint(data); hint != "" {
			return domain.AppSpec{}, nil, fmt.Errorf("%w: %s", domain.ErrInvalidAppSpec, hint)
		}
		return domain.AppSpec{}, nil, fmt.Errorf("%w: %s", domain.ErrInvalidAppSpec, formatDecodeError(err))
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

func formatDecodeError(err error) string {
	var strictErr *toml.StrictMissingError
	if !errors.As(err, &strictErr) {
		return err.Error()
	}
	unknown := make([]string, 0, len(strictErr.Errors))
	for i := range strictErr.Errors {
		key := strictErr.Errors[i].Key()
		if len(key) > 0 {
			unknown = append(unknown, strings.Join(key, "."))
		}
	}
	if len(unknown) == 0 {
		return strictErr.Error()
	}
	return "unknown TOML fields or tables: " + strings.Join(unknown, ", ")
}

// legacyKeyedSchemaHint recognizes the retired array-of-tables app shapes
// ([[service]] and [[services]]) and returns a stable, actionable
// diagnostic. It inspects the loosely decoded document rather than
// matching go-toml error wording, which is not a stable contract.
func legacyKeyedSchemaHint(data []byte) string {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	const fix = "array-of-tables is a retired app shape; use one [services.<name>] table per service (keyed schema), e.g. [services.web]"
	switch {
	case isArrayTable(doc["service"]):
		return "[[service]] " + fix
	case isArrayTable(doc["services"]):
		return "[[services]] " + fix
	default:
		return ""
	}
}

// isArrayTable reports whether a loosely decoded value is the [[name]]
// TOML shape: a non-empty array whose elements are all tables. Scalar
// values, string arrays, and keyed tables are not array tables, so they
// keep the plain unknown-field diagnostic.
func isArrayTable(v any) bool {
	tables, ok := v.([]any)
	if !ok || len(tables) == 0 {
		return false
	}
	for _, table := range tables {
		if _, ok := table.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// toDomain maps raw TOML onto domain types with normalization and defaults.
func toDomain(raw rawManifest) (domain.AppSpec, error) {
	spec := domain.AppSpec{
		Name:     raw.Name,
		Env:      map[string]string{},
		Services: make([]domain.AppService, 0, len(raw.Services)),
		Networks: make([]domain.AppSharedNetwork, 0, len(raw.Network.Shared)),
	}
	for key, value := range raw.Env {
		spec.Env[key] = value
	}
	// Iterate the service map in sorted key order so that domain
	// conversion and every downstream projection are deterministic
	// regardless of TOML declaration order.
	for _, name := range slices.Sorted(maps.Keys(raw.Services)) {
		svc, err := toDomainService(name, raw.Services[name])
		if err != nil {
			return domain.AppSpec{}, err
		}
		svc.LogExportDisabled = !resolveLogExport(raw.Telemetry.Logs, raw.Services[name].Telemetry.Logs)
		spec.Services = append(spec.Services, svc)
	}
	for _, net := range raw.Network.Shared {
		spec.Networks = append(spec.Networks, domain.AppSharedNetwork{
			Network:  net.Network,
			Services: append([]string(nil), net.Services...),
			Aliases:  append([]string(nil), net.Aliases...),
		})
	}
	return spec, nil
}

// resolveLogExport applies the precedence service > app > default (on).
func resolveLogExport(app, service *bool) bool {
	if service != nil {
		return *service
	}
	if app != nil {
		return *app
	}
	return true
}

func toDomainHTTP(service string, h rawHTTP) (domain.AppHTTPInterface, error) {
	visibility := h.Visibility
	if visibility == "" {
		visibility = domain.AppVisibilityPublic
	}
	if visibility == domain.AppVisibilityInternal && h.TLS != nil {
		return domain.AppHTTPInterface{}, fmt.Errorf("%w: service %q internal http port %d must not declare tls", domain.ErrInvalidAppSpec, service, h.Port)
	}
	tls := ""
	if h.TLS != nil {
		tls = *h.TLS
	} else if visibility == domain.AppVisibilityPublic {
		tls = domain.AppTLSAuto
	}
	return domain.AppHTTPInterface{
		Host: domain.CanonicalHTTPHost(h.Host), Port: h.Port, TLS: tls, Visibility: visibility,
	}, nil
}

// toDomainService maps one raw service with defaults. serviceName is the
// [services.<name>] table key and becomes the service identity.
func toDomainService(serviceName string, raw rawService) (domain.AppService, error) {
	svc := domain.AppService{
		Name:    serviceName,
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
			return domain.AppService{}, fmt.Errorf("%w: service %q stop_grace %q is invalid: %v", domain.ErrInvalidAppSpec, serviceName, raw.StopGrace, err)
		}
		stopGrace = parsed
	}
	svc.StopGrace = stopGrace
	readiness, err := toDomainReadiness(serviceName, raw.Readiness)
	if err != nil {
		return domain.AppService{}, err
	}
	svc.Readiness = readiness
	for _, h := range raw.HTTP {
		iface, err := toDomainHTTP(serviceName, h)
		if err != nil {
			return domain.AppService{}, err
		}
		svc.HTTP = append(svc.HTTP, iface)
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
	for _, b := range raw.Binds {
		svc.Binds = append(svc.Binds, domain.AppBind{
			Name:     b.Name,
			Path:     b.Path,
			ReadOnly: b.ReadOnly,
		})
	}
	// Preserve manifest order; domain equalDevices treats the slice as a
	// set, so reorder-only input is a no-op in diffs.
	svc.Devices = append([]string(nil), raw.Devices...)
	for _, db := range raw.Databases {
		svc.Databases = append(svc.Databases, domain.AppDatabase{
			Name:     db.Name,
			Type:     db.Type,
			Schedule: db.Schedule,
		})
	}
	if len(raw.Env) > 0 {
		return domain.AppService{}, fmt.Errorf("%w: service %q [services.%s.env] is not allowed, use secrets", domain.ErrInvalidAppSpec, serviceName, tomlKey(serviceName))
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

// tomlKey renders a service name as a TOML key, quoting it when it is
// not a bare key (service names may contain dots, e.g. web.api).
func tomlKey(name string) string {
	if isBareTOMLKey(name) {
		return name
	}
	return strconv.Quote(name)
}

// isBareTOMLKey reports whether name is a TOML bare key (A-Za-z0-9_-).
func isBareTOMLKey(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
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
