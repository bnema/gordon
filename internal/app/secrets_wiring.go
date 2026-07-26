// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"

	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/adapters/out/envloader"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

func createDomainSecretStore(cfg Config, log zerowrap.Logger) (string, domain.SecretsBackend, *domainsecrets.PassStore, out.DomainSecretStore, error) {
	envDir := resolveEnvDir(cfg)
	backend, err := resolveSecretsBackend(cfg.Auth.SecretsBackend)
	if err != nil {
		return "", "", nil, nil, log.WrapErr(err, "failed to resolve secrets backend")
	}

	switch backend {
	case domain.SecretsBackendPass:
		if managedPassEnvironment() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := ensureManagedPassStore(ctx, managedPassRoot, execPassCommandRunner{})
			cancel()
			if err != nil {
				return "", backend, nil, nil, log.WrapErr(err, "failed to initialize managed pass backend")
			}
		}
		passStore, err := domainsecrets.NewPassStore(log)
		if err != nil {
			return "", backend, nil, nil, log.WrapErr(err, "failed to create pass domain secret store")
		}
		return envDir, backend, passStore, passStore, nil
	default:
		store, err := domainsecrets.NewFileStore(envDir, log)
		if err != nil {
			return "", backend, nil, nil, log.WrapErr(err, "failed to create domain secret store")
		}
		return envDir, backend, nil, store, nil
	}
}

const (
	managedPassMarkerName = ".gordon-managed-pass-fingerprint"
	managedPassIdentity   = "Gordon Managed Secrets"
)

type passCommandRunner interface {
	Run(context.Context, string, ...string) error
	Output(context.Context, string, ...string) ([]byte, error)
}

type execPassCommandRunner struct{}

func (execPassCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run() //nolint:gosec // executable and arguments are fixed internal constants.
}

func (execPassCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // executable and arguments are fixed internal constants.
}

func managedPassEnvironment() bool {
	return os.Getenv("GNUPGHOME") == managedPassGPGHome && os.Getenv("PASSWORD_STORE_DIR") == managedPassStoreDir
}

// ensureManagedPassStore creates the control-owned keyring only on a genuinely
// empty volume. Any partial state is treated as corruption and is never healed
// by generating replacement key material.
func ensureManagedPassStore(ctx context.Context, root string, runner passCommandRunner) error {
	if runner == nil {
		return fmt.Errorf("managed pass command runner is unavailable")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("prepare managed pass root")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect managed pass state")
	}
	if len(entries) == 0 {
		return initializeManagedPassStore(ctx, root, runner)
	}
	return validateManagedPassStore(ctx, root, runner)
}

func initializeManagedPassStore(ctx context.Context, root string, runner passCommandRunner) error {
	gnupgHome := filepath.Join(root, "gnupg")
	storeDir := filepath.Join(root, "password-store")
	if err := os.Chmod(root, 0o700); err != nil { // #nosec G302 -- keyring directories require owner execute.
		return fmt.Errorf("restrict managed pass root")
	}
	for _, directory := range []string{gnupgHome, storeDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return fmt.Errorf("prepare managed pass state")
		}
	}
	args := []string{"--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", managedPassIdentity, "future-default", "default", "0"}
	if err := runner.Run(ctx, "gpg", args...); err != nil {
		return fmt.Errorf("generate managed pass identity")
	}
	fingerprint, err := managedPassFingerprint(ctx, runner)
	if err != nil {
		return err
	}
	if err := runner.Run(ctx, "pass", "init", fingerprint); err != nil {
		return fmt.Errorf("initialize managed password store")
	}
	if err := writeManagedPassMarker(root, fingerprint); err != nil {
		return err
	}
	return validateManagedPassStore(ctx, root, runner)
}

