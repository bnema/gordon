package deployment

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestWaitHTTPReady_ProbeURLKeepsLoopbackAuthority proves the probe URL is
// built from an immutable loopback host and an escaped path, so no declared
// path can redirect the request to a different authority. The Host header
// must name the declared container port, not the random published port.
func TestWaitHTTPReady_ProbeURLKeepsLoopbackAuthority(t *testing.T) {
	var gotURL, gotHost string
	deps := ProbeDeps{httpGet: func(_ context.Context, url, hostAuthority string) (int, error) {
		gotURL, gotHost = url, hostAuthority
		return 200, nil
	}}
	spec := domain.AppService{
		Name:      "web",
		HTTP:      []domain.AppHTTPInterface{{Host: "web.example.com", Port: 8080}},
		Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz?probe=1", Timeout: time.Second},
	}
	require.NoError(t, waitHTTPReadyWithDeps(context.Background(), deps, spec, map[int]int{8080: 18080}))
	assert.Equal(t, "http://127.0.0.1:18080/healthz?probe=1", gotURL)
	assert.Equal(t, "127.0.0.1:8080", gotHost, "the Host header names the declared container port, not the published port")
}

// TestWaitHTTPReady_SendsContainerPortAsHostAuthority is the qBittorrent
// regression: readiness dials the random loopback publication, but the
// workload rejects a Host whose port is not its declared container port
// with 401 "Invalid Host header, port mismatch". The probe must dial the
// published port while sending the container port as the Host authority.
func TestWaitHTTPReady_SendsContainerPortAsHostAuthority(t *testing.T) {
	const containerPort = 8080
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:8080" {
			http.Error(w, "Invalid Host header, port mismatch", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	publishedPort := server.Listener.Addr().(*net.TCPAddr).Port

	spec := domain.AppService{
		Name:      "qbittorrent",
		HTTP:      []domain.AppHTTPInterface{{Host: "qbit.example.com", Port: containerPort}},
		Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/api/v2/app/version", Timeout: 2 * time.Second},
	}
	require.NoError(t, waitHTTPReadyWithDeps(context.Background(), ProbeDeps{httpGet: httpGetOnce}, spec, map[int]int{containerPort: publishedPort}))
}

// TestWaitHTTPReady_HangingProbeRespectsDeadline proves a workload that
// never answers cannot hold the readiness operation open past its bound.
func TestWaitHTTPReady_HangingProbeRespectsDeadline(t *testing.T) {
	deps := ProbeDeps{httpGet: func(ctx context.Context, _, _ string) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}}
	spec := domain.AppService{
		Name:      "web",
		HTTP:      []domain.AppHTTPInterface{{Host: "web.example.com", Port: 8080}},
		Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: time.Second},
	}
	start := time.Now()
	err := waitHTTPReadyWithDeps(context.Background(), deps, spec, map[int]int{8080: 18080})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 6*time.Second)
}
