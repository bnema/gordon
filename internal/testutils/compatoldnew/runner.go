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

// SensitiveEnvironment is a deliberately registered, in-memory fixture value.
// It is never retained in captured artifacts.
type SensitiveEnvironment struct {
	// Side binds the secret to one old/new subprocess fixture.
	Side  string
	Key   string
	Value string
}

type GordonInstance struct {
	BinaryPath string
	ConfigPath string
	DataDir    string
	WorkingDir string
	Env        []string
	// SensitiveEnv is limited to explicit side fixture secrets, such as a
	// component token signing key. Do not put secrets in Env.
	SensitiveEnv []SensitiveEnvironment
	// RuntimeRequired permits an explicitly requested Docker transport only
	// when the existing real-runtime gate is enabled.
	RuntimeRequired bool
	// ExcludeEnv removes explicitly supplied variables for split roles.
	ExcludeEnv     []string
	HealthProbeURL string
	ReadinessProbe ReadinessProbe

	cmd           *exec.Cmd
	waitDone      chan struct{}
	waitErr       error
	processExited bool
	stdout        syncBuffer
	stderr        syncBuffer
	redactions    []string
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

var scenarioEnvironmentKeys = map[string]bool{
	"HOME": true, "TMPDIR": true, "PATH": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_RUNTIME_DIR": true,
	"CGO_ENABLED":   true,
	"GORDON_CONFIG": true, "GORDON_ROLE": true, "GORDON_REMOTE": true,
	"GORDON_TOKEN": true, "GORDON_INSECURE": true, "GORDON_AUTH_TOKEN_SECRET": true,
}

func commandEnvironmentForScenario(overrides []string, sensitive []SensitiveEnvironment, runtimeRequired bool) ([]string, error) {
	values := inheritedSafeEnvironment()
	if err := applyScenarioEnvironment(values, overrides, runtimeRequired); err != nil {
		return nil, err
	}
	if err := applySensitiveEnvironment(values, sensitive); err != nil {
		return nil, err
	}
	addRuntimeDockerHost(values, runtimeRequired)
	if err := addIsolatedEnvironmentPaths(values); err != nil {
		return nil, err
	}
	return orderedScenarioEnvironment(values), nil
}

func inheritedSafeEnvironment() map[string]string {
	values := make(map[string]string, 5)
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ"} {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			values[key] = value
		}
	}
	return values
}

func applyScenarioEnvironment(values map[string]string, overrides []string, runtimeRequired bool) error {
	for _, entry := range overrides {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid subprocess environment entry %q", entry)
		}
		if err := validateScenarioEnvironment(key, value, runtimeRequired); err != nil {
			return err
		}
		values[key] = value
	}
	return nil
}

func validateScenarioEnvironment(key, value string, runtimeRequired bool) error {
	if strings.HasPrefix(key, "GO_WANT_") {
		if secretLikeEnvironmentKey(key) {
			return fmt.Errorf("subprocess secret %q must be registered as SensitiveEnv", key)
		}
		return nil
	}
	if handled, err := validateRuntimeScenarioEnvironment(key, value, runtimeRequired); handled {
		return err
	}
	if !scenarioEnvironmentKeys[key] {
		return fmt.Errorf("subprocess environment key %q is not allowed by the scenario contract", key)
	}
	if value != "" && secretLikeEnvironmentKey(key) {
		return fmt.Errorf("subprocess secret %q must be registered as SensitiveEnv", key)
	}
	if value != "" && isIsolatedPathKey(key) && !isolatedScenarioPath(value) {
		return fmt.Errorf("subprocess %s must use an isolated path", key)
	}
	return nil
}

func validateRuntimeScenarioEnvironment(key, value string, runtimeRequired bool) (bool, error) {
	if !runtimeRequired {
		return false, nil
	}
	switch key {
	case "DOCKER_CONFIG":
		if !isolatedScenarioPath(value) {
			return true, fmt.Errorf("subprocess Docker config must use an isolated path")
		}
		return true, nil
	case "DOCKER_HOST":
		if !strings.HasPrefix(value, "unix://") || !isolatedScenarioPath(strings.TrimPrefix(value, "unix://")) {
			return true, fmt.Errorf("subprocess Docker host must use an isolated unix socket")
		}
		return true, nil
	default:
		return false, nil
	}
}

func isIsolatedPathKey(key string) bool {
	return key == "HOME" || key == "TMPDIR" || strings.HasPrefix(key, "XDG_")
}

