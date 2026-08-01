package domainsecrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/bnema/gordon/internal/domain"
)

type fakePassCommandRunner struct {
	root        string
	fingerprint string
	fail        error
	calls       []string
}

func (f *fakePassCommandRunner) Run(_ context.Context, env []string, name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	root := fakePassStateFromEnv(env)
	if f.fail != nil {
		return f.fail
	}
	if name == "gpg" && strings.Contains(strings.Join(args, " "), "--quick-generate-key") {
		return os.WriteFile(filepath.Join(root, "gnupg", "pubring.kbx"), []byte("generated"), 0o600)
	}
	if name == "pass" && len(args) == 2 && args[0] == "init" {
		return os.WriteFile(filepath.Join(root, "password-store", ".gpg-id"), []byte(args[1]+"\n"), 0o600)
	}
	return nil
}

func (f *fakePassCommandRunner) Output(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.fail != nil {
		return []byte("external path=/private secret=value"), f.fail
	}
	return []byte("sec:u:255:22:KEY:0:0:::::scESC:::+::ed25519:::0:\nfpr:::::::::" + f.fingerprint + ":\n"), nil
}

func fakePassStateFromEnv(env []string) string {
	for _, value := range env {
		if strings.HasPrefix(value, "GNUPGHOME=") {
			return filepath.Dir(strings.TrimPrefix(value, "GNUPGHOME="))
		}
	}
	return ""
}

func testManagedPassStore(t *testing.T, root string, runner PassCommandRunner) *ManagedPassStore {
	t.Helper()
	return NewManagedPassStore(ManagedPassPaths{Root: root}, runner)
}

func TestEnsureManagedPassStoreInitializesOnceAndValidatesIdempotently(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	runner := &fakePassCommandRunner{root: root, fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}
	store := testManagedPassStore(t, root, runner)

	require.NoError(t, store.Ensure(context.Background()))
	firstCalls := append([]string(nil), runner.calls...)
	require.NoError(t, store.Ensure(context.Background()))
	allCalls := strings.Join(runner.calls, "\n")
	assert.Equal(t, 1, strings.Count(allCalls, "--quick-generate-key"), "validated startup must not regenerate")
	assert.Equal(t, 1, strings.Count(allCalls, "pass init "), "validated startup must not reinitialize pass")
	assert.Contains(t, strings.Join(firstCalls, "\n"), "pass init "+runner.fingerprint)
	assert.NotContains(t, strings.Join(firstCalls, "\n"), "@")

	current := filepath.Join(root, "current")
	for _, path := range []string{root, current, filepath.Join(current, "gnupg"), filepath.Join(current, "password-store")} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}
	for _, path := range []string{filepath.Join(current, ManagedPassMarkerName), filepath.Join(current, "password-store", ".gpg-id")} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestEnsureManagedPassStoreFailsClosedForPartialOrMismatchedState(t *testing.T) {
	fingerprint := "0123456789ABCDEF0123456789ABCDEF01234567"
	t.Run("partial", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, "unknown"), 0o700))
		runner := &fakePassCommandRunner{root: root, fingerprint: fingerprint}
		store := testManagedPassStore(t, root, runner)
		err := store.Ensure(context.Background())
		require.Error(t, err)
		assert.Empty(t, runner.calls, "partial state must never trigger regeneration")
	})
	t.Run("marker mismatch", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "secrets")
		runner := &fakePassCommandRunner{root: root, fingerprint: fingerprint}
		store := testManagedPassStore(t, root, runner)
		require.NoError(t, store.Ensure(context.Background()))
		require.NoError(t, os.WriteFile(filepath.Join(root, "current", ManagedPassMarkerName), []byte("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF\n"), 0o600))
		err := store.Ensure(context.Background())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), fingerprint)
	})
	t.Run("key fingerprint mismatch", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "secrets")
		runner := &fakePassCommandRunner{root: root, fingerprint: fingerprint}
		store := testManagedPassStore(t, root, runner)
		require.NoError(t, store.Ensure(context.Background()))
		runner.fingerprint = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
		err := store.Ensure(context.Background())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), runner.fingerprint)
	})
}

