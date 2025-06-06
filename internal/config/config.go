package config

import (
	"fmt"
	"strings"

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
	viper.SetDefault("server.runtime", "docker")
	viper.SetDefault("server.data_dir", "./data")
	viper.SetDefault("registry_auth.enabled", true)

	// Handle the routes manually since Viper struggles with domain names
	cfg.Routes = make(map[string]string)
	
	// Get server config first
	if err := viper.UnmarshalKey("server", &cfg.Server); err != nil {
		return nil, fmt.Errorf("unable to decode server config: %v", err)
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

	if cfg.Server.Runtime != "docker" && cfg.Server.Runtime != "podman" {
		return nil, fmt.Errorf("server.runtime must be 'docker' or 'podman'")
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