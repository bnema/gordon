package app

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/bnema/gordon/internal/domain"
)

// ComponentConfigFile is a role-scoped, non-secret configuration reference.
// Its content is intentionally never included in migration status or errors.
type ComponentConfigFile struct {
	Role domain.ComponentRole `json:"role"`
	Path string               `json:"path"`
}

// WriteComponentConfigManifests materializes only the configuration each role
// needs. Files are private, atomically replaced, and live beneath the
// migration directory so the runtime can reject arbitrary host binds.
func WriteComponentConfigManifests(cfg Config, directory string) ([]ComponentConfigFile, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("component configuration directory is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create component configuration directory: %w", err)
	}
	// #nosec G302 -- a private directory requires owner execute permission.
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("secure component configuration directory: %w", err)
	}
	migrationID, ok := componentConfigMigrationID(directory)
	if !ok {
		return nil, fmt.Errorf("component configuration directory must be beneath migration/config/<id>/<generation>")
	}
	controlListenAddress, controlEndpoint, err := componentControlNetworking(cfg.Control.ListenAddress)
	if err != nil {
		return nil, err
	}
	files := make([]ComponentConfigFile, 0, len(componentRoles))
	for _, role := range componentRoles {
		data, err := toml.Marshal(componentRoleConfig(cfg, role, migrationID, controlListenAddress, controlEndpoint, true))
		if err != nil {
			return nil, fmt.Errorf("encode %s component configuration: %w", role, err)
		}
		path := filepath.Join(directory, string(role)+".toml")
		if err := writePrivateAtomicFile(path, data); err != nil {
			return nil, fmt.Errorf("write %s component configuration: %w", role, err)
		}
		files = append(files, ComponentConfigFile{Role: role, Path: path})
	}
	// Activation recreates edge with this sibling config. Keeping it separate
	// makes the prepared-only bypass impossible to retain on the public edge;
	// runtime selects this fixed name only for the final listener transaction.
	finalEdge, err := toml.Marshal(componentRoleConfig(cfg, domain.ComponentRoleEdge, migrationID, controlListenAddress, controlEndpoint, false))
	if err != nil {
		return nil, fmt.Errorf("encode final edge component configuration: %w", err)
	}
	if err := writePrivateAtomicFile(filepath.Join(directory, "edge-final.toml"), finalEdge); err != nil {
		return nil, fmt.Errorf("write final edge component configuration: %w", err)
	}
	return files, nil
}

