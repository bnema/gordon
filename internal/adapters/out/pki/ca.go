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
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	out "github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/pkg/validation"
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

	mu        sync.RWMutex
	interCert *x509.Certificate
	interKey  crypto.Signer

	renewMu sync.Mutex // serialises intermediate renewal
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
func (ca *CA) RootCertificateDER() []byte { return ca.rootCert.Raw }

// RootFingerprint returns the SHA-256 fingerprint of the root CA cert
// formatted as colon-separated hex.
func (ca *CA) RootFingerprint() string {
	sum := sha256.Sum256(ca.rootCert.Raw)
	parts := make([]string, 0, sha256.Size)
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02X", b))
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
	ca.renewMu.Lock()
	defer ca.renewMu.Unlock()

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

	certMissing := certErr != nil && errors.Is(certErr, os.ErrNotExist)
	keyMissing := keyErr != nil && errors.Is(keyErr, os.ErrNotExist)

	switch {
	case certErr == nil && keyErr == nil:
		cert, key, err := parseCertAndKey(certPEM, keyPEM)
		if err != nil {
			return fmt.Errorf("corrupt root CA files: %w", err)
		}
		if err := verifyKeyPair(cert, key); err != nil {
			return fmt.Errorf("root CA cert/key mismatch: %w", err)
		}
		ca.rootCert = cert
		ca.rootKey = key
		ca.rootPEM = certPEM
		ca.log.Info().Str("cn", cert.Subject.CommonName).Msg("loaded existing root CA")
		return nil

	case certMissing && keyMissing:
		return ca.bootstrapRoot(certPath, keyPath)

	case certMissing || keyMissing:
		return fmt.Errorf("incomplete root CA: cert missing=%v, key missing=%v", certMissing, keyMissing)

	default:
		if certErr != nil {
			return fmt.Errorf("read root cert: %w", certErr)
		}
		return fmt.Errorf("read root key: %w", keyErr)
	}
}

func (ca *CA) bootstrapRoot(certPath, keyPath string) error {
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

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal root key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := writeSecure(certPath, certPEM, 0644); err != nil {
		return err
	}
	if err := writeSecure(keyPath, keyPEM, 0600); err != nil {
		return err
	}

	ca.rootCert = cert
	ca.rootKey = key
	ca.rootPEM = certPEM
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
		if err != nil {
			ca.log.Warn().Err(err).Msg("failed to parse intermediate CA, regenerating")
		} else if err := verifyKeyPair(cert, key); err != nil {
			ca.log.Warn().Err(err).Msg("intermediate CA cert/key mismatch, regenerating")
		} else if err := cert.CheckSignatureFrom(ca.rootCert); err != nil {
			ca.log.Warn().Err(err).Msg("intermediate CA not signed by current root, regenerating")
		} else if time.Now().Before(cert.NotAfter) {
			ca.interCert = cert
			ca.interKey = key
			ca.log.Info().
				Str("cn", cert.Subject.CommonName).
				Time("expires", cert.NotAfter).
				Msg("loaded existing intermediate CA")
			return nil
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
	if err := validateDomain(domain); err != nil {
		return nil, fmt.Errorf("invalid domain: %w", err)
	}

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
	clear(keyDER)
	clear(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("create TLS cert: %w", err)
	}

	return &tlsCert, nil
}

// LeafLifetime returns the configured leaf certificate lifetime.
func (ca *CA) LeafLifetime() time.Duration {
	return leafLifetime
}

// IntermediateLifetime returns the configured intermediate CA lifetime.
func (ca *CA) IntermediateLifetime() time.Duration {
	return intermediateLifetime
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

// validateDomain performs defense-in-depth validation of a domain name
// before issuing a certificate.
func validateDomain(domain string) error {
	if err := validation.ValidateDomainParam(domain); err != nil {
		return err
	}
	if len(domain) > 253 {
		return fmt.Errorf("domain too long (%d chars), limit is 253", len(domain))
	}
	if strings.HasPrefix(domain, "*") {
		return fmt.Errorf("wildcard domains not allowed")
	}
	if net.ParseIP(domain) != nil {
		return fmt.Errorf("IP addresses not allowed, must be a domain name")
	}
	return nil
}

func writeSecure(path string, data []byte, perm os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	// Atomic write: temp file → chmod → rename to avoid TOCTOU on permissions.
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	return nil
}

// verifyKeyPair checks that a certificate's public key matches the private key.
func verifyKeyPair(cert *x509.Certificate, key crypto.Signer) error {
	certPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("certificate has non-ECDSA public key")
	}
	keyPub, ok := key.Public().(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("signer has non-ECDSA public key")
	}
	if !certPub.Equal(keyPub) {
		return fmt.Errorf("public keys do not match")
	}
	return nil
}
