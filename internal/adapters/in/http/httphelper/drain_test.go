package httphelper

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPDrainLogsForcedConnections(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(exited)
	}))
	drain := TrackHTTPDrain(srv.Config)
	srv.Start()
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: test\r\n\r\n"))
	require.NoError(t, err)
	<-entered
	var mu sync.Mutex
	var warnings int
	log := zerowrap.WithHook(zerowrap.Default(), zerolog.HookFunc(func(_ *zerolog.Event, level zerolog.Level, msg string) {
		if level == zerolog.WarnLevel && msg == "HTTP drain deadline reached; force-closing connections" {
			mu.Lock()
			warnings++
			mu.Unlock()
		}
	}))
	ctx, cancel := context.WithTimeout(zerowrap.WithCtx(context.Background(), log), 20*time.Millisecond)
	defer cancel()
	drain.Shutdown(ctx, srv.Config, "test HTTP")
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("handler not cancelled")
	}
	mu.Lock()
	assert.Equal(t, 1, warnings)
	mu.Unlock()
}
