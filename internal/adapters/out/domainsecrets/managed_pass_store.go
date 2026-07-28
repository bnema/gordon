package domainsecrets

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

const (
	// ManagedPassMarkerName is the integrity marker filename in a managed pass tree.
	ManagedPassMarkerName = ".gordon-managed-pass-fingerprint"

	managedPassIdentity    = "Gordon Managed Secrets"
	managedPassLockName    = ".control.lock"
	managedPassStagePrefix = ".initialize-"
)

// PassCommandRunner executes pass and gpg commands with a fixed environment.
type PassCommandRunner interface {
	Run(context.Context, []string, string, ...string) error
	Output(context.Context, []string, string, ...string) ([]byte, error)
}

// ExecPassCommandRunner runs pass and gpg using os/exec.
type ExecPassCommandRunner struct{}

func (ExecPassCommandRunner) Run(ctx context.Context, env []string, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // executable and arguments are fixed internal constants.
	command.Env = env
	return command.Run()
}

func (ExecPassCommandRunner) Output(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // executable and arguments are fixed internal constants.
	command.Env = env
	return command.Output()
}

// ManagedPassPaths locates the control-owned managed pass backend on disk.
type ManagedPassPaths struct {
	Root     string
	GPGHome  string
	StoreDir string
}

// ManagedPassStore owns initialization, validation, and lease coordination for
// the managed pass backend.
type ManagedPassStore struct {
	paths  ManagedPassPaths
	runner PassCommandRunner

	processLeaseMu sync.Mutex
	processLease   *os.File
}

// NewManagedPassStore creates a managed pass store for the given paths and runner.
func NewManagedPassStore(paths ManagedPassPaths, runner PassCommandRunner) *ManagedPassStore {
	return &ManagedPassStore{paths: paths, runner: runner}
}

// EnvironmentConfigured reports whether the current process environment points
// at this store's published pass tree.
func (s *ManagedPassStore) EnvironmentConfigured() bool {
	return os.Getenv("GNUPGHOME") == s.paths.GPGHome && os.Getenv("PASSWORD_STORE_DIR") == s.paths.StoreDir
}

// Ensure acquires the managed pass lease, validates or initializes state, and
// releases the lease before returning.
func (s *ManagedPassStore) Ensure(ctx context.Context) error {
	lease, err := s.acquireLease()
	if err != nil {
		return err
	}
	defer s.releaseLease(lease)
	return s.ensureLocked(ctx)
}

// EnsureProcess validates or initializes managed pass state and retains the
// production lease for the process lifetime after the first successful call.
func (s *ManagedPassStore) EnsureProcess(ctx context.Context) error {
	s.processLeaseMu.Lock()
	defer s.processLeaseMu.Unlock()
	if s.processLease != nil {
		return s.ensureLocked(ctx)
	}
	lease, err := s.acquireLease()
	if err != nil {
		return err
	}
	if err := s.ensureLocked(ctx); err != nil {
		s.releaseLease(lease)
		return err
	}
	s.processLease = lease
	return nil
}

// RunDoctor validates the managed pass store while holding its exclusive lease
// for the duration of the optional check callback.
func (s *ManagedPassStore) RunDoctor(ctx context.Context, check func() error) error {
	lease, err := s.acquireLease()
	if err != nil {
		return err
	}
	defer s.releaseLease(lease)
	if err := s.ensureLocked(ctx); err != nil {
		return err
	}
	if check != nil {
		return check()
	}
	return nil
}

// Hold validates the managed pass store, signals readiness, and retains its
// production lease until the context is canceled.
func (s *ManagedPassStore) Hold(ctx context.Context, ready func() error) error {
	lease, err := s.acquireLease()
	if err != nil {
		return err
	}
	defer s.releaseLease(lease)
	if err := s.ensureLocked(ctx); err != nil {
		return err
	}
	if ready == nil {
		return fmt.Errorf("managed pass readiness callback is unavailable")
	}
	if err := ready(); err != nil {
		return fmt.Errorf("report managed pass readiness: %w", err)
	}
	<-ctx.Done()
	return nil
}

