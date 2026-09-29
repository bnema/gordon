package app

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/gordon/internal/usecase/publictls"
	"github.com/bnema/gordon/internal/usecase/traffic"
)

// startProxyServers sets up the HTTP proxy server and, when tls_port != 0,
// an HTTPS proxy server with on-demand TLS certificates from the internal CA.
// certificateSelector implements a multi-source TLS certificate lookup.
// Priority: static certs → public ACME TLS → local PKI (internal CA).
type certificateSelector struct {
	staticCerts []staticTLSCertificate
	publicTLS   in.PublicTLSService
	localPKI    *pkiusecase.Service
}

type staticTLSCertificate struct {
	cert tls.Certificate
	leaf *x509.Certificate
}

// GetCertificate selects a TLS certificate based on the ClientHello SNI.
//
// Priority:
//  1. Static certs — exact SNI match (leaf VerifyHostname)
//  2. Public ACME TLS — if the host requires ACME coverage
//  3. Local PKI (internal CA) — fallback for all other hosts
//  4. nil, nil — if no source can serve the host
func (s *certificateSelector) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	// 1. Static certs — exact match via leaf VerifyHostname.
	if cert := matchingPreparedStaticCert(s.staticCerts, hello.ServerName); cert != nil {
		return cert, nil
	}

	// 2. Public ACME TLS.
	if s.publicTLS != nil {
		cert, err := s.publicTLS.GetCertificateForHost(hello.ServerName)
		if err == nil && cert != nil {
			return cert, nil
		}
		// nil, nil means this host is not an ACME-required route. Errors mean
		// public ACME cannot currently serve this host. In both cases, fall
		// through to local PKI instead of aborting the TLS handshake.
	}

	// 3. Local PKI (internal CA).
	if s.localPKI != nil {
		return s.localPKI.GetCertificate(hello)
	}

	return nil, nil
}

func prepareStaticTLSCertificates(certs []tls.Certificate) []staticTLSCertificate {
	prepared := make([]staticTLSCertificate, 0, len(certs))
	for _, cert := range certs {
		if cert.Leaf == nil && len(cert.Certificate) > 0 {
			leaf, err := x509.ParseCertificate(cert.Certificate[0])
			if err == nil {
				cert.Leaf = leaf
			}
		}
		prepared = append(prepared, staticTLSCertificate{cert: cert, leaf: cert.Leaf})
	}
	return prepared
}

func matchingPreparedStaticCert(certs []staticTLSCertificate, serverName string) *tls.Certificate {
	if serverName == "" {
		if len(certs) == 0 {
			return nil
		}
		return &certs[0].cert
	}
	for i := range certs {
		if certs[i].leaf == nil {
			continue
		}
		if err := certs[i].leaf.VerifyHostname(serverName); err == nil {
			return &certs[i].cert
		}
	}
	return nil
}

// matchingStaticCert returns a pointer to the first static certificate whose
// leaf verifies the given serverName. Returns nil if no match is found.
func matchingStaticCert(certs []tls.Certificate, serverName string) *tls.Certificate {
	return matchingPreparedStaticCert(prepareStaticTLSCertificates(certs), serverName)
}

func startProxyServers(cfg Config, httpHandler, httpsHandler http.Handler, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, trafficManager *trafficadapter.Manager, log zerowrap.Logger) (*http.Server, <-chan struct{}, *http.Server, <-chan struct{}, error) {
	var httpSrv *http.Server
	var httpReady <-chan struct{}

	needsTLS := hasTLSCapableEntrypoint(cfg)
	if !hasSmartTCPEntrypoint(cfg) && !needsTLS {
		return httpSrv, httpReady, nil, nil, nil
	}
	cleanupHTTP := func(err error) (*http.Server, <-chan struct{}, *http.Server, <-chan struct{}, error) {
		return httpSrv, httpReady, nil, nil, err
	}

	var tlsConfig *tls.Config
	if needsTLS {
		var err error
		tlsConfig, err = proxyTLSConfig(cfg, pkiSvc, publicTLS, log)
		if err != nil {
			return cleanupHTTP(err)
		}
	}
	if trafficManager == nil {
		return cleanupHTTP(fmt.Errorf("traffic manager is required when traffic entrypoints are enabled"))
	}
	registerTLSMuxHTTPServers(trafficManager, cfg, httpsHandler, tlsConfig, nil)
	registerSmartTCPHTTPServers(trafficManager, cfg, httpHandler, httpsHandler, tlsConfig, nil)

	return httpSrv, httpReady, nil, nil, nil
}

