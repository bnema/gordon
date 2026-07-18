package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
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

func TestInitEdgeConfigRequiresProbeTokenEnvironmentWhenEnabled(t *testing.T) {
	path := writeEdgeConfig(t, `
[control]
endpoint = "control.internal:9090"
token_env = "EDGE_TOKEN"
insecure_tls = true
[edge]
listen_address = "127.0.0.1:8080"
trusted_proxy_cidrs = ["10.0.0.0/8"]
migration_probe_enabled = true
[edge.tls]
mode = "external"
`)
	_, err := initEdgeConfig(path)
	require.ErrorContains(t, err, "migration_probe_token_env")
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

func TestEdgeCertificateReloadKeepsLastKnownGoodAndRecovers(t *testing.T) {
	certPath, keyPath := writeEdgeCertificate(t)
	cfg := defaultEdgeConfig()
	cfg.Edge.TLS.Mode, cfg.Edge.TLS.CertFile, cfg.Edge.TLS.KeyFile = edgeTLSModeFiles, certPath, keyPath
	config, reloader, err := edgeTLSConfigWithReloader(cfg)
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	tlsListener := tls.NewListener(listener, config)
	go func() {
		for {
			connection, acceptErr := tlsListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() { defer connection.Close(); _ = connection.(*tls.Conn).Handshake() }()
		}
	}()

	first := edgeHandshakeCertificate(t, listener.Addr().String())
	assert.Equal(t, int64(1), first.SerialNumber.Int64())
	writeEdgeCertificateAt(t, certPath, keyPath, 2, "rotated.edge.test")
	rotated := edgeHandshakeCertificate(t, listener.Addr().String())
	assert.Equal(t, int64(2), rotated.SerialNumber.Int64())
	assert.Contains(t, rotated.DNSNames, "rotated.edge.test")

	require.NoError(t, os.WriteFile(certPath, []byte("incomplete certificate"), 0o600))
	stillServing := edgeHandshakeCertificate(t, listener.Addr().String())
	assert.Equal(t, int64(2), stillServing.SerialNumber.Int64())
	assert.False(t, reloader.Healthy())

	writeEdgeCertificateAt(t, certPath, keyPath, 3, "recovered.edge.test")
	recovered := edgeHandshakeCertificate(t, listener.Addr().String())
	assert.Equal(t, int64(3), recovered.SerialNumber.Int64())
	assert.True(t, reloader.Healthy())

	var handshakes sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		handshakes.Add(1)
		go func() { defer handshakes.Done(); errs <- edgeHandshakeCertificateError(listener.Addr().String()) }()
	}
	handshakes.Wait()
	close(errs)
	for handshakeErr := range errs {
		require.NoError(t, handshakeErr)
	}
}

func edgeHandshakeCertificate(t *testing.T, address string) *x509.Certificate {
	t.Helper()
	connection, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- generated test certificates.
	require.NoError(t, err)
	defer connection.Close()
	return connection.ConnectionState().PeerCertificates[0]
}

func edgeHandshakeCertificateError(address string) error {
	connection, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- generated test certificates.
	if err != nil {
		return err
	}
	return connection.Close()
}

func writeEdgeCertificateAt(t *testing.T, certPath, keyPath string, serial int64, san string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	certificate := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: san}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{san}}
	certificateDER, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
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
