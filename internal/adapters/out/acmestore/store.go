package acmestore

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// certMetadata is the JSON structure persisted in each certificate directory.
type certMetadata struct {
	ID        string    `json:"id"`
	Names     []string  `json:"names"`
	Challenge string    `json:"challenge"`
	NotAfter  time.Time `json:"not_after,omitempty"`
	LastError string    `json:"last_error,omitempty"`
}

const (
	lockFile      = ".lock"
	accountFile   = "account.json"
	certDir       = "certs"
	certFile      = "cert.pem"
	chainFile     = "chain.pem"
	fullchainFile = "fullchain.pem"
	privkeyFile   = "privkey.pem"
	metaFile      = "metadata.json"

	dirMode     os.FileMode = 0700
	privKeyMode os.FileMode = 0600
	metaMode    os.FileMode = 0600
	accountMode os.FileMode = 0600
	lockMode    os.FileMode = 0600
	pemMode     os.FileMode = 0644
)

// Store implements out.CertificateStore using the local filesystem.
type Store struct {
	root string
}

// New creates a Store rooted at root. root must be non-empty and is created
// along with the certs subdirectory if they do not exist.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("acmestore: root is required")
	}
	if err := os.MkdirAll(root, dirMode); err != nil {
		return nil, fmt.Errorf("acmestore: mkdir root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(root, certDir), dirMode); err != nil {
		return nil, fmt.Errorf("acmestore: mkdir certs: %w", err)
	}
	return &Store{root: root}, nil
}

// LoadAccount reads the account from <root>/account.json.
// It returns nil, nil when the file does not exist.
func (s *Store) LoadAccount(_ context.Context) (*out.ACMEAccount, error) {
	path := filepath.Join(s.root, accountFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("acmestore: read account: %w", err)
	}
	var acct out.ACMEAccount
	if err := json.Unmarshal(data, &acct); err != nil {
		return nil, fmt.Errorf("acmestore: unmarshal account: %w", err)
	}
	return &acct, nil
}

// SaveAccount writes the account to <root>/account.json atomically with mode 0600.
func (s *Store) SaveAccount(_ context.Context, account out.ACMEAccount) error {
	data, err := json.MarshalIndent(account, "", "  ")
	if err != nil {
		return fmt.Errorf("acmestore: marshal account: %w", err)
	}
	if err := writeAtomic(filepath.Join(s.root, accountFile), data, accountMode); err != nil {
		return fmt.Errorf("acmestore: save account: %w", err)
	}
	return nil
}

// LoadAll reads all certificate directories under <root>/certs/ and returns
// the stored certificates. Directories with unsafe IDs are skipped.
func (s *Store) LoadAll(_ context.Context) ([]out.StoredCertificate, error) {
	certsPath := filepath.Join(s.root, certDir)
	entries, err := os.ReadDir(certsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("acmestore: list certs: %w", err)
	}

	var result []out.StoredCertificate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if !safeID(id) {
			continue
		}

		cert, err := s.loadOne(id)
		if err != nil {
			return nil, fmt.Errorf("acmestore: load %s: %w", id, err)
		}
		result = append(result, *cert)
	}
	return result, nil
}

// loadOne reads a single certificate directory and returns the stored
// certificate.
func (s *Store) loadOne(id string) (*out.StoredCertificate, error) {
	dir := filepath.Join(s.root, certDir, id)

	meta, err := readMetadata(filepath.Join(dir, metaFile))
	if err != nil {
		return nil, fmt.Errorf("acmestore: metadata %s: %w", id, err)
	}

	certPEM, err := readPEM(filepath.Join(dir, certFile))
	if err != nil {
		return nil, fmt.Errorf("acmestore: read %s cert.pem: %w", id, err)
	}
	chainPEM, err := readPEM(filepath.Join(dir, chainFile))
	if err != nil {
		return nil, fmt.Errorf("acmestore: read %s chain.pem: %w", id, err)
	}
	fullchainPEM, err := readPEM(filepath.Join(dir, fullchainFile))
	if err != nil {
		return nil, fmt.Errorf("acmestore: read %s fullchain.pem: %w", id, err)
	}
	privKeyPEM, err := readPEM(filepath.Join(dir, privkeyFile))
	if err != nil {
		return nil, fmt.Errorf("acmestore: read %s privkey.pem: %w", id, err)
	}

	var tlsCert tls.Certificate
	if len(fullchainPEM) > 0 && len(privKeyPEM) > 0 {
		parsed, err := tls.X509KeyPair(fullchainPEM, privKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("acmestore: parse key pair %s: %w", id, err)
		}
		tlsCert = parsed
	}

	challenge, err := domain.ParseACMEChallengeMode(meta.Challenge)
	if err != nil {
		return nil, fmt.Errorf("acmestore: challenge %s: %w", id, err)
	}

	return &out.StoredCertificate{
		ID:            id,
		Names:         meta.Names,
		Challenge:     challenge,
		Certificate:   tlsCert,
		CertPEM:       certPEM,
		ChainPEM:      chainPEM,
		FullchainPEM:  fullchainPEM,
		PrivateKeyPEM: privKeyPEM,
		NotAfter:      meta.NotAfter,
		LastError:     meta.LastError,
	}, nil
}

