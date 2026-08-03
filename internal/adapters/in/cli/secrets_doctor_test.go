package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSecretsDoctorInvokesWriteCheckInsideDoctorLease(t *testing.T) {
	original := runManagedPassDoctor
	t.Cleanup(func() { runManagedPassDoctor = original })

	store := &fakeManagedPassWriteCheckStore{values: map[string]string{}}
	originalStore := openManagedPassWriteCheckStore
	t.Cleanup(func() { openManagedPassWriteCheckStore = originalStore })
	openManagedPassWriteCheckStore = func() (managedPassWriteCheckStore, error) { return store, nil }

	leaseHeld := false
	runManagedPassDoctor = func(_ context.Context, check func() error) error {
		require.NotNil(t, check)
		leaseHeld = true
		defer func() { leaseHeld = false }()
		return check()
	}

	configFile := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configFile, []byte("[auth]\nsecrets_backend = \"pass\"\n"), 0o600))

	store.afterSet = func() { assert.True(t, leaseHeld, "the write check must run while the doctor lease is held") }

	var output bytes.Buffer
	require.NoError(t, runSecretsDoctor(context.Background(), configFile, true, &output, false))
	assert.Equal(t, 1, store.deleteAttempts, "the write check must run and clean up its marker")
	assert.Equal(t, "Managed pass backend is healthy\n", output.String())
}

func TestRunSecretsDoctorSkipsWriteCheckWhenDisabled(t *testing.T) {
	original := runManagedPassDoctor
	t.Cleanup(func() { runManagedPassDoctor = original })

	store := &fakeManagedPassWriteCheckStore{values: map[string]string{}}
	originalStore := openManagedPassWriteCheckStore
	t.Cleanup(func() { openManagedPassWriteCheckStore = originalStore })
	openManagedPassWriteCheckStore = func() (managedPassWriteCheckStore, error) { return store, nil }

	runManagedPassDoctor = func(_ context.Context, check func() error) error {
		require.NotNil(t, check)
		return check()
	}

	configFile := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configFile, []byte("[auth]\nsecrets_backend = \"pass\"\n"), 0o600))

	var output bytes.Buffer
	require.NoError(t, runSecretsDoctor(context.Background(), configFile, false, &output, false))
	assert.Equal(t, 0, store.deleteAttempts, "the write check must not touch the backend when disabled")
	assert.Empty(t, store.lastValue)
	assert.Equal(t, "Managed pass backend is healthy\n", output.String())
}

func TestRunSecretsDoctorPropagatesWriteCheckFailure(t *testing.T) {
	original := runManagedPassDoctor
	t.Cleanup(func() { runManagedPassDoctor = original })

	store := &fakeManagedPassWriteCheckStore{values: map[string]string{}, getAllErr: errors.New("read failed")}
	originalStore := openManagedPassWriteCheckStore
	t.Cleanup(func() { openManagedPassWriteCheckStore = originalStore })
	openManagedPassWriteCheckStore = func() (managedPassWriteCheckStore, error) { return store, nil }

	runManagedPassDoctor = func(_ context.Context, check func() error) error { return check() }

	configFile := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configFile, []byte("[auth]\nsecrets_backend = \"pass\"\n"), 0o600))

	var output bytes.Buffer
	err := runSecretsDoctor(context.Background(), configFile, true, &output, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validate managed pass backend")
	assert.NotContains(t, err.Error(), store.lastValue)
	assert.Empty(t, output.String())
}

func TestRunSecretsDoctorJSONOutput(t *testing.T) {
	original := runManagedPassDoctor
	t.Cleanup(func() { runManagedPassDoctor = original })
	runManagedPassDoctor = func(context.Context, func() error) error { return nil }

	configFile := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configFile, []byte("[auth]\nsecrets_backend = \"pass\"\n"), 0o600))

	var output bytes.Buffer
	require.NoError(t, runSecretsDoctor(context.Background(), configFile, true, &output, true))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &payload))
	assert.Equal(t, "healthy", payload["status"])
	assert.Equal(t, true, payload["write_check"])
}