func componentRoleConfig(cfg Config, role domain.ComponentRole, migrationID, controlListenAddress, controlEndpoint string, migrationProbeEnabled bool) map[string]any {
	// These are role contracts, not filtered copies of Config. In particular,
	// edge and registry use strict TOML decoders and must never receive legacy
	// server/auth sections (which may contain control-plane secret references).
	switch role {
	case domain.ComponentRoleControl:
		return map[string]any{
			// Components share an isolated network; binding wildcard here permits
			// the edge's gordon-control DNS alias without publishing a host port.
			"control": map[string]any{"listen_address": controlListenAddress, "http": cfg.Control.HTTP, "insecure_tls": true},
			// Control reaches the new runtime only through its private Gordon RPC
			// socket. This is not, and cannot be confused with, the Podman socket.
			"runtime": map[string]any{"endpoint": migrationRuntimeSocketEndpoint(migrationID), "token_env": "GORDON_COMPONENT_RUNTIME_TOKEN"},
			// Split control uses its own data volume. The only shared migration
			// state is the narrowly mounted /var/lib/gordon/migration child.
			"server": map[string]any{"data_dir": componentDataDirectory},
			"auth":   map[string]any{"enabled": cfg.Auth.Enabled, "type": cfg.Auth.Type, "secrets_backend": cfg.Auth.SecretsBackend, "username": cfg.Auth.Username, "token_secret": cfg.Auth.TokenSecret},
		}
	case domain.ComponentRoleRuntime:
		return map[string]any{
			"server":  map[string]any{"data_dir": componentDataDirectory, "runtime": "unix:///run/gordon/runtime.sock"},
			"runtime": map[string]any{"listen_address": migrationRuntimeSocketEndpoint(migrationID), "token_env": "GORDON_COMPONENT_RUNTIME_TOKEN"},
			// Runtime validates scoped component credentials before it binds the
			// private Unix listener. It needs the backend selection, but never a
			// control-plane token reference or credential value in this manifest.
			"auth":    map[string]any{"secrets_backend": cfg.Auth.SecretsBackend},
			"volumes": cfg.Volumes,
		}
	case domain.ComponentRoleRegistry:
		return map[string]any{
			"storage": map[string]any{"data_dir": "/var/lib/gordon"},
			"auth":    map[string]any{"enabled": cfg.Auth.Enabled, "type": cfg.Auth.Type, "secrets_backend": cfg.Auth.SecretsBackend, "username": cfg.Auth.Username},
			"limits":  map[string]any{"max_blob_chunk_size": cfg.Server.MaxBlobChunkSize, "max_blob_size": cfg.Server.MaxBlobSize, "allowed_ips": cfg.Server.RegistryAllowedIPs},
			"listen":  map[string]any{"address": "0.0.0.0:" + fmt.Sprint(cfg.Server.RegistryPort), "tls": map[string]any{"mode": registryTLSDisabled}},
			"control": map[string]any{"event_endpoint": controlEndpoint, "event_token_env": "GORDON_COMPONENT_REGISTRY_TOKEN", "insecure_tls": true, "outbox_max_entries": 10000, "outbox_max_bytes": "64MB"},
		}
	case domain.ComponentRoleEdge:
		edge := map[string]any{"listen_address": "0.0.0.0:" + fmt.Sprint(cfg.Server.Port), "registry_domain": cfg.Server.RegistryDomain, "max_proxy_body_size": cfg.Server.MaxProxyBodySize, "max_proxy_response_size": cfg.Server.MaxProxyResponseSize, "max_concurrent_connections": cfg.Server.MaxConcurrentConns, "trusted_proxy_cidrs": []string{"127.0.0.1/32"}, "migration_probe_enabled": migrationProbeEnabled, "tls": map[string]any{"mode": edgeTLSModeExternal}}
		if migrationProbeEnabled {
			edge["migration_probe_token_env"] = "GORDON_MIGRATION_PROBE_TOKEN"
		}
		return map[string]any{
			"control": map[string]any{"endpoint": controlEndpoint, "token_env": "GORDON_COMPONENT_EDGE_TOKEN", "insecure_tls": true},
			"edge":    edge,
			"logging": map[string]any{"access_log": map[string]any{
				"enabled": cfg.Logging.AccessLog.Enabled, "format": cfg.Logging.AccessLog.Format, "output": cfg.Logging.AccessLog.Output,
				"file_path": cfg.Logging.AccessLog.FilePath, "max_size": cfg.Logging.AccessLog.MaxSize,
				"max_backups": cfg.Logging.AccessLog.MaxBackups, "max_age": cfg.Logging.AccessLog.MaxAge,
				"exclude_health_checks": cfg.Logging.AccessLog.ExcludeHealthChecks, "syslog_identifier": cfg.Logging.AccessLog.SyslogIdentifier,
			}},
		}
	default:
		return nil
	}
}

// componentControlNetworking preserves the configured control port while
// forcing the candidate control to bind every interface on its private,
// un-published component network. Public/external Control.Endpoint values are
// deliberately never injected into edge or registry containers.
func componentControlNetworking(listenAddress string) (string, string, error) {
	const defaultControlPort = 9443
	listenAddress = strings.TrimSpace(listenAddress)
	if listenAddress == "" {
		return fmt.Sprintf("0.0.0.0:%d", defaultControlPort), fmt.Sprintf("gordon-control:%d", defaultControlPort), nil
	}
	_, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "", "", fmt.Errorf("invalid control listen address")
	}
	value, err := strconv.Atoi(port)
	if err != nil || value <= 0 || value > 65535 {
		return "", "", fmt.Errorf("invalid control listen address")
	}
	return "0.0.0.0:" + port, "gordon-control:" + port, nil
}

func migrationRuntimeSocketEndpoint(migrationID string) string {
	return "unix://" + filepath.Join(componentDataDirectory, "migration", migrationID, bootstrapRuntimeSocketName)
}

func componentConfigMigrationID(directory string) (string, bool) {
	clean := filepath.Clean(directory)
	// <data>/migration/config/<id>/<generation>
	if filepath.Base(filepath.Dir(filepath.Dir(clean))) != "config" || filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(clean)))) != "migration" {
		return "", false
	}
	id := filepath.Base(filepath.Dir(clean))
	return id, componentLabelValue.MatchString(id)
}

func writePrivateAtomicFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".component-config-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// componentConfigReferences makes the launch plan deterministic even if the
// checkpoint was serialized with arbitrary ordering.
func componentConfigReferences(references []string) map[domain.ComponentRole]string {
	copyOf := append([]string(nil), references...)
	sort.Strings(copyOf)
	byRole := make(map[domain.ComponentRole]string)
	for _, reference := range copyOf {
		role := domain.ComponentRole(strings.TrimSuffix(filepath.Base(reference), filepath.Ext(reference)))
		if domain.IsKnownComponentRole(role) {
			byRole[role] = reference
		}
	}
	return byRole
}