// Save persists a certificate to <root>/certs/<safe-id>/.
// All PEM files and metadata are written atomically.
func (s *Store) Save(_ context.Context, cert out.StoredCertificate) error {
	if !safeID(cert.ID) {
		return fmt.Errorf("acmestore: unsafe certificate id %q", cert.ID)
	}

	dir := filepath.Join(s.root, certDir, cert.ID)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("acmestore: mkdir %s: %w", cert.ID, err)
	}

	meta := certMetadata{
		ID:        cert.ID,
		Names:     cert.Names,
		Challenge: string(cert.Challenge),
		NotAfter:  cert.NotAfter,
		LastError: cert.LastError,
	}

	if err := writeFileIfNonNil(filepath.Join(dir, certFile), cert.CertPEM, pemMode); err != nil {
		return fmt.Errorf("acmestore: write cert.pem: %w", err)
	}
	if err := writeFileIfNonNil(filepath.Join(dir, chainFile), cert.ChainPEM, pemMode); err != nil {
		return fmt.Errorf("acmestore: write chain.pem: %w", err)
	}
	if err := writeFileIfNonNil(filepath.Join(dir, fullchainFile), cert.FullchainPEM, pemMode); err != nil {
		return fmt.Errorf("acmestore: write fullchain.pem: %w", err)
	}
	if err := writeFileIfNonNil(filepath.Join(dir, privkeyFile), cert.PrivateKeyPEM, privKeyMode); err != nil {
		return fmt.Errorf("acmestore: write privkey.pem: %w", err)
	}

	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("acmestore: marshal metadata: %w", err)
	}
	if err := writeAtomic(filepath.Join(dir, metaFile), metaData, metaMode); err != nil {
		return fmt.Errorf("acmestore: write metadata: %w", err)
	}

	return nil
}

// Lock acquires an exclusive lock using a lock file at <root>/.lock.
// The lock is released by calling the returned unlock function.
// If the lock file already exists, Lock returns an error.
// Context cancellation is respected before attempting the lock.
func (s *Store) Lock(ctx context.Context) (func() error, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	path := filepath.Join(s.root, lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, lockMode)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("acmestore: lock already held")
		}
		return nil, fmt.Errorf("acmestore: acquire lock: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("acmestore: close lock file: %w", err)
	}

	return func() error {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("acmestore: release lock: %w", err)
		}
		return nil
	}, nil
}

// safeID rejects IDs that could cause path traversal.
func safeID(id string) bool {
	if id == "" {
		return false
	}
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return false
	}
	return true
}

// writeAtomic writes data to path atomically by writing to a temporary file
// in the same directory and then renaming.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()

	// Clean up temp file on any failure
	removeOnErr := true
	defer func() {
		if removeOnErr {
			os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	// Sync before close to ensure data is on disk.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	removeOnErr = false
	return nil
}

// writeFileIfNonNil writes data to path atomically only when data is non-nil.
func writeFileIfNonNil(path string, data []byte, mode os.FileMode) error {
	if data == nil {
		return nil
	}
	return writeAtomic(path, data, mode)
}

// readPEM reads a PEM file from path. If the file does not exist it returns
// nil, nil without error. Other read errors are returned.
func readPEM(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// readMetadata reads and unmarshals a metadata JSON file.
func readMetadata(path string) (*certMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var meta certMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &meta, nil
}
