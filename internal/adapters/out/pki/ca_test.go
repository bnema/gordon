package pki_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestNewCA_GeneratesRootAndIntermediate(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.NewCA(dir, testLogger())
	require.NoError(t, err)

	// Root cert exists on disk
	rootPEM, err := os.ReadFile(filepath.Join(dir, "pki", "root.crt"))
	require.NoError(t, err)
	block, _ := pem.Decode(rootPEM)
	require.NotNil(t, block)
	rootCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.True(t, rootCert.IsCA)
	assert.Equal(t, rootCert.Issuer.CommonName, rootCert.Subject.CommonName) // self-signed

	// Intermediate cert exists on disk
	interPEM, err := os.ReadFile(filepath.Join(dir, "pki", "intermediate.crt"))
	require.NoError(t, err)
	block, _ = pem.Decode(interPEM)
	require.NotNil(t, block)
	interCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.True(t, interCert.IsCA)
	assert.NotEqual(t, interCert.Issuer.CommonName, interCert.Subject.CommonName) // signed by root

	// Interface methods work
	assert.NotEmpty(t, ca.RootCertificate())
	assert.NotEmpty(t, ca.RootCertificateDER())
	assert.NotEmpty(t, ca.RootFingerprint())
	assert.NotEmpty(t, ca.RootCommonName())
	assert.False(t, ca.IntermediateExpiresAt().IsZero())
}

func TestNewCA_LoadsExistingOnRestart(t *testing.T) {
	dir := t.TempDir()

	// First boot: generate
	ca1, err := pki.NewCA(dir, testLogger())
	require.NoError(t, err)
	fp1 := ca1.RootFingerprint()

	// Second boot: load
	ca2, err := pki.NewCA(dir, testLogger())
	require.NoError(t, err)
	fp2 := ca2.RootFingerprint()

	assert.Equal(t, fp1, fp2, "root CA should be the same across restarts")
}

func TestCA_IssueCertificate(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.NewCA(dir, testLogger())
	require.NoError(t, err)

	cert, err := ca.IssueCertificate("test.example.com")
	require.NoError(t, err)
	require.NotNil(t, cert)

	// Parse the leaf cert
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	assert.Equal(t, "test.example.com", leaf.Subject.CommonName)
	assert.Contains(t, leaf.DNSNames, "test.example.com")
	assert.False(t, leaf.IsCA)

	// Verify chain: leaf -> intermediate -> root
	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertificate())
	interPool := x509.NewCertPool()
	inter, err := x509.ParseCertificate(cert.Certificate[1])
	require.NoError(t, err)
	interPool.AddCert(inter)

	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:         rootPool,
		Intermediates: interPool,
	})
	assert.NoError(t, err, "leaf cert should verify against root CA")
}

func TestCA_RenewIntermediate(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.NewCA(dir, testLogger())
	require.NoError(t, err)

	oldExpiry := ca.IntermediateExpiresAt()

	// x509 timestamps have second precision, so we must cross
	// a second boundary to observe a different NotAfter.
	time.Sleep(1100 * time.Millisecond)

	err = ca.RenewIntermediate()
	require.NoError(t, err)

	newExpiry := ca.IntermediateExpiresAt()
	assert.True(t, newExpiry.After(oldExpiry), "renewed intermediate should have later expiry")

	// Leaf certs should still work after renewal
	cert, err := ca.IssueCertificate("after-renewal.example.com")
	require.NoError(t, err)
	require.NotNil(t, cert)
}
