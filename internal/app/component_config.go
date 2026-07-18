package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

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
	files := make([]ComponentConfigFile, 0, len(componentRoles))
	for _, role := range componentRoles {
		data, err := yaml.Marshal(componentRoleConfig(cfg, role))
		if err != nil {
			return nil, fmt.Errorf("encode %s component configuration: %w", role, err)
		}
		path := filepath.Join(directory, string(role)+".yaml")
		if err := writePrivateAtomicFile(path, data); err != nil {
			return nil, fmt.Errorf("write %s component configuration: %w", role, err)
		}
		files = append(files, ComponentConfigFile{Role: role, Path: path})
	}
	return files, nil
}

func componentRoleConfig(cfg Config, role domain.ComponentRole) map[string]any {
	server := map[string]any{"data_dir": resolveDataDir(cfg.Server.DataDir)}
	switch role {
	case domain.ComponentRoleControl:
		return map[string]any{
			"control": map[string]any{"listen_address": cfg.Control.ListenAddress, "http": cfg.Control.HTTP},
			"runtime": map[string]any{"endpoint": cfg.Runtime.Endpoint},
			"server":  server,
			"auth":    map[string]any{"enabled": cfg.Auth.Enabled, "type": cfg.Auth.Type, "secrets_backend": cfg.Auth.SecretsBackend, "username": cfg.Auth.Username, "token_secret": cfg.Auth.TokenSecret},
		}
	case domain.ComponentRoleRuntime:
		server["data_dir"] = "/var/lib/gordon"
		return map[string]any{"server": server, "volumes": cfg.Volumes}
	case domain.ComponentRoleRegistry:
		server["data_dir"] = "/var/lib/gordon"
		server["registry_port"] = cfg.Server.RegistryPort
		server["registry_domain"] = cfg.Server.RegistryDomain
		// Registry receives its scoped credentials through its role env file;
		// do not copy control authentication references into this manifest.
		return map[string]any{"server": server}
	case domain.ComponentRoleEdge:
		server["port"] = cfg.Server.Port
		server["tls_port"] = cfg.Server.TLSPort
		server["registry_port"] = cfg.Server.RegistryPort
		server["gordon_domain"] = cfg.Server.GordonDomain
		server["registry_domain"] = cfg.Server.RegistryDomain
		return map[string]any{"server": server, "entrypoints": cfg.EntryPoints, "traffic": cfg.Traffic, "tls": cfg.TLS}
	default:
		return map[string]any{"server": server}
	}
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
