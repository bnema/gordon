package releasesmoke

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// waitManagedPassReadiness reads the first readiness line from r, honoring ctx.
// Prefer StdoutPipe over a FIFO: open the reader before Start so the owner cannot
// block forever on an unopened writer end.
func waitManagedPassReadiness(ctx context.Context, r io.Reader) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				done <- result{err: err}
				return
			}
			done <- result{err: io.EOF}
			return
		}
		done <- result{line: strings.TrimSpace(scanner.Text())}
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("timed out waiting for managed pass backend lock: %w", ctx.Err())
	case res := <-done:
		if res.err != nil {
			return "", res.err
		}
		return res.line, nil
	}
}

// startManagedPassOwner starts cmd with StdoutPipe, waits for the readiness line,
// and returns a terminate function that must run on every return path.
func startManagedPassOwner(ctx context.Context, cmd *exec.Cmd) (readiness string, terminate func(), err error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", func() {}, fmt.Errorf("owner stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", func() {}, fmt.Errorf("start lease owner: %w", err)
	}
	var once sync.Once
	terminate = func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}
		})
	}

	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(ReadinessPollAttempts)*time.Second)
	defer cancel()
	readiness, err = waitManagedPassReadiness(waitCtx, stdout)
	if err != nil {
		return "", terminate, err
	}
	return readiness, terminate, nil
}

// managedPassOwnerCleanup returns an idempotent cleanup that removes the named owner
// container via the engine CLI using a bounded background context, then terminates the
// attached owner process. Safe when the owner never existed or was already removed.
func managedPassOwnerCleanup(runner CommandRunner, owner string, terminate func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), time.Duration(ReadinessPollAttempts)*time.Second)
			defer cancel()
			_ = runQuiet(bgCtx, runner, "rm", "-f", owner)
			if terminate != nil {
				terminate()
			}
		})
	}
}
