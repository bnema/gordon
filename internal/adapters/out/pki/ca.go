// Package pki implements the certificate authority adapter for internal PKI operations.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	out "github.com/bnema/gordon/internal/boundaries/out"
)

// Compile-time check: *CA satisfies the CertificateAuthority boundary.
var _ out.CertificateAuthority = (*CA)(nil)

const (
	rootLifetime         = 10 * 365 * 24 * time.Hour // ~10 years
	intermediateLifetime = 7 * 24 * time.Hour        // 7 days
	leafLifetime         = 12 * time.Hour            // 12 hours

	pkiDir = "pki"
)

// CA implements the CertificateAuthority boundary interface.
type CA struct {
	dataDir string
	log     zerowrap.Logger
	aiaURL  string // optional: URL for AIA extension in leaf certs

	rootCert *x509.Certificate
	rootKey  crypto.Signer
	rootPEM  []byte
	rootDER  []byte

	mu        sync.RWMutex
	interCert *x509.Certificate
	interKey  crypto.Signer
}

// SetAIAURL sets the Authority Information Access URL embedded in leaf certs.
// Format: http://{gordon_domain}[:{port}]/ca.crt
func (ca *CA) SetAIAURL(url string) { ca.aiaURL = url }

// NewCA loads or generates the root and intermediate CA certificates.
func NewCA(dataDir string, log zerowrap.Logger) (*CA, error) {
	ca := &CA{
		dataDir: dataDir,
		log:     log,
	}

	dir := filepath.Join(dataDir, pkiDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create pki dir: %w", err)
	}

	if err := ca.loadOrGenerateRoot(); err != nil {
		return nil, fmt.Errorf("root CA: %w", err)
	}

	if err := ca.loadOrGenerateIntermediate(); err != nil {
		return nil, fmt.Errorf("intermediate CA: %w", err)
	}

	return ca, nil
}

// RootCertificate returns the root CA certificate in PEM format.
func (ca *CA) RootCertificate() []byte { return ca.rootPEM }

// RootCertificateDER returns the root CA certificate in DER format.
func (ca *CA) RootCertificateDER() []byte { return ca.rootDER }

