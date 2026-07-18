package compatoldnew

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCommandEnvironmentUsesOnlySafeScenarioContract(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("DOCKER_CONFIG", "/home/user/.docker")
	t.Setenv("PATH", "/essential/path")

	env, err := commandEnvironmentForScenario([]string{"GORDON_CONFIG=/explicit/config", "GORDON_ROLE=monolith"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentValues(t, env)
	if values["PATH"] != "/essential/path" || values["GORDON_CONFIG"] != "/explicit/config" {
		t.Fatalf("safe scenario values missing: %#v", values)
	}
	for _, key := range []string{"AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK", "DOCKER_CONFIG", "DOCKER_HOST"} {
		if _, found := values[key]; found {
			t.Fatalf("ambient %s was passed to child: %#v", key, values)
		}
	}
	if _, err := commandEnvironmentForScenario([]string{"ADMIN_TOKEN=leak"}, nil, false); err == nil {
		t.Fatal("expected unregistered secret to be rejected")
	}
	if _, err := commandEnvironmentForScenario([]string{"UNRELATED=leak"}, nil, false); err == nil {
		t.Fatal("expected non-contract environment key to be rejected")
	}
}

func environmentValues(t *testing.T, env []string) map[string]string {
	t.Helper()
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("invalid environment entry %q", entry)
		}
		if _, duplicate := values[key]; duplicate {
			t.Fatalf("duplicate environment key %q in %#v", key, env)
		}
		values[key] = value
	}
	return values
}

func TestRunnerReadinessSupportsCallbackTCPExitAndTimeout(t *testing.T) {
	t.Run("callback can define readiness beyond HTTP 2xx", func(t *testing.T) {
		inst := startTestInstance(t, "1")
		attempts := 0
		inst.ReadinessProbe = ReadinessProbe{Check: func(context.Context) error {
			attempts++
			if attempts < 2 {
				return fmt.Errorf("HTTP 401 is expected before credentials are loaded")
			}
			return nil
		}}
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := inst.WaitReady(waitCtx); err != nil {
			t.Fatal(err)
		}
		stopTestInstance(t, inst)
	})
	t.Run("tcp", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		inst := startTestInstance(t, "1")
		inst.ReadinessProbe = ReadinessProbe{TCPAddress: listener.Addr().String()}
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := inst.WaitReady(waitCtx); err != nil {
			t.Fatal(err)
		}
		stopTestInstance(t, inst)
	})
	t.Run("early exit includes logs", func(t *testing.T) {
		inst := startTestInstance(t, "exit")
		inst.ReadinessProbe = ReadinessProbe{Check: func(context.Context) error { return fmt.Errorf("not ready") }}
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := inst.WaitReady(waitCtx)
		if err == nil || !strings.Contains(err.Error(), "exited before ready") || !strings.Contains(err.Error(), "helper exited early") {
			t.Fatalf("unexpected readiness error: %v", err)
		}
	})
	t.Run("timeout includes actionable logs", func(t *testing.T) {
		inst := startTestInstance(t, "1")
		inst.ReadinessProbe = ReadinessProbe{Check: func(context.Context) error { return fmt.Errorf("not ready") }}
		waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := inst.WaitReady(waitCtx)
		if err == nil || !strings.Contains(err.Error(), "logs:") {
			t.Fatalf("unexpected readiness error: %v", err)
		}
		stopTestInstance(t, inst)
	})
}

func startTestInstance(t *testing.T, mode string) *GordonInstance {
	t.Helper()
	inst := &GordonInstance{BinaryPath: os.Args[0], DataDir: filepath.Join(t.TempDir(), "data"), Env: []string{"GO_WANT_HELPER_PROCESS=" + mode}}
	if err := inst.Start(context.Background(), "-test.run=TestRunnerStartWaitReadyStopAndLogs"); err != nil {
		t.Fatal(err)
	}
	return inst
}

func stopTestInstance(t *testing.T, inst *GordonInstance) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := inst.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStartWaitReadyStopAndLogs(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "exit" {
		fmt.Fprintln(os.Stderr, "helper exited early")
		os.Exit(17)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		fmt.Println("helper stdout")
		fmt.Fprintln(os.Stderr, "helper stderr")
		select {}
	}
	ready := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ready {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	inst := &GordonInstance{BinaryPath: os.Args[0], DataDir: filepath.Join(t.TempDir(), "data"), Env: []string{"GO_WANT_HELPER_PROCESS=1"}, HealthProbeURL: server.URL}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := inst.Start(ctx, "-test.run=TestRunnerStartWaitReadyStopAndLogs"); err != nil {
		t.Fatal(err)
	}
	ready = true
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := inst.WaitReady(waitCtx); err != nil {
		t.Fatal(err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if runtime.GOOS == "windows" {
		cancel()
	}
	if err := inst.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if inst.cmd != nil {
		t.Fatalf("expected stopped instance to clear cmd state")
	}
	logs := inst.Logs()
	if logs == "" || filepath.Base(inst.BinaryPath) == "" {
		t.Fatalf("expected logs, got %q", logs)
	}
}