func validateManagedPassStore(ctx context.Context, root string, runner passCommandRunner) error {
	entries, err := os.ReadDir(root)
	if err != nil || !managedPassRootEntries(entries) {
		return fmt.Errorf("managed pass state is inconsistent")
	}
	gnupgHome := filepath.Join(root, "gnupg")
	storeDir := filepath.Join(root, "password-store")
	markerPath := filepath.Join(root, managedPassMarkerName)
	gpgIDPath := filepath.Join(storeDir, ".gpg-id")
	for _, directory := range []string{root, gnupgHome, storeDir} {
		info, err := os.Lstat(directory)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("managed pass state is incomplete")
		}
		if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- keyring directories require owner execute.
			return fmt.Errorf("restrict managed pass state")
		}
	}
	marker, err := readRestrictedManagedPassFile(markerPath)
	if err != nil {
		return err
	}
	gpgID, err := readRestrictedManagedPassFile(gpgIDPath)
	if err != nil {
		return err
	}
	fingerprint, err := managedPassFingerprint(ctx, runner)
	if err != nil {
		return err
	}
	if marker == "" || marker != gpgID || marker != fingerprint {
		return fmt.Errorf("managed pass integrity validation failed")
	}
	return nil
}

func managedPassRootEntries(entries []os.DirEntry) bool {
	if len(entries) != 3 {
		return false
	}
	allowed := map[string]bool{"gnupg": false, "password-store": false, managedPassMarkerName: false}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || allowed[entry.Name()] {
			return false
		}
		allowed[entry.Name()] = true
	}
	return allowed["gnupg"] && allowed["password-store"] && allowed[managedPassMarkerName]
}

func readRestrictedManagedPassFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("managed pass state is incomplete")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("restrict managed pass state")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read managed pass state")
	}
	return strings.TrimSpace(string(value)), nil
}

func managedPassFingerprint(ctx context.Context, runner passCommandRunner) (string, error) {
	output, err := runner.Output(ctx, "gpg", "--batch", "--no-tty", "--with-colons", "--list-secret-keys", managedPassIdentity)
	if err != nil {
		return "", fmt.Errorf("inspect managed pass identity")
	}
	var fingerprints []string
	awaitPrimaryFingerprint := false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "sec" {
			awaitPrimaryFingerprint = true
			continue
		}
		if awaitPrimaryFingerprint && fields[0] == "fpr" && len(fields) > 9 && validManagedPassFingerprint(fields[9]) {
			fingerprints = append(fingerprints, fields[9])
			awaitPrimaryFingerprint = false
		}
	}
	if len(fingerprints) != 1 {
		return "", fmt.Errorf("managed pass identity is invalid")
	}
	return fingerprints[0], nil
}

func validManagedPassFingerprint(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func writeManagedPassMarker(root, fingerprint string) error {
	file, err := os.CreateTemp(root, ".managed-pass-marker-*")
	if err != nil {
		return fmt.Errorf("create managed pass integrity marker")
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("restrict managed pass integrity marker")
	}
	if _, err := file.WriteString(fingerprint + "\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("write managed pass integrity marker")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync managed pass integrity marker")
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close managed pass integrity marker")
	}
	if err := os.Rename(temporary, filepath.Join(root, managedPassMarkerName)); err != nil {
		return fmt.Errorf("commit managed pass integrity marker")
	}
	return nil
}

func resolveSecretsBackend(backend string) (domain.SecretsBackend, error) {
	switch backend {
	case "pass":
		return domain.SecretsBackendPass, nil
	case "sops":
		return domain.SecretsBackendSops, nil
	case "unsafe":
		return domain.SecretsBackendUnsafe, nil
	case "":
		return "", fmt.Errorf("auth.secrets_backend is required")
	default:
		return "", fmt.Errorf("unsupported auth.secrets_backend %q", backend)
	}
}

func resolveEnvDir(cfg Config) string {
	dataDir := resolveDataDir(cfg.Server.DataDir)
	envDir := cfg.Env.Dir
	if envDir == "" {
		envDir = filepath.Join(dataDir, "env")
	}
	return envDir
}