func hasSmartTCPEntrypoint(cfg Config) bool {
	for _, entryPoint := range cfg.EntryPoints {
		if entryPoint.Protocol == domain.EntryPointProtocolSmartTCP {
			return true
		}
	}
	return false
}

func hasTLSCapableEntrypoint(cfg Config) bool {
	for _, entryPoint := range cfg.EntryPoints {
		switch entryPoint.Protocol {
		case domain.EntryPointProtocolSmartTCP, domain.EntryPointProtocolTLSMux:
			return true
		}
	}
	return false
}

func effectivePublicTLSPort(cfg Config) int {
	return effectiveEntrypointPort(cfg, tlsCapableEntryPoint)
}

func effectiveProxyTLSPort(cfg Config) int {
	return effectivePublicTLSPort(cfg)
}

func effectiveProxyHTTPPort(cfg Config) int {
	return effectiveEntrypointPort(cfg, func(protocol domain.EntryPointProtocol) bool {
		return protocol == domain.EntryPointProtocolSmartTCP
	})
}

func effectiveEntrypointPort(cfg Config, match func(domain.EntryPointProtocol) bool) int {
	if entryPoint, ok := cfg.EntryPoints[traffic.DefaultEdgeEntryPointName]; ok && match(entryPoint.Protocol) {
		if port := portFromAddress(entryPoint.Address); port > 0 {
			return port
		}
	}

	var candidatePort int
	candidates := 0
	for name, entryPoint := range cfg.EntryPoints {
		if name == traffic.DefaultEdgeEntryPointName || !match(entryPoint.Protocol) {
			continue
		}
		port := portFromAddress(entryPoint.Address)
		if port == 0 {
			continue
		}
		candidatePort = port
		candidates++
	}
	if candidates == 1 {
		return candidatePort
	}
	return 0
}

func tlsCapableEntryPoint(protocol domain.EntryPointProtocol) bool {
	switch protocol {
	case domain.EntryPointProtocolSmartTCP, domain.EntryPointProtocolTLSMux:
		return true
	default:
		return false
	}
}

func effectiveHTTP01Port(cfg Config) int {
	if hasSmartTCPHTTP01Entrypoint(cfg) {
		return 80
	}
	return 0
}

func validatePublicTLSReadiness(cfg Config) error {
	mode, err := domain.ParseACMEChallengeMode(cfg.TLS.ACME.Challenge)
	if err != nil {
		return err
	}
	switch mode {
	case domain.ACMEChallengeCloudflareDNS01, domain.ACMEChallengeAuto:
		return nil
	case domain.ACMEChallengeHTTP01:
		return validateHTTP01ChallengeReadiness(cfg)
	default:
		return fmt.Errorf("%w: %q", domain.ErrACMEChallengeInvalid, mode)
	}
}

func validateEffectivePublicTLSReadiness(cfg Config, effective publictls.EffectiveChallenge) error {
	if effective.Mode != domain.ACMEChallengeHTTP01 {
		return nil
	}
	return validateHTTP01ChallengeReadiness(cfg)
}

func validateHTTP01ChallengeReadiness(cfg Config) error {
	if hasBoundHTTP01ChallengeListener(cfg) {
		return nil
	}
	return fmt.Errorf("%w: http-01 requires an actually bound HTTP-01 challenge listener on external :80", domain.ErrACMEChallengeInvalid)
}

func hasBoundHTTP01ChallengeListener(cfg Config) bool {
	return hasSmartTCPHTTP01Entrypoint(cfg)
}

func hasSmartTCPHTTP01Entrypoint(cfg Config) bool {
	for _, entryPoint := range cfg.EntryPoints {
		if entryPoint.Protocol == domain.EntryPointProtocolSmartTCP && portFromAddress(entryPoint.Address) == 80 {
			return true
		}
	}
	return false
}

func portFromAddress(address string) int {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0
	}
	return port
}

func proxyTLSConfig(cfg Config, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, log zerowrap.Logger) (*tls.Config, error) {
	var staticCerts []tls.Certificate
	if cfg.Server.TLSCertFile != "" {
		staticCert, err := tls.LoadX509KeyPair(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS keypair: %w", err)
		}
		staticCerts = []tls.Certificate{staticCert}
		log.Info().
			Str("cert", cfg.Server.TLSCertFile).
			Str("key", cfg.Server.TLSKeyFile).
			Msg("loaded static TLS certificate (public ACME and internal CA handle remaining domains)")
	}
	selector := &certificateSelector{
		staticCerts: prepareStaticTLSCertificates(staticCerts),
		publicTLS:   publicTLS,
		localPKI:    pkiSvc,
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: selector.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}, nil
}

