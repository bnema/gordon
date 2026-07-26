package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestEnsureManagedPassStoreInitializesOnceAndValidatesIdempotently(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	runner := &fakePassCommandRunner{root: root, fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}

	require.NoError(t, ensureManagedPassStore(context.Background(), root, runner))
	firstCalls := append([]string(nil), runner.calls...)
	require.NoError(t, ensureManagedPassStore(context.Background(), root, runner))
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
	for _, path := range []string{filepath.Join(current, managedPassMarkerName), filepath.Join(current, "password-store", ".gpg-id")} {
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
		err := ensureManagedPassStore(context.Background(), root, runner)
		require.Error(t, err)
		assert.Empty(t, runner.calls, "partial state must never trigger regeneration")
	})
	t.Run("marker mismatch", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "secrets")
		runner := &fakePassCommandRunner{root: root, fingerprint: fingerprint}
		require.NoError(t, ensureManagedPassStore(context.Background(), root, runner))
		require.NoError(t, os.WriteFile(filepath.Join(root, "current", managedPassMarkerName), []byte("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF\n"), 0o600))
		err := ensureManagedPassStore(context.Background(), root, runner)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), fingerprint)
	})
	t.Run("key fingerprint mismatch", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "secrets")
		runner := &fakePassCommandRunner{root: root, fingerprint: fingerprint}
		require.NoError(t, ensureManagedPassStore(context.Background(), root, runner))
		runner.fingerprint = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
		err := ensureManagedPassStore(context.Background(), root, runner)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), runner.fingerprint)
	})
}

func TestManagedControlSecretsVolumeNameIsStableAndInstallationScoped(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	assert.Equal(t, managedControlSecretsVolumeName(root), managedControlSecretsVolumeName(filepath.Join(root, ".")))
	assert.NotEqual(t, managedControlSecretsVolumeName(root), managedControlSecretsVolumeName(filepath.Join(t.TempDir(), "data")))
	assert.NotContains(t, managedControlSecretsVolumeName(root), root)
}

func TestManagedPassLeaseRejectsConcurrentWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	first, err := acquireManagedPassLease(root)
	require.NoError(t, err)
	defer releaseManagedPassLease(first)
	second, err := acquireManagedPassLease(root)
	require.Error(t, err)
	assert.Nil(t, second)
}

func TestEnsureManagedPassStoreCleansRecognizedCrashStageOnlyBeforePublish(t *testing.T) {
	fingerprint := "0123456789ABCDEF0123456789ABCDEF01234567"
	root := filepath.Join(t.TempDir(), "secrets")
	require.NoError(t, os.MkdirAll(filepath.Join(root, managedPassStagePrefix+"crash", "gnupg"), 0o700))
	runner := &fakePassCommandRunner{root: root, fingerprint: fingerprint}
	require.NoError(t, ensureManagedPassStore(context.Background(), root, runner))
	_, err := os.Stat(filepath.Join(root, managedPassStagePrefix+"crash"))
	require.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, os.Mkdir(filepath.Join(root, managedPassStagePrefix+"late"), 0o700))
	err = ensureManagedPassStore(context.Background(), root, runner)
	require.Error(t, err, "published current plus staging must fail closed")
	assert.Equal(t, 1, strings.Count(strings.Join(runner.calls, "\n"), "--quick-generate-key"))
}

func TestEnsureManagedPassStoreNeverRegeneratesInvalidPublishedCurrent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	runner := &fakePassCommandRunner{root: root, fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}
	require.NoError(t, ensureManagedPassStore(context.Background(), root, runner))
	require.NoError(t, os.Remove(filepath.Join(root, "current", "password-store", ".gpg-id")))
	err := ensureManagedPassStore(context.Background(), root, runner)
	require.Error(t, err)
	assert.Equal(t, 1, strings.Count(strings.Join(runner.calls, "\n"), "--quick-generate-key"))
}

func TestEnsureManagedPassStoreRedactsCommandFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-external-path")
	runner := &fakePassCommandRunner{root: root, fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567", fail: errors.New("secret=value path=/private")}
	err := ensureManagedPassStore(context.Background(), root, runner)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret=value")
	assert.NotContains(t, err.Error(), root)
	assert.NotContains(t, err.Error(), "/private")
}
