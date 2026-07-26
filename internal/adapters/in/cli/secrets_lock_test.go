package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSecretsLockPrintsFixedReadinessAndWaitsForCancellation(t *testing.T) {
	original := holdManagedPassBackend
	t.Cleanup(func() { holdManagedPassBackend = original })

	entered := make(chan struct{})
	holdManagedPassBackend = func(ctx context.Context, ready func() error) error {
		require.NoError(t, ready())
		close(entered)
		<-ctx.Done()
		return nil
	}

	configFile := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configFile, []byte("[auth]\nsecrets_backend = \"pass\"\n"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	var output bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	var runErr error
	go func() {
		defer wg.Done()
		runErr = runSecretsLock(ctx, configFile, &output)
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("lock command did not become ready")
	}
	assert.Equal(t, "Managed pass backend lock acquired\n", output.String())

	cancel()
	wg.Wait()
	require.NoError(t, runErr)
	assert.Equal(t, "Managed pass backend lock acquired\n", output.String())
}
