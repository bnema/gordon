package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindRunningPidFileInLocations_PrefersLiveProcessOverStale(t *testing.T) {
	tmpDir := t.TempDir()
	staleFile := filepath.Join(tmpDir, "stale.pid")
	liveFile := filepath.Join(tmpDir, "live.pid")

	require.NoError(t, os.WriteFile(staleFile, []byte("999999"), 0600))
	require.NoError(t, os.WriteFile(liveFile, []byte("1"), 0600))

	path, pid, err := findRunningPidFileInLocations([]string{staleFile, liveFile})
	require.NoError(t, err)
	assert.Equal(t, liveFile, path)
	assert.Equal(t, 1, pid)

	_, statErr := os.Stat(staleFile)
	assert.True(t, os.IsNotExist(statErr))
}

func TestFindRunningPidFileInLocations_RemovesInvalidAndReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	invalidFile := filepath.Join(tmpDir, "invalid.pid")

	require.NoError(t, os.WriteFile(invalidFile, []byte("not-a-pid"), 0600))

	_, _, err := findRunningPidFileInLocations([]string{invalidFile})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale gordon PID file")

	_, statErr := os.Stat(invalidFile)
	assert.True(t, os.IsNotExist(statErr))
}

func TestFindRunningPidFileInLocations_NoFiles(t *testing.T) {
	_, _, err := findRunningPidFileInLocations([]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gordon PID file not found")
}
