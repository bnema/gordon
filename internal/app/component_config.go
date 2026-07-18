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
	configusecase "github.com/bnema/gordon/internal/usecase/config"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
)

// ComponentConfigFile is a role-scoped, non-secret configuration reference.
// Its content is intentionally never included in migration status or errors.
type ComponentConfigFile struct {
	Role domain.ComponentRole `json:"role"`
	Path string               `json:"path"`
}

// ComponentConfigOptions contains the narrow, non-secret configuration copied
// into generated role manifests. ExternalRoutes must be the raw value loaded
// from Viper, not an arbitrary config file or environment value.
type ComponentConfigOptions struct {
	ExternalRoutes   any
	FinalEdgeBinding *MigrationPortBinding
}

// WriteComponentConfigManifests materializes only the configuration each role
// needs. Files are private, atomically replaced, and live beneath the
// migration directory so the runtime can reject arbitrary host binds.
func WriteComponentConfigManifests(cfg Config, directory string, options ...ComponentConfigOptions) ([]ComponentConfigFile, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("only one component configuration option is allowed")
	}
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
	if err := normalizeComponentServingLimits(&cfg); err != nil {
		return nil, err
	}
	controlListenAddress, controlEndpoint, err := componentControlNetworking(cfg.Control.ListenAddress)
	if err != nil {
		return nil, err
	}
	externalRoutes, finalEdgeBinding, err := componentManifestOptions(options, cfg.Server.Port)
	if err != nil {
		return nil, err
	}
	controlRouting, err := componentControlRoutingConfig(cfg, externalRoutes)
	if err != nil {
		return nil, err
	}
	files := make([]ComponentConfigFile, 0, len(componentRoles))
	for _, role := range componentRoles {
		data, err := toml.Marshal(componentRoleConfig(cfg, role, migrationID, controlListenAddress, controlEndpoint, controlRouting, true, ""))
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
	finalEdge, err := toml.Marshal(componentRoleConfig(cfg, domain.ComponentRoleEdge, migrationID, controlListenAddress, controlEndpoint, controlRouting, false, finalEdgeHostIP(finalEdgeBinding)))
	if err != nil {
		return nil, fmt.Errorf("encode final edge component configuration: %w", err)
	}
	if err := writePrivateAtomicFile(filepath.Join(directory, "edge-final.toml"), finalEdge); err != nil {
		return nil, fmt.Errorf("write final edge component configuration: %w", err)
	}
	return files, nil
}

func finalEdgeHostIP(binding *MigrationPortBinding) string {
	if binding == nil {
		return ""
	}
	return binding.HostIP
}

func componentManifestOptions(options []ComponentConfigOptions, edgePort int) (any, *MigrationPortBinding, error) {
	if len(options) == 0 {
		return nil, nil, nil
	}
	binding := options[0].FinalEdgeBinding
	if binding != nil && !validFinalEdgeConfigBinding(*binding, edgePort) {
		return nil, nil, fmt.Errorf("invalid final edge listener binding")
	}
	return options[0].ExternalRoutes, binding, nil
}

// normalizeComponentServingLimits translates omitted monolith settings into the
// explicit values strict component role manifests require. Non-empty values are
// validated, rather than replaced, so a malformed operator configuration stops
// migration before any component is launched.
func normalizeComponentServingLimits(cfg *Config) error {
	defaults := defaultRegistryConfig()
	if strings.TrimSpace(cfg.Server.MaxBlobChunkSize) == "" {
		cfg.Server.MaxBlobChunkSize = defaults.Limits.MaxBlobChunkSize
	}
	if strings.TrimSpace(cfg.Server.MaxBlobSize) == "" {
		cfg.Server.MaxBlobSize = defaults.Limits.MaxBlobSize
	}
	chunk, err := registrySize(cfg.Server.MaxBlobChunkSize, "server.max_blob_chunk_size")
	if err != nil {
		return err
	}
	total, err := registrySize(cfg.Server.MaxBlobSize, "server.max_blob_size")
	if err != nil {
		return err
	}
	if total < chunk {
		return fmt.Errorf("server.max_blob_size must be greater than or equal to server.max_blob_chunk_size")
	}
	if strings.TrimSpace(cfg.Server.MaxProxyBodySize) == "" {
		cfg.Server.MaxProxyBodySize = "512MB"
	}
	if strings.TrimSpace(cfg.Server.MaxProxyResponseSize) == "" {
		cfg.Server.MaxProxyResponseSize = "1GB"
	}
	if _, err := edgeByteSize(cfg.Server.MaxProxyBodySize, 512<<20, "server.max_proxy_body_size"); err != nil {
		return err
	}
	if _, err := edgeByteSize(cfg.Server.MaxProxyResponseSize, 1<<30, "server.max_proxy_response_size"); err != nil {
		return err
	}
	return nil
}

