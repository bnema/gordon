package deployment

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// probeHTTPClient is the dedicated readiness client. It never consults
// environment proxy settings, never follows redirects, and bounds every
// request so a nonresponding workload cannot hold the probe open.
var probeHTTPClient = &http.Client{
	Transport: &http.Transport{Proxy: nil},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Timeout: 5 * time.Second,
}

// httpGetOnce performs one HTTP GET without following redirects.
func httpGetOnce(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := probeHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// tcpDialOnce attempts one TCP connection.
func tcpDialOnce(ctx context.Context, addr string) error {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// scanForMarker reports whether the stream contains the marker.
func scanForMarker(stream io.ReadCloser, marker string) (bool, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), marker) {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("deployment: read logs: %w", err)
	}
	return false, nil
}
