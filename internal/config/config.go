package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

type Config struct {
	Server       ServerConfig      `mapstructure:"server"`
	RegistryAuth RegistryAuthConfig `mapstructure:"registry_auth"`
	Routes       map[string]string `mapstructure:"routes"`
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
	
	// Get registry auth config
	if err := viper.UnmarshalKey("registry_auth", &cfg.RegistryAuth); err != nil {
		return nil, fmt.Errorf("unable to decode registry auth config: %v", err)
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

	// Validate required fields
	if cfg.Server.SSLEmail == "" {
		return nil, fmt.Errorf("server.ssl_email is required")
	}

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