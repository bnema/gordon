package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitEdgeConfigRejectsFullSecretBearingConfig(t *testing.T) {
	path := writeEdgeConfig(t, `
[control]
endpoint = "control.internal:9090"
token = "edge-token"
insecure_tls = true
[edge]
listen_address = "127.0.0.1:8080"
trusted_proxy_cidrs = ["127.0.0.0/8"]
[edge.tls]
mode = "external"
[auth]
token_secret = "must-not-be-loaded"
`)

	_, err := initEdgeConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strict mode")
}

func TestInitEdgeConfigAcceptsMinimalExternalContract(t *testing.T) {
	path := writeEdgeConfig(t, `
[control]
endpoint = "control.internal:9090"
token_env = "EDGE_TOKEN"
insecure_tls = true
[edge]
listen_address = "127.0.0.1:8080"
trusted_proxy_cidrs = ["10.0.0.0/8"]
[edge.tls]
mode = "external"
`)

	cfg, err := initEdgeConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "control.internal:9090", cfg.Control.Endpoint)
	assert.Equal(t, "EDGE_TOKEN", cfg.Control.TokenEnv)
	assert.Equal(t, edgeTLSModeExternal, cfg.Edge.TLS.Mode)
}

func TestInitEdgeConfigTLSModes(t *testing.T) {
	certPath, keyPath := writeEdgeCertificate(t)
	filesPath := writeEdgeConfig(t, `
[control]
endpoint = "control.internal:9090"
token = "edge-token"
insecure_tls = true
[edge]
listen_address = "127.0.0.1:8080"
[edge.tls]
mode = "files"
cert_file = "`+certPath+`"
key_file = "`+keyPath+`"
`)
	files, err := initEdgeConfig(filesPath)
	require.NoError(t, err)
	_, err = edgeTLSConfig(files)
	require.NoError(t, err)

	externalWithoutTrustedProxy := writeEdgeConfig(t, `
[control]
endpoint = "control.internal:9090"
token = "edge-token"
insecure_tls = true
[edge]
listen_address = "127.0.0.1:8080"
[edge.tls]
mode = "external"
`)
	_, err = initEdgeConfig(externalWithoutTrustedProxy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trusted_proxy_cidrs")
}

func TestInitEdgeConfigRejectsUnknownKeysAndInvalidCertificate(t *testing.T) {
	unknownPath := writeEdgeConfig(t, `
[control]
endpoint = "control.internal:9090"
token = "edge-token"
insecure_tls = true
[edge]
listen_address = "127.0.0.1:8080"
trusted_proxy_cidrs = ["127.0.0.0/8"]
unknown = true
[edge.tls]
mode = "external"
`)
	_, err := initEdgeConfig(unknownPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strict mode")

	invalidCertPath := writeEdgeConfig(t, `
[control]
endpoint = "control.internal:9090"
token = "edge-token"
insecure_tls = true
[edge]
listen_address = "127.0.0.1:8080"
[edge.tls]
mode = "files"
cert_file = "missing-cert.pem"
key_file = "missing-key.pem"
`)
	_, err = initEdgeConfig(invalidCertPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load edge TLS keypair")
}

func writeEdgeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "edge.toml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func writeEdgeCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	certTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "edge.test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"edge.test"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &key.PublicKey, key)
	require.NoError(t, err)
	certPath := filepath.Join(t.TempDir(), "edge-cert.pem")
	keyPath := filepath.Join(t.TempDir(), "edge-key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
	return certPath, keyPath
}
