package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	proxyadapter "github.com/bnema/gordon/internal/adapters/in/http/proxy"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
	"github.com/bnema/gordon/internal/usecase/proxy"
)

func TestDrainIngressReleasesHeldRequest(t *testing.T) {
	cfg := inmocks.NewMockConfigService(t)
	cfg.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	index := apptraffic.NewHostIndex()
	index.Replace([]apptraffic.RouteEntry{{
		Kind: "http", Host: "app.example.com", App: "blog", Service: "web",
		HoldUntil: time.Now().Add(time.Minute),
	}})
	proxySvc := proxy.NewService(cfg, proxy.Config{MaxHold: time.Minute}).WithAppTargets(index)
	frontend := httptest.NewServer(proxyadapter.NewHandler(proxySvc, nil, zerowrap.Default()))
	defer frontend.Close()

	status := make(chan int, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, frontend.URL, nil)
		if err != nil {
			status <- 0
			return
		}
		req.Host = "app.example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		defer resp.Body.Close()
		status <- resp.StatusCode
	}()
	// The request is held: it neither resolves nor fails on its own.
	require.Never(t, func() bool { return len(status) > 0 }, 100*time.Millisecond, 10*time.Millisecond)

	const drainTimeout = 10 * time.Second
	start := time.Now()
	drainIngress(nil, frontend.Config, nil, proxySvc, nil, drainTimeout, zerowrap.Default())
	assert.Less(t, time.Since(start), 2*time.Second, "a held request must not keep the drain waiting until its timeout")
	select {
	case code := <-status:
		assert.Equal(t, http.StatusServiceUnavailable, code)
	case <-time.After(2 * time.Second):
		t.Fatal("held request was not released")
	}
}
