package releasesmoke

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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

type fakeOwnerRMRunner struct {
	engine  string
	mu      sync.Mutex
	rmCalls []rmCall
}

type rmCall struct {
	ctx    context.Context
	ctxErr error
	engine string
	owner  string
}

func (f *fakeOwnerRMRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "rm" && args[1] == "-f" {
		f.mu.Lock()
		f.rmCalls = append(f.rmCalls, rmCall{
			ctx:    ctx,
			ctxErr: ctx.Err(),
			engine: f.engine,
			owner:  args[2],
		})
		f.mu.Unlock()
		return "", nil
	}
	return "", nil
}

func (f *fakeOwnerRMRunner) rmCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rmCalls)
}

func (f *fakeOwnerRMRunner) lastRMContext() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rmCalls) == 0 {
		return nil
	}
	return f.rmCalls[len(f.rmCalls)-1].ctx
}

func (f *fakeOwnerRMRunner) lastRMContextErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rmCalls) == 0 {
		return nil
	}
	return f.rmCalls[len(f.rmCalls)-1].ctxErr
}

func TestManagedPassOwnerCleanupOnReadinessFailure(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		t.Run(engine, func(t *testing.T) {
			runner := &fakeOwnerRMRunner{engine: engine}
			owner := "gordon-test-owner-" + engine

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", "printf 'unexpected readiness\\n'; sleep 30")
			readiness, terminate, err := startManagedPassOwner(ctx, cmd)
			require.NoError(t, err)
			require.NotEqual(t, ManagedPassLockMessage, readiness)

			cleanup := managedPassOwnerCleanup(runner, owner, terminate)
			cleanup()

			require.Equal(t, 1, runner.rmCount(), "rm -f must run exactly once on unexpected readiness")
			require.Equal(t, engine, runner.rmCalls[0].engine)
			require.Equal(t, owner, runner.rmCalls[0].owner)
			require.NoError(t, runner.lastRMContextErr(), "cleanup must not use canceled request context")
			_, hasDeadline := runner.lastRMContext().Deadline()
			require.True(t, hasDeadline, "cleanup must use bounded background context")
		})
	}
}

func TestManagedPassOwnerCleanupOnCancellation(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		t.Run(engine, func(t *testing.T) {
			runner := &fakeOwnerRMRunner{engine: engine}
			owner := "gordon-test-owner-cancel-" + engine

			ctx, cancel := context.WithCancel(t.Context())
			cmd := exec.CommandContext(context.Background(), "sh", "-c", "sleep 60")
			go func() {
				time.Sleep(50 * time.Millisecond)
				cancel()
			}()

			_, terminate, err := startManagedPassOwner(ctx, cmd)
			require.Error(t, err)
			require.ErrorIs(t, err, context.Canceled)

			cleanup := managedPassOwnerCleanup(runner, owner, terminate)
			cleanup()

			require.Equal(t, 1, runner.rmCount(), "rm -f must run exactly once on cancellation")
			require.NoError(t, runner.lastRMContextErr(), "cleanup must use independent bounded context")
			_, hasDeadline := runner.lastRMContext().Deadline()
			require.True(t, hasDeadline, "cleanup must use bounded background context")
		})
	}
}

func TestManagedPassOwnerCleanupIsIdempotent(t *testing.T) {
	runner := &fakeOwnerRMRunner{engine: "docker"}
	owner := "gordon-test-owner-idempotent"
	var terminateCalls int
	terminate := func() { terminateCalls++ }

	cleanup := managedPassOwnerCleanup(runner, owner, terminate)
	cleanup()
	cleanup()

	require.Equal(t, 1, runner.rmCount())
	require.Equal(t, 1, terminateCalls)
}
