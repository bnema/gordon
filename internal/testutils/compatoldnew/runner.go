package compatoldnew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ReadinessProbe supports services whose readiness is not a successful HTTP
// response, including raw TCP listeners and authenticated endpoints.
type ReadinessProbe struct {
	TCPAddress string
	Check      func(context.Context) error
}

type GordonInstance struct {
	BinaryPath string
	ConfigPath string
	DataDir    string
	WorkingDir string
	Env        []string
	// ExcludeEnv removes ambient variables for roles that must prove they do
	// not inherit runtime transport configuration (for example, split control).
	ExcludeEnv     []string
	HealthProbeURL string
	ReadinessProbe ReadinessProbe

	cmd           *exec.Cmd
	waitDone      chan struct{}
	waitErr       error
	processExited bool
	stdout        syncBuffer
	stderr        syncBuffer
	mu            sync.Mutex
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// commandEnvironment retains essential inherited settings but isolates child
// processes from every ambient GORDON_* setting. Explicit overrides are
// de-duplicated (last value wins), since duplicate environment entries have
// platform-dependent lookup behavior.
func commandEnvironment(overrides []string) []string {
	overrideValues := make(map[string]string, len(overrides))
	overrideOrder := make([]string, 0, len(overrides))
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if _, seen := overrideValues[key]; !seen {
			overrideOrder = append(overrideOrder, key)
		}
		overrideValues[key] = entry
	}
	env := make([]string, 0, len(os.Environ())+len(overrideOrder))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GORDON_") {
			continue
		}
		if _, overridden := overrideValues[key]; !overridden {
			env = append(env, entry)
		}
	}
	for _, key := range overrideOrder {
		env = append(env, overrideValues[key])
	}
	return env
}

func (g *GordonInstance) Start(ctx context.Context, args ...string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cmd != nil && g.cmd.Process != nil {
		return fmt.Errorf("gordon instance already started")
	}
	if g.BinaryPath == "" {
		return fmt.Errorf("gordon binary path is required")
	}
	if g.DataDir != "" {
		if err := os.MkdirAll(g.DataDir, 0o750); err != nil {
			return fmt.Errorf("create data dir: %w", err)
		}
	}
	// #nosec G204 -- the harness must execute the Gordon binary under comparison.
	cmd := exec.CommandContext(ctx, g.BinaryPath, args...)
	if g.WorkingDir != "" {
		cmd.Dir = g.WorkingDir
	} else if g.DataDir != "" {
		cmd.Dir = g.DataDir
	}
	env := append([]string(nil), g.Env...)
	if g.ConfigPath != "" {
		env = append(env, "GORDON_CONFIG="+g.ConfigPath)
	}
	cmd.Env = excludeCommandEnvironment(commandEnvironment(env), g.ExcludeEnv)
	cmd.Stdout = &g.stdout
	cmd.Stderr = &g.stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", filepath.Base(g.BinaryPath), err)
	}
	g.cmd = cmd
	g.waitDone = make(chan struct{})
	g.waitErr = nil
	g.processExited = false
	go g.watchProcess(cmd, g.waitDone)
	return nil
}

func excludeCommandEnvironment(env, excluded []string) []string {
	if len(excluded) == 0 {
		return env
	}
	blocked := make(map[string]struct{}, len(excluded))
	for _, key := range excluded {
		blocked[key] = struct{}{}
	}
	filtered := env[:0]
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, found := blocked[key]; !found {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (g *GordonInstance) watchProcess(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	g.mu.Lock()
	if g.cmd == cmd {
		g.waitErr = err
		g.processExited = true
	}
	close(done)
	g.mu.Unlock()
}

func (g *GordonInstance) Stop(ctx context.Context) error {
	g.mu.Lock()
	cmd, done, exited := g.cmd, g.waitDone, g.processExited
	g.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if !exited {
		select {
		case <-done:
			exited = true
		default:
		}
	}
	if !exited {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			select {
			case <-done:
			default:
				return fmt.Errorf("stop instance: %w", err)
			}
		}
	}
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		g.clearCmd(cmd)
		return ctx.Err()
	case <-done:
		g.clearCmd(cmd)
		return nil
	}
}

func (g *GordonInstance) clearCmd(cmd *exec.Cmd) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cmd == cmd {
		g.cmd = nil
	}
}

func (g *GordonInstance) WaitReady(ctx context.Context) error {
	g.mu.Lock()
	probe, healthURL, done := g.ReadinessProbe, g.HealthProbeURL, g.waitDone
	exited, waitErr := g.processExited, g.waitErr
	g.mu.Unlock()
	if exited {
		return g.earlyExitError(waitErr)
	}
	if probe.Check == nil && probe.TCPAddress == "" && healthURL == "" {
		return nil
	}
	if probe.Check != nil && probe.TCPAddress != "" {
		return fmt.Errorf("wait ready: configure either readiness callback or TCP address")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if done != nil {
			select {
			case <-done:
				g.mu.Lock()
				err := g.waitErr
				g.mu.Unlock()
				return g.earlyExitError(err)
			default:
			}
		}
		if err := g.checkReadiness(ctx, probe, healthURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait ready: %w (last probe error: %v); logs: %s", ctx.Err(), lastErr, g.Logs())
		case <-ticker.C:
		}
	}
}

func (g *GordonInstance) checkReadiness(ctx context.Context, probe ReadinessProbe, healthURL string) error {
	if probe.Check != nil {
		return probe.Check(ctx)
	}
	if probe.TCPAddress != "" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", probe.TCPAddress)
		if err != nil {
			return err
		}
		return conn.Close()
	}
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("health probe returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (g *GordonInstance) earlyExitError(waitErr error) error {
	if waitErr == nil {
		return fmt.Errorf("gordon exited before ready; logs: %s", g.Logs())
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok {
		return fmt.Errorf("gordon exited before ready with status %d; logs: %s", exitErr.ExitCode(), g.Logs())
	}
	return fmt.Errorf("gordon exited before ready: %w; logs: %s", waitErr, g.Logs())
}

func (g *GordonInstance) Logs() string {
	return "stdout:\n" + g.stdout.String() + "\nstderr:\n" + g.stderr.String()
}