func TestValidateManagedPassConfigReportsResolvedBackend(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configFile, []byte("[auth]\nsecrets_backend = \"unsafe\"\n"), 0o600))

	err := validateManagedPassConfig(configFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `resolved "unsafe"`)
}

func TestRunManagedPassWriteCheckCancelsBeforeWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runManagedPassWriteCheck(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestRunManagedPassWriteCheckJoinsPrimaryAndCleanupErrors(t *testing.T) {
	original := openManagedPassWriteCheckStore
	t.Cleanup(func() { openManagedPassWriteCheckStore = original })

	store := &fakeManagedPassWriteCheckStore{
		values:    map[string]string{},
		getAllErr: errors.New("read failed"),
		deleteErr: errors.New("cleanup failed"),
	}
	openManagedPassWriteCheckStore = func() (managedPassWriteCheckStore, error) { return store, nil }

	err := runManagedPassWriteCheck(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, store.deleteAttempts)
	assert.Contains(t, err.Error(), "read managed pass check failed")
	assert.Contains(t, err.Error(), "cleanup failed")
	assert.NotContains(t, err.Error(), "secret=")
	assert.NotContains(t, err.Error(), store.lastValue)
}

func TestRunManagedPassWriteCheckJoinsCleanupOnlyErrors(t *testing.T) {
	original := openManagedPassWriteCheckStore
	t.Cleanup(func() { openManagedPassWriteCheckStore = original })

	store := &fakeManagedPassWriteCheckStore{
		values:    map[string]string{},
		deleteErr: errors.New("cleanup failed"),
	}
	openManagedPassWriteCheckStore = func() (managedPassWriteCheckStore, error) { return store, nil }

	ctx, cancel := context.WithCancel(context.Background())
	store.afterSet = cancel

	err := runManagedPassWriteCheck(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.Contains(t, err.Error(), "cleanup failed")
	assert.NotContains(t, err.Error(), "secret=")
	assert.NotContains(t, err.Error(), store.lastValue)
}

func TestRunManagedPassWriteCheckDeletesExactlyOnceOnSuccess(t *testing.T) {
	original := openManagedPassWriteCheckStore
	t.Cleanup(func() { openManagedPassWriteCheckStore = original })

	store := &fakeManagedPassWriteCheckStore{values: map[string]string{}}
	openManagedPassWriteCheckStore = func() (managedPassWriteCheckStore, error) { return store, nil }

	require.NoError(t, runManagedPassWriteCheck(context.Background()))
	assert.Equal(t, 1, store.deleteAttempts)
	assert.NotContains(t, store.lastValue, "secret=")
}

func TestRunManagedPassWriteCheckDeletesExactlyOnceWhenFinalDeleteFails(t *testing.T) {
	original := openManagedPassWriteCheckStore
	t.Cleanup(func() { openManagedPassWriteCheckStore = original })

	store := &fakeManagedPassWriteCheckStore{
		values:    map[string]string{},
		deleteErr: errors.New("cleanup failed"),
	}
	openManagedPassWriteCheckStore = func() (managedPassWriteCheckStore, error) { return store, nil }

	err := runManagedPassWriteCheck(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, store.deleteAttempts)
	assert.Contains(t, err.Error(), "cleanup failed")
	assert.NotContains(t, err.Error(), "secret=")
	assert.NotContains(t, err.Error(), store.lastValue)
}

type fakeManagedPassWriteCheckStore struct {
	values         map[string]string
	lastValue      string
	getAllErr      error
	deleteErr      error
	deleteAttempts int
	afterSet       func()
}

func (f *fakeManagedPassWriteCheckStore) Set(_ string, secretsMap map[string]string) error {
	for _, value := range secretsMap {
		f.lastValue = value
		f.values["GORDON_DOCTOR_MARKER"] = value
	}
	if f.afterSet != nil {
		f.afterSet()
	}
	return nil
}

func (f *fakeManagedPassWriteCheckStore) GetAll(string) (map[string]string, error) {
	if f.getAllErr != nil {
		return nil, f.getAllErr
	}
	out := make(map[string]string, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}
	return out, nil
}

func (f *fakeManagedPassWriteCheckStore) Delete(string, string) error {
	f.deleteAttempts++
	return f.deleteErr
}
