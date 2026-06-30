package traffic

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type smartTCPDispatchKind string

const (
	dispatchHTTP         smartTCPDispatchKind = "http"
	dispatchH2C          smartTCPDispatchKind = "h2c"
	dispatchTLS          smartTCPDispatchKind = "tls"
	dispatchUnknown      smartTCPDispatchKind = "unknown"
	dispatchRejectPROXY  smartTCPDispatchKind = "reject_proxy"
	dispatchSniffTimeout smartTCPDispatchKind = "sniff_timeout"
	dispatchReject       smartTCPDispatchKind = "reject"
	dispatchRejectLarge  smartTCPDispatchKind = "reject_large"
)

type smartTCPSniffResult struct {
	kind smartTCPDispatchKind
	conn net.Conn
}

const (
	maxSmartTCPSniffBytes = 4096
	h2cPreface            = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	proxyV2Signature      = "\r\n\r\n\x00\r\nQUIT\n"
)

func sniffSmartTCP(conn net.Conn, timeout time.Duration) (smartTCPSniffResult, error) {
	if timeout > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return smartTCPSniffResult{}, fmt.Errorf("set sniff read deadline: %w", err)
		}
	}

	buf := make([]byte, 0, 32)
	for len(buf) < maxSmartTCPSniffBytes {
		remaining := maxSmartTCPSniffBytes - len(buf)
		readSize := min(32, remaining)
		tmp := make([]byte, readSize)
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if kind, done := classifySmartTCPPrefix(buf); done {
				return sniffResult(conn, buf, kind)
			}
		}
		if err != nil {
			if isTimeoutError(err) && len(buf) > 0 {
				return sniffResult(conn, buf, dispatchSniffTimeout)
			}
			return smartTCPSniffResult{}, fmt.Errorf("read smart tcp prefix: %w", err)
		}
	}
	return sniffResult(conn, buf, dispatchRejectLarge)
}

func sniffResult(conn net.Conn, buf []byte, kind smartTCPDispatchKind) (smartTCPSniffResult, error) {
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return smartTCPSniffResult{}, fmt.Errorf("clear sniff read deadline: %w", err)
	}
	return smartTCPSniffResult{kind: kind, conn: replayConn{Conn: conn, reader: bytes.NewReader(buf)}}, nil
}

func classifySmartTCPPrefix(buf []byte) (smartTCPDispatchKind, bool) {
	if bytes.Equal(buf, []byte(proxyV2Signature)) || (len(buf) >= len(proxyV2Signature) && bytes.HasPrefix(buf, []byte(proxyV2Signature))) {
		return dispatchRejectPROXY, true
	}
	if bytes.Equal(buf, []byte("PROXY ")) || (len(buf) >= len("PROXY ") && bytes.HasPrefix(buf, []byte("PROXY "))) {
		return dispatchRejectPROXY, true
	}
	if isCompleteH2CPreface(buf) {
		return dispatchH2C, true
	}
	if hasAnyPrefix([]byte(h2cPreface), buf) {
		return "", false
	}
	if isHTTPRequestLine(buf) {
		return dispatchHTTP, true
	}
	if isMalformedHTTPLookingPrefix(buf) {
		return dispatchReject, true
	}
	if isTLSClientHelloPrefix(buf) {
		return dispatchTLS, true
	}
	if isMalformedTLSRecordHeader(buf) {
		return dispatchReject, true
	}
	if isAmbiguousSmartTCPPrefix(buf) {
		return "", false
	}
	return dispatchUnknown, true
}

func isCompleteH2CPreface(buf []byte) bool {
	return len(buf) >= len(h2cPreface) && bytes.HasPrefix(buf, []byte(h2cPreface))
}

func isHTTPRequestLine(buf []byte) bool {
	lineBytes, _, ok := bytes.Cut(buf, []byte("\r\n"))
	if !ok {
		return false
	}
	line := string(lineBytes)
	parts := strings.Split(line, " ")
	return len(parts) == 3 && parts[0] != "" && isHTTPToken(parts[0]) && parts[1] != "" && strings.HasPrefix(parts[2], "HTTP/")
}

func isHTTPToken(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r <= 32 || r >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}\t", r) {
			return false
		}
	}
	return true
}

func isTLSClientHelloPrefix(buf []byte) bool {
	if len(buf) < 5 || buf[0] != 22 {
		return false
	}
	if buf[1] != 3 || buf[2] > 4 {
		return false
	}
	recordLen := int(buf[3])<<8 | int(buf[4])
	return recordLen > 0
}

func isMalformedTLSRecordHeader(buf []byte) bool {
	if len(buf) < 5 || buf[0] != 22 {
		return false
	}
	if buf[1] != 3 || buf[2] > 4 {
		return false
	}
	recordLen := int(buf[3])<<8 | int(buf[4])
	return recordLen == 0
}

func isAmbiguousSmartTCPPrefix(buf []byte) bool {
	return hasAnyPrefix([]byte(proxyV2Signature), buf) || hasAnyPrefix([]byte(h2cPreface), buf) || hasAnyPrefix([]byte("PROXY "), buf) || couldBecomeHTTPRequest(buf) || couldBecomeTLS(buf)
}

func hasAnyPrefix(full []byte, partial []byte) bool {
	return len(partial) < len(full) && bytes.HasPrefix(full, partial)
}

func isMalformedHTTPLookingPrefix(buf []byte) bool {
	lineBytes, _, ok := bytes.Cut(buf, []byte("\r\n"))
	if !ok || !bytes.Contains(lineBytes, []byte(" ")) {
		return false
	}
	method, _, _ := bytes.Cut(lineBytes, []byte(" "))
	return isKnownHTTPMethod(string(method))
}

func isKnownHTTPMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PROPFIND", "MKCOL":
		return true
	default:
		return false
	}
}

func couldBecomeHTTPRequest(buf []byte) bool {
	if len(buf) == 0 || bytes.Contains(buf, []byte{0}) {
		return false
	}
	if bytes.Contains(buf, []byte("\r\n")) {
		return false
	}
	method, _, _ := bytes.Cut(buf, []byte(" "))
	return isHTTPToken(string(method))
}

func couldBecomeTLS(buf []byte) bool {
	if len(buf) == 0 || buf[0] != 22 {
		return false
	}
	if len(buf) == 1 {
		return true
	}
	if buf[1] != 3 {
		return false
	}
	if len(buf) == 2 {
		return true
	}
	if buf[2] > 4 {
		return false
	}
	return len(buf) < 5
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