func applySensitiveEnvironment(values map[string]string, sensitive []SensitiveEnvironment) error {
	for _, entry := range sensitive {
		if (entry.Side != SideOld && entry.Side != SideNew) || entry.Key == "" || entry.Value == "" || !secretLikeEnvironmentKey(entry.Key) || entry.Key != "GORDON_AUTH_TOKEN_SECRET" {
			return fmt.Errorf("invalid registered subprocess secret %q", entry.Key)
		}
		values[entry.Key] = entry.Value
	}
	return nil
}

func addRuntimeDockerHost(values map[string]string, runtimeRequired bool) {
	if runtimeRequired && values["DOCKER_HOST"] == "" && os.Getenv("GORDON_COMPAT_REQUIRE_RUNTIME") == "1" {
		if host, ok := os.LookupEnv("DOCKER_HOST"); ok && host != "" {
			values["DOCKER_HOST"] = host
		}
	}
}

func addIsolatedEnvironmentPaths(values map[string]string) error {
	if values["HOME"] == "" {
		values["HOME"] = isolatedSubprocessPath("home")
	}
	for key, suffix := range map[string]string{"TMPDIR": "tmp", "XDG_CONFIG_HOME": ".config", "XDG_CACHE_HOME": ".cache", "XDG_DATA_HOME": ".local/share"} {
		if values[key] == "" {
			values[key] = filepath.Join(values["HOME"], suffix)
		}
	}
	for _, path := range []string{values["HOME"], values["TMPDIR"], values["XDG_CONFIG_HOME"], values["XDG_CACHE_HOME"], values["XDG_DATA_HOME"], values["XDG_RUNTIME_DIR"]} {
		if path != "" {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return fmt.Errorf("create isolated subprocess path: %w", err)
			}
		}
	}
	return nil
}

func orderedScenarioEnvironment(values map[string]string) []string {
	keys := []string{"PATH", "HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "CGO_ENABLED", "GORDON_CONFIG", "GORDON_ROLE", "GORDON_REMOTE", "GORDON_TOKEN", "GORDON_INSECURE", "GORDON_AUTH_TOKEN_SECRET", "DOCKER_HOST", "DOCKER_CONFIG"}
	for key := range values {
		if strings.HasPrefix(key, "GO_WANT_") {
			keys = append(keys, key)
		}
	}
	env := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] || values[key] == "" && key != "GORDON_REMOTE" && key != "GORDON_TOKEN" && key != "GORDON_INSECURE" {
			continue
		}
		if value, ok := values[key]; ok {
			env = append(env, key+"="+value)
			seen[key] = true
		}
	}
	return env
}

func isolatedSubprocessPath(name string) string {
	return filepath.Join(os.TempDir(), "gordon-compat-subprocess", name)
}

func isolatedScenarioPath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(os.TempDir(), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func secretLikeEnvironmentKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "KEY") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL")
}

func newIsolatedCommand(ctx context.Context, name string, args []string, env []string, sensitive []SensitiveEnvironment, runtimeRequired bool) (*exec.Cmd, error) {
	commandEnv, err := commandEnvironmentForScenario(env, sensitive, runtimeRequired)
	if err != nil {
		return nil, err
	}
	// #nosec G204 -- compatibility launch sites execute selected binaries or harness-generated commands.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = commandEnv
	return cmd, nil
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
	env := append([]string(nil), g.Env...)
	if g.ConfigPath != "" {
		env = append(env, "GORDON_CONFIG="+g.ConfigPath)
	}
	cmd, err := newIsolatedCommand(ctx, g.BinaryPath, args, env, g.SensitiveEnv, g.RuntimeRequired)
	if err != nil {
		return fmt.Errorf("start %s: %w", filepath.Base(g.BinaryPath), err)
	}
	if g.WorkingDir != "" {
		cmd.Dir = g.WorkingDir
	} else if g.DataDir != "" {
		cmd.Dir = g.DataDir
	}
	cmd.Env = excludeCommandEnvironment(cmd.Env, g.ExcludeEnv)
	g.redactions = sensitiveValues(g.SensitiveEnv)
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

func sensitiveValues(entries []SensitiveEnvironment) []string {
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Value != "" {
			values = append(values, entry.Value)
		}
	}
	return values
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
	return "stdout:\n" + redactCapturedOutput(g.stdout.String(), g.redactions...) + "\nstderr:\n" + redactCapturedOutput(g.stderr.String(), g.redactions...)
}
