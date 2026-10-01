package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bnema/zerowrap"
)

const (
	internalRegistryUsername = "gordon-internal"
	serviceTokenSubject      = "gordon-service"
	serviceTokenDefaultTTL   = 30 * 24 * time.Hour
)

func generateInternalRegistryAuth() (string, string, error) {
	password, err := randomTokenHex(32)
	if err != nil {
		return "", "", err
	}
	return internalRegistryUsername, password, nil
}

func randomTokenHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// InternalCredentials holds the internal registry credentials for CLI access.
type InternalCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// getSecureRuntimeDir returns a secure directory for runtime files.
// Priority: XDG_RUNTIME_DIR > ~/.gordon/run
func getSecureRuntimeDir() (string, error) {
	// Try XDG_RUNTIME_DIR first (typically /run/user/<uid> on Linux)
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		gordonDir := filepath.Join(runtimeDir, "gordon")
		if err := os.MkdirAll(gordonDir, 0700); err == nil {
			return gordonDir, nil
		}
	}

	// Fall back to ~/.gordon/run
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	gordonDir := filepath.Join(homeDir, ".gordon", "run")
	if err := os.MkdirAll(gordonDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create runtime directory: %w", err)
	}

	return gordonDir, nil
}

// getInternalCredentialsFile returns the path to the internal credentials file.
// SECURITY: Credentials are stored in a secure location with restricted permissions.
func getInternalCredentialsFile() string {
	runtimeDir, err := getSecureRuntimeDir()
	if err != nil {
		// Fall back to temp dir if we can't get secure dir (shouldn't happen)
		return filepath.Join(os.TempDir(), "gordon-internal-creds.json")
	}
	return filepath.Join(runtimeDir, "internal-creds.json")
}

// persistInternalCredentials saves the internal registry credentials to a secure file.
// SECURITY: Credentials are stored in XDG_RUNTIME_DIR or ~/.gordon/run with 0600 permissions.
// The file is cleaned up on graceful shutdown but may persist if Gordon crashes.
// These credentials are for internal loopback communication only and are regenerated on each start.
func persistInternalCredentials(username, password string) error {
	creds := InternalCredentials{
		Username: username,
		Password: password,
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	credFile := getInternalCredentialsFile()

	// Ensure parent directory exists with secure permissions
	if err := os.MkdirAll(filepath.Dir(credFile), 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	// Write file with restrictive permissions (owner read/write only)
	if err := os.WriteFile(credFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}
	return nil
}

// cleanupInternalCredentials removes the internal credentials file.
func cleanupInternalCredentials() {
	_ = os.Remove(getInternalCredentialsFile())
}

// getInternalCredentialsCandidates returns candidate file paths in priority order:
// 1. XDG_RUNTIME_DIR/gordon/ (set by systemd for the daemon)
// 2. /run/user/<uid>/gordon/ (well-known systemd default, for CLI in shells without XDG_RUNTIME_DIR)
// 3. ~/.gordon/run/ (fallback for non-systemd environments)
// 4. os.TempDir() (last resort, matches getInternalCredentialsFile fallback path)
func getInternalCredentialsCandidates() []string {
	var candidates []string

	// 1. XDG_RUNTIME_DIR (set in daemon's environment)
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		candidates = append(candidates, filepath.Join(runtimeDir, "gordon", "internal-creds.json"))
	}

	// 2. /run/user/<uid>/gordon/ (systemd default, may not be in CLI's env)
	uid := os.Getuid()
	sysRuntime := filepath.Join("/run/user", fmt.Sprintf("%d", uid), "gordon", "internal-creds.json")
	// Avoid duplicate if XDG_RUNTIME_DIR already points here
	if len(candidates) == 0 || candidates[0] != sysRuntime {
		candidates = append(candidates, sysRuntime)
	}

	// 3. ~/.gordon/run/ fallback
	if homeDir, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(homeDir, ".gordon", "run", "internal-creds.json"))
	}

	// 4. os.TempDir() last resort — matches the fallback path in getInternalCredentialsFile,
	// ensuring GetInternalCredentials can find credentials even when getSecureRuntimeDir fails.
	candidates = append(candidates, filepath.Join(os.TempDir(), "gordon-internal-creds.json"))

	return candidates
}

// GetInternalCredentialsFromCandidates reads credentials from the first candidate file that exists.
// Exported for testing.
func GetInternalCredentialsFromCandidates(candidates []string) (*InternalCredentials, error) {
	var lastErr error
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			// Non-permission errors (e.g. EACCES) may be transient or path-specific;
			// record and try the next candidate rather than failing immediately.
			lastErr = fmt.Errorf("failed to read credentials file %s: %w", path, err)
			continue
		}
		var creds InternalCredentials
		if err := json.Unmarshal(data, &creds); err != nil {
			// Corrupt file — record and fall through to lower-priority candidates.
			lastErr = fmt.Errorf("failed to parse credentials at %s: %w", path, err)
			continue
		}
		return &creds, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no credentials file found (is Gordon running?): checked %v", candidates)
}

// GetInternalCredentials reads the internal registry credentials from file.
// Probes all candidate runtime directories so CLI works regardless of whether
// XDG_RUNTIME_DIR is set in the current shell environment.
func GetInternalCredentials() (*InternalCredentials, error) {
	return GetInternalCredentialsFromCandidates(getInternalCredentialsCandidates())
}

func setupInternalRegistryAuth(svc *services, log zerowrap.Logger) error {
	var err error
	svc.internalRegUser, svc.internalRegPass, err = generateInternalRegistryAuth()
	if err != nil {
		return log.WrapErr(err, "failed to generate internal registry credentials")
	}

	// Persist credentials to file for CLI access (gordon auth internal)
	if err := persistInternalCredentials(svc.internalRegUser, svc.internalRegPass); err != nil {
		log.Warn().Err(err).Msg("failed to persist internal credentials for CLI access")
	}

	log.Debug().Msg("internal registry auth generated for loopback pulls")
	return nil
}
