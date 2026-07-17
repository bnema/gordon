package app

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	edgeTLSModeFiles    = "files"
	edgeTLSModeExternal = "external"
)

// EdgeConfig is the complete configuration contract for the edge role. It is
// intentionally separate from Config: an edge process must never deserialize
// control-plane, runtime, storage, or secret configuration.
type EdgeConfig struct {
	Control EdgeControlConfig `toml:"control"`
	Edge    EdgeServingConfig `toml:"edge"`
	Logging EdgeLoggingConfig `toml:"logging"`
}

// EdgeControlConfig contains only the credentials and transport contract used
// to consume the control-owned route snapshot stream.
type EdgeControlConfig struct {
	Endpoint    string `toml:"endpoint"`
	Token       string `toml:"token"`
	TokenEnv    string `toml:"token_env"`
	InsecureTLS bool   `toml:"insecure_tls"`
}

// EdgeServingConfig describes the public listener. TLS is deliberately not
// inferred: files terminates TLS locally; external accepts cleartext only from
// the configured terminating proxy CIDRs.
type EdgeServingConfig struct {
	ListenAddress        string        `toml:"listen_address"`
	RegistryDomain       string        `toml:"registry_domain"`
	MaxProxyBodySize     string        `toml:"max_proxy_body_size"`
	MaxProxyResponseSize string        `toml:"max_proxy_response_size"`
	MaxConcurrentConns   int           `toml:"max_concurrent_connections"`
	TrustedProxyCIDRs    []string      `toml:"trusted_proxy_cidrs"`
	TLS                  EdgeTLSConfig `toml:"tls"`
}

type EdgeTLSConfig struct {
	Mode     string `toml:"mode"`
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
}

type EdgeLoggingConfig struct {
	AccessLog EdgeAccessLogConfig `toml:"access_log"`
}

type EdgeAccessLogConfig struct {
	Enabled             bool   `toml:"enabled"`
	Format              string `toml:"format"`
	Output              string `toml:"output"`
	FilePath            string `toml:"file_path"`
	MaxSize             int    `toml:"max_size"`
	MaxBackups          int    `toml:"max_backups"`
	MaxAge              int    `toml:"max_age"`
	ExcludeHealthChecks bool   `toml:"exclude_health_checks"`
	SyslogIdentifier    string `toml:"syslog_identifier"`
}

func defaultEdgeConfig() EdgeConfig {
	var cfg EdgeConfig
	cfg.Edge.MaxConcurrentConns = -1
	cfg.Logging.AccessLog.Format = "json"
	cfg.Logging.AccessLog.Output = "stdout"
	cfg.Logging.AccessLog.MaxSize = 100
	cfg.Logging.AccessLog.MaxBackups = 3
	cfg.Logging.AccessLog.MaxAge = 28
	cfg.Logging.AccessLog.ExcludeHealthChecks = true
	cfg.Logging.AccessLog.SyslogIdentifier = "gordon-edge-access"
	return cfg
}

// initEdgeConfig strictly decodes the edge-only TOML schema. Unknown sections
// are rejected rather than silently discarded, preventing a full Gordon config
// (which can contain secrets) from being supplied to an edge process.
func initEdgeConfig(configPath string) (EdgeConfig, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return EdgeConfig{}, fmt.Errorf("open edge config: %w", err)
	}
	defer file.Close()

	cfg := defaultEdgeConfig()
	decoder := toml.NewDecoder(file).DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return EdgeConfig{}, fmt.Errorf("decode edge config: %w", err)
	}
	if err := validateEdgeConfig(cfg); err != nil {
		return EdgeConfig{}, err
	}
	return cfg, nil
}

func validateEdgeConfig(cfg EdgeConfig) error {
	if strings.TrimSpace(cfg.Control.Endpoint) == "" {
		return fmt.Errorf("control.endpoint is required for edge role")
	}
	if strings.TrimSpace(cfg.Control.Token) == "" && strings.TrimSpace(cfg.Control.TokenEnv) == "" {
		return fmt.Errorf("control.token or control.token_env is required for edge role")
	}
	if strings.TrimSpace(cfg.Edge.ListenAddress) == "" {
		return fmt.Errorf("edge.listen_address is required")
	}
	if _, err := net.ResolveTCPAddr("tcp", cfg.Edge.ListenAddress); err != nil {
		return fmt.Errorf("invalid edge.listen_address: %w", err)
	}

	if err := validateEdgeTrustedProxyCIDRs(cfg.Edge.TrustedProxyCIDRs); err != nil {
		return err
	}
	return validateEdgeTLSContract(cfg)
}

func validateEdgeTrustedProxyCIDRs(cidrs []string) error {
	for _, cidr := range cidrs {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err != nil {
			return fmt.Errorf("invalid edge.trusted_proxy_cidrs entry %q: %w", cidr, err)
		}
	}
	return nil
}

func validateEdgeTLSContract(cfg EdgeConfig) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.Edge.TLS.Mode))
	if mode == edgeTLSModeFiles {
		return validateEdgeFileTLS(cfg)
	}
	if mode == edgeTLSModeExternal {
		return validateEdgeExternalTLS(cfg)
	}
	return fmt.Errorf("edge.tls.mode must be %q or %q", edgeTLSModeFiles, edgeTLSModeExternal)
}

func validateEdgeFileTLS(cfg EdgeConfig) error {
	if strings.TrimSpace(cfg.Edge.TLS.CertFile) == "" || strings.TrimSpace(cfg.Edge.TLS.KeyFile) == "" {
		return fmt.Errorf("edge.tls.mode=files requires edge.tls.cert_file and edge.tls.key_file")
	}
	_, err := edgeTLSConfig(cfg)
	return err
}

func validateEdgeExternalTLS(cfg EdgeConfig) error {
	if strings.TrimSpace(cfg.Edge.TLS.CertFile) != "" || strings.TrimSpace(cfg.Edge.TLS.KeyFile) != "" {
		return fmt.Errorf("edge.tls.mode=external must not set edge.tls.cert_file or edge.tls.key_file")
	}
	if len(cfg.Edge.TrustedProxyCIDRs) == 0 {
		return fmt.Errorf("edge.tls.mode=external requires edge.trusted_proxy_cidrs to restrict plaintext to terminating proxies")
	}
	return nil
}

func edgeTLSConfig(cfg EdgeConfig) (*tls.Config, error) {
	if strings.ToLower(strings.TrimSpace(cfg.Edge.TLS.Mode)) != edgeTLSModeFiles {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(cfg.Edge.TLS.CertFile, cfg.Edge.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load edge TLS keypair: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}
