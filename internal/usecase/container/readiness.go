package container

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/bnema/zerowrap"
)

var errProbeTimeout = errors.New("probe timeout")

func probeLoop(
	ctx context.Context,
	deadline time.Time,
	baseSleep time.Duration,
	defaultAttemptTimeout time.Duration,
	work func(attemptCtx context.Context) (success bool, err error),
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errProbeTimeout
		}

		remaining := time.Until(deadline)
		attemptTimeout := defaultAttemptTimeout
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}

		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		success, err := work(attemptCtx)
		cancel()
		if err != nil {
			return err
		}
		if success {
			return nil
		}

		sleepInterval := baseSleep
		if remProbe := time.Until(deadline); remProbe < sleepInterval {
			sleepInterval = remProbe
		}
		if ctxDeadline, ok := ctx.Deadline(); ok {
			if remCtx := time.Until(ctxDeadline); remCtx < sleepInterval {
				sleepInterval = remCtx
			}
		}
		if sleepInterval <= 0 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errProbeTimeout
		}

		t := time.NewTimer(sleepInterval)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// tcpProbe attempts a TCP connection to addr, retrying every 500ms until
// success or timeout. This is the universal fallback readiness check —
// it verifies the process is at least accepting connections.
func tcpProbe(ctx context.Context, addr string, timeout time.Duration) error {
	log := zerowrap.FromCtx(ctx)
	deadline := time.Now().Add(timeout)
	dialer := &net.Dialer{}
	attempts := 0
	var lastErr error
	err := probeLoop(ctx, deadline, 500*time.Millisecond, time.Second, func(attemptCtx context.Context) (bool, error) {
		conn, err := dialer.DialContext(attemptCtx, "tcp", addr)
		attempts++
		if err == nil {
			conn.Close()
			log.Debug().Str("addr", addr).Int("attempts", attempts).Msg("TCP probe connected")
			return true, nil
		}

		lastErr = err
		if attempts <= 3 || attempts%10 == 0 {
			log.Debug().Err(err).Str("addr", addr).Int("attempt", attempts).Msg("TCP probe attempt failed")
		}
		return false, nil
	})
	if errors.Is(err, errProbeTimeout) {
		return fmt.Errorf("TCP probe timeout after %s: %s not reachable (attempts=%d, last_error=%v)", timeout, addr, attempts, lastErr)
	}
	return err
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
	err := probeLoop(ctx, deadline, time.Second, 2*time.Second, func(attemptCtx context.Context) (bool, error) {
		req, reqErr := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
		if reqErr != nil {
			return false, reqErr
		}

		resp, err := client.Do(req)
		if err != nil {
			return false, nil
		}

		lastStatus = resp.StatusCode
		resp.Body.Close()
		if lastStatus >= 200 && lastStatus < 400 {
			return true, nil
		}

		return false, nil
	})
	if errors.Is(err, errProbeTimeout) {
		return fmt.Errorf("HTTP probe timeout after %s: last status %d from %s", timeout, lastStatus, url)
	}
	return err
}
