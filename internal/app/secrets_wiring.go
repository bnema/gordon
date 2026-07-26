// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
			err := ensureProcessManagedPassStore(ctx, managedPassRoot, execPassCommandRunner{})
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
	managedPassMarkerName  = ".gordon-managed-pass-fingerprint"
	managedPassIdentity    = "Gordon Managed Secrets"
	managedPassLockName    = ".control.lock"
	managedPassStagePrefix = ".initialize-"
)

var (
	processManagedPassLeaseMu sync.Mutex
	processManagedPassLease   *os.File
)

type passCommandRunner interface {
	Run(context.Context, []string, string, ...string) error
	Output(context.Context, []string, string, ...string) ([]byte, error)
}

type execPassCommandRunner struct{}

func (execPassCommandRunner) Run(ctx context.Context, env []string, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // executable and arguments are fixed internal constants.
	command.Env = env
	return command.Run()
}

func (execPassCommandRunner) Output(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // executable and arguments are fixed internal constants.
	command.Env = env
	return command.Output()
}

func managedPassEnvironment() bool {
	return os.Getenv("GNUPGHOME") == managedPassGPGHome && os.Getenv("PASSWORD_STORE_DIR") == managedPassStoreDir
}

// ensureManagedPassStore acquires the same lease as production but releases it
// after validation. Production retains the lease for the process lifetime.
func ensureManagedPassStore(ctx context.Context, root string, runner passCommandRunner) error {
	lease, err := acquireManagedPassLease(root)
	if err != nil {
		return err
	}
	defer releaseManagedPassLease(lease)
	return ensureManagedPassStoreLocked(ctx, root, runner)
}

func ensureProcessManagedPassStore(ctx context.Context, root string, runner passCommandRunner) error {
	processManagedPassLeaseMu.Lock()
	defer processManagedPassLeaseMu.Unlock()
	if processManagedPassLease != nil {
		return ensureManagedPassStoreLocked(ctx, root, runner)
	}
	lease, err := acquireManagedPassLease(root)
	if err != nil {
		return err
	}
	if err := ensureManagedPassStoreLocked(ctx, root, runner); err != nil {
		releaseManagedPassLease(lease)
		return err
	}
	processManagedPassLease = lease
	return nil
}

func acquireManagedPassLease(root string) (*os.File, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("prepare managed pass root")
	}
	if err := os.Chmod(root, 0o700); err != nil { // #nosec G302 -- private state directory.
		return nil, fmt.Errorf("restrict managed pass root")
	}
	fd, err := unix.Open(filepath.Join(root, managedPassLockName), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open managed pass lease")
	}
	file := os.NewFile(uintptr(fd), managedPassLockName)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open managed pass lease")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("restrict managed pass lease")
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("managed pass store is already in use")
	}
	return file, nil
}

func releaseManagedPassLease(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

func ensureManagedPassStoreLocked(ctx context.Context, root string, runner passCommandRunner) error {
	if runner == nil {
		return fmt.Errorf("managed pass command runner is unavailable")
	}
	current := filepath.Join(root, "current")
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect managed pass state")
	}
	currentExists := false
	for _, entry := range entries {
		if entry.Name() == "current" {
			currentExists = true
		}
	}
	for _, entry := range entries {
		switch {
		case entry.Name() == managedPassLockName, entry.Name() == "current":
		case strings.HasPrefix(entry.Name(), managedPassStagePrefix) && !currentExists:
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() || os.RemoveAll(filepath.Join(root, entry.Name())) != nil {
				return fmt.Errorf("managed pass state is inconsistent")
			}
		default:
			return fmt.Errorf("managed pass state is inconsistent")
		}
	}
	if currentExists {
		return validateManagedPassStore(ctx, current, runner)
	}
	return initializeManagedPassStore(ctx, root, runner)
}

func initializeManagedPassStore(ctx context.Context, root string, runner passCommandRunner) error {
	stage, err := os.MkdirTemp(root, managedPassStagePrefix)
	if err != nil {
		return fmt.Errorf("prepare managed pass staging state")
	}
	// A failed initialization intentionally leaves a recognizable staging
	// directory. The next lease holder may remove it only while current is absent.
	if err := os.Chmod(stage, 0o700); err != nil { // #nosec G302 -- private state directory.
		return fmt.Errorf("restrict managed pass staging state")
	}
	gnupgHome := filepath.Join(stage, "gnupg")
	storeDir := filepath.Join(stage, "password-store")
	for _, directory := range []string{gnupgHome, storeDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return fmt.Errorf("prepare managed pass state")
		}
	}
	env := managedPassCommandEnvironment(stage)
	args := []string{"--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", managedPassIdentity, "future-default", "default", "0"}
	if err := runner.Run(ctx, env, "gpg", args...); err != nil {
		return fmt.Errorf("generate managed pass identity")
	}
	fingerprint, err := managedPassFingerprint(ctx, stage, runner)
	if err != nil {
		return err
	}
	if err := runner.Run(ctx, env, "pass", "init", fingerprint); err != nil {
		return fmt.Errorf("initialize managed password store")
	}
	if err := writeManagedPassMarker(stage, fingerprint); err != nil {
		return err
	}
	if err := validateManagedPassStore(ctx, stage, runner); err != nil {
		return err
	}
	if err := syncManagedPassTree(stage); err != nil {
		return err
	}
	if err := os.Rename(stage, filepath.Join(root, "current")); err != nil {
		return fmt.Errorf("publish managed pass state")
	}
	directory, err := os.Open(root)
	if err != nil {
		return fmt.Errorf("open managed pass root")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync managed pass root")
	}
	return nil
}

func managedPassCommandEnvironment(state string) []string {
	return []string{
		"GNUPGHOME=" + filepath.Join(state, "gnupg"),
		"PASSWORD_STORE_DIR=" + filepath.Join(state, "password-store"),
		"HOME=" + state,
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
	}
}

func syncManagedPassTree(root string) error {
	scopedRoot, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open managed pass staging root")
	}
	defer scopedRoot.Close()
	directories := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("inspect managed pass staging state")
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve managed pass staging state")
		}
		file, err := scopedRoot.Open(relative)
		if err != nil {
			return fmt.Errorf("open managed pass staging state")
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("sync managed pass staging state")
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close managed pass staging state")
		}
		return nil
	}); err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		directory, err := os.Open(directories[index])
		if err != nil {
			return fmt.Errorf("open managed pass staging directory")
		}
		if err := directory.Sync(); err != nil {
			_ = directory.Close()
			return fmt.Errorf("sync managed pass staging directory")
		}
		if err := directory.Close(); err != nil {
			return fmt.Errorf("close managed pass staging directory")
		}
	}
	return nil
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
	fingerprint, err := managedPassFingerprint(ctx, root, runner)
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

func managedPassFingerprint(ctx context.Context, root string, runner passCommandRunner) (string, error) {
	output, err := runner.Output(ctx, managedPassCommandEnvironment(root), "gpg", "--batch", "--no-tty", "--with-colons", "--list-secret-keys", managedPassIdentity)
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
