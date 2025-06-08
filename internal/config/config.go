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
	Server       ServerConfig      `mapstructure:"server"`
	RegistryAuth RegistryAuthConfig `mapstructure:"registry_auth"`
	Routes       map[string]string `mapstructure:"routes"`
	AutoRoute    AutoRouteConfig   `mapstructure:"auto_route"`
	Env          EnvConfig         `mapstructure:"env"`
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

	// Handle the routes manually since Viper struggles with domain names
	cfg.Routes = make(map[string]string)
	
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