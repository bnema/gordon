package acmestore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// generateTestCertPEM creates a self-signed ECDSA P256 certificate and its
// private key, both PEM-encoded, for use in tests that require a valid
// tls.Certificate.
func generateTestCertPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test.example.com",
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	return certPEM, keyPEM
}

func TestStoreSaveLoadAll(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	fullchainPEM, privKeyPEM := generateTestCertPEM(t)

	cert := out.StoredCertificate{
		ID:            "dns01-example.com",
		Names:         []string{"example.com", "*.example.com"},
		Challenge:     domain.ACMEChallengeCloudflareDNS01,
		CertPEM:       []byte("dummy-cert"),
		ChainPEM:      []byte("dummy-chain"),
		FullchainPEM:  fullchainPEM,
		PrivateKeyPEM: privKeyPEM,
		NotAfter:      now,
	}

	err = store.Save(ctx, cert)
	require.NoError(t, err)

	certs, err := store.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, certs, 1)

	assert.Equal(t, "dns01-example.com", certs[0].ID)
	assert.Equal(t, []string{"example.com", "*.example.com"}, certs[0].Names)
	assert.Equal(t, privKeyPEM, certs[0].PrivateKeyPEM)
	// Verify tls.Certificate is populated from valid PEM
	assert.NotEmpty(t, certs[0].Certificate.Certificate, "tls.Certificate.Certificate should be populated")
}

func TestStorePrivateKeyMode(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	cert := out.StoredCertificate{
		ID:            "http01-app.example.com",
		Names:         []string{"app.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		PrivateKeyPEM: []byte("key"),
	}

	err = store.Save(ctx, cert)
	require.NoError(t, err)

	privkeyPath := filepath.Join(root, certDir, "http01-app.example.com", privkeyFile)
	info, err := os.Stat(privkeyPath)
	require.NoError(t, err)

	// Mode 0600 (owner read+write only)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestStoreRejectsUnsafeID(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	for _, id := range []string{"../escape", ".", ".hidden"} {
		t.Run(id, func(t *testing.T) {
			cert := out.StoredCertificate{
				ID:            id,
				PrivateKeyPEM: []byte("key"),
			}

			err = store.Save(ctx, cert)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsafe certificate id")
			assert.ErrorIs(t, err, domain.ErrPathTraversal)
		})
	}
}

func TestStoreLockAcquireRelease(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	unlock, err := store.Lock(ctx)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(root, lockFile))

	_, err = store.Lock(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock already held")

	require.NoError(t, unlock())
	assert.NoFileExists(t, filepath.Join(root, lockFile))

	unlock, err = store.Lock(ctx)
	require.NoError(t, err)
	require.NoError(t, unlock())
}

func TestStoreLockContextCanceled(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.Lock(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.NoFileExists(t, filepath.Join(root, lockFile))
}

func TestStoreNewRestoresBackupDir(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	fullchainPEM, privKeyPEM := generateTestCertPEM(t)
	cert := out.StoredCertificate{
		ID:            "restore.example.com",
		Names:         []string{"restore.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		CertPEM:       fullchainPEM,
		FullchainPEM:  fullchainPEM,
		PrivateKeyPEM: privKeyPEM,
	}
	require.NoError(t, store.Save(ctx, cert))

	certPath := filepath.Join(root, certDir, cert.ID)
	backupPath := certPath + ".old"
	require.NoError(t, os.Rename(certPath, backupPath))
	require.NoDirExists(t, certPath)

	store, err = New(root)
	require.NoError(t, err)
	assert.DirExists(t, certPath)

	certs, err := store.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, certs, 1)
	assert.Equal(t, cert.ID, certs[0].ID)
}

func TestStoreSaveLoadAccount(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	acct := out.ACMEAccount{
		Email:           "admin@example.com",
		PrivateKeyPEM:   []byte("key"),
		RegistrationURI: "https://acme.test/acct/1",
		BodyJSON:        []byte(`{"status":"valid"}`),
	}

	err = store.SaveAccount(ctx, acct)
	require.NoError(t, err)

	loaded, err := store.LoadAccount(ctx)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, "admin@example.com", loaded.Email)
	assert.Equal(t, []byte("key"), loaded.PrivateKeyPEM)
	assert.Equal(t, "https://acme.test/acct/1", loaded.RegistrationURI)
	assert.Equal(t, []byte(`{"status":"valid"}`), loaded.BodyJSON)
}

