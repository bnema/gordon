package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

var componentRoles = []domain.ComponentRole{
	domain.ComponentRoleControl,
	domain.ComponentRoleRuntime,
	domain.ComponentRoleEdge,
	domain.ComponentRoleRegistry,
}

// RequiredEnvVar identifies a host variable needed by a configured component.
// Values intentionally never appear in this model or its JSON representation.
type RequiredEnvVar struct {
	Key   string                 `json:"key"`
	Roles []domain.ComponentRole `json:"roles"`
}

// OptionalEnvVar identifies a compatible host variable that is forwarded only
// when set. It is useful for provider defaults without making them mandatory.
type OptionalEnvVar struct {
	Key   string                 `json:"key"`
	Roles []domain.ComponentRole `json:"roles"`
}

// ComponentEnvFile is a non-secret reference to one generated role env file.
type ComponentEnvFile struct {
	Role domain.ComponentRole `json:"role"`
	Path string               `json:"path"`
}

// MissingEnvVarError deliberately reports names only. It is safe to return to
// CLI/API callers and to retain in a migration checkpoint diagnostic.
type MissingEnvVarError struct{ Keys []string }

func (e *MissingEnvVarError) Error() string {
	if e == nil || len(e.Keys) == 0 {
		return "required component environment variable is missing"
	}
	return "required component environment variables are missing: " + strings.Join(e.Keys, ", ")
}

// ComponentEnvManifest is the explicit, role-scoped environment transfer
// plan. Its values are private so status, JSON, errors, and logs cannot leak
// credentials accidentally.
type ComponentEnvManifest struct {
	Required []RequiredEnvVar   `json:"required,omitempty"`
	Optional []OptionalEnvVar   `json:"optional,omitempty"`
	Files    []ComponentEnvFile `json:"files,omitempty"`

	values map[domain.ComponentRole]map[string]string
}

// ComponentEnvManifestOptions uses the already-loaded configuration and a
// caller-supplied environment snapshot. Supplying the snapshot makes planning
// deterministic and prevents unrelated workload process environment bleed.
type ComponentEnvManifestOptions struct {
	Config            Config
	Environment       map[string]string
	ExplicitAllowlist []string
}

// MigrationEnvOptions connects the explicit manifest to migration prepare.
type MigrationEnvOptions struct {
	Config            Config
	Environment       map[string]string
	ExplicitAllowlist []string
	// ExplicitEnvFile is an optional migration-command supplied file. Its keys
	// must be explicitly allowlisted and non-secret-like before values are read.
	ExplicitEnvFile string
	Directory       string
	// ExternalRoutes is the raw Viper external_routes value. It is validated
	// before the non-secret control routing manifest is written.
	ExternalRoutes any
}

const (
	maxExplicitComponentEnvBytes int64 = 64 << 10
	// registryForwardTokenEnvVar is injected only into the split edge and
	// registry role environment files. It is never a control/runtime credential.
	registryForwardTokenEnvVar = "GORDON_REGISTRY_FORWARD_TOKEN"
)

// BuildMigrationComponentEnvManifest extends config-derived host detection
// with the explicit migration env file mechanism. The parser accepts only
// KEY=value records and reports keys, never their values.
func BuildMigrationComponentEnvManifest(options MigrationEnvOptions) (*ComponentEnvManifest, error) {
	environment := make(map[string]string, len(options.Environment))
	for key, value := range options.Environment {
		environment[key] = value
	}
	if strings.TrimSpace(options.ExplicitEnvFile) != "" {
		values, err := readExplicitComponentEnvFile(options.ExplicitEnvFile, options.ExplicitAllowlist)
		if err != nil {
			return nil, err
		}
		for key, value := range values {
			environment[key] = value
		}
	}
	return BuildComponentEnvManifest(ComponentEnvManifestOptions{Config: options.Config, Environment: environment, ExplicitAllowlist: options.ExplicitAllowlist})
}

func readExplicitComponentEnvFile(path string, allowlist []string) (map[string]string, error) {
	if err := validateExplicitComponentEnvFile(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open explicit component environment file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxExplicitComponentEnvBytes+1))
	if err != nil || int64(len(data)) > maxExplicitComponentEnvBytes {
		return nil, fmt.Errorf("invalid explicit component environment file")
	}
	return parseExplicitComponentEnvFile(data, explicitComponentEnvAllowlist(allowlist))
}

func validateExplicitComponentEnvFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("invalid explicit component environment file")
	}
	return nil
}

func explicitComponentEnvAllowlist(allowlist []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(allowlist))
	for _, key := range allowlist {
		allowed[strings.TrimSpace(key)] = struct{}{}
	}
	return allowed
}

func parseExplicitComponentEnvFile(data []byte, allowed map[string]struct{}) (map[string]string, error) {
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, skip, err := parseExplicitComponentEnvLine(line, allowed)
		if err != nil {
			return nil, err
		}
		if !skip {
			values[key] = value
		}
	}
	return values, nil
}

func parseExplicitComponentEnvLine(line string, allowed map[string]struct{}) (string, string, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", true, nil
	}
	key, value, found := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	if !found || key == "" {
		return "", "", false, fmt.Errorf("invalid explicit component environment variable")
	}
	if _, ok := allowed[key]; !ok || !isSafeExplicitComponentEnvKey(key) {
		return "", "", false, fmt.Errorf("invalid explicit component environment variable: %s", key)
	}
	return key, value, false, nil
}

// BuildComponentEnvManifest derives only variables which are relevant to the
// loaded configuration. Explicit variables are limited to non-secret, known
// safe names; secret-like or workload names must use config-owned providers.
func BuildComponentEnvManifest(options ComponentEnvManifestOptions) (*ComponentEnvManifest, error) {
	manifest := &ComponentEnvManifest{values: make(map[domain.ComponentRole]map[string]string)}
	for _, role := range componentRoles {
		manifest.values[role] = make(map[string]string)
	}
	missing := make(map[string]struct{})
	add := componentEnvAdder(manifest, options.Environment, missing)
	addConfigOverrideEnv(options.Environment, add)
	addSecretProviderEnv(options.Config, manifest, add)
	addACMEEnv(options.Config, add)
	addS3BackupEnv(options.Config, add)
	addTelemetryEnv(options.Config, add)
	addRuntimeSocketEnv(add)
	addRuntimeBootstrapToken(options.Config, options.Environment, manifest)
	if err := addExplicitComponentEnv(options.ExplicitAllowlist, add); err != nil {
		return nil, err
	}
	sortManifest(manifest)
	if len(missing) != 0 {
		keys := make([]string, 0, len(missing))
		for key := range missing {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return manifest, &MissingEnvVarError{Keys: keys}
	}
	return manifest, nil
}

type componentEnvAdd func(string, []domain.ComponentRole, bool)

func componentEnvAdder(manifest *ComponentEnvManifest, environment map[string]string, missing map[string]struct{}) componentEnvAdd {
	return func(key string, roles []domain.ComponentRole, required bool) {
		if required {
			manifest.Required = append(manifest.Required, RequiredEnvVar{Key: key, Roles: append([]domain.ComponentRole(nil), roles...)})
		} else {
			manifest.Optional = append(manifest.Optional, OptionalEnvVar{Key: key, Roles: append([]domain.ComponentRole(nil), roles...)})
		}
		value, present := environment[key]
		if !present || value == "" {
			if required {
				missing[key] = struct{}{}
			}
			return
		}
		for _, role := range roles {
			manifest.values[role][key] = value
		}
	}
}

func addConfigOverrideEnv(environment map[string]string, add componentEnvAdd) {
	// GORDON_ROLE is process-local role selection, never a config override.
	for _, key := range sortedEnvironmentKeys(environment, func(key string) bool {
		return strings.HasPrefix(key, "GORDON_") && key != "GORDON_ROLE" && key != "GORDON_MIGRATION_IMAGE"
	}) {
		roles := []domain.ComponentRole{domain.ComponentRoleControl}
		// Runtime must initialize the token issuer used by its local worker.
		// Keep the signing secret scoped to the control/runtime bootstrap pair;
		// edge and registry must never receive it.
		if key == TokenSecretEnvVar {
			roles = append(roles, domain.ComponentRoleRuntime)
		}
		add(key, roles, true)
	}
}

const (
	managedPassRoot       = "/var/lib/gordon/secrets" // #nosec G101 -- fixed container state path, not credential material.
	managedPassCurrentDir = managedPassRoot + "/current"
	managedPassGPGHome    = managedPassCurrentDir + "/gnupg"
	managedPassStoreDir   = managedPassCurrentDir + "/password-store"
)

func addSecretProviderEnv(cfg Config, manifest *ComponentEnvManifest, add componentEnvAdd) {
	control := []domain.ComponentRole{domain.ComponentRoleControl}
	switch strings.TrimSpace(cfg.Auth.SecretsBackend) {
	case string(domain.SecretsBackendPass):
		// Managed pass state is container-owned. Never copy host provider paths.
		manifest.values[domain.ComponentRoleControl]["GNUPGHOME"] = managedPassGPGHome
		manifest.values[domain.ComponentRoleControl]["PASSWORD_STORE_DIR"] = managedPassStoreDir
	case string(domain.SecretsBackendSops):
		add("SOPS_AGE_KEY_FILE", control, false)
		add("SOPS_KMS_ARN", control, false)
	}
}

func addACMEEnv(cfg Config, add componentEnvAdd) {
	if cfg.TLS.ACME.Enabled && strings.TrimSpace(cfg.TLS.ACME.Challenge) == string(domain.ACMEChallengeCloudflareDNS01) {
		add("CLOUDFLARE_DNS_API_TOKEN", []domain.ComponentRole{domain.ComponentRoleEdge}, true)
	}
}

func addS3BackupEnv(cfg Config, add componentEnvAdd) {
	if !cfg.Backups.Volumes.Enabled || strings.TrimSpace(cfg.Backups.Volumes.S3.Bucket) == "" {
		return
	}
	runtime := []domain.ComponentRole{domain.ComponentRoleRuntime}
	add("AWS_ACCESS_KEY_ID", runtime, true)
	add("AWS_SECRET_ACCESS_KEY", runtime, true)
	add("AWS_SESSION_TOKEN", runtime, false)
}

func addTelemetryEnv(cfg Config, add componentEnvAdd) {
	if cfg.Telemetry.Enabled {
		add("OTEL_EXPORTER_OTLP_HEADERS", componentRoles, false)
	}
}

// addRuntimeBootstrapToken places the configured migration handoff credential
// only in the control/runtime pair. Its value remains private to 0600 role
// environment files and never appears in manifest status or diagnostics.
func addRuntimeBootstrapToken(cfg Config, environment map[string]string, manifest *ComponentEnvManifest) {
	if manifest == nil {
		return
	}
	token := strings.TrimSpace(cfg.Runtime.Token)
	if token == "" && strings.TrimSpace(cfg.Runtime.TokenEnv) != "" {
		token = strings.TrimSpace(environment[cfg.Runtime.TokenEnv])
	}
	if token == "" {
		return
	}
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime} {
		manifest.values[role]["GORDON_COMPONENT_RUNTIME_TOKEN"] = token
	}
	// Edge and registry are authenticated to control before receiving snapshots
	// or emitting events. They get distinct derived credentials, never the
	// control/runtime seed that can operate the Unix runtime handoff.
	manifest.values[domain.ComponentRoleEdge]["GORDON_COMPONENT_EDGE_TOKEN"] = migrationComponentToken(token, domain.ComponentRoleEdge)
	// This is intentionally neither a control credential nor an edge snapshot
	// credential. It exists only while a prepared edge must prove that a
	// rootless-NAT loopback request originated from the migration coordinator.
	manifest.values[domain.ComponentRoleEdge]["GORDON_MIGRATION_PROBE_TOKEN"] = migrationProbeToken(token)
	manifest.values[domain.ComponentRoleRegistry]["GORDON_COMPONENT_REGISTRY_TOKEN"] = migrationComponentToken(token, domain.ComponentRoleRegistry)
	// Edge-to-registry forwarding has a separate purpose and credential from
	// either component's control credential. Both values remain in 0600 role
	// environment files and are regenerated when the component token rotates.
	forwardToken := migrationRegistryForwardToken(token)
	manifest.values[domain.ComponentRoleEdge][registryForwardTokenEnvVar] = forwardToken
	manifest.values[domain.ComponentRoleRegistry][registryForwardTokenEnvVar] = forwardToken
}

