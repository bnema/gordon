package releasesmoke

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWaitManagedPassReadinessFromStdoutPipe(t *testing.T) {
	ctx := t.Context()
	cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$1"; sleep 30`, "sh", ManagedPassLockMessage)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
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
		_ = cmd.Wait()
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

	cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$1"; sleep 30`, "sh", ManagedPassLockMessage)
	readiness, terminate, err := startManagedPassOwner(ctx, cmd)
	require.NoError(t, err)
	defer terminate()
	require.Equal(t, ManagedPassLockMessage, readiness)
	terminate()
}

func TestAssertExclusiveGenerationVolumeU(t *testing.T) {
	t.Parallel()

	const (
		vol          = "gordon-vol"
		mountOK      = "/var/lib/gordon U\n/private \n"
		projectionOK = "gordon-vol:U"
		bindOK       = `["gordon-vol:/var/lib/gordon:U,rprivate"]`
	)

	t.Run("green", func(t *testing.T) {
		t.Parallel()
		for _, bind := range []string{bindOK, `["gordon-vol:/var/lib/gordon:U"]`, `["gordon-vol:/var/lib/gordon:rprivate,U"]`} {
			require.NoError(t, assertExclusiveGenerationVolumeU(
				"control", vol, mountOK, projectionOK, bind,
			))
		}
		require.NoError(t, assertExclusiveGenerationVolumeU(
			"edge", "", "/private \n", "", `["/tmp/roles/edge:/private"]`,
		))
	})

	invalid := []struct {
		name          string
		role          string
		stateVolume   string
		mountModes    string
		gordonProject string
		bindsJSON     string
	}{
		{
			name:          "projection wrong volume",
			role:          "control",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: "other-vol:U",
			bindsJSON:     bindOK,
		},
		{
			name:          "bind U,z",
			role:          "runtime",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: projectionOK,
			bindsJSON:     `["gordon-vol:/var/lib/gordon:U,z"]`,
		},
		{
			name:          "bind rw,U",
			role:          "control",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: projectionOK,
			bindsJSON:     `["gordon-vol:/var/lib/gordon:rw,U"]`,
		},
		{
			name:          "bind duplicate U token",
			role:          "control",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: projectionOK,
			bindsJSON:     `["gordon-vol:/var/lib/gordon:U,rprivate,U"]`,
		},
		{
			name:          "bind missing mode",
			role:          "runtime",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: projectionOK,
			bindsJSON:     `["gordon-vol:/var/lib/gordon"]`,
		},
		{
			name:          "duplicate gordon bind",
			role:          "control",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: projectionOK,
			bindsJSON:     `["gordon-vol:/var/lib/gordon:U,rprivate","gordon-vol:/var/lib/gordon:U,rprivate"]`,
		},
		{
			name:          "wrong volume bind",
			role:          "registry",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: projectionOK,
			bindsJSON:     `["other-vol:/var/lib/gordon:U,rprivate"]`,
		},
		{
			name:          "unrelated bind U",
			role:          "control",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: projectionOK,
			bindsJSON:     `["/tmp/private:/private:U","gordon-vol:/var/lib/gordon:U,rprivate"]`,
		},
		{
			name:          "malformed binds JSON",
			role:          "edge",
			stateVolume:   "",
			mountModes:    "/private \n",
			gordonProject: "",
			bindsJSON:     `{`,
		},
		{
			name:          "missing gordon bind",
			role:          "runtime",
			stateVolume:   vol,
			mountModes:    mountOK,
			gordonProject: projectionOK,
			bindsJSON:     `["/tmp/private:/private"]`,
		},
		{
			name:          "edge gordon mount projection",
			role:          "edge",
			stateVolume:   "",
			mountModes:    "/var/lib/gordon U\n",
			gordonProject: "edge-vol:U",
			bindsJSON:     `[]`,
		},
		{
			name:          "edge gordon bind",
			role:          "edge",
			stateVolume:   "",
			mountModes:    "/private \n",
			gordonProject: "",
			bindsJSON:     `["edge-vol:/var/lib/gordon:U,rprivate"]`,
		},
		{
			name:          "edge unrelated U bind",
			role:          "edge",
			stateVolume:   "",
			mountModes:    "/private \n",
			gordonProject: "",
			bindsJSON:     `["/tmp/roles/edge:/private:U"]`,
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, assertExclusiveGenerationVolumeU(
				tc.role, tc.stateVolume, tc.mountModes, tc.gordonProject, tc.bindsJSON,
			))
		})
	}
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

func (f fakeInspectRunner) Command(ctx context.Context, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
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

func (f *fakeOwnerRMRunner) Command(ctx context.Context, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
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

func (f *fakeOwnerRMRunner) rmCallAt(index int) rmCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rmCalls[index]
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
			first := runner.rmCallAt(0)
			require.Equal(t, engine, first.engine)
			require.Equal(t, owner, first.owner)
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
	var terminateCalls atomic.Int64
	terminate := func() { terminateCalls.Add(1) }

	cleanup := managedPassOwnerCleanup(runner, owner, terminate)
	cleanup()
	cleanup()

	require.Equal(t, 1, runner.rmCount())
	require.Equal(t, int64(1), terminateCalls.Load())
}
