package remote

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// ClientConfig represents the client-mode configuration.
type ClientConfig struct {
	Client  ClientSettings         `toml:"client"`
	Targets map[string]TargetEntry `toml:"targets"`
	Active  string                 `toml:"active"` // Active target name
}

// ClientSettings represents the [client] section.
type ClientSettings struct {
	Mode     string `toml:"mode"`      // "local" (default) or "remote"
	Target   string `toml:"target"`    // Remote Gordon URL
	Token    string `toml:"token"`     // Auth token
	TokenEnv string `toml:"token_env"` // Env var name for token
}

// TargetEntry represents a saved target in [targets.*].
type TargetEntry struct {
	URL      string `toml:"url"`
	Token    string `toml:"token,omitempty"`
	TokenEnv string `toml:"token_env,omitempty"`
}

// DefaultClientConfigPath returns the default client config path.
func DefaultClientConfigPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("HOME")
	}
	return filepath.Join(configDir, "gordon", "gordon.toml")
}

// DefaultTargetsPath returns the default targets config path.
func DefaultTargetsPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("HOME")
	}
	return filepath.Join(configDir, "gordon", "targets.toml")
}

// LoadClientConfig loads the client configuration.
func LoadClientConfig(path string) (*ClientConfig, error) {
	if path == "" {
		path = DefaultClientConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default config
			return &ClientConfig{
				Client: ClientSettings{
					Mode: "local",
				},
				Targets: make(map[string]TargetEntry),
			}, nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config ClientConfig
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if config.Targets == nil {
		config.Targets = make(map[string]TargetEntry)
	}

	return &config, nil
}

// LoadTargets loads the targets configuration.
func LoadTargets(path string) (*ClientConfig, error) {
	if path == "" {
		path = DefaultTargetsPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ClientConfig{
				Targets: make(map[string]TargetEntry),
			}, nil
		}
		return nil, fmt.Errorf("failed to read targets: %w", err)
	}

	var config ClientConfig
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse targets: %w", err)
	}

	if config.Targets == nil {
		config.Targets = make(map[string]TargetEntry)
	}

	return &config, nil
}

// SaveTargets saves the targets configuration.
func SaveTargets(path string, config *ClientConfig) error {
	if path == "" {
		path = DefaultTargetsPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal targets: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write targets: %w", err)
	}

	return nil
}

// ResolveTarget resolves the target URL and token from configuration.
// Precedence: flag > env > config > active target
func ResolveTarget(flagTarget, flagToken string) (url, token string, isRemote bool) {
	config, _ := LoadClientConfig("")
	targets, _ := LoadTargets("")

	url, isRemote = resolveTargetURL(flagTarget, config, targets)
	if isRemote {
		token = resolveToken(flagToken, config, targets)
	}

	return url, token, isRemote
}

// resolveTargetURL resolves the target URL from various sources.
func resolveTargetURL(flagTarget string, config *ClientConfig, targets *ClientConfig) (string, bool) {
	// 1. Check flag
	if flagTarget != "" {
		return flagTarget, true
	}

	// 2. Check environment variable
	if envTarget := os.Getenv("GORDON_TARGET"); envTarget != "" {
		return envTarget, true
	}

	// 3. Check client config
	if config != nil && config.Client.Mode == "remote" && config.Client.Target != "" {
		return config.Client.Target, true
	}

	// 4. Check active target
	if targets != nil && targets.Active != "" {
		if target, ok := targets.Targets[targets.Active]; ok {
			return target.URL, true
		}
	}

	return "", false
}

// resolveToken resolves the authentication token from various sources.
func resolveToken(flagToken string, config *ClientConfig, targets *ClientConfig) string {
	// 1. Flag token takes precedence
	if flagToken != "" {
		return flagToken
	}

	// 2. Environment variable
	if envToken := os.Getenv("GORDON_TOKEN"); envToken != "" {
		return envToken
	}

	// 3. Config token
	if config != nil {
		if config.Client.Token != "" {
			return config.Client.Token
		}
		if config.Client.TokenEnv != "" {
			return os.Getenv(config.Client.TokenEnv)
		}
	}

	// 4. Active target token
	if targets != nil && targets.Active != "" {
		if target, ok := targets.Targets[targets.Active]; ok {
			if target.Token != "" {
				return target.Token
			}
			if target.TokenEnv != "" {
				return os.Getenv(target.TokenEnv)
			}
		}
	}

	return ""
}

// AddTarget adds a new target to the targets configuration.
func AddTarget(name, url, token string) error {
	config, err := LoadTargets("")
	if err != nil {
		return err
	}

	config.Targets[name] = TargetEntry{
		URL:   url,
		Token: token,
	}

	return SaveTargets("", config)
}

// RemoveTarget removes a target from the targets configuration.
func RemoveTarget(name string) error {
	config, err := LoadTargets("")
	if err != nil {
		return err
	}

	delete(config.Targets, name)

	// Clear active if it was the removed target
	if config.Active == name {
		config.Active = ""
	}

	return SaveTargets("", config)
}

// SetActiveTarget sets the active target.
func SetActiveTarget(name string) error {
	config, err := LoadTargets("")
	if err != nil {
		return err
	}

	// Verify target exists
	if _, ok := config.Targets[name]; !ok {
		return fmt.Errorf("target '%s' not found", name)
	}

	config.Active = name

	return SaveTargets("", config)
}

// ListTargets returns all saved targets.
func ListTargets() (map[string]TargetEntry, string, error) {
	config, err := LoadTargets("")
	if err != nil {
		return nil, "", err
	}

	return config.Targets, config.Active, nil
}