func (s *ManagedPassStore) acquireLease() (*os.File, error) {
	root := s.paths.Root
	log := zerowrap.Default()
	if err := os.MkdirAll(root, 0o700); err != nil {
		log.Debug().Err(err).Msg("failed to prepare managed pass root")
		return nil, fmt.Errorf("prepare managed pass root")
	}
	if err := os.Chmod(root, 0o700); err != nil { // #nosec G302 -- private state directory.
		log.Debug().Err(err).Msg("failed to restrict managed pass root")
		return nil, fmt.Errorf("restrict managed pass root")
	}
	fd, err := unix.Open(filepath.Join(root, managedPassLockName), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		log.Debug().Err(err).Msg("failed to open managed pass lease")
		return nil, fmt.Errorf("open managed pass lease")
	}
	file := os.NewFile(uintptr(fd), managedPassLockName)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open managed pass lease")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		log.Debug().Err(err).Msg("failed to restrict managed pass lease")
		return nil, fmt.Errorf("restrict managed pass lease")
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		log.Debug().Err(err).Msg("managed pass lease unavailable")
		return nil, fmt.Errorf("managed pass store is already in use")
	}
	return file, nil
}

func (s *ManagedPassStore) releaseLease(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

func (s *ManagedPassStore) ensureLocked(ctx context.Context) error {
	if s.runner == nil {
		return fmt.Errorf("managed pass command runner is unavailable")
	}
	root := s.paths.Root
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
		return s.validateStore(ctx, current)
	}
	return s.initializeStore(ctx, root)
}

func (s *ManagedPassStore) initializeStore(ctx context.Context, root string) error {
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
	env := s.commandEnvironment(stage)
	args := []string{"--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", managedPassIdentity, "future-default", "default", "0"}
	if err := s.runner.Run(ctx, env, "gpg", args...); err != nil {
		return fmt.Errorf("generate managed pass identity")
	}
	fingerprint, err := s.fingerprint(ctx, stage)
	if err != nil {
		return err
	}
	if err := s.runner.Run(ctx, env, "pass", "init", fingerprint); err != nil {
		return fmt.Errorf("initialize managed password store")
	}
	if err := s.writeMarker(stage, fingerprint); err != nil {
		return err
	}
	if err := s.validateStore(ctx, stage); err != nil {
		return err
	}
	// GPG may leave agent sockets whose paths would become stale after the
	// atomic staging-directory rename. Stop the staging agent before syncing
	// and publishing the durable tree.
	if err := s.runner.Run(ctx, env, "gpgconf", "--kill", "gpg-agent"); err != nil {
		return fmt.Errorf("stop managed pass staging agent")
	}
	if err := s.syncTree(stage); err != nil {
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

func (s *ManagedPassStore) commandEnvironment(state string) []string {
	return []string{
		"GNUPGHOME=" + filepath.Join(state, "gnupg"),
		"PASSWORD_STORE_DIR=" + filepath.Join(state, "password-store"),
		"HOME=" + state,
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
	}
}

func (s *ManagedPassStore) syncTree(root string) error {
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
		return s.syncRegularFile(scopedRoot, root, path, entry)
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

func (s *ManagedPassStore) syncRegularFile(scopedRoot *os.Root, root, path string, entry os.DirEntry) error {
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() {
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
}

func (s *ManagedPassStore) validateStore(ctx context.Context, root string) error {
	entries, err := os.ReadDir(root)
	if err != nil || !managedPassRootEntries(entries) {
		return fmt.Errorf("managed pass state is inconsistent")
	}
	gnupgHome := filepath.Join(root, "gnupg")
	storeDir := filepath.Join(root, "password-store")
	markerPath := filepath.Join(root, ManagedPassMarkerName)
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
	fingerprint, err := s.fingerprint(ctx, root)
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
	allowed := map[string]bool{"gnupg": false, "password-store": false, ManagedPassMarkerName: false}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || allowed[entry.Name()] {
			return false
		}
		allowed[entry.Name()] = true
	}
	return allowed["gnupg"] && allowed["password-store"] && allowed[ManagedPassMarkerName]
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

func (s *ManagedPassStore) fingerprint(ctx context.Context, root string) (string, error) {
	output, err := s.runner.Output(ctx, s.commandEnvironment(root), "gpg", "--batch", "--no-tty", "--with-colons", "--list-secret-keys", managedPassIdentity)
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

func (s *ManagedPassStore) writeMarker(root, fingerprint string) error {
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
	if err := os.Rename(temporary, filepath.Join(root, ManagedPassMarkerName)); err != nil {
		return fmt.Errorf("commit managed pass integrity marker")
	}
	return nil
}
