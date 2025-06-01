package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server ServerConfig      `mapstructure:"server"`
	Auth   AuthConfig        `mapstructure:"auth"`
	Routes map[string]string `mapstructure:"routes"`
}

type ServerConfig struct {
	Port         int    `mapstructure:"port"`
	RegistryPort int    `mapstructure:"registry_port"`
	Runtime      string `mapstructure:"runtime"`
	SSLEmail     string `mapstructure:"ssl_email"`
	DataDir      string `mapstructure:"data_dir"`
}

type AuthConfig struct {
	Enabled        bool     `mapstructure:"enabled"`
	Method         string   `mapstructure:"method"`         // "jwt", "api_key", or "basic"
	JWTSecret      string   `mapstructure:"jwt_secret"`
	APIKey         string   `mapstructure:"api_key"`
	Username       string   `mapstructure:"username"`
	Password       string   `mapstructure:"password"`
	RegistryAuth   bool     `mapstructure:"registry_auth"`
	AllowedIPs     []string `mapstructure:"allowed_ips"`
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
	viper.SetDefault("auth.enabled", false)
	viper.SetDefault("auth.method", "basic")
	viper.SetDefault("auth.registry_auth", false)
	viper.SetDefault("auth.allowed_ips", []string{"127.0.0.1", "::1"})

	// Handle the routes manually since Viper struggles with domain names
	cfg.Routes = make(map[string]string)
	
	// Get server config first
	if err := viper.UnmarshalKey("server", &cfg.Server); err != nil {
		return nil, fmt.Errorf("unable to decode server config: %v", err)
	}
	
	// Get auth config
	if err := viper.UnmarshalKey("auth", &cfg.Auth); err != nil {
		return nil, fmt.Errorf("unable to decode auth config: %v", err)
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

	// Validate auth config
	if cfg.Auth.Enabled {
		switch cfg.Auth.Method {
		case "jwt":
			if cfg.Auth.JWTSecret == "" {
				return nil, fmt.Errorf("JWT method selected but no jwt_secret provided")
			}
		case "api_key":
			if cfg.Auth.APIKey == "" {
				return nil, fmt.Errorf("API key method selected but no api_key provided")
			}
		case "basic":
			if cfg.Auth.Username == "" || cfg.Auth.Password == "" {
				return nil, fmt.Errorf("Basic auth method selected but username/password not provided")
			}
		default:
			return nil, fmt.Errorf("invalid auth method: %s (must be 'jwt', 'api_key', or 'basic')", cfg.Auth.Method)
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