// createEnvLoader creates the environment loader with secret providers.
func createEnvLoader(backend domain.SecretsBackend, envDir string, passStore *domainsecrets.PassStore, log zerowrap.Logger) (out.EnvLoader, error) {
	switch backend {
	case domain.SecretsBackendPass:
		loader, err := envloader.NewPassLoader(passStore, log)
		if err != nil {
			return nil, log.WrapErr(err, "failed to create pass env loader")
		}
		return loader, nil
	default:
		loader, err := envloader.NewFileLoader(envDir, log)
		if err != nil {
			return nil, log.WrapErr(err, "failed to create env loader")
		}

		// Register secret providers
		passProvider := secrets.NewPassProvider(log)
		if passProvider.IsAvailable() {
			loader.RegisterSecretProvider(passProvider)
			log.Debug().Msg("pass secret provider registered")
		}

		sopsProvider := secrets.NewSopsProvider(log)
		if sopsProvider.IsAvailable() {
			loader.RegisterSecretProvider(sopsProvider)
			log.Debug().Msg("sops secret provider registered")
		}

		return loader, nil
	}
}

// loadSecret loads a secret from the configured backend.
func loadSecret(ctx context.Context, backend domain.SecretsBackend, path, dataDir string, log zerowrap.Logger) (string, error) {
	switch backend {
	case domain.SecretsBackendPass:
		provider := secrets.NewPassProvider(log)
		return provider.GetSecret(ctx, path)
	case domain.SecretsBackendSops:
		provider := secrets.NewSopsProvider(log)
		return provider.GetSecret(ctx, path)
	case domain.SecretsBackendUnsafe:
		// For unsafe backend, path is relative to dataDir/secrets/.
		return readUnsafeSecret(dataDir, path)
	default:
		return "", fmt.Errorf("unknown secrets backend: %s", backend)
	}
}

func readFileBeneath(root, cleanedRelPath string) ([]byte, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open secrets root: %w", err)
	}
	defer unix.Close(rootFD)

	parts := strings.Split(filepath.ToSlash(cleanedRelPath), "/")
	dirFD := rootFD
	var closeDirFDs []int
	defer func() {
		for i := len(closeDirFDs) - 1; i >= 0; i-- {
			_ = unix.Close(closeDirFDs[i])
		}
	}()

	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid secret path: path must stay under dataDir/secrets")
		}
		last := i == len(parts)-1
		if last {
			fd, err := unix.Openat(dirFD, part, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return nil, fmt.Errorf("failed to read secret file: %w", err)
			}
			defer unix.Close(fd)
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil {
				return nil, fmt.Errorf("failed to stat secret file: %w", err)
			}
			if st.Mode&unix.S_IFMT != unix.S_IFREG {
				return nil, fmt.Errorf("invalid secret path: secret must be a regular file")
			}
			data, err := os.ReadFile(fmt.Sprintf("/proc/self/fd/%d", fd))
			if err != nil {
				return nil, fmt.Errorf("failed to read secret file: %w", err)
			}
			return data, nil
		}

		nextFD, err := unix.Openat(dirFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to open secret path component: %w", err)
		}
		closeDirFDs = append(closeDirFDs, nextFD)
		dirFD = nextFD
	}

	return nil, fmt.Errorf("invalid secret path: empty path")
}

type unsafeSecretProvider struct {
	dataDir string
}

func (p unsafeSecretProvider) Name() string { return string(domain.SecretsBackendUnsafe) }

func (p unsafeSecretProvider) IsAvailable() bool { return true }

func (p unsafeSecretProvider) GetSecret(_ context.Context, path string) (string, error) {
	return readUnsafeSecret(p.dataDir, path)
}

func createStandaloneServiceSecretProvider(backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) out.SecretProvider {
	switch backend {
	case domain.SecretsBackendPass:
		return secrets.NewPassProvider(log)
	case domain.SecretsBackendSops:
		return secrets.NewSopsProvider(log)
	case domain.SecretsBackendUnsafe:
		return unsafeSecretProvider{dataDir: dataDir}
	default:
		return nil
	}
}

func readUnsafeSecret(dataDir, secretPath string) (string, error) {
	if filepath.IsAbs(secretPath) {
		return "", fmt.Errorf("invalid secret path: absolute paths are not allowed")
	}
	cleaned := filepath.Clean(secretPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid secret path: path must stay under dataDir/secrets")
	}

	root := filepath.Clean(filepath.Join(dataDir, "secrets"))
	data, err := readFileBeneath(root, cleaned)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
