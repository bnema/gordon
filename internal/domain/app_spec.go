package domain

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// App manifest validation. Frozen by docs/plans/v2.50.0/01-manifest.md.
// The parser adapter (internal/adapters/in/appmanifest) decodes TOML
// into these types; all semantic rules live here, not in the parser.

var (
	appNamePattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	serviceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9_.\-]{0,61}[a-z0-9])?$`)
	dnsLabelPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Reserved app names that collide with installation identity.
var reservedAppNames = map[string]struct{}{
	"gordon":    {},
	"registry":  {},
	"admin":     {},
	"localhost": {},
}

// App readiness types. HTTP is new in v2.50.
const (
	AppReadinessNone = "none"
	AppReadinessTCP  = "tcp"
	AppReadinessHTTP = "http"
	AppReadinessLog  = "log"
)

// App TLS modes for HTTP interfaces.
const (
	AppTLSAuto   = "auto"
	AppTLSAlways = "always"
	AppTLSNever  = "never"
)

// App database engines. Only postgres in v2.50.
const (
	AppDBPostgres = "postgres"
)

// Backup schedules reuse the BackupSchedule vocabulary.
var appBackupSchedules = map[string]struct{}{
	"hourly":  {},
	"daily":   {},
	"weekly":  {},
	"monthly": {},
}

// App size/time bounds frozen by the contract.
const (
	// MaxAppEnvValueLen caps a single [env] value.
	MaxAppEnvValueLen = 64 * 1024
	// AppDefaultReadinessTimeout applies when timeout is unset.
	AppDefaultReadinessTimeout = 30 * time.Second
	// AppMinReadinessTimeout bounds readiness timeout below.
	AppMinReadinessTimeout = time.Second
	// AppMaxReadinessTimeout bounds readiness timeout above.
	AppMaxReadinessTimeout = 10 * time.Minute
	// AppDefaultStopGrace applies when stop_grace is unset.
	AppDefaultStopGrace = 10 * time.Second
	// AppMaxStopGrace caps stop_grace.
	AppMaxStopGrace = 5 * time.Minute
)

// AppSpec is the normalized form of one app manifest file.
type AppSpec struct {
	Name     string
	Env      map[string]string
	Services []AppService
	Networks []AppSharedNetwork
}

// AppService is one explicitly named image-backed service.
type AppService struct {
	Name      string
	Image     string
	Command   []string
	Replicas  int
	StopGrace time.Duration
	Readiness AppReadiness
	HTTP      []AppHTTPInterface
	TCP       []AppTCPInterface
	UDP       []AppUDPInterface
	RCON      []AppRCONInterface
	Secrets   map[string]string
	Volumes   []AppVolume
	Databases []AppDatabase
	Backup    AppBackup
}

// AppReadiness is the explicit readiness check for a service.
type AppReadiness struct {
	Type     string
	Path     string
	Contains string
	Port     int
	Timeout  time.Duration
}

// AppHTTPInterface is one HTTP service interface.
type AppHTTPInterface struct {
	Host string
	Port int
	TLS  string
}

// AppTCPInterface is one TCP service interface.
type AppTCPInterface struct {
	Entrypoint string
	Port       int
	Publish    string
}

// AppUDPInterface is one UDP service interface.
type AppUDPInterface struct {
	Entrypoint string
	Port       int
	Publish    string
}

// AppRCONInterface is one RCON service interface (TCP transport + policy).
type AppRCONInterface struct {
	Entrypoint   string
	Port         int
	Publish      string
	Public       bool
	TrustedCIDRs []string
}

// AppVolume is one named volume mount.
type AppVolume struct {
	Name     string
	Path     string
	ReadOnly bool
}

// AppDatabase is one explicit database declaration.
type AppDatabase struct {
	Name     string
	Type     string
	Schedule string
}

// AppBackup declares backup targets by reference.
type AppBackup struct {
	Postgres []string
	Volume   []string
}

// AppSharedNetwork is one shared-network membership declaration.
type AppSharedNetwork struct {
	Network  string
	Services []string
	Aliases  []string
}

// ValidateAppName checks the app identity rules.
func ValidateAppName(name string) error {
	if !appNamePattern.MatchString(name) {
		return fmt.Errorf("%w: app name %q must be a DNS label (lowercase alphanumerics and hyphens, max 63)", ErrInvalidAppSpec, name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("%w: app name %q must not contain -- (reserved separator)", ErrInvalidAppSpec, name)
	}
	if _, reserved := reservedAppNames[strings.ToLower(name)]; reserved {
		return fmt.Errorf("%w: app name %q is reserved", ErrInvalidAppSpec, name)
	}
	return nil
}

// ValidateServiceName checks the service identity rules.
func ValidateServiceName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("%w: service name %q must match [a-z0-9_.-], max 63", ErrInvalidAppSpec, name)
	}
	return nil
}

// ValidateVolumeName checks volume names (service charset plus -- ban).
func ValidateVolumeName(name string) error {
	if err := ValidateServiceName(name); err != nil {
		return err
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("%w: volume name %q must not contain -- (reserved separator)", ErrInvalidAppSpec, name)
	}
	return nil
}

// ValidateSecretName checks service-local secret names.
func ValidateSecretName(name string) error {
	return ValidateServiceName(name)
}

// NormalizeServiceName applies the runtime-identifier normalization.
func NormalizeServiceName(name string) string {
	return strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(name)
}

// LogicalServiceIdentity returns the stable gordon-<app>--<service> identity.
func LogicalServiceIdentity(app, service string) string {
	return "gordon-" + app + "--" + NormalizeServiceName(service)
}

// RuntimeVolumeName returns the generated gordon-<app>--<service>--vol--<name>.
func RuntimeVolumeName(app, service, volume string) string {
	return LogicalServiceIdentity(app, service) + "--vol--" + volume
}

// AppSecretPath returns the pass path gordon/apps/<app>/<service>/<name>.
func AppSecretPath(app, service, name string) string {
	return "gordon/apps/" + app + "/" + NormalizeServiceName(service) + "/" + name
}

// CanonicalHTTPHost lowercases and strips a trailing dot.
func CanonicalHTTPHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.TrimSuffix(host, ".")
}

// ParsePublish splits a publish bind into host and port.
// Acceptable forms: "IP:port" or "port". Hostnames are rejected.
func ParsePublish(publish string) (string, int, error) {
	publish = strings.TrimSpace(publish)
	if publish == "" {
		return "", 0, fmt.Errorf("%w: publish must not be empty", ErrInvalidAppSpec)
	}
	host := ""
	portText := publish
	if h, p, err := net.SplitHostPort(publish); err == nil {
		host = h
		portText = p
	}
	if host != "" && net.ParseIP(host) == nil {
		return "", 0, fmt.Errorf("%w: publish host %q must be a literal IP, not a hostname", ErrInvalidAppSpec, host)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("%w: publish port %q must be 1-65535", ErrInvalidAppSpec, portText)
	}
	return host, port, nil
}

// Validate checks the full normalized spec.
func (s AppSpec) Validate() error {
	if err := ValidateAppName(s.Name); err != nil {
		return err
	}
	if len(s.Services) == 0 {
		return fmt.Errorf("%w: app %q must declare at least one service", ErrInvalidAppSpec, s.Name)
	}
	if err := s.validateEnv(); err != nil {
		return err
	}
	if err := s.validateServices(); err != nil {
		return err
	}
	if err := checkEnvSecretKeyDisjoint(s); err != nil {
		return err
	}
	if err := checkVolumeOwnership(s); err != nil {
		return err
	}
	for i := range s.Networks {
		if err := s.Networks[i].validate(s); err != nil {
			return err
		}
	}
	return nil
}

// validateEnv checks app-wide public environment values.
func (s AppSpec) validateEnv() error {
	for key, value := range s.Env {
		if err := ValidateEnvKey(key); err != nil {
			return fmt.Errorf("%w: env key %q: %v", ErrInvalidAppSpec, key, err)
		}
		if strings.Contains(value, "\n") {
			return fmt.Errorf("%w: env value for %q must not contain newlines", ErrInvalidAppSpec, key)
		}
		if len(value) == 0 || len(value) > MaxAppEnvValueLen {
			return fmt.Errorf("%w: env value for %q must be 1-%d bytes", ErrInvalidAppSpec, key, MaxAppEnvValueLen)
		}
		if ContainsSecretReference(value) {
			return fmt.Errorf("%w: env value for %q must not contain secret references", ErrInvalidAppSpec, key)
		}
	}
	return nil
}

// validateServices checks service identity uniqueness and delegates per-service rules.
func (s AppSpec) validateServices() error {
	seenServices := map[string]string{}
	seenNormalized := map[string]string{}
	for i := range s.Services {
		svc := &s.Services[i]
		if err := ValidateServiceName(svc.Name); err != nil {
			return err
		}
		lower := strings.ToLower(svc.Name)
		if _, ok := seenServices[lower]; ok {
			return fmt.Errorf("%w: duplicate service name %q", ErrInvalidAppSpec, svc.Name)
		}
		seenServices[lower] = svc.Name
		normalized := NormalizeServiceName(svc.Name)
		if prev, ok := seenNormalized[normalized]; ok {
			return fmt.Errorf("%w: service name %q normalizes to the same runtime identifier as %q", ErrInvalidAppSpec, svc.Name, prev)
		}
		seenNormalized[normalized] = svc.Name
		if err := svc.validate(); err != nil {
			return err
		}
	}
	return nil
}

// checkEnvSecretKeyDisjoint enforces [env] keys disjoint from secret map keys.
func checkEnvSecretKeyDisjoint(s AppSpec) error {
	for key := range s.Env {
		for _, svc := range s.Services {
			if _, ok := svc.Secrets[key]; ok {
				return fmt.Errorf("%w: env key %q collides with a secret key in service %q", ErrInvalidAppSpec, key, svc.Name)
			}
		}
	}
	return nil
}

// checkVolumeOwnership enforces one claimant per volume name.
func checkVolumeOwnership(s AppSpec) error {
	claimants := map[string]string{}
	for _, svc := range s.Services {
		for _, vol := range svc.Volumes {
			if prev, ok := claimants[vol.Name]; ok {
				return fmt.Errorf("%w: volume %q claimed by both %q and %q", ErrInvalidAppSpec, vol.Name, prev, svc.Name)
			}
			claimants[vol.Name] = svc.Name
		}
	}
	return nil
}

// validate checks one service.
func (s *AppService) validate() error {
	if strings.TrimSpace(s.Image) == "" {
		return fmt.Errorf("%w: service %q requires an image", ErrInvalidAppSpec, s.Name)
	}
	if s.Replicas != 1 {
		return fmt.Errorf("%w: service %q replicas must be 1 in v2.50", ErrInvalidAppSpec, s.Name)
	}
	if s.StopGrace <= 0 || s.StopGrace > AppMaxStopGrace {
		return fmt.Errorf("%w: service %q stop_grace must be within (0, 5m]", ErrInvalidAppSpec, s.Name)
	}
	if err := s.Readiness.validate(s); err != nil {
		return err
	}
	if err := s.validateInterfaces(); err != nil {
		return err
	}
	if err := s.validateSecrets(); err != nil {
		return err
	}
	volumes, err := s.validateVolumes()
	if err != nil {
		return err
	}
	if err := s.validateDatabases(volumes); err != nil {
		return err
	}
	return nil
}

// validateInterfaces checks HTTP/TCP/UDP/RCON entries.
func (s *AppService) validateInterfaces() error {
	for i := range s.HTTP {
		if err := s.HTTP[i].validate(s.Name); err != nil {
			return err
		}
	}
	for i := range s.TCP {
		if err := s.TCP[i].validate(s.Name); err != nil {
			return err
		}
	}
	for i := range s.UDP {
		if err := s.UDP[i].validate(s.Name); err != nil {
			return err
		}
	}
	for i := range s.RCON {
		if err := s.RCON[i].validate(s.Name); err != nil {
			return err
		}
	}
	return nil
}

// validateSecrets checks the ENV-key to secret-name map.
func (s *AppService) validateSecrets() error {
	for envKey, secretName := range s.Secrets {
		if err := ValidateEnvKey(envKey); err != nil {
			return fmt.Errorf("%w: service %q secret env key %q: %v", ErrInvalidAppSpec, s.Name, envKey, err)
		}
		if err := ValidateSecretName(secretName); err != nil {
			return fmt.Errorf("%w: service %q secret name %q: %v", ErrInvalidAppSpec, s.Name, secretName, err)
		}
	}
	return nil
}

// validateVolumes checks volume declarations and returns known names.
func (s *AppService) validateVolumes() (map[string]struct{}, error) {
	seenVolumes := map[string]struct{}{}
	for i := range s.Volumes {
		vol := &s.Volumes[i]
		if err := ValidateVolumeName(vol.Name); err != nil {
			return nil, fmt.Errorf("%w: service %q: %v", ErrInvalidAppSpec, s.Name, err)
		}
		if _, ok := seenVolumes[vol.Name]; ok {
			return nil, fmt.Errorf("%w: service %q duplicate volume %q", ErrInvalidAppSpec, s.Name, vol.Name)
		}
		seenVolumes[vol.Name] = struct{}{}
		if !strings.HasPrefix(vol.Path, "/") || strings.Contains(vol.Path, "..") {
			return nil, fmt.Errorf("%w: service %q volume %q path must be absolute without dot-dot", ErrInvalidAppSpec, s.Name, vol.Name)
		}
	}
	return seenVolumes, nil
}

// validateDatabases checks database declarations and backup references.
func (s *AppService) validateDatabases(seenVolumes map[string]struct{}) error {
	seenDBs := map[string]struct{}{}
	for i := range s.Databases {
		db := &s.Databases[i]
		if strings.TrimSpace(db.Name) == "" {
			return fmt.Errorf("%w: service %q database name must not be empty", ErrInvalidAppSpec, s.Name)
		}
		if _, ok := seenDBs[db.Name]; ok {
			return fmt.Errorf("%w: service %q duplicate database %q", ErrInvalidAppSpec, s.Name, db.Name)
		}
		seenDBs[db.Name] = struct{}{}
		if db.Type != AppDBPostgres {
			return fmt.Errorf("%w: service %q database %q type must be postgres", ErrInvalidAppSpec, s.Name, db.Name)
		}
		if _, ok := appBackupSchedules[db.Schedule]; !ok {
			return fmt.Errorf("%w: service %q database %q schedule must be hourly|daily|weekly|monthly", ErrInvalidAppSpec, s.Name, db.Name)
		}
	}
	for _, ref := range s.Backup.Postgres {
		if _, ok := seenDBs[ref]; !ok {
			return fmt.Errorf("%w: service %q backup references unknown database %q", ErrInvalidAppSpec, s.Name, ref)
		}
	}
	for _, ref := range s.Backup.Volume {
		if _, ok := seenVolumes[ref]; !ok {
			return fmt.Errorf("%w: service %q backup references unknown volume %q", ErrInvalidAppSpec, s.Name, ref)
		}
	}
	return nil
}

// validate checks readiness rules including UDP restrictions and port selection.
func (r AppReadiness) validate(svc *AppService) error {
	if err := r.validateType(svc); err != nil {
		return err
	}
	if r.Timeout < AppMinReadinessTimeout || r.Timeout > AppMaxReadinessTimeout {
		return fmt.Errorf("%w: service %q readiness timeout must be within [1s, 10m]", ErrInvalidAppSpec, svc.Name)
	}
	return nil
}

// validateType checks the type-specific readiness constraints.
func (r AppReadiness) validateType(svc *AppService) error {
	switch r.Type {
	case "", AppReadinessNone:
		if r.Path != "" || r.Contains != "" || r.Port != 0 {
			return fmt.Errorf("%w: service %q readiness none takes no path/contains/port", ErrInvalidAppSpec, svc.Name)
		}
		return nil
	case AppReadinessTCP, AppReadinessHTTP:
		return r.validateL4(svc)
	case AppReadinessLog:
		if strings.TrimSpace(r.Path) == "" || strings.TrimSpace(r.Contains) == "" {
			return fmt.Errorf("%w: service %q log readiness requires path and contains", ErrInvalidAppSpec, svc.Name)
		}
		return nil
	default:
		return fmt.Errorf("%w: service %q readiness type must be none|tcp|http|log", ErrInvalidAppSpec, svc.Name)
	}
}

// validateL4 checks TCP/HTTP readiness including UDP exclusion and port selection.
func (r AppReadiness) validateL4(svc *AppService) error {
	if hasUDPOnly(svc) {
		return fmt.Errorf("%w: service %q with UDP-only interfaces must use none or log readiness", ErrInvalidAppSpec, svc.Name)
	}
	if countTCPInterfaces(svc) > 1 && r.Port == 0 {
		return fmt.Errorf("%w: service %q has multiple TCP-capable interfaces, readiness.port is required", ErrInvalidAppSpec, svc.Name)
	}
	if r.Port != 0 && !hasTCPContainerPort(svc, r.Port) {
		return fmt.Errorf("%w: service %q readiness.port %d matches no declared container port", ErrInvalidAppSpec, svc.Name, r.Port)
	}
	if r.Type == AppReadinessHTTP && strings.TrimSpace(r.Path) == "" {
		return fmt.Errorf("%w: service %q http readiness requires path", ErrInvalidAppSpec, svc.Name)
	}
	return nil
}

// hasUDPOnly reports services with UDP interfaces and no TCP/HTTP interface.
func hasUDPOnly(svc *AppService) bool {
	if len(svc.UDP) == 0 {
		return false
	}
	return len(svc.HTTP) == 0 && len(svc.TCP) == 0 && len(svc.RCON) == 0
}

// countTCPInterfaces counts http/tcp/rcon container ports.
func countTCPInterfaces(svc *AppService) int {
	return len(svc.HTTP) + len(svc.TCP) + len(svc.RCON)
}

// hasTCPContainerPort checks readiness.port against declared ports.
func hasTCPContainerPort(svc *AppService, port int) bool {
	for _, h := range svc.HTTP {
		if h.Port == port {
			return true
		}
	}
	for _, t := range svc.TCP {
		if t.Port == port {
			return true
		}
	}
	for _, r := range svc.RCON {
		if r.Port == port {
			return true
		}
	}
	return false
}

// validate checks one HTTP interface.
func (h AppHTTPInterface) validate(service string) error {
	if h.Host == "" {
		return fmt.Errorf("%w: service %q http host is required", ErrInvalidAppSpec, service)
	}
	if _, ok := CanonicalRouteDomain(h.Host); !ok {
		return fmt.Errorf("%w: service %q http host %q is not a valid public hostname", ErrInvalidAppSpec, service, h.Host)
	}
	if h.Port < 1 || h.Port > 65535 {
		return fmt.Errorf("%w: service %q http port must be 1-65535", ErrInvalidAppSpec, service)
	}
	switch h.TLS {
	case AppTLSAuto, AppTLSAlways, AppTLSNever:
	default:
		return fmt.Errorf("%w: service %q http tls must be auto|always|never", ErrInvalidAppSpec, service)
	}
	return nil
}

// validate checks one TCP interface.
func (t AppTCPInterface) validate(service string) error {
	if strings.TrimSpace(t.Entrypoint) == "" {
		return fmt.Errorf("%w: service %q tcp entrypoint is required", ErrInvalidAppSpec, service)
	}
	if t.Port < 1 || t.Port > 65535 {
		return fmt.Errorf("%w: service %q tcp port must be 1-65535", ErrInvalidAppSpec, service)
	}
	if _, _, err := ParsePublish(t.Publish); err != nil {
		return fmt.Errorf("%w: service %q tcp: %v", ErrInvalidAppSpec, service, err)
	}
	return nil
}

// validate checks one UDP interface.
func (u AppUDPInterface) validate(service string) error {
	if strings.TrimSpace(u.Entrypoint) == "" {
		return fmt.Errorf("%w: service %q udp entrypoint is required", ErrInvalidAppSpec, service)
	}
	if u.Port < 1 || u.Port > 65535 {
		return fmt.Errorf("%w: service %q udp port must be 1-65535", ErrInvalidAppSpec, service)
	}
	if _, _, err := ParsePublish(u.Publish); err != nil {
		return fmt.Errorf("%w: service %q udp: %v", ErrInvalidAppSpec, service, err)
	}
	return nil
}

// validate checks one RCON interface.
func (r AppRCONInterface) validate(service string) error {
	if strings.TrimSpace(r.Entrypoint) == "" {
		return fmt.Errorf("%w: service %q rcon entrypoint is required", ErrInvalidAppSpec, service)
	}
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("%w: service %q rcon port must be 1-65535", ErrInvalidAppSpec, service)
	}
	if _, _, err := ParsePublish(r.Publish); err != nil {
		return fmt.Errorf("%w: service %q rcon: %v", ErrInvalidAppSpec, service, err)
	}
	if r.Public && len(r.TrustedCIDRs) == 0 {
		return fmt.Errorf("%w: service %q public rcon requires trusted_cidrs", ErrInvalidAppSpec, service)
	}
	if !r.Public && len(r.TrustedCIDRs) > 0 {
		return fmt.Errorf("%w: service %q private rcon must not set trusted_cidrs without public", ErrInvalidAppSpec, service)
	}
	for _, cidr := range r.TrustedCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("%w: service %q rcon trusted_cidrs %q is not a valid CIDR", ErrInvalidAppSpec, service, cidr)
		}
	}
	return nil
}

// validate checks one shared-network declaration.
func (n AppSharedNetwork) validate(s AppSpec) error {
	if strings.TrimSpace(n.Network) == "" {
		return fmt.Errorf("%w: shared network name must not be empty", ErrInvalidAppSpec)
	}
	if len(n.Services) == 0 {
		return fmt.Errorf("%w: shared network %q must list at least one service", ErrInvalidAppSpec, n.Network)
	}
	seen := map[string]struct{}{}
	for _, name := range n.Services {
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%w: shared network %q duplicate service %q", ErrInvalidAppSpec, n.Network, name)
		}
		seen[name] = struct{}{}
		found := false
		for _, svc := range s.Services {
			if svc.Name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: shared network %q references unknown service %q", ErrInvalidAppSpec, n.Network, name)
		}
	}
	for _, alias := range n.Aliases {
		if !dnsLabelPattern.MatchString(alias) {
			return fmt.Errorf("%w: shared network %q alias %q must be a DNS label", ErrInvalidAppSpec, n.Network, alias)
		}
	}
	return nil
}

// AppDiff describes normalized differences between desired and effective specs.
type AppDiff struct {
	Added   []string
	Removed []string
	Changed []string
}

// DiffAppSpec returns a deterministic, stably sorted diff.
// It carries no secret values: only secret PATHS appear.
func DiffAppSpec(desired, effective AppSpec) AppDiff {
	diff := AppDiff{}
	desiredServices := map[string]AppService{}
	for _, svc := range desired.Services {
		desiredServices[svc.Name] = svc
	}
	effectiveServices := map[string]AppService{}
	for _, svc := range effective.Services {
		effectiveServices[svc.Name] = svc
	}
	for name := range desiredServices {
		if _, ok := effectiveServices[name]; !ok {
			diff.Added = append(diff.Added, "service/"+name)
		}
	}
	for name := range effectiveServices {
		if _, ok := desiredServices[name]; !ok {
			diff.Removed = append(diff.Removed, "service/"+name)
		}
	}
	for name, desiredSvc := range desiredServices {
		effectiveSvc, ok := effectiveServices[name]
		if !ok {
			continue
		}
		diff.Changed = append(diff.Changed, diffService(name, desiredSvc, effectiveSvc)...)
	}
	if !equalStringMaps(desired.Env, effective.Env) {
		diff.Changed = append(diff.Changed, "env")
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Changed)
	return diff
}

// diffService compares two services field by field.
func diffService(name string, desired, effective AppService) []string {
	var changed []string
	if desired.Image != effective.Image {
		changed = append(changed, "service/"+name+"/image")
	}
	if strings.Join(desired.Command, "\x00") != strings.Join(effective.Command, "\x00") {
		changed = append(changed, "service/"+name+"/command")
	}
	if desired.Readiness != effective.Readiness {
		changed = append(changed, "service/"+name+"/readiness")
	}
	if len(desired.HTTP) != len(effective.HTTP) || len(desired.TCP) != len(effective.TCP) ||
		len(desired.UDP) != len(effective.UDP) || len(desired.RCON) != len(effective.RCON) {
		changed = append(changed, "service/"+name+"/interfaces")
	} else if interfacesChanged(desired, effective) {
		changed = append(changed, "service/"+name+"/interfaces")
	}
	if !equalStringMaps(desired.Secrets, effective.Secrets) {
		for path := range secretPathChanges(desired, effective) {
			changed = append(changed, path)
		}
	}
	if !equalVolumes(desired.Volumes, effective.Volumes) {
		changed = append(changed, "service/"+name+"/volumes")
	}
	return changed
}

// secretPathChanges lists secret PATH changes (never values).
func secretPathChanges(desired, effective AppService) map[string]struct{} {
	paths := map[string]struct{}{}
	for envKey, secretName := range desired.Secrets {
		if effective.Secrets[envKey] != secretName {
			paths["service/"+desired.Name+"/secret/"+secretName] = struct{}{}
		}
	}
	for envKey, secretName := range effective.Secrets {
		if desired.Secrets[envKey] != secretName {
			paths["service/"+effective.Name+"/secret/"+secretName] = struct{}{}
		}
	}
	return paths
}

// interfacesChanged compares interface slices.
func interfacesChanged(desired, effective AppService) bool {
	for i := range desired.HTTP {
		if desired.HTTP[i] != effective.HTTP[i] {
			return true
		}
	}
	for i := range desired.TCP {
		if desired.TCP[i] != effective.TCP[i] {
			return true
		}
	}
	for i := range desired.UDP {
		if desired.UDP[i] != effective.UDP[i] {
			return true
		}
	}
	for i := range desired.RCON {
		if !equalRCON(desired.RCON[i], effective.RCON[i]) {
			return true
		}
	}
	return false
}

// equalRCON compares RCON interfaces including CIDR slices.
func equalRCON(a, b AppRCONInterface) bool {
	return a.Entrypoint == b.Entrypoint && a.Port == b.Port &&
		a.Publish == b.Publish && a.Public == b.Public &&
		slices.Equal(a.TrustedCIDRs, b.TrustedCIDRs)
}

// equalStringMaps compares string maps.
func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// equalVolumes compares volume slices by value.
func equalVolumes(a, b []AppVolume) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
