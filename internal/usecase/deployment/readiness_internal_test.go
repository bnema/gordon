package deployment

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestWaitHTTPReady_ProbeURLKeepsLoopbackAuthority proves the probe URL is
// built from an immutable loopback host and an escaped path, so no declared
// path can redirect the request to a different authority.
func TestWaitHTTPReady_ProbeURLKeepsLoopbackAuthority(t *testing.T) {
	var got string
	deps := ProbeDeps{httpGet: func(_ context.Context, url string) (int, error) {
		got = url
		return 200, nil
	}}
	spec := domain.AppService{
		Name:      "web",
		HTTP:      []domain.AppHTTPInterface{{Host: "web.example.com", Port: 8080}},
		Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz?probe=1", Timeout: time.Second},
	}
	require.NoError(t, waitHTTPReadyWithDeps(context.Background(), deps, spec, map[int]int{8080: 18080}))
	assert.Equal(t, "http://127.0.0.1:18080/healthz?probe=1", got)
}

// TestWaitHTTPReady_HangingProbeRespectsDeadline proves a workload that
// never answers cannot hold the readiness operation open past its bound.
func TestWaitHTTPReady_HangingProbeRespectsDeadline(t *testing.T) {
	deps := ProbeDeps{httpGet: func(ctx context.Context, _ string) (int, error) {
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