func TestManagedPassLeaseRejectsConcurrentWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	store := testManagedPassStore(t, root, &fakePassCommandRunner{})
	first, err := store.acquireLease()
	require.NoError(t, err)
	defer store.releaseLease(first)
	second, err := store.acquireLease()
	require.Error(t, err)
	assert.Nil(t, second)
}

func TestHoldManagedPassStoreRejectsDoctorUntilCancellationThenReleases(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	runner := &fakePassCommandRunner{root: root, fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}
	store := testManagedPassStore(t, root, runner)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.Hold(ctx, func() error {
			close(ready)
			return nil
		})
	}()

	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("managed pass holder did not become ready")
	}
	err := store.Ensure(context.Background())
	require.ErrorIs(t, err, domain.ErrManagedPassLeaseUnavailable)

	cancel()
	require.NoError(t, <-done)
	require.NoError(t, store.Ensure(context.Background()))
}

func TestEnsureManagedPassStoreCleansRecognizedCrashStageOnlyBeforePublish(t *testing.T) {
	fingerprint := "0123456789ABCDEF0123456789ABCDEF01234567"
	root := filepath.Join(t.TempDir(), "secrets")
	require.NoError(t, os.MkdirAll(filepath.Join(root, managedPassStagePrefix+"crash", "gnupg"), 0o700))
	runner := &fakePassCommandRunner{root: root, fingerprint: fingerprint}
	store := testManagedPassStore(t, root, runner)
	require.NoError(t, store.Ensure(context.Background()))
	_, err := os.Stat(filepath.Join(root, managedPassStagePrefix+"crash"))
	require.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, os.Mkdir(filepath.Join(root, managedPassStagePrefix+"late"), 0o700))
	err = store.Ensure(context.Background())
	require.Error(t, err, "published current plus staging must fail closed")
	assert.Equal(t, 1, strings.Count(strings.Join(runner.calls, "\n"), "--quick-generate-key"))
}

func TestEnsureManagedPassStoreNeverRegeneratesInvalidPublishedCurrent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	runner := &fakePassCommandRunner{root: root, fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}
	store := testManagedPassStore(t, root, runner)
	require.NoError(t, store.Ensure(context.Background()))
	require.NoError(t, os.Remove(filepath.Join(root, "current", "password-store", ".gpg-id")))
	err := store.Ensure(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, strings.Count(strings.Join(runner.calls, "\n"), "--quick-generate-key"))
}

func TestEnsureManagedPassStoreRedactsCommandFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-external-path")
	runner := &fakePassCommandRunner{root: root, fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567", fail: errors.New("secret=value path=/private")}
	store := testManagedPassStore(t, root, runner)
	err := store.Ensure(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret=value")
	assert.NotContains(t, err.Error(), root)
	assert.NotContains(t, err.Error(), "/private")
}

func TestRunManagedPassDoctorHoldsLeaseDuringCallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	runner := &fakePassCommandRunner{root: root, fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}
	store := testManagedPassStore(t, root, runner)
	require.NoError(t, store.Ensure(context.Background()))

	checkStarted := make(chan struct{})
	releaseCheck := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.RunDoctor(context.Background(), func() error {
			close(checkStarted)
			<-releaseCheck
			return nil
		})
	}()

	select {
	case <-checkStarted:
	case <-time.After(time.Second):
		t.Fatal("managed pass doctor callback did not start")
	}
	second, err := store.acquireLease()
	require.Error(t, err)
	assert.Nil(t, second)
	close(releaseCheck)
	require.NoError(t, <-done)
	require.NoError(t, store.Ensure(context.Background()))
}

func TestSyncManagedPassTreeSkipsNonRegular(t *testing.T) {
	root := t.TempDir()
	store := testManagedPassStore(t, root, &fakePassCommandRunner{})
	require.NoError(t, os.WriteFile(filepath.Join(root, "regular"), []byte("data"), 0o600))
	fifo := filepath.Join(root, "agent.sock")
	require.NoError(t, unix.Mkfifo(fifo, 0o600))
	require.NoError(t, store.syncTree(root))
}

func TestSyncManagedPassTreeRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	store := testManagedPassStore(t, root, &fakePassCommandRunner{})
	target := filepath.Join(root, "target")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(root, "link")))
	err := store.syncTree(root)
	require.Error(t, err)
}
