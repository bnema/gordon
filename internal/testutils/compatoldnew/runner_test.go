package compatoldnew

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunnerStartWaitReadyStopAndLogs(t *testing.T) {
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