func TestStoreAccountFileMode(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	acct := out.ACMEAccount{
		Email:           "test@example.com",
		PrivateKeyPEM:   []byte("test-key"),
		RegistrationURI: "https://acme.test/acct/99",
		BodyJSON:        []byte(`{"status":"valid"}`),
	}

	err = store.SaveAccount(ctx, acct)
	require.NoError(t, err)

	accountPath := filepath.Join(root, accountFile)
	info, err := os.Stat(accountPath)
	require.NoError(t, err)

	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestStoreLoadAllInvalidChallenge(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	// Create a cert dir manually with invalid challenge in metadata
	certDirPath := filepath.Join(root, certDir, "bad-challenge.example.com")
	err = os.MkdirAll(certDirPath, dirMode)
	require.NoError(t, err)

	meta := certMetadata{
		ID:        "bad-challenge.example.com",
		Names:     []string{"bad-challenge.example.com"},
		Challenge: "invalid-challenge-mode",
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(certDirPath, metaFile), metaData, metaMode)
	require.NoError(t, err)

	// LoadAll should return an error wrapping domain.ErrACMEChallengeInvalid
	_, err = store.LoadAll(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrACMEChallengeInvalid)
}

func TestStoreLoadAllCorruptPrivkey(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	// Save a valid cert first
	cert := out.StoredCertificate{
		ID:            "corrupt-privkey.example.com",
		Names:         []string{"corrupt-privkey.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		FullchainPEM:  []byte("fullchain-data"),
		PrivateKeyPEM: []byte("privkey-data"),
	}
	err = store.Save(ctx, cert)
	require.NoError(t, err)

	// Replace privkey.pem with an unreadable directory (simulate corruption)
	privkeyPath := filepath.Join(root, certDir, "corrupt-privkey.example.com", privkeyFile)
	err = os.Remove(privkeyPath)
	require.NoError(t, err)
	// Create a directory in its place to cause a read error
	err = os.MkdirAll(privkeyPath, 0700)
	require.NoError(t, err)

	// LoadAll should return an error
	_, err = store.LoadAll(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "privkey.pem")
}

func TestStoreLoadAllCorruptFullchain(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	// Save a valid cert first
	cert := out.StoredCertificate{
		ID:            "corrupt-fullchain.example.com",
		Names:         []string{"corrupt-fullchain.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		FullchainPEM:  []byte("fullchain-data"),
		PrivateKeyPEM: []byte("privkey-data"),
	}
	err = store.Save(ctx, cert)
	require.NoError(t, err)

	// Replace fullchain.pem with an unreadable directory
	fullchainPath := filepath.Join(root, certDir, "corrupt-fullchain.example.com", fullchainFile)
	err = os.Remove(fullchainPath)
	require.NoError(t, err)
	err = os.MkdirAll(fullchainPath, 0700)
	require.NoError(t, err)

	// LoadAll should return an error
	_, err = store.LoadAll(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fullchain.pem")
}

func TestStoreLoadAllSkipsTempAndBackupDirs(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	fullchainPEM, privKeyPEM := generateTestCertPEM(t)

	// Save a valid certificate first.
	cert := out.StoredCertificate{
		ID:            "valid.example.com",
		Names:         []string{"valid.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		CertPEM:       fullchainPEM,
		FullchainPEM:  fullchainPEM,
		PrivateKeyPEM: privKeyPEM,
	}
	err = store.Save(ctx, cert)
	require.NoError(t, err)

	// Create a tmp-* directory that Save would have left behind.
	tmpDir := filepath.Join(root, certDir, "orphan.tmp-abc123")
	err = os.MkdirAll(tmpDir, dirMode)
	require.NoError(t, err)

	// Create a .old directory that Save would have left behind.
	oldDir := filepath.Join(root, certDir, "old-cert.example.com.old")
	err = os.MkdirAll(oldDir, dirMode)
	require.NoError(t, err)

	// LoadAll should return only the valid cert, skipping both temp and backup dirs.
	certs, err := store.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, certs, 1, "should skip tmp-* and *.old directories")
	assert.Equal(t, "valid.example.com", certs[0].ID)
}

func TestStoreLoadAllInvalidPEM(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	store, err := New(root)
	require.NoError(t, err)

	// Save a cert with non-empty but invalid PEM bytes (mismatched key)
	cert := out.StoredCertificate{
		ID:            "invalid-pem.example.com",
		Names:         []string{"invalid-pem.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		FullchainPEM:  []byte("invalid-fullchain"),
		PrivateKeyPEM: []byte("invalid-privkey"),
	}
	err = store.Save(ctx, cert)
	require.NoError(t, err)

	// LoadAll should return an error because tls.X509KeyPair fails
	_, err = store.LoadAll(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse key pair")
}
