package traffic

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxClientHelloBytes = 64 << 10

// TLSFallback handles tls_mux connections that do not match a passthrough router.
type TLSFallback func(context.Context, net.Conn)

type tlsFallbacks map[string]TLSFallback

// SetTLSFallback installs or removes an HTTPS fallback for a tls_mux entrypoint.
func (m *Manager) SetTLSFallback(entryPoint string, fallback TLSFallback) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.loadTLSFallbacks()
	next := make(tlsFallbacks, len(current)+1)
	for name, value := range current {
		next[name] = value
	}
	if fallback == nil {
		delete(next, entryPoint)
	} else {
		next[entryPoint] = fallback
	}
	m.tlsFallbacks.Store(next)
}

func (m *Manager) tlsFallback(entryPoint string) TLSFallback {
	return m.loadTLSFallbacks()[entryPoint]
}

func (m *Manager) loadTLSFallbacks() tlsFallbacks {
	value := m.tlsFallbacks.Load()
	if value == nil {
		return tlsFallbacks{}
	}
	return value.(tlsFallbacks)
}

func peekClientHelloSNI(conn net.Conn) (string, net.Conn, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for len(buf) < maxClientHelloBytes {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			sni, complete, parseErr := parseClientHelloSNI(buf)
			if parseErr != nil {
				return "", nil, parseErr
			}
			if complete {
				return sni, replayConn{Conn: conn, reader: bytes.NewReader(buf)}, nil
			}
		}
		if err != nil {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("client hello exceeds %d bytes", maxClientHelloBytes)
}

func parseClientHelloSNI(data []byte) (string, bool, error) {
	if len(data) < 5 {
		return "", false, nil
	}
	if data[0] != 22 {
		return "", false, fmt.Errorf("tls record is not a handshake")
	}
	record, consumed, complete, err := tlsRecordPayload(data, 0)
	if err != nil || !complete {
		return "", complete, err
	}
	if len(record) < 4 {
		return "", true, fmt.Errorf("tls handshake is truncated")
	}
	if record[0] != 1 {
		return "", true, fmt.Errorf("tls handshake is not client hello")
	}
	handshakeLen := int(record[1])<<16 | int(record[2])<<8 | int(record[3])
	handshake := append([]byte(nil), record[4:]...)
	for len(handshake) < handshakeLen {
		if len(data) < consumed+5 {
			return "", false, nil
		}
		next, nextConsumed, nextComplete, err := tlsRecordPayload(data, consumed)
		if err != nil || !nextComplete {
			return "", nextComplete, err
		}
		handshake = append(handshake, next...)
		consumed = nextConsumed
	}
	sni, err := parseClientHelloExtensions(handshake[:handshakeLen])
	return sni, true, err
}

func tlsRecordPayload(data []byte, offset int) ([]byte, int, bool, error) {
	if data[offset] != 22 {
		return nil, offset, false, fmt.Errorf("tls record is not a handshake")
	}
	recordLen := int(data[offset+3])<<8 | int(data[offset+4])
	if recordLen <= 0 {
		return nil, offset, false, fmt.Errorf("tls handshake record is empty")
	}
	end := offset + 5 + recordLen
	if len(data) < end {
		return nil, offset, false, nil
	}
	return data[offset+5 : end], end, true, nil
}

func parseClientHelloExtensions(body []byte) (string, error) {
	reader := bytes.NewReader(body)
	if reader.Len() < 34 {
		return "", fmt.Errorf("client hello is truncated")
	}
	_, _ = readBytes(reader, 34) // legacy_version + random
	if err := skipLengthPrefixed(reader, 1); err != nil {
		return "", err
	}
	if err := skipLengthPrefixed(reader, 2); err != nil {
		return "", err
	}
	if err := skipLengthPrefixed(reader, 1); err != nil {
		return "", err
	}
	if reader.Len() == 0 {
		return "", nil
	}
	extLen, err := readUint16(reader)
	if err != nil {
		return "", err
	}
	if extLen > reader.Len() {
		return "", fmt.Errorf("client hello extensions are truncated")
	}
	extBytes, _ := readBytes(reader, extLen)
	extensions := bytes.NewReader(extBytes)
	for extensions.Len() > 0 {
		extType, err := readUint16(extensions)
		if err != nil {
			return "", err
		}
		extDataLen, err := readUint16(extensions)
		if err != nil {
			return "", err
		}
		if extDataLen > extensions.Len() {
			return "", fmt.Errorf("client hello extension is truncated")
		}
		extData, _ := readBytes(extensions, extDataLen)
		if extType == 0 {
			return parseSNIExtension(extData)
		}
	}
	return "", nil
}

func parseSNIExtension(data []byte) (string, error) {
	reader := bytes.NewReader(data)
	listLen, err := readUint16(reader)
	if err != nil {
		return "", err
	}
	if listLen > reader.Len() {
		return "", fmt.Errorf("sni extension is truncated")
	}
	listBytes, _ := readBytes(reader, listLen)
	list := bytes.NewReader(listBytes)
	for list.Len() > 0 {
		nameType, err := list.ReadByte()
		if err != nil {
			return "", err
		}
		nameLen, err := readUint16(list)
		if err != nil {
			return "", err
		}
		if nameLen > list.Len() {
			return "", fmt.Errorf("sni name is truncated")
		}
		nameBytes, _ := readBytes(list, nameLen)
		name := string(nameBytes)
		if nameType == 0 {
			return normalizeTLSName(name), nil
		}
	}
	return "", nil
}

func skipLengthPrefixed(reader *bytes.Reader, size int) error {
	length, err := readLength(reader, size)
	if err != nil {
		return err
	}
	if length > reader.Len() {
		return fmt.Errorf("client hello field is truncated")
	}
	_, _ = readBytes(reader, length)
	return nil
}

func readBytes(reader *bytes.Reader, size int) ([]byte, error) {
	if reader.Len() < size {
		return nil, fmt.Errorf("client hello field is truncated")
	}
	value := make([]byte, size)
	_, _ = reader.Read(value)
	return value, nil
}

func readLength(reader *bytes.Reader, size int) (int, error) {
	if reader.Len() < size {
		return 0, fmt.Errorf("client hello length is truncated")
	}
	value := 0
	for i := 0; i < size; i++ {
		b, _ := reader.ReadByte()
		value = value<<8 | int(b)
	}
	return value, nil
}

func readUint16(reader *bytes.Reader) (int, error) {
	return readLength(reader, 2)
}

func normalizeTLSName(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

type replayConn struct {
	net.Conn
	reader *bytes.Reader
}

func (c replayConn) Read(p []byte) (int, error) {
	if c.reader != nil && c.reader.Len() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}

// ServeHTTPSFallback serves a single TLS connection with the provided HTTP handler.
func ServeHTTPSFallback(ctx context.Context, conn net.Conn, handler http.Handler, tlsConfig *tls.Config) error {
	tracked := newTrackedCloseConn(tls.Server(conn, tlsConfig))
	listener := newSingleConnListener(tracked)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()

	select {
	case <-tracked.done:
		_ = server.Close()
		err := <-errCh
		if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		_ = server.Close()
		_ = tracked.Close()
		<-errCh
		return ctx.Err()
	}
}

type singleConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{conn: conn, done: make(chan struct{})}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = l.conn })
	if conn != nil {
		return conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

type trackedCloseConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func newTrackedCloseConn(conn net.Conn) *trackedCloseConn {
	return &trackedCloseConn{Conn: conn, done: make(chan struct{})}
}

func (c *trackedCloseConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}
