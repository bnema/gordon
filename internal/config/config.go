package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

type Config struct {
	Server           ServerConfig            `mapstructure:"server"`
	RegistryAuth     RegistryAuthConfig      `mapstructure:"registry_auth"`
	Routes           map[string]string       `mapstructure:"routes"`
	AutoRoute        AutoRouteConfig         `mapstructure:"auto_route"`
	Env              EnvConfig               `mapstructure:"env"`
	Logging          LoggingConfig           `mapstructure:"logging"`
	Volumes          VolumeConfig            `mapstructure:"volumes"`
	Attachments      map[string][]string     `mapstructure:"attachments"`
	NetworkGroups    map[string][]string     `mapstructure:"network_groups"`
	NetworkIsolation NetworkIsolationConfig  `mapstructure:"network_isolation"`
}

type ServerConfig struct {
	Port           int    `mapstructure:"port"`
	RegistryPort   int    `mapstructure:"registry_port"`
	RegistryDomain string `mapstructure:"registry_domain"`
	Runtime        string `mapstructure:"runtime"`
	SocketPath     string `mapstructure:"socket_path"`
	SSLEmail       string `mapstructure:"ssl_email"`
	DataDir        string `mapstructure:"data_dir"`
}

type RegistryAuthConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type AutoRouteConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

type EnvConfig struct {
	Dir       string   `mapstructure:"dir"`
	Providers []string `mapstructure:"providers"`
}

type LoggingConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	Level           string `mapstructure:"level"`
	Dir             string `mapstructure:"dir"`
	MainLogFile     string `mapstructure:"main_log_file"`
	ProxyLogFile    string `mapstructure:"proxy_log_file"`
	ContainerLogDir string `mapstructure:"container_log_dir"`
	MaxSize         int    `mapstructure:"max_size"`
	MaxBackups      int    `mapstructure:"max_backups"`
	MaxAge          int    `mapstructure:"max_age"`
	Compress        bool   `mapstructure:"compress"`
}

type VolumeConfig struct {
	AutoCreate bool   `mapstructure:"auto_create"`
	Prefix     string `mapstructure:"prefix"`
	Preserve   bool   `mapstructure:"preserve"`
}

type NetworkIsolationConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	NetworkPrefix string `mapstructure:"network_prefix"`
	DNSSuffix     string `mapstructure:"dns_suffix"`
}

type SecretProvider struct {
	Type   string            `mapstructure:"type"`
	Config map[string]string `mapstructure:"config"`
}

type Route struct {
	Domain string
	Image  string
	HTTPS  bool
}