func registerSmartTCPHTTPServers(manager *trafficadapter.Manager, cfg Config, httpHandler, httpsHandler http.Handler, tlsConfig *tls.Config, previous map[string]struct{}) map[string]struct{} {
	if manager == nil {
		return previous
	}
	next := smartTCPHTTPServerNames(cfg)
	for name := range previous {
		if _, ok := next[name]; !ok {
			manager.SetSmartTCPHTTPServer(name, nil, nil)
			manager.SetSmartTCPTLSServer(name, nil, nil)
		}
	}
	var httpProtos http.Protocols
	httpProtos.SetHTTP1(true)
	httpProtos.SetUnencryptedHTTP2(true)
	for name := range next {
		if httpHandler != nil {
			manager.SetSmartTCPHTTPServer(name, httpHandler, &httpProtos)
		}
		if httpsHandler != nil && tlsConfig != nil {
			manager.SetSmartTCPTLSServer(name, httpsHandler, tlsConfig)
		}
	}
	return next
}

func smartTCPHTTPServerNames(cfg Config) map[string]struct{} {
	names := map[string]struct{}{}
	for name, entryPoint := range cfg.EntryPoints {
		if entryPoint.Protocol == domain.EntryPointProtocolSmartTCP {
			names[name] = struct{}{}
		}
	}
	return names
}

func registerTLSMuxHTTPServers(manager *trafficadapter.Manager, cfg Config, httpsHandler http.Handler, tlsConfig *tls.Config, previous map[string]struct{}) map[string]struct{} {
	if manager == nil {
		return previous
	}
	next := tlsMuxHTTPServerNames(cfg)
	for name := range previous {
		if _, ok := next[name]; !ok {
			manager.SetTLSHTTPServer(name, nil, nil)
		}
	}
	if httpsHandler == nil || tlsConfig == nil {
		return next
	}
	for name := range next {
		manager.SetTLSHTTPServer(name, httpsHandler, tlsConfig)
	}
	return next
}

func tlsMuxHTTPServerNames(cfg Config) map[string]struct{} {
	names := map[string]struct{}{}
	for name, entryPoint := range cfg.EntryPoints {
		domainEntryPoint := domain.EntryPoint{Name: name, Protocol: entryPoint.Protocol}
		if trafficManagerOwnsEntryPoint(domainEntryPoint) && domainEntryPoint.Protocol == domain.EntryPointProtocolTLSMux {
			names[name] = struct{}{}
		}
	}
	return names
}

func startRegistryServers(cfg Config, handler http.Handler, errChan chan<- error, log zerowrap.Logger) (*http.Server, <-chan struct{}, *http.Server, <-chan struct{}) {
	port := strconv.Itoa(cfg.Server.RegistryPort)
	externalAddr := net.JoinHostPort(cfg.Server.RegistryListenAddr, port)
	externalSrv, externalReady := startServer(externalAddr, handler, "registry", nil, errChan, log)
	internalReady := closedReadyChannel()
	if !needsInternalRegistryListener(cfg.Server.RegistryListenAddr) {
		return externalSrv, externalReady, nil, internalReady
	}
	internalAddr := net.JoinHostPort("127.0.0.1", port)
	internalSrv, internalReady := startServer(internalAddr, handler, "internal registry", nil, errChan, log)
	return externalSrv, externalReady, internalSrv, internalReady
}

func closedReadyChannel() <-chan struct{} {
	ready := make(chan struct{})
	close(ready)
	return ready
}

func needsInternalRegistryListener(listenAddr string) bool {
	addr := net.ParseIP(strings.TrimSpace(listenAddr))
	if addr == nil {
		return listenAddr != ""
	}
	return !addr.IsLoopback() && !addr.IsUnspecified()
}

// startServer starts an HTTP server, returning the server instance and a channel
// that closes once the listening socket is bound. This lets callers wait for the
// port to be ready before taking actions that depend on it (e.g. auto-start
// pulling from the local registry). The returned *http.Server can be used for
// graceful shutdown.
func startServer(addr string, handler http.Handler, name string, protocols *http.Protocols, errChan chan<- error, log zerowrap.Logger) (*http.Server, <-chan struct{}) {
	ready := make(chan struct{})

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Info().Str("address", addr).Msgf("%s server starting", name)

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errChan <- fmt.Errorf("%s server error: %w", name, err)
			return
		}
		close(ready) // signal: port is bound and accepting connections

		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("%s server error: %w", name, err)
		}
	}()

	return server, ready
}
