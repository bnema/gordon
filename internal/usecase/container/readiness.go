package container

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// tcpProbe attempts a TCP connection to addr, retrying every 500ms until
// success or timeout. This is the universal fallback readiness check —
// it verifies the process is at least accepting connections.
func tcpProbe(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dialer := &net.Dialer{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("TCP probe timeout after %s: %s not reachable", timeout, addr)
		}

		remaining := time.Until(deadline)
		attemptTimeout := time.Second
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		conn, err := dialer.DialContext(attemptCtx, "tcp", addr)
		cancel()
		if err == nil {
			conn.Close()
			return nil
		}

		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// httpProbe performs HTTP GET requests to url, retrying every 1s until a
// 2xx/3xx response or timeout. Used when gordon.health label is set.
func httpProbe(ctx context.Context, url string, timeout time.Duration) error {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Don't follow redirects — treat 3xx as a successful response.
			return http.ErrUseLastResponse
		},
	}
	deadline := time.Now().Add(timeout)
	var lastStatus int

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("HTTP probe timeout after %s: last status %d from %s", timeout, lastStatus, url)
		}

		remaining := time.Until(deadline)
		attemptTimeout := 2 * time.Second
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if reqErr != nil {
			return reqErr
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		req = req.WithContext(attemptCtx)
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			lastStatus = resp.StatusCode
			resp.Body.Close()
			if lastStatus >= 200 && lastStatus < 400 {
				return nil
			}
		}

		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
