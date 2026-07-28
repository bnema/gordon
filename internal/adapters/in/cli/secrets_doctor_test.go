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

	writeCheckRan := false
	runManagedPassDoctor = func(_ context.Context, check func() error) error {
		require.NotNil(t, check)
		writeCheckRan = true
		return nil
	}

	configFile := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configFile, []byte("[auth]\nsecrets_backend = \"pass\"\n"), 0o600))

	var output bytes.Buffer
	require.NoError(t, runSecretsDoctor(context.Background(), configFile, true, false, &output))
	assert.True(t, writeCheckRan)
	assert.Equal(t, "Managed pass backend is healthy\n", output.String())
}

func TestRunSecretsDoctorJSONOutput(t *testing.T) {
	original := runManagedPassDoctor
	t.Cleanup(func() { runManagedPassDoctor = original })
	runManagedPassDoctor = func(context.Context, func() error) error { return nil }

	configFile := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configFile, []byte("[auth]\nsecrets_backend = \"pass\"\n"), 0o600))

	var output bytes.Buffer
	require.NoError(t, runSecretsDoctor(context.Background(), configFile, true, true, &output))

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
