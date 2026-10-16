package deployment

import (
	"bufio"
	"context"
	"errors"
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

// httpGetOnce performs one HTTP GET without following redirects. It dials
// probeURL's authority (the loopback published bind) while sending
// hostAuthority as the Host header, so a workload that validates the Host
// port of its declared container port accepts the probe.
func httpGetOnce(ctx context.Context, probeURL, hostAuthority string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return 0, err
	}
	if hostAuthority != "" {
		req.Host = hostAuthority
	}
	resp, err := probeHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// tcpSettleWindow is how long an accepted TCP probe connection must stay open
// (or deliver data) before the target counts as ready.
const tcpSettleWindow = 300 * time.Millisecond

// errTCPProbeClosed reports a connection that was accepted and then closed
// within the settle window.
var errTCPProbeClosed = errors.New("connection closed right after accept")

// tcpDialOnce attempts one TCP readiness check against a published loopback
// bind. A successful connect alone proves nothing: the host-side port relay
// (rootless Docker, docker-proxy) accepts on behalf of the container and then
// closes at once while the workload is not listening yet. So the connection
// must also survive tcpSettleWindow: data from the workload (a banner) or a
// connection still open at the end of the window means ready; EOF or a reset
// means not ready.
func tcpDialOnce(ctx context.Context, addr string) error {
	return tcpProbeOnce(ctx, addr, tcpSettleWindow)
}

func tcpProbeOnce(ctx context.Context, addr string, settle time.Duration) error {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(settle)); err != nil {
		return fmt.Errorf("set probe read deadline: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	var buf [1]byte
	n, err := conn.Read(buf[:])
	if n > 0 {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		return errTCPProbeClosed
	}
	return err
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