func validFinalEdgeConfigBinding(binding MigrationPortBinding, edgePort int) bool {
	return validMigrationPortBinding(binding) && binding.Role == string(domain.ComponentRoleEdge) && binding.Protocol == "tcp" &&
		binding.HostPort == edgePort && binding.ContainerPort == edgePort
}

func componentRoleConfig(cfg Config, role domain.ComponentRole, migrationID, controlListenAddress, controlEndpoint string, controlRouting map[string]any, migrationProbeEnabled bool, publishedHostIP string) map[string]any {
	// These are role contracts, not filtered copies of Config. In particular,
	// edge and registry use strict TOML decoders and must never receive legacy
	// server/auth sections (which may contain control-plane secret references).
	switch role {
	case domain.ComponentRoleControl:
		control := map[string]any{
			// Components share an isolated network; binding wildcard here permits
			// the edge's gordon-control DNS alias without publishing a host port.
			"control": map[string]any{"listen_address": controlListenAddress, "insecure_tls": true, "edge_alias": cfg.Control.EdgeAlias, "registry_alias": cfg.Control.RegistryAlias, "registry_port": cfg.Server.RegistryPort, "drain_registration_timeout": cfg.Control.DrainRegistrationTimeout},
			// Control reaches the new runtime only through its private Gordon RPC
			// socket. This is not, and cannot be confused with, the Podman socket.
			"runtime": map[string]any{"endpoint": migrationRuntimeSocketEndpoint(migrationID), "token_env": "GORDON_COMPONENT_RUNTIME_TOKEN"},
			// Split control uses its own data volume. The only shared migration
			// state is the narrowly mounted /var/lib/gordon/migration child.
			"server": map[string]any{"data_dir": componentDataDirectory},
			// Token references and provider credentials remain in the private role
			// environment/secrets store, never in a generated TOML manifest.
			"auth": map[string]any{"enabled": cfg.Auth.Enabled, "type": cfg.Auth.Type, "secrets_backend": cfg.Auth.SecretsBackend},
		}
		for key, value := range controlRouting {
			if key == "server" {
				server := control["server"].(map[string]any)
				for serverKey, serverValue := range value.(map[string]any) {
					server[serverKey] = serverValue
				}
				continue
			}
			control[key] = value
		}
		return control
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
			"storage":    map[string]any{"data_dir": "/var/lib/gordon"},
			"auth":       map[string]any{"enabled": cfg.Auth.Enabled, "type": cfg.Auth.Type, "secrets_backend": cfg.Auth.SecretsBackend, "username": cfg.Auth.Username},
			"limits":     map[string]any{"max_blob_chunk_size": cfg.Server.MaxBlobChunkSize, "max_blob_size": cfg.Server.MaxBlobSize},
			"listen":     map[string]any{"address": "0.0.0.0:" + fmt.Sprint(cfg.Server.RegistryPort), "tls": map[string]any{"mode": registryTLSDisabled}},
			"forwarding": map[string]any{"token_env": registryForwardTokenEnvVar},
			"control":    map[string]any{"event_endpoint": controlEndpoint, "event_token_env": "GORDON_COMPONENT_REGISTRY_TOKEN", "insecure_tls": true, "outbox_max_entries": 10000, "outbox_max_bytes": "64MB"},
		}
	case domain.ComponentRoleEdge:
		// Only a generated final manifest whose typed runtime publish is confined
		// to loopback enables rootless hairpin admission. Public host bindings
		// never gain an unauthenticated plaintext path.
		hostIP := net.ParseIP(publishedHostIP)
		hairpinEnabled := !migrationProbeEnabled && hostIP != nil && hostIP.IsLoopback()
		edge := map[string]any{"listen_address": "0.0.0.0:" + fmt.Sprint(cfg.Server.Port), "published_host_ip": publishedHostIP, "registry_domain": cfg.Server.RegistryDomain, "max_proxy_body_size": cfg.Server.MaxProxyBodySize, "max_proxy_response_size": cfg.Server.MaxProxyResponseSize, "max_concurrent_connections": cfg.Server.MaxConcurrentConns, "trusted_proxy_cidrs": []string{"127.0.0.1/32"}, "migration_probe_enabled": migrationProbeEnabled, "migration_hairpin_enabled": hairpinEnabled, "registry_forward_token_env": registryForwardTokenEnvVar, "tls": map[string]any{"mode": edgeTLSModeExternal}}
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

// componentControlRoutingConfig copies only values needed to construct control
// route and traffic snapshots. It deliberately builds maps rather than copying
// Config so TLS files, runtime authority, provider settings, service secrets,
// and environment values cannot reach the control container.
func componentControlRoutingConfig(cfg Config, rawExternalRoutes any) (map[string]any, error) {
	externalRoutes, err := safeComponentExternalRoutes(rawExternalRoutes)
	if err != nil {
		return nil, err
	}
	entryPoints := make(map[string]any, len(cfg.EntryPoints))
	for name, entry := range cfg.EntryPoints {
		entryPoints[name] = map[string]any{
			"address": entry.Address, "protocol": entry.Protocol, "trusted_cidrs": entry.TrustedCIDRs,
			"raw_fallback": entry.RawFallback, "raw_fallback_trusted_cidrs": entry.RawFallbackTrustedCIDRs,
			"allow_public_raw_fallback": entry.AllowPublicRawFallback,
		}
	}
	tcpRouters := make([]map[string]any, 0, len(cfg.Traffic.TCP.Routers))
	for _, router := range cfg.Traffic.TCP.Routers {
		tcpRouters = append(tcpRouters, safeComponentRouter(router.Name, router.EntryPoint, router.Host, router.SNI, router.Service))
	}
	udpRouters := make([]map[string]any, 0, len(cfg.Traffic.UDP.Routers))
	for _, router := range cfg.Traffic.UDP.Routers {
		udpRouters = append(udpRouters, safeComponentRouter(router.Name, router.EntryPoint, router.Host, router.SNI, router.Service))
	}
	tlsRouters := make([]map[string]any, 0, len(cfg.Traffic.TLS.Routers))
	for _, router := range cfg.Traffic.TLS.Routers {
		tlsRouters = append(tlsRouters, safeComponentRouter(router.Name, router.EntryPoint, router.Host, router.SNI, router.Service))
	}
	return map[string]any{
		"server": map[string]any{
			"port": cfg.Server.Port, "registry_port": cfg.Server.RegistryPort,
			"gordon_domain": cfg.Server.GordonDomain, "registry_domain": cfg.Server.RegistryDomain,
			"legacy_registry_domains": cfg.Server.LegacyRegistryDomains,
			"max_proxy_body_size":     cfg.Server.MaxProxyBodySize, "max_proxy_response_size": cfg.Server.MaxProxyResponseSize,
			"max_concurrent_connections": cfg.Server.MaxConcurrentConns,
		},
		"entrypoints": entryPoints,
		"traffic": map[string]any{
			"tcp": map[string]any{"routers": tcpRouters, "dial_timeout": cfg.Traffic.TCP.DialTimeout, "idle_timeout": cfg.Traffic.TCP.IdleTimeout, "drain_timeout": cfg.Traffic.TCP.DrainTimeout, "max_connections": cfg.Traffic.TCP.MaxConnections},
			"udp": map[string]any{"routers": udpRouters, "idle_timeout": cfg.Traffic.UDP.IdleTimeout, "drain_timeout": cfg.Traffic.UDP.DrainTimeout, "max_sessions": cfg.Traffic.UDP.MaxSessions},
			"tls": map[string]any{"routers": tlsRouters},
		},
		"network_services": safeComponentNetworkServices(cfg),
		"services":         safeComponentServices(cfg),
		"external_routes":  externalRoutes,
	}, nil
}

func safeComponentExternalRoutes(raw any) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	routes, err := configusecase.LoadExternalRoutes(raw)
	if err != nil {
		return nil, err
	}
	domains := make([]string, 0, len(routes))
	for routeDomain := range routes {
		domains = append(domains, routeDomain)
	}
	sort.Strings(domains)
	safe := make(map[string]string, len(domains))
	for _, routeDomain := range domains {
		if _, err := proxyusecase.ResolveExternalRouteTarget(routeDomain, routes[routeDomain], 1); err != nil {
			return nil, fmt.Errorf("external route %q: %w", routeDomain, err)
		}
		safe[routeDomain] = routes[routeDomain]
	}
	return safe, nil
}