// RootFingerprint returns the SHA-256 fingerprint of the root CA cert
// formatted as colon-separated hex.
func (ca *CA) RootFingerprint() string {
	sum := sha256.Sum256(ca.rootCert.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// RootCommonName returns the CN of the root CA certificate.
func (ca *CA) RootCommonName() string {
	return ca.rootCert.Subject.CommonName
}

// IntermediateExpiresAt returns the intermediate CA certificate expiry.
func (ca *CA) IntermediateExpiresAt() time.Time {
	ca.mu.RLock()
	defer ca.mu.RUnlock()
	return ca.interCert.NotAfter
}

// RenewIntermediate regenerates the intermediate CA certificate,
// signed by the root CA. Existing leaf certs remain valid.
func (ca *CA) RenewIntermediate() error {
	cert, key, err := ca.generateIntermediate()
	if err != nil {
		return err
	}
	if err := ca.storeIntermediate(cert, key); err != nil {
		return err
	}
	ca.mu.Lock()
	ca.interCert = cert
	ca.interKey = key
	ca.mu.Unlock()
	ca.log.Info().Time("expires", cert.NotAfter).Msg("intermediate CA renewed")
	return nil
}

func (ca *CA) loadOrGenerateRoot() error {
	certPath := filepath.Join(ca.dataDir, pkiDir, "root.crt")
	keyPath := filepath.Join(ca.dataDir, pkiDir, "root.key")

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	if certErr == nil && keyErr == nil {
		cert, key, err := parseCertAndKey(certPEM, keyPEM)
		if err == nil {
			ca.rootCert = cert
			ca.rootKey = key
			ca.rootPEM = certPEM
			ca.rootDER = cert.Raw
			ca.log.Info().Str("cn", cert.Subject.CommonName).Msg("loaded existing root CA")
			return nil
		}
		ca.log.Warn().Err(err).Msg("failed to parse existing root CA, regenerating")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate root key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("Gordon Internal CA - %d ECC Root", now.Year()),
			Organization: []string{"Gordon"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(rootLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return fmt.Errorf("create root cert: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("parse root cert: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal root key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := writeSecure(certPath, certPEM, 0644); err != nil {
		return err
	}
	if err := writeSecure(keyPath, keyPEM, 0600); err != nil {
		return err
	}

	ca.rootCert = cert
	ca.rootKey = key
	ca.rootPEM = certPEM
	ca.rootDER = certDER
	ca.log.Info().Str("cn", cert.Subject.CommonName).Msg("generated new root CA")
	return nil
}

func (ca *CA) loadOrGenerateIntermediate() error {
	certPath := filepath.Join(ca.dataDir, pkiDir, "intermediate.crt")
	keyPath := filepath.Join(ca.dataDir, pkiDir, "intermediate.key")

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	if certErr == nil && keyErr == nil {
		cert, key, err := parseCertAndKey(certPEM, keyPEM)
		if err == nil && time.Now().Before(cert.NotAfter) {
			ca.interCert = cert
			ca.interKey = key
			ca.log.Info().
				Str("cn", cert.Subject.CommonName).
				Time("expires", cert.NotAfter).
				Msg("loaded existing intermediate CA")
			return nil
		}
		if err != nil {
			ca.log.Warn().Err(err).Msg("failed to parse intermediate CA, regenerating")
		} else {
			ca.log.Warn().Msg("intermediate CA expired, regenerating")
		}
	}

	cert, key, err := ca.generateIntermediate()
	if err != nil {
		return err
	}
	if err := ca.storeIntermediate(cert, key); err != nil {
		return err
	}
	ca.interCert = cert
	ca.interKey = key
	return nil
}

func (ca *CA) generateIntermediate() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate intermediate key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Gordon Internal CA - ECC Intermediate",
			Organization: []string{"Gordon"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(intermediateLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.rootCert, key.Public(), ca.rootKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create intermediate cert: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse intermediate cert: %w", err)
	}

	ca.log.Info().
		Str("cn", cert.Subject.CommonName).
		Time("expires", cert.NotAfter).
		Msg("generated new intermediate CA")
	return cert, key, nil
}

func (ca *CA) storeIntermediate(cert *x509.Certificate, key *ecdsa.PrivateKey) error {
	certPath := filepath.Join(ca.dataDir, pkiDir, "intermediate.crt")
	keyPath := filepath.Join(ca.dataDir, pkiDir, "intermediate.key")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal intermediate key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := writeSecure(certPath, certPEM, 0644); err != nil {
		return err
	}
	return writeSecure(keyPath, keyPEM, 0600)
}

// IssueCertificate generates a leaf cert for the given domain,
// signed by the current intermediate CA.
func (ca *CA) IssueCertificate(domain string) (*tls.Certificate, error) {
	ca.mu.RLock()
	interCert := ca.interCert
	interKey := ca.interKey
	ca.mu.RUnlock()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: domain,
		},
		DNSNames:    []string{domain},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.Add(leafLifetime),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	// AIA extension: URL where clients can fetch the CA cert.
	if ca.aiaURL != "" {
		template.IssuingCertificateURL = []string{ca.aiaURL}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, interCert, key.Public(), interKey)
	if err != nil {
		return nil, fmt.Errorf("create leaf cert: %w", err)
	}

	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	interPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: interCert.Raw})
	chainPEM := append(leafPEM, interPEM...)

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal leaf key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(chainPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("create TLS cert: %w", err)
	}

	return &tlsCert, nil
}

// LeafLifetime returns the configured leaf certificate lifetime.
func (ca *CA) LeafLifetime() time.Duration {
	return leafLifetime
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}

func parseCertAndKey(certPEM, keyPEM []byte) (*x509.Certificate, crypto.Signer, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("failed to decode key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse key: %w", err)
	}

	return cert, key, nil
}

func writeSecure(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	return os.WriteFile(path, data, perm)
}
