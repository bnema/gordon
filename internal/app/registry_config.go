package app

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/bnema/gordon/pkg/bytesize"
)

const (
	registryTLSDisabled = "disabled"
	registryTLSFiles    = "files"
)

// RegistryConfig is deliberately not Config. Strict decoding prevents a
// registry process from receiving control-plane, runtime, proxy, or admin
// configuration and their credentials.
type RegistryConfig struct {
	Storage    RegistryStorageConfig    `toml:"storage"`
	Auth       RegistryAuthConfig       `toml:"auth"`
	Limits     RegistryLimitsConfig     `toml:"limits"`
	Listen     RegistryListenConfig     `toml:"listen"`
	Control    RegistryControlConfig    `toml:"control"`
	Forwarding RegistryForwardingConfig `toml:"forwarding"`
}
type RegistryStorageConfig struct {
	DataDir string `toml:"data_dir"`
}
type RegistryAuthConfig struct {
	Enabled        bool   `toml:"enabled"`
	Type           string `toml:"type"`
	SecretsBackend string `toml:"secrets_backend"`
	Username       string `toml:"username"`
	TokenSecret    string `toml:"token_secret"`
	TokenExpiry    string `toml:"token_expiry"`
	AccessTokenTTL string `toml:"access_token_ttl"`
}
type RegistryLimitsConfig struct {
	MaxBlobChunkSize string   `toml:"max_blob_chunk_size"`
	MaxBlobSize      string   `toml:"max_blob_size"`
	AllowedIPs       []string `toml:"allowed_ips"`
}
type RegistryListenConfig struct {
	Address string            `toml:"address"`
	TLS     RegistryTLSConfig `toml:"tls"`
}
type RegistryTLSConfig struct {
	Mode     string `toml:"mode"`
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
}
type RegistryForwardingConfig struct {
	TokenEnv string `toml:"token_env"`
}

type RegistryControlConfig struct {
	EventEndpoint    string `toml:"event_endpoint"`
	EventToken       string `toml:"event_token"`
	EventTokenEnv    string `toml:"event_token_env"`
	InsecureTLS      bool   `toml:"insecure_tls"`
	OutboxMaxEntries int    `toml:"outbox_max_entries"`
	OutboxMaxBytes   string `toml:"outbox_max_bytes"`
}

func defaultRegistryConfig() RegistryConfig {
	return RegistryConfig{Storage: RegistryStorageConfig{DataDir: DefaultDataDir()}, Limits: RegistryLimitsConfig{MaxBlobChunkSize: "95MB", MaxBlobSize: "1GB"}, Listen: RegistryListenConfig{Address: "127.0.0.1:5000", TLS: RegistryTLSConfig{Mode: registryTLSDisabled}}, Control: RegistryControlConfig{OutboxMaxEntries: 10000, OutboxMaxBytes: "64MB"}}
}

func initRegistryConfig(path string) (RegistryConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return RegistryConfig{}, fmt.Errorf("open registry config: %w", err)
	}
	defer file.Close()
	cfg := defaultRegistryConfig()
	decoder := toml.NewDecoder(file).DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return RegistryConfig{}, fmt.Errorf("decode registry config: %w", err)
	}
	if err := validateRegistryConfig(cfg); err != nil {
		return RegistryConfig{}, err
	}
	return cfg, nil
}
func validateRegistryConfig(cfg RegistryConfig) error {
	for _, validate := range []func(RegistryConfig) error{validateRegistryServing, validateRegistryLimits, validateRegistryAuth, validateRegistryControl, validateRegistryTLS} {
		if err := validate(cfg); err != nil {
			return err
		}
	}
	return nil
}
func validateRegistryServing(cfg RegistryConfig) error {
	if strings.TrimSpace(cfg.Storage.DataDir) == "" {
		return fmt.Errorf("storage.data_dir is required")
	}
	if strings.TrimSpace(cfg.Listen.Address) == "" {
		return fmt.Errorf("listen.address is required")
	}
	if _, err := net.ResolveTCPAddr("tcp", cfg.Listen.Address); err != nil {
		return fmt.Errorf("invalid listen.address: %w", err)
	}
	return nil
}
func validateRegistryLimits(cfg RegistryConfig) error {
	chunk, err := registrySize(cfg.Limits.MaxBlobChunkSize, "limits.max_blob_chunk_size")
	if err != nil {
		return err
	}
	total, err := registrySize(cfg.Limits.MaxBlobSize, "limits.max_blob_size")
	if err != nil {
		return err
	}
	if total < chunk {
		return fmt.Errorf("limits.max_blob_size must be greater than or equal to limits.max_blob_chunk_size")
	}
	if _, err := registrySize(cfg.Control.OutboxMaxBytes, "control.outbox_max_bytes"); err != nil {
		return err
	}
	if cfg.Control.OutboxMaxEntries <= 0 {
		return fmt.Errorf("control.outbox_max_entries must be positive")
	}
	if len(cfg.Limits.AllowedIPs) > 0 {
		// A private component network address is not an identity. In particular,
		// do not turn a broad RFC1918 range into registry authorization.
		return fmt.Errorf("limits.allowed_ips is not supported; registry access requires authenticated edge forwarding")
	}
	return nil
}
func validateRegistryAuth(cfg RegistryConfig) error {
	if cfg.Auth.Enabled && strings.TrimSpace(cfg.Auth.TokenSecret) == "" && os.Getenv(TokenSecretEnvVar) == "" {
		return fmt.Errorf("auth.token_secret or %s is required when auth is enabled", TokenSecretEnvVar)
	}
	if cfg.Auth.Type != "" && cfg.Auth.Type != "token" {
		return fmt.Errorf("unsupported auth.type %q; only token is supported", cfg.Auth.Type)
	}
	return nil
}
func validateRegistryControl(cfg RegistryConfig) error {
	if strings.TrimSpace(cfg.Control.EventEndpoint) == "" {
		return fmt.Errorf("control.event_endpoint is required")
	}
	if strings.TrimSpace(cfg.Control.EventToken) == "" && strings.TrimSpace(cfg.Control.EventTokenEnv) == "" {
		return fmt.Errorf("control.event_token or control.event_token_env is required")
	}
	return nil
}
func validateRegistryTLS(cfg RegistryConfig) error {
	switch strings.ToLower(strings.TrimSpace(cfg.Listen.TLS.Mode)) {
	case registryTLSDisabled:
		if cfg.Listen.TLS.CertFile != "" || cfg.Listen.TLS.KeyFile != "" {
			return fmt.Errorf("listen.tls.mode=disabled must not set certificate files")
		}
	case registryTLSFiles:
		if cfg.Listen.TLS.CertFile == "" || cfg.Listen.TLS.KeyFile == "" {
			return fmt.Errorf("listen.tls.mode=files requires listen.tls.cert_file and listen.tls.key_file")
		}
		if _, err := tls.LoadX509KeyPair(cfg.Listen.TLS.CertFile, cfg.Listen.TLS.KeyFile); err != nil {
			return fmt.Errorf("load registry TLS keypair: %w", err)
		}
	default:
		return fmt.Errorf("listen.tls.mode must be %q or %q", registryTLSDisabled, registryTLSFiles)
	}
	return nil
}
func registrySize(raw, name string) (int64, error) {
	size, err := bytesize.Parse(raw)
	if err != nil || size <= 0 {
		if err == nil {
			err = fmt.Errorf("must be positive")
		}
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return size, nil
}
