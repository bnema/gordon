package app

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

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
	ListenAddress        string   `toml:"listen_address"`
	RegistryDomain       string   `toml:"registry_domain"`
	MaxProxyBodySize     string   `toml:"max_proxy_body_size"`
	MaxProxyResponseSize string   `toml:"max_proxy_response_size"`
	MaxConcurrentConns   int      `toml:"max_concurrent_connections"`
	TrustedProxyCIDRs    []string `toml:"trusted_proxy_cidrs"`
	// MigrationProbeEnabled is a narrowly scoped, prepare-only escape hatch
	// for rootless NAT, whose direct peer is not the loopback address selected
	// for the host-only bootstrap listener. It is never enabled in final edge
	// configuration.
	MigrationProbeEnabled   bool          `toml:"migration_probe_enabled"`
	MigrationProbeTokenEnv  string        `toml:"migration_probe_token_env"`
	RegistryForwardTokenEnv string        `toml:"registry_forward_token_env"`
	TLS                     EdgeTLSConfig `toml:"tls"`
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
	if cfg.Edge.MigrationProbeEnabled && strings.TrimSpace(cfg.Edge.MigrationProbeTokenEnv) == "" {
		return fmt.Errorf("edge.migration_probe_token_env is required when edge.migration_probe_enabled is true")
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

// edgeCertificateReloader serves the last complete key pair while checking
// files on each new TLS handshake. A partial rotation never replaces a known
// good certificate.
type edgeCertificateReloader struct {
	certFile string
	keyFile  string

	mu       sync.Mutex
	cert     tls.Certificate
	certHash [sha256.Size]byte
	keyHash  [sha256.Size]byte
	healthy  bool
}

func newEdgeCertificateReloader(certFile, keyFile string) (*edgeCertificateReloader, error) {
	r := &edgeCertificateReloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, fmt.Errorf("load edge TLS keypair: %w", err)
	}
	return r, nil
}

func (r *edgeCertificateReloader) reload() error {
	certPEM, err := os.ReadFile(r.certFile)
	if err != nil {
		r.healthy = false
		return err
	}
	keyPEM, err := os.ReadFile(r.keyFile)
	if err != nil {
		r.healthy = false
		return err
	}
	certHash, keyHash := sha256.Sum256(certPEM), sha256.Sum256(keyPEM)
	if r.healthy && certHash == r.certHash && keyHash == r.keyHash {
		return nil
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		r.healthy = false
		return err
	}
	r.cert, r.certHash, r.keyHash, r.healthy = certificate, certHash, keyHash, true
	return nil
}

func (r *edgeCertificateReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The error is intentionally not returned: callers continue to receive the
	// last-known-good pair and health reports the failed rotation separately.
	_ = r.reload()
	return &r.cert, nil
}

func (r *edgeCertificateReloader) Healthy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.healthy
}

func edgeTLSConfigWithReloader(cfg EdgeConfig) (*tls.Config, *edgeCertificateReloader, error) {
	if strings.ToLower(strings.TrimSpace(cfg.Edge.TLS.Mode)) != edgeTLSModeFiles {
		return nil, nil, nil
	}
	reloader, err := newEdgeCertificateReloader(cfg.Edge.TLS.CertFile, cfg.Edge.TLS.KeyFile)
	if err != nil {
		return nil, nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: reloader.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}, reloader, nil
}

func edgeTLSConfig(cfg EdgeConfig) (*tls.Config, error) {
	config, _, err := edgeTLSConfigWithReloader(cfg)
	return config, err
}
