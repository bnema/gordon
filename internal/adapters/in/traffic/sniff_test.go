package traffic

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSniffSmartTCPClassifiesHTTP(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "get", data: "GET / HTTP/1.1\r\n"},
		{name: "propfind", data: "PROPFIND / HTTP/1.1\r\n"},
		{name: "mkcol", data: "MKCOL / HTTP/1.1\r\n"},
		{name: "unknown_method", data: "FOO / HTTP/1.1\r\n"},
		{name: "extension_method", data: "BAR / HTTP/1.1\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := sniffBytes(t, []byte(tc.data), time.Second)
			assert.Equal(t, dispatchHTTP, result.kind)
			assertReplayPrefix(t, result.conn, []byte(tc.data))
		})
	}
}

func TestSniffSmartTCPClassifiesH2C(t *testing.T) {
	preface := []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	result := sniffBytes(t, preface, time.Second)
	require.Equal(t, dispatchH2C, result.kind)
	assertReplayPrefix(t, result.conn, preface)
}

func TestSniffSmartTCPClassifiesTLSAndAllowsClientHelloParsing(t *testing.T) {
	hello := clientHelloBytes(t, "sniff.example.com")
	result := sniffBytes(t, hello, time.Second)
	require.Equal(t, dispatchTLS, result.kind)

	sni, replayed, err := peekClientHelloSNI(result.conn)
	require.NoError(t, err)
	assert.Equal(t, "sniff.example.com", sni)
	assertReplayPrefix(t, replayed, hello)
}

func TestSniffSmartTCPRejectsMalformedTLSLookingZeroLengthRecord(t *testing.T) {
	result := sniffBytes(t, []byte{22, 3, 3, 0, 0}, time.Second)
	assert.Equal(t, dispatchReject, result.kind)
	assertReplayPrefix(t, result.conn, []byte{22, 3, 3, 0, 0})
}

func TestSniffSmartTCPClassifiesSSHBannerAsUnknown(t *testing.T) {
	banner := []byte("SSH-2.0-client\r\n")
	kind, done := classifySmartTCPPrefix(banner)
	require.True(t, done)
	assert.Equal(t, dispatchUnknown, kind)

	result := sniffBytes(t, banner, time.Second)
	assert.Equal(t, dispatchUnknown, result.kind)
	assertReplayPrefix(t, result.conn, banner)
}

func TestSniffSmartTCPRejectsMalformedHTTPLookingRequestLine(t *testing.T) {
	data := []byte("GET /broken\r\n")
	kind, done := classifySmartTCPPrefix(data)
	require.True(t, done)
	assert.Equal(t, dispatchReject, kind)

	result := sniffBytes(t, data, time.Second)
	assert.Equal(t, dispatchReject, result.kind)
	assertReplayPrefix(t, result.conn, data)
}

func TestSniffSmartTCPClassifiesTokenSpacePlaintextAsUnknown(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("USER anonymous\r\n"),
		[]byte("MAIL FROM:<a@example.com>\r\n"),
	} {
		result := sniffBytes(t, data, time.Second)
		assert.Equal(t, dispatchUnknown, result.kind)
		assertReplayPrefix(t, result.conn, data)
	}
}

func TestSniffSmartTCPRejectsPROXY(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "v1", data: []byte("PROXY TCP4 192.0.2.1 192.0.2.2 12345 443\r\n")},
		{name: "v2", data: append([]byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}, []byte{0x21, 0x11, 0x00, 0x0c}...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := sniffBytes(t, tc.data, time.Second)
			assert.Equal(t, dispatchRejectPROXY, result.kind)
			assertReplayPrefix(t, result.conn, tc.data)
		})
	}
}

func TestSniffSmartTCPUnknownIsImmediate(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	start := time.Now()
	done := make(chan smartTCPSniffResult, 1)
	go func() {
		result, err := sniffSmartTCP(server, time.Second)
		require.NoError(t, err)
		done <- result
	}()

	_, err := client.Write([]byte{0})
	require.NoError(t, err)
	select {
	case result := <-done:
		assert.Equal(t, dispatchUnknown, result.kind)
		assert.Less(t, time.Since(start), 500*time.Millisecond)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sniff did not classify unknown bytes immediately")
	}
}

func TestSniffSmartTCPAmbiguousPartialsTimeOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "g", data: []byte("G")},
		{name: "b", data: []byte("B")},
		{name: "bar", data: []byte("BAR")},
		{name: "pr", data: []byte("PR")},
		{name: "tls_content_type", data: []byte{22}},
		{name: "tls_version_major", data: []byte{22, 3}},
		{name: "tls_version_minor", data: []byte{22, 3, 3}},
		{name: "tls_record_length_partial", data: []byte{22, 3, 3, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := sniffBytes(t, tc.data, 20*time.Millisecond)
			assert.Equal(t, dispatchSniffTimeout, result.kind)
			assertReplayPrefix(t, result.conn, tc.data)
		})
	}
}

func TestSniffSmartTCPReplayReturnsSniffedBytesBeforeUnderlyingBytes(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan smartTCPSniffResult, 1)
	go func() {
		result, err := sniffSmartTCP(server, time.Second)
		require.NoError(t, err)
		done <- result
	}()
	_, err := client.Write([]byte("GET / HTTP/1.1\r\n"))
	require.NoError(t, err)
	result := <-done
	writeDone := make(chan error, 1)
	go func() {
		_, err := client.Write([]byte("Host: example.com\r\n\r\n"))
		writeDone <- err
	}()

	buf := make([]byte, len("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))
	_, err = io.ReadFull(result.conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", string(buf))
	require.NoError(t, <-writeDone)
}

func TestSniffSmartTCPClearsReadDeadlineBeforeHandoff(t *testing.T) {
	conn := &deadlineConn{Conn: pipeConnWithData(t, []byte("GET / HTTP/1.1\r\n"))}
	result, err := sniffSmartTCP(conn, time.Second)
	require.NoError(t, err)
	assert.Equal(t, dispatchHTTP, result.kind)
	assert.True(t, conn.sawNonZeroReadDeadline)
	assert.True(t, conn.sawClearedReadDeadline)
}

func sniffBytes(t *testing.T, data []byte, timeout time.Duration) smartTCPSniffResult {
	t.Helper()
	conn := pipeConnWithData(t, data)
	result, err := sniffSmartTCP(conn, timeout)
	require.NoError(t, err)
	return result
}

func pipeConnWithData(t *testing.T, data []byte) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Write(data)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		<-done
	})
	return server
}

func assertReplayPrefix(t *testing.T, conn net.Conn, want []byte) {
	t.Helper()
	buf := make([]byte, len(want))
	_, err := io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, want, buf)
}

type deadlineConn struct {
	net.Conn
	sawNonZeroReadDeadline bool
	sawClearedReadDeadline bool
}

func (c *deadlineConn) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		c.sawClearedReadDeadline = true
	} else {
		c.sawNonZeroReadDeadline = true
	}
	return c.Conn.SetReadDeadline(t)
}

func TestSniffSmartTCPDoesNotTreatClosedPipeAsSuccess(t *testing.T) {
	client, server := net.Pipe()
	_ = client.Close()
	_, err := sniffSmartTCP(server, time.Second)
	if err == nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal("expected closed connection to return an error")
	}
}
