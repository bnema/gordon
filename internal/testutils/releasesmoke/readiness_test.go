package releasesmoke

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWaitManagedPassReadinessFromStdoutPipe(t *testing.T) {
	ctx := t.Context()
	cmd := exec.CommandContext(ctx, "sh", "-c", "printf '%s\\n' '"+ManagedPassLockMessage+"'; sleep 30")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	line, err := waitManagedPassReadiness(waitCtx, stdout)
	require.NoError(t, err)
	require.Equal(t, ManagedPassLockMessage, line)
}

func TestWaitManagedPassReadinessCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "sleep 60")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err = waitManagedPassReadiness(ctx, stdout)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestStartManagedPassOwnerAvoidsFIFODeadlock(t *testing.T) {
	// Prior recipe opened a FIFO O_WRONLY before any reader existed and deadlocked.
	// StdoutPipe establishes the reader before Start/write and must complete quickly.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", "printf '%s\\n' '"+ManagedPassLockMessage+"'; sleep 30")
	readiness, terminate, err := startManagedPassOwner(ctx, cmd)
	require.NoError(t, err)
	defer terminate()
	require.Equal(t, ManagedPassLockMessage, readiness)
	terminate()
}

func TestAssertExclusiveGenerationVolumeU(t *testing.T) {
	t.Parallel()
	require.NoError(t, assertExclusiveGenerationVolumeU(
		"control",
		"gordon-vol",
		"/var/lib/gordon U\n/private \n",
		`["gordon-vol:/var/lib/gordon:U,rprivate"]`,
	))
	require.Error(t, assertExclusiveGenerationVolumeU(
		"control",
		"gordon-vol",
		"/private U\n",
		`["gordon-vol:/var/lib/gordon:U"]`,
	))
	require.NoError(t, assertExclusiveGenerationVolumeU(
		"edge",
		"",
		"/private \n",
		`["/tmp/roles/edge:/private"]`,
	))
	require.Error(t, assertExclusiveGenerationVolumeU(
		"edge",
		"",
		"/var/lib/gordon U\n",
		`[]`,
	))
	require.Error(t, assertExclusiveGenerationVolumeU(
		"runtime",
		"gordon-vol",
		"/var/lib/gordon \n",
		`["gordon-vol:/var/lib/gordon:rw"]`,
	))
}

func TestAssertResourcesUninspectable(t *testing.T) {
	runner := fakeInspectRunner{missing: map[string]bool{
		"container missing": true,
		"volume missing":    true,
	}}
	require.NoError(t, assertResourcesUninspectable(t.Context(), runner, []string{"missing"}, []string{"missing"}))
	require.Error(t, assertResourcesUninspectable(t.Context(), fakeInspectRunner{}, []string{"present"}, nil))
}

type fakeInspectRunner struct {
	missing map[string]bool
}

func (f fakeInspectRunner) Run(_ context.Context, args ...string) (string, error) {
	key := ""
	if len(args) >= 2 {
		key = args[0] + " " + args[len(args)-1]
	}
	if f.missing[key] {
		return "", os.ErrNotExist
	}
	return "{}", nil
}

func TestRepositoryRootHelperCompilesViaFixturePath(t *testing.T) {
	// Smoke that readiness helpers and contracts stay co-located with harness sources.
	_, err := os.Stat(filepath.Join(".", "readiness.go"))
	require.NoError(t, err)
}