func safeComponentRouter(name, entryPoint, host, sni, service string) map[string]any {
	return map[string]any{"name": name, "entrypoint": entryPoint, "host": host, "sni": sni, "service": service}
}

func safeComponentNetworkServices(cfg Config) []map[string]any {
	services := make([]map[string]any, 0, len(cfg.NetworkServices))
	for _, service := range cfg.NetworkServices {
		ports := make([]map[string]any, 0, len(service.Ports))
		for _, port := range service.Ports {
			ports = append(ports, map[string]any{"name": port.Name, "container": port.Container, "protocol": port.Protocol})
		}
		services = append(services, map[string]any{"name": service.Name, "ports": ports})
	}
	return services
}

func safeComponentServices(cfg Config) []map[string]any {
	services := make([]map[string]any, 0, len(cfg.Services))
	for _, service := range cfg.Services {
		ports := make([]map[string]any, 0, len(service.Ports))
		for _, port := range service.Ports {
			ports = append(ports, map[string]any{
				"name": port.Name, "container": port.Container, "protocol": port.Protocol, "publish": port.Publish,
				"private": port.Private, "public": port.Public, "trusted_cidrs": port.TrustedCIDRs,
			})
		}
		// Image and port routing are required by traffic graph validation. Do not
		// carry Env, EnvFile, Secrets, Volumes, or provider-backed readiness data.
		services = append(services, map[string]any{"name": service.Name, "image": service.Image, "enabled": service.Enabled, "ports": ports})
	}
	return services
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
