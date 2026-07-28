package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
)

func TestManagedControlSecretsVolumeNameIsStableAndInstallationScoped(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	assert.Equal(t, managedControlSecretsVolumeName(root), managedControlSecretsVolumeName(filepath.Join(root, ".")))
	assert.NotEqual(t, managedControlSecretsVolumeName(root), managedControlSecretsVolumeName(filepath.Join(t.TempDir(), "data")))
	assert.NotContains(t, managedControlSecretsVolumeName(root), root)
}

func TestRunManagedPassDoctorHoldsLeaseDuringCallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	current := filepath.Join(root, "current")
	paths := domainsecrets.ManagedPassPaths{
		Root:     root,
		GPGHome:  filepath.Join(current, "gnupg"),
		StoreDir: filepath.Join(current, "password-store"),
	}
	runner := &appFakePassCommandRunner{fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}
	store := domainsecrets.NewManagedPassStore(paths, runner)

	restore := setManagedPassStoreForTest(store)
	t.Cleanup(restore)

	t.Setenv("GNUPGHOME", paths.GPGHome)
	t.Setenv("PASSWORD_STORE_DIR", paths.StoreDir)

	require.NoError(t, ValidateManagedPassBackend(context.Background()))

	checkStarted := make(chan struct{})
	releaseCheck := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunManagedPassDoctor(context.Background(), func() error {
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
	err := ValidateManagedPassBackend(context.Background())
	require.ErrorContains(t, err, "managed pass store is already in use")
	close(releaseCheck)
	require.NoError(t, <-done)
	require.NoError(t, ValidateManagedPassBackend(context.Background()))
}

func TestManagedPassStoreAccessorIsRaceFree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	current := filepath.Join(root, "current")
	paths := domainsecrets.ManagedPassPaths{
		Root:     root,
		GPGHome:  filepath.Join(current, "gnupg"),
		StoreDir: filepath.Join(current, "password-store"),
	}
	store := domainsecrets.NewManagedPassStore(paths, &appFakePassCommandRunner{fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"})
	restore := setManagedPassStoreForTest(store)
	t.Cleanup(restore)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			_ = managedPassStore()
		}
	}()
	for range 100 {
		_ = managedPassStore()
	}
	<-done
	require.Same(t, store, managedPassStore())
}

func TestManagedPassStoreInstanceIsRaceFree(t *testing.T) {
	start := make(chan struct{})
	done := make(chan struct{})
	for range 32 {
		go func() {
			<-start
			_ = managedPassStore()
			done <- struct{}{}
		}()
	}
	close(start)
	for range 32 {
		<-done
	}
	require.NotNil(t, managedPassStore())
}

type appFakePassCommandRunner struct {
	fingerprint string
}

func (f *appFakePassCommandRunner) Run(_ context.Context, env []string, name string, args ...string) error {
	root := appFakePassStateFromEnv(env)
	if name == "gpg" && strings.Contains(strings.Join(args, " "), "--quick-generate-key") {
		return os.WriteFile(filepath.Join(root, "gnupg", "pubring.kbx"), []byte("generated"), 0o600)
	}
	if name == "pass" && len(args) == 2 && args[0] == "init" {
		return os.WriteFile(filepath.Join(root, "password-store", ".gpg-id"), []byte(args[1]+"\n"), 0o600)
	}
	return nil
}

func (f *appFakePassCommandRunner) Output(_ context.Context, _ []string, _ string, _ ...string) ([]byte, error) {
	return []byte("sec:u:255:22:KEY:0:0:::::scESC:::+::ed25519:::0:\nfpr:::::::::" + f.fingerprint + ":\n"), nil
}

func appFakePassStateFromEnv(env []string) string {
	for _, value := range env {
		if strings.HasPrefix(value, "GNUPGHOME=") {
			return filepath.Dir(strings.TrimPrefix(value, "GNUPGHOME="))
		}
	}
	return ""
}