func addRuntimeSocketEnv(add componentEnvAdd) {
	// Runtime authority is not a general component capability.
	for _, key := range []string{"CONTAINER_HOST", "DOCKER_HOST", "PODMAN_HOST"} {
		add(key, []domain.ComponentRole{domain.ComponentRoleRuntime}, false)
	}
}

func addExplicitComponentEnv(allowlist []string, add componentEnvAdd) error {
	for _, key := range allowlist {
		key = strings.TrimSpace(key)
		if !isSafeExplicitComponentEnvKey(key) {
			return fmt.Errorf("invalid explicit component environment variable: %s", key)
		}
		add(key, []domain.ComponentRole{domain.ComponentRoleControl}, true)
	}
	return nil
}

func componentEnvironmentFromEnviron(environ []string) map[string]string {
	environment := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, found := strings.Cut(entry, "=")
		if found && key != "" {
			environment[key] = value
		}
	}
	return environment
}

func sortedEnvironmentKeys(environment map[string]string, include func(string) bool) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		if include(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func isSafeExplicitComponentEnvKey(key string) bool {
	if key == "" || !componentLabelValue.MatchString(key) || !strings.Contains(key, "_") {
		return false
	}
	upper := strings.ToUpper(key)
	for _, unsafe := range []string{"SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE", "_KEY"} {
		if strings.Contains(upper, unsafe) {
			return false
		}
	}
	return true
}

func sortManifest(manifest *ComponentEnvManifest) {
	sort.Slice(manifest.Required, func(i, j int) bool { return manifest.Required[i].Key < manifest.Required[j].Key })
	sort.Slice(manifest.Optional, func(i, j int) bool { return manifest.Optional[i].Key < manifest.Optional[j].Key })
}

// KeysForRole returns sorted names only, suitable for reports and assertions.
func (m *ComponentEnvManifest) KeysForRole(role domain.ComponentRole) []string {
	if m == nil {
		return nil
	}
	values := m.values[role]
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// RedactedSummary is a value-free, deterministic status representation.
func (m *ComponentEnvManifest) RedactedSummary() string {
	if m == nil {
		return ""
	}
	parts := make([]string, 0, len(componentRoles))
	for _, role := range componentRoles {
		parts = append(parts, string(role)+":"+strings.Join(m.KeysForRole(role), ","))
	}
	return strings.Join(parts, ";")
}

// WriteFiles atomically writes restrictive, role-minimized env files. Values
// are never returned or logged; callers receive only file references.
func (m *ComponentEnvManifest) WriteFiles(directory string) ([]ComponentEnvFile, error) {
	if m == nil {
		return nil, fmt.Errorf("component environment manifest is required")
	}
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("component environment directory is required")
	}
	if err := prepareComponentEnvDirectory(directory); err != nil {
		return nil, err
	}
	files := make([]ComponentEnvFile, 0, len(componentRoles))
	for _, role := range componentRoles {
		path := filepath.Join(directory, string(role)+".env")
		if err := writeComponentEnvFile(path, m.values[role]); err != nil {
			return nil, err
		}
		files = append(files, ComponentEnvFile{Role: role, Path: path})
	}
	m.Files = append([]ComponentEnvFile(nil), files...)
	return files, nil
}

func prepareComponentEnvDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create component environment directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("invalid component environment directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- component env directories must be owner-only.
		return fmt.Errorf("restrict component environment directory: %w", err)
	}
	return nil
}

func writeComponentEnvFile(path string, values map[string]string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid component environment file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect component environment file: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".component-env-*")
	if err != nil {
		return fmt.Errorf("create component environment file: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("restrict component environment file: %w", err)
	}
	for _, key := range sortedEnvironmentKeys(values, func(string) bool { return true }) {
		if _, err := file.WriteString(key + "=" + values[key] + "\n"); err != nil {
			_ = file.Close()
			return fmt.Errorf("write component environment file: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync component environment file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close component environment file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace component environment file: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open component environment directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync component environment directory: %w", err)
	}
	return nil
}