func Load() (*Config, error) {
	var cfg Config

	// Set defaults
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.registry_port", 5000)
	viper.SetDefault("server.runtime", "auto")
	viper.SetDefault("server.socket_path", "")

	// Set data_dir default based on environment
	defaultDataDir := getDefaultDataDir()
	viper.SetDefault("server.data_dir", defaultDataDir)
	viper.SetDefault("registry_auth.enabled", true)
	viper.SetDefault("auto_route.enabled", false)
	viper.SetDefault("env.dir", filepath.Join(defaultDataDir, "env"))
	viper.SetDefault("env.providers", []string{"pass", "sops"})

	// Logging defaults
	viper.SetDefault("logging.enabled", true)
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.dir", filepath.Join(defaultDataDir, "logs"))
	viper.SetDefault("logging.main_log_file", "gordon.log")
	viper.SetDefault("logging.proxy_log_file", "proxy.log")
	viper.SetDefault("logging.container_log_dir", "containers")
	viper.SetDefault("logging.max_size", 100)  // 100MB
	viper.SetDefault("logging.max_backups", 3) // Keep 3 old files
	viper.SetDefault("logging.max_age", 28)    // 28 days
	viper.SetDefault("logging.compress", true)

	// Volume defaults
	viper.SetDefault("volumes.auto_create", true)
	viper.SetDefault("volumes.prefix", "gordon")
	viper.SetDefault("volumes.preserve", true)

	// Network isolation defaults
	viper.SetDefault("network_isolation.enabled", true)
	viper.SetDefault("network_isolation.network_prefix", "gordon")
	viper.SetDefault("network_isolation.dns_suffix", ".internal")

	// Handle the routes manually since Viper struggles with domain names
	cfg.Routes = make(map[string]string)
	cfg.Attachments = make(map[string][]string)
	cfg.NetworkGroups = make(map[string][]string)

	// Get server config first
	if err := viper.UnmarshalKey("server", &cfg.Server); err != nil {
		return nil, fmt.Errorf("unable to decode server config: %v", err)
	}

	// If data_dir is empty after loading config, use the default
	if cfg.Server.DataDir == "" {
		cfg.Server.DataDir = defaultDataDir
		log.Debug().Str("data_dir", cfg.Server.DataDir).Msg("Config had empty data_dir, using default")
	}

	// If registry_port is 0 after loading config, use the default
	if cfg.Server.RegistryPort == 0 {
		cfg.Server.RegistryPort = 5000
		log.Debug().Int("registry_port", cfg.Server.RegistryPort).Msg("Config had empty registry_port, using default")
	}

	// Get registry auth config
	if err := viper.UnmarshalKey("registry_auth", &cfg.RegistryAuth); err != nil {
		return nil, fmt.Errorf("unable to decode registry auth config: %v", err)
	}

	// Get auto route config
	if err := viper.UnmarshalKey("auto_route", &cfg.AutoRoute); err != nil {
		return nil, fmt.Errorf("unable to decode auto route config: %v", err)
	}

	// Get env config
	if err := viper.UnmarshalKey("env", &cfg.Env); err != nil {
		return nil, fmt.Errorf("unable to decode env config: %v", err)
	}

	// If env.dir is empty after loading config, use the default
	if cfg.Env.Dir == "" {
		cfg.Env.Dir = filepath.Join(cfg.Server.DataDir, "env")
		log.Debug().Str("env_dir", cfg.Env.Dir).Msg("Config had empty env.dir, using default")
	}

	// Get logging config
	if err := viper.UnmarshalKey("logging", &cfg.Logging); err != nil {
		return nil, fmt.Errorf("unable to decode logging config: %v", err)
	}

	// If logging.dir is empty after loading config, use the default
	if cfg.Logging.Dir == "" {
		cfg.Logging.Dir = filepath.Join(cfg.Server.DataDir, "logs")
		log.Debug().Str("logging_dir", cfg.Logging.Dir).Msg("Config had empty logging.dir, using default")
	}

	// Get volumes config
	if err := viper.UnmarshalKey("volumes", &cfg.Volumes); err != nil {
		return nil, fmt.Errorf("unable to decode volumes config: %v", err)
	}

	// Get network isolation config
	if err := viper.UnmarshalKey("network_isolation", &cfg.NetworkIsolation); err != nil {
		return nil, fmt.Errorf("unable to decode network isolation config: %v", err)
	}

	// Get routes manually from the raw config
	routesRaw := viper.Get("routes")
	if routesRaw != nil {
		if routes, ok := routesRaw.(map[string]interface{}); ok {
			for domain, image := range routes {
				if imageStr, ok := image.(string); ok {
					cfg.Routes[domain] = imageStr
				}
			}
		}
	}

	// Get attachments manually from the raw config
	attachmentsRaw := viper.Get("attachments")
	if attachmentsRaw != nil {
		if attachments, ok := attachmentsRaw.(map[string]interface{}); ok {
			for identifier, services := range attachments {
				if servicesList, ok := services.([]interface{}); ok {
					var serviceStrings []string
					for _, service := range servicesList {
						if serviceStr, ok := service.(string); ok {
							serviceStrings = append(serviceStrings, serviceStr)
						}
					}
					cfg.Attachments[identifier] = serviceStrings
				}
			}
		}
	}

	// Get network_groups manually from the raw config
	networkGroupsRaw := viper.Get("network_groups")
	if networkGroupsRaw != nil {
		if networkGroups, ok := networkGroupsRaw.(map[string]interface{}); ok {
			for groupName, domains := range networkGroups {
				if domainsList, ok := domains.([]interface{}); ok {
					var domainStrings []string
					for _, domain := range domainsList {
						if domainStr, ok := domain.(string); ok {
							domainStrings = append(domainStrings, domainStr)
						}
					}
					cfg.NetworkGroups[groupName] = domainStrings
				}
			}
		}
	}

	// Note: ssl_email is optional and only needed for future Let's Encrypt integration
	// Currently using Cloudflare for HTTPS

	validRuntimes := []string{"auto", "docker", "podman", "podman-rootless"}
	isValid := false
	for _, valid := range validRuntimes {
		if cfg.Server.Runtime == valid {
			isValid = true
			break
		}
	}
	if !isValid {
		return nil, fmt.Errorf("server.runtime must be one of: %s", strings.Join(validRuntimes, ", "))
	}

	// Validate registry auth config
	if cfg.RegistryAuth.Enabled {
		if cfg.RegistryAuth.Username == "" || cfg.RegistryAuth.Password == "" {
			return nil, fmt.Errorf("Registry auth enabled but username/password not provided")
		}
	}

	// Validate registry domain if provided
	if cfg.Server.RegistryDomain != "" {
		// Basic domain validation - should not contain protocol or paths
		if strings.Contains(cfg.Server.RegistryDomain, "://") || strings.Contains(cfg.Server.RegistryDomain, "/") {
			return nil, fmt.Errorf("registry_domain should be just the domain name (e.g. 'registry.example.com')")
		}
	}

	// Validate env config
	if err := validateEnvConfig(&cfg.Env); err != nil {
		return nil, fmt.Errorf("invalid env config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) GetRoutes() []Route {
	var routes []Route

	for domain, image := range c.Routes {
		route := Route{
			Image: image,
			HTTPS: true, // Default to HTTPS
		}

		// Check if domain has http:// prefix
		if strings.HasPrefix(domain, "http://") {
			route.Domain = strings.TrimPrefix(domain, "http://")
			route.HTTPS = false
		} else {
			route.Domain = domain
		}

		routes = append(routes, route)
	}

	return routes
}

// getDefaultDataDir returns a platform-appropriate default data directory
func getDefaultDataDir() string {
	uid := os.Getuid()
	log.Debug().Int("uid", uid).Msg("Detecting default data directory")

	// Check if we're running in a rootless environment
	if uid != 0 {
		// For rootless environments, use user's home directory or current directory
		if homeDir, err := os.UserHomeDir(); err == nil {
			dataDir := filepath.Join(homeDir, ".local/share/gordon")
			log.Debug().Str("data_dir", dataDir).Msg("Using user data directory for rootless environment")
			return dataDir
		}
		log.Debug().Msg("Failed to get user home directory, falling back to ./data")
	} else {
		log.Debug().Msg("Running as root, using ./data")
	}

	// For root or when home directory is not available, use relative path
	return "./data"
}

// ExtractDomainFromImageName extracts domain from image names like "myapp.bamen.dev:latest"
func ExtractDomainFromImageName(imageName string) (string, bool) {
	// Split by colon to separate image name from tag
	parts := strings.Split(imageName, ":")
	imageNamePart := parts[0]

	// Domain pattern: should contain at least one dot and valid domain characters
	domainPattern := regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

	if domainPattern.MatchString(imageNamePart) {
		return imageNamePart, true
	}

	return "", false
}

// AddRoute adds a new route to the configuration and saves it
func (c *Config) AddRoute(domain, image string) error {
	if c.Routes == nil {
		c.Routes = make(map[string]string)
	}

	c.Routes[domain] = image

	// Read the current config file to preserve formatting
	configFile := viper.ConfigFileUsed()
	if configFile == "" {
		return fmt.Errorf("no config file path available")
	}

	// Read existing content
	content, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse and update only the routes section while preserving other formatting
	lines := strings.Split(string(content), "\n")
	var newLines []string
	inRoutesSection := false
	routeAdded := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check if we're entering or leaving the routes section
		if trimmed == "[routes]" {
			inRoutesSection = true
			newLines = append(newLines, line)
			continue
		}

		// Check if we're entering a new section
		if strings.HasPrefix(trimmed, "[") && trimmed != "[routes]" {
			// If we were in routes section and haven't added the route yet, add it
			if inRoutesSection && !routeAdded {
				newLines = append(newLines, fmt.Sprintf("\"%s\" = \"%s\"", domain, image))
				routeAdded = true
			}
			inRoutesSection = false
		}

		// If we're in routes section, check if this route already exists (check both quote styles)
		if inRoutesSection && (strings.Contains(trimmed, fmt.Sprintf("'%s'", domain)) || strings.Contains(trimmed, fmt.Sprintf("\"%s\"", domain))) {
			// Update existing route using double quotes for consistency
			newLines = append(newLines, fmt.Sprintf("\"%s\" = \"%s\"", domain, image))
			routeAdded = true
		} else {
			newLines = append(newLines, line)
		}
	}

	// If we were in routes section at the end and haven't added the route yet, add it
	if inRoutesSection && !routeAdded {
		newLines = append(newLines, fmt.Sprintf("\"%s\" = \"%s\"", domain, image))
	}

	// Write the updated content back
	newContent := strings.Join(newLines, "\n")
	if err := os.WriteFile(configFile, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	log.Info().
		Str("domain", domain).
		Str("image", image).
		Msg("Auto-added route to configuration")

	// Create env file for the new route (if env loader is available)
	// This will be called from the auto-route handler which has access to the env loader

	return nil
}

// UpdateRoute updates an existing route configuration
func (c *Config) UpdateRoute(domain, image string) error {
	if c.Routes == nil {
		c.Routes = make(map[string]string)
	}

	// Check if route exists
	if _, exists := c.Routes[domain]; !exists {
		return fmt.Errorf("route %s does not exist", domain)
	}

	c.Routes[domain] = image

	// Read the current config file to preserve formatting
	configFile := viper.ConfigFileUsed()
	if configFile == "" {
		return fmt.Errorf("no config file path available")
	}

	// Read existing content
	content, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse and update only the routes section while preserving other formatting
	lines := strings.Split(string(content), "\n")
	var newLines []string
	inRoutesSection := false
	routeUpdated := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check if we're entering or leaving the routes section
		if trimmed == "[routes]" {
			inRoutesSection = true
			newLines = append(newLines, line)
			continue
		}

		// Check if we're entering a new section
		if strings.HasPrefix(trimmed, "[") && trimmed != "[routes]" {
			inRoutesSection = false
		}

		// If we're in routes section, check if this route matches (check both quote styles)
		if inRoutesSection && (strings.Contains(trimmed, fmt.Sprintf("'%s'", domain)) || strings.Contains(trimmed, fmt.Sprintf("\"%s\"", domain))) {
			// Update existing route using double quotes for consistency
			newLines = append(newLines, fmt.Sprintf("\"%s\" = \"%s\"", domain, image))
			routeUpdated = true
		} else {
			newLines = append(newLines, line)
		}
	}

	if !routeUpdated {
		return fmt.Errorf("route %s not found in config file", domain)
	}

	// Write the updated content back
	newContent := strings.Join(newLines, "\n")
	if err := os.WriteFile(configFile, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	log.Info().
		Str("domain", domain).
		Str("image", image).
		Msg("Updated route configuration")

	return nil
}

// validateEnvConfig validates the environment configuration
func validateEnvConfig(envCfg *EnvConfig) error {
	// Validate env directory path
	if envCfg.Dir != "" {
		if filepath.IsAbs(envCfg.Dir) {
			// For absolute paths, check if the directory is accessible
			if info, err := os.Stat(envCfg.Dir); err != nil {
				if !os.IsNotExist(err) {
					return fmt.Errorf("env.dir path is not accessible: %w", err)
				}
				// Directory doesn't exist - that's okay, it will be created
			} else if !info.IsDir() {
				return fmt.Errorf("env.dir path exists but is not a directory: %s", envCfg.Dir)
			}
		}
		// For relative paths, we can't easily validate without changing working directory
	}

	// Validate secret providers configuration
	validProviders := []string{"pass", "sops"}
	for _, providerName := range envCfg.Providers {
		if providerName == "" {
			return fmt.Errorf("empty provider name in env.providers")
		}

		isValid := false
		for _, validType := range validProviders {
			if providerName == validType {
				isValid = true
				break
			}
		}
		if !isValid {
			return fmt.Errorf("unsupported provider '%s' in env.providers. Supported providers: %s",
				providerName, strings.Join(validProviders, ", "))
		}
	}

	return nil
}
