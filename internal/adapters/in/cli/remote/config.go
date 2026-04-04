package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/bnema/gordon/internal/domain"
)

// ClientConfig represents the client-mode configuration.
type ClientConfig struct {
	Remotes map[string]RemoteEntry `toml:"remotes"`
	Active  string                 `toml:"active"` // Active remote name
}

// RemoteEntry represents a saved remote in [remotes.*].
type RemoteEntry struct {
	URL         string `toml:"url"`
	Token       string `toml:"token,omitempty"`
	TokenEnv    string `toml:"token_env,omitempty"`
	InsecureTLS bool   `toml:"insecure_tls,omitempty"`
}

// DefaultRemotesPath returns the default remotes config path.
func DefaultRemotesPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("HOME")
	}
	return filepath.Join(configDir, "gordon", "remotes.toml")
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

func resolveRemoteEntryToken(remote *RemoteEntry) string {
	if remote == nil {
		return ""
	}
	if remote.Token != "" {
		return remote.Token
	}
	if remote.TokenEnv != "" {
		return os.Getenv(remote.TokenEnv)
	}
	return ""
}

// ResolveTokenForRemote resolves the token for a named remote.
// Precedence: TOML token field > token_env > pass store.
func ResolveTokenForRemote(name string, entry RemoteEntry) string {
	if token := resolveRemoteEntryToken(&entry); token != "" {
		return token
	}
	if passAvailable() {
		if token, err := passReadToken(name); err == nil && token != "" {
			return token
		}
	}
	return ""
}

// ResolveInsecureTLSForRemote resolves insecure TLS behavior for a named remote.
// Precedence: flag > env > specific remote config.
func ResolveInsecureTLSForRemote(flagInsecure bool, remoteName string) bool {
	remotes, _ := LoadRemotes("")
	return resolveInsecureTLS(flagInsecure, remotes, remoteName)
}

func resolveInsecureTLS(flagInsecure bool, remotes *ClientConfig, remoteName string) bool {
	if flagInsecure {
		return true
	}

	if env := os.Getenv("GORDON_INSECURE"); env != "" {
		if value, err := strconv.ParseBool(env); err == nil {
			return value
		}
		fmt.Fprintf(os.Stderr, "WARNING: invalid GORDON_INSECURE value %q, ignoring\n", env)
	}

	if remoteName != "" && remotes != nil {
		if remote, ok := remotes.Remotes[remoteName]; ok && remote.InsecureTLS {
			return true
		}
	}

	return false
}

// ResolvedRemote holds the fully resolved remote target.
// Name is empty for ad-hoc URLs passed directly via flag or env.
type ResolvedRemote struct {
	Name        string
	URL         string
	Token       string
	InsecureTLS bool
}

// Resolve resolves the remote target from flags, environment, and config.
// Returns nil, false when no remote is configured (local mode).
//
// URL precedence: flag > GORDON_REMOTE env > active remote.
// The flag and env values are treated as a saved remote name first;
// if no match and the value starts with http:// or https://, used as ad-hoc URL.
//
// Token precedence: flag > GORDON_TOKEN env > named remote (pass > TOML token > token_env).
// InsecureTLS precedence: flag > GORDON_INSECURE env > remote entry field.
func Resolve(flagRemote, flagToken string, flagInsecure bool) (*ResolvedRemote, bool) {
	remotes, _ := LoadRemotes("")

	name, url, found := resolveTarget(flagRemote, remotes)
	if !found {
		if envRemote := os.Getenv("GORDON_REMOTE"); envRemote != "" {
			name, url, found = resolveTarget(envRemote, remotes)
		}
	}
	if !found {
		if remotes != nil && remotes.Active != "" {
			if entry, ok := remotes.Remotes[remotes.Active]; ok {
				name = remotes.Active
				url = entry.URL
				found = true
			}
		}
	}
	if !found {
		return nil, false
	}

	r := &ResolvedRemote{
		Name:        name,
		URL:         url,
		InsecureTLS: flagInsecure,
	}

	// Token: flag > env > stored
	switch {
	case flagToken != "":
		r.Token = flagToken
	case os.Getenv("GORDON_TOKEN") != "":
		r.Token = os.Getenv("GORDON_TOKEN")
	case name != "":
		if remotes != nil {
			if entry, ok := remotes.Remotes[name]; ok {
				r.Token = ResolveTokenForRemote(name, entry)
			}
		}
	}

	// InsecureTLS: flag already set; check env then entry
	if !r.InsecureTLS {
		if env := os.Getenv("GORDON_INSECURE"); env != "" {
			if v, err := strconv.ParseBool(env); err == nil {
				r.InsecureTLS = v
			}
		}
	}
	if !r.InsecureTLS && name != "" && remotes != nil {
		if entry, ok := remotes.Remotes[name]; ok {
			r.InsecureTLS = entry.InsecureTLS
		}
	}

	return r, true
}

// resolveTarget checks if value is a saved remote name or an ad-hoc URL.
func resolveTarget(value string, remotes *ClientConfig) (name, url string, found bool) {
	if value == "" {
		return "", "", false
	}
	if remotes != nil {
		if entry, ok := remotes.Remotes[value]; ok {
			return value, entry.URL, true
		}
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return "", value, true
	}
	return "", "", false
}

// AddRemote adds a new remote to the remotes configuration.
func AddRemote(name, url, token string, insecureTLS bool) error {
	config, err := LoadRemotes("")
	if err != nil {
		return err
	}

	config.Remotes[name] = RemoteEntry{
		URL:         url,
		Token:       token,
		InsecureTLS: insecureTLS,
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
		return fmt.Errorf("%w: %s", domain.ErrRemoteNotFound, name)
	}

	config.Active = name

	return SaveRemotes("", config)
}

// ClearRemoteToken removes the token and token_env fields for a named remote
// from the remotes TOML config, and deletes the pass store entry if present.
func ClearRemoteToken(name string) error {
	// Delete from pass (best-effort, no error if pass unavailable)
	_ = passDeleteToken(name)

	// Clear TOML fields
	config, err := LoadRemotes("")
	if err != nil {
		return err
	}

	entry, ok := config.Remotes[name]
	if !ok {
		return fmt.Errorf("%w: %s", domain.ErrRemoteNotFound, name)
	}

	entry.Token = ""
	entry.TokenEnv = ""
	config.Remotes[name] = entry

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
