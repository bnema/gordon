package compatoldnew

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type GordonInstance struct {
	BinaryPath     string
	ConfigPath     string
	DataDir        string
	Env            []string
	HealthProbeURL string

	cmd    *exec.Cmd
	stdout syncBuffer
	stderr syncBuffer
	mu     sync.Mutex
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
	cmdArgs := append([]string{}, args...)
	// #nosec G204 -- the harness must execute the Gordon binary under comparison.
	cmd := exec.CommandContext(ctx, g.BinaryPath, cmdArgs...)
	if g.DataDir != "" {
		cmd.Dir = g.DataDir
	}
	cmd.Env = append(os.Environ(), g.Env...)
	if g.ConfigPath != "" {
		cmd.Env = append(cmd.Env, "GORDON_CONFIG="+g.ConfigPath)
	}
	cmd.Stdout = &g.stdout
	cmd.Stderr = &g.stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", filepath.Base(g.BinaryPath), err)
	}
	g.cmd = cmd
	return nil
}

func (g *GordonInstance) Stop(ctx context.Context) error {
	g.mu.Lock()
	cmd := g.cmd
	g.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil && cmd.ProcessState == nil {
		g.clearCmd(cmd)
		return fmt.Errorf("stop instance: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		g.clearCmd(cmd)
		return ctx.Err()
	case err := <-done:
		g.clearCmd(cmd)
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				return fmt.Errorf("wait after stop: %w", err)
			}
		}
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
	if g.HealthProbeURL == "" {
		return nil
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	client := http.Client{Timeout: time.Second}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait ready %s: %w; logs: %s", g.HealthProbeURL, ctx.Err(), g.Logs())
		case <-ticker.C:
			resp, err := client.Get(g.HealthProbeURL)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}
	}
}

func (g *GordonInstance) Logs() string {
	return "stdout:\n" + g.stdout.String() + "\nstderr:\n" + g.stderr.String()
}
