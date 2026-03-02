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
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("TCP probe timeout after %s: %s not reachable", timeout, addr)
		}

		conn, err := net.DialTimeout("tcp", addr, time.Second)
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
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastStatus int

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("HTTP probe timeout after %s: last status %d from %s", timeout, lastStatus, url)
		}

		resp, err := client.Get(url)
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
