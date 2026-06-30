package traffic

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"maps"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const (
	maxClientHelloBytes    = 64 << 10
	clientHelloReadTimeout = 250 * time.Millisecond
)

// TLSHTTPServerConfig describes the HTTPS server attached to a tls_mux entrypoint.
type TLSHTTPServerConfig struct {
	Handler   http.Handler
	TLSConfig *tls.Config
}

type tlsHTTPServers map[string]TLSHTTPServerConfig

// SetTLSHTTPServer installs or removes the HTTPS server for a tls_mux entrypoint.
func (m *Manager) SetTLSHTTPServer(entryPoint string, handler http.Handler, tlsConfig *tls.Config) {
	m.mu.Lock()
	current := m.loadTLSHTTPServers()
	next := make(tlsHTTPServers, len(current)+1)
	maps.Copy(next, current)
	if handler == nil || tlsConfig == nil {
		delete(next, entryPoint)
	} else {
		next[entryPoint] = TLSHTTPServerConfig{Handler: handler, TLSConfig: tlsConfig.Clone()}
	}
	m.tlsHTTPServers.Store(next)
	runtimes := make([]*entryPointRuntime, 0, len(m.listeners))
	for _, runtime := range m.listeners {
		if runtime.entryPointSnapshot().Name == entryPoint {
			runtimes = append(runtimes, runtime)
		}
	}
	m.mu.Unlock()

	for _, runtime := range runtimes {
		runtime.refreshTLSHTTPServer(entryPoint)
	}
}

func (m *Manager) tlsHTTPServer(entryPoint string) (TLSHTTPServerConfig, bool) {
	config, ok := m.loadTLSHTTPServers()[entryPoint]
	return config, ok
}

func (m *Manager) loadTLSHTTPServers() tlsHTTPServers {
	value := m.tlsHTTPServers.Load()
	if value == nil {
		return tlsHTTPServers{}
	}
	return value.(tlsHTTPServers)
}

func peekClientHelloSNI(conn net.Conn) (string, net.Conn, error) {
	return peekClientHelloSNIWithLimit(conn, maxClientHelloBytes)
}

func peekClientHelloSNIWithLimit(conn net.Conn, maxBytes int) (string, net.Conn, error) {
	if err := conn.SetReadDeadline(time.Now().Add(clientHelloReadTimeout)); err != nil {
		return "", nil, fmt.Errorf("set client hello read deadline: %w", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	buf := make([]byte, 0, 4096)
	for len(buf) < maxBytes {
		remaining := maxBytes - len(buf)
		readSize := min(1024, remaining)
		tmp := make([]byte, readSize)
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			sni, complete, parseErr := parseClientHelloSNI(buf)
			if parseErr != nil {
				return "", nil, fmt.Errorf("parse client hello sni: %w", parseErr)
			}
			if complete {
				return sni, replayConn{Conn: conn, reader: bytes.NewReader(buf)}, nil
			}
		}
		if err != nil {
			return "", nil, fmt.Errorf("read client hello: %w", err)
		}
	}
	return "", nil, fmt.Errorf("%w: %d bytes", domain.ErrClientHelloTooLarge, maxBytes)
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
	handshake := append([]byte(nil), record...)
	for len(handshake) < 4 {
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
	if handshake[0] != 1 {
		return "", true, fmt.Errorf("tls handshake is not client hello")
	}
	handshakeLen := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
	for len(handshake)-4 < handshakeLen {
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
	sni, err := parseClientHelloExtensions(handshake[4 : 4+handshakeLen])
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
	for range size {
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

type tlsHTTPListener struct {
	addr   net.Addr
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newTLSHTTPListener(addr net.Addr) *tlsHTTPListener {
	return &tlsHTTPListener{addr: addr, conns: make(chan net.Conn, 128), done: make(chan struct{})}
}

func (l *tlsHTTPListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *tlsHTTPListener) Close() error {
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		close(l.done)
	})
	return nil
}

func (l *tlsHTTPListener) Addr() net.Addr { return l.addr }

func (l *tlsHTTPListener) serve(conn net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	select {
	case l.conns <- conn:
		return true
	default:
		return false
	}
}
