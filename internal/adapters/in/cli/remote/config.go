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
	Remotes map[string]RemoteEntry `toml:"remotes"`
	Active  string                 `toml:"active"` // Active remote name
}

// ClientSettings represents the [client] section.
type ClientSettings struct {
	Mode     string `toml:"mode"`      // "local" (default) or "remote"
	Remote   string `toml:"remote"`    // Remote Gordon URL
	Token    string `toml:"token"`     // Auth token
	TokenEnv string `toml:"token_env"` // Env var name for token
}

// RemoteEntry represents a saved remote in [remotes.*].
type RemoteEntry struct {
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

// DefaultRemotesPath returns the default remotes config path.
func DefaultRemotesPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("HOME")
	}
	return filepath.Join(configDir, "gordon", "remotes.toml")
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
				Remotes: make(map[string]RemoteEntry),
			}, nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config ClientConfig
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if config.Remotes == nil {
		config.Remotes = make(map[string]RemoteEntry)
	}

	return &config, nil
}

// LoadRemotes loads the remotes configuration.
func LoadRemotes(path string) (*ClientConfig, error) {
	if path == "" {
		path = DefaultRemotesPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ClientConfig{
				Remotes: make(map[string]RemoteEntry),
			}, nil
		}
		return nil, fmt.Errorf("failed to read remotes: %w", err)
	}

	var config ClientConfig
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse remotes: %w", err)
	}

	if config.Remotes == nil {
		config.Remotes = make(map[string]RemoteEntry)
	}

	return &config, nil
}

// SaveRemotes saves the remotes configuration.
func SaveRemotes(path string, config *ClientConfig) error {
	if path == "" {
		path = DefaultRemotesPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal remotes: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write remotes: %w", err)
	}

	return nil
}

// ResolveRemote resolves the remote URL and token from configuration.
// Precedence: flag > env > config > active remote
func ResolveRemote(flagRemote, flagToken string) (url, token string, isRemote bool) {
	config, _ := LoadClientConfig("")
	remotes, _ := LoadRemotes("")

	url, isRemote = resolveRemoteURL(flagRemote, config, remotes)
	if isRemote {
		token = resolveToken(flagToken, config, remotes)
	}

	return url, token, isRemote
}

// resolveRemoteURL resolves the remote URL from various sources.
func resolveRemoteURL(flagRemote string, config *ClientConfig, remotes *ClientConfig) (string, bool) {
	// 1. Check flag
	if flagRemote != "" {
		return flagRemote, true
	}

	// 2. Check environment variable
	if envRemote := os.Getenv("GORDON_REMOTE"); envRemote != "" {
		return envRemote, true
	}

	// 3. Check client config
	if config != nil && config.Client.Mode == "remote" && config.Client.Remote != "" {
		return config.Client.Remote, true
	}

	// 4. Check active remote
	if remotes != nil && remotes.Active != "" {
		if remote, ok := remotes.Remotes[remotes.Active]; ok {
			return remote.URL, true
		}
	}

	return "", false
}

// resolveToken resolves the authentication token from various sources.
func resolveToken(flagToken string, config *ClientConfig, remotes *ClientConfig) string {
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

	// 4. Active remote token
	if remotes != nil && remotes.Active != "" {
		if remote, ok := remotes.Remotes[remotes.Active]; ok {
			if remote.Token != "" {
				return remote.Token
			}
			if remote.TokenEnv != "" {
				return os.Getenv(remote.TokenEnv)
			}
		}
	}

	return ""
}

// AddRemote adds a new remote to the remotes configuration.
func AddRemote(name, url, token string) error {
	config, err := LoadRemotes("")
	if err != nil {
		return err
	}

	config.Remotes[name] = RemoteEntry{
		URL:   url,
		Token: token,
	}

	return SaveRemotes("", config)
}

// RemoveRemote removes a remote from the remotes configuration.
func RemoveRemote(name string) error {
	config, err := LoadRemotes("")
	if err != nil {
		return err
	}

	delete(config.Remotes, name)

	// Clear active if it was the removed remote
	if config.Active == name {
		config.Active = ""
	}

	return SaveRemotes("", config)
}

// SetActiveRemote sets the active remote.
func SetActiveRemote(name string) error {
	config, err := LoadRemotes("")
	if err != nil {
		return err
	}

	// Verify remote exists
	if _, ok := config.Remotes[name]; !ok {
		return fmt.Errorf("remote '%s' not found", name)
	}

	config.Active = name

	return SaveRemotes("", config)
}

// ListRemotes returns all saved remotes.
func ListRemotes() (map[string]RemoteEntry, string, error) {
	config, err := LoadRemotes("")
	if err != nil {
		return nil, "", err
	}

	return config.Remotes, config.Active, nil
}
