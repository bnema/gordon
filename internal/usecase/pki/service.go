package pki

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"golang.org/x/sync/singleflight"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	renewalWindowRatio = 0.2
	checkInterval      = 10 * time.Minute
)

type cachedCert struct {
	cert      *tls.Certificate
	expiresAt time.Time
}

// Service provides on-demand TLS certificate issuance with caching
// and automatic intermediate CA renewal.
//
// TODO: consider adding CRL/OCSP support for certificate revocation,
// especially given the 10-year root CA lifetime.
type Service struct {
	ca             out.CertificateAuthority
	routes         out.RouteChecker
	allowedMu      sync.RWMutex
	allowedDomains map[string]struct{}
	log            zerowrap.Logger

	cache  sync.Map // map[string]*cachedCert
	flight singleflight.Group
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService creates a PKI service and starts background maintenance goroutines.
// It performs an initial intermediate renewal check synchronously so the first
// TLS handshakes never use a nearly-expired intermediate.
func NewService(ctx context.Context, ca out.CertificateAuthority, routes out.RouteChecker, allowedDomains []string, log zerowrap.Logger) *Service {
	ctx, cancel := context.WithCancel(ctx)
	allowed := canonicalDomainSet(allowedDomains)
	svc := &Service{
		ca:             ca,
		routes:         routes,
		allowedDomains: allowed,
		log:            log,
		cancel:         cancel,
		done:           make(chan struct{}),
	}
	svc.renewIntermediateIfNeeded()
	go svc.maintenanceLoop(ctx)
	return svc
}

// Stop cancels background goroutines and waits for them to finish.
func (s *Service) Stop() {
	s.cancel()
	<-s.done
}

// CachedCertCount returns the number of cached leaf certificates.
func (s *Service) CachedCertCount() int {
	count := 0
	s.cache.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// GetCertificate is the tls.Config.GetCertificate callback.
// It returns (nil, nil) for unknown domains so Go's TLS stack
// falls back to tls.Config.Certificates.
func (s *Service) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	domainName := canonicalServerName(hello.ServerName)
	if domainName == "" {
		return nil, nil
	}

	if cert := s.validCachedCertificate(domainName); cert != nil {
		if s.isDomainAllowed(hello.Context(), domainName) {
			return cert, nil
		}
		s.cache.Delete(domainName)
		return nil, nil
	}
	if !s.isDomainAllowed(hello.Context(), domainName) {
		return nil, nil
	}

	result, err, _ := s.flight.Do(domainName, func() (any, error) {
		return s.issueCertificate(hello.Context(), domainName)
	})
	if err != nil {
		return nil, err
	}
	if result == nil || !s.isDomainAllowed(hello.Context(), domainName) {
		s.cache.Delete(domainName)
		return nil, nil
	}
	return result.(*tls.Certificate), nil
}

func canonicalServerName(serverName string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(serverName), "."))
}

func (s *Service) validCachedCertificate(domainName string) *tls.Certificate {
	entry, ok := s.cache.Load(domainName)
	if !ok {
		return nil
	}
	cached, ok := entry.(*cachedCert)
	if !ok || !time.Now().Before(cached.expiresAt) {
		s.cache.Delete(domainName)
		return nil
	}
	return cached.cert
}

func (s *Service) issueCertificate(ctx context.Context, domainName string) (*tls.Certificate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = zerowrap.WithCtx(ctx, s.log)
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "IssueCertificate",
		"domain":              domainName,
	})
	log := zerowrap.FromCtx(ctx)

	if !s.isDomainAllowed(ctx, domainName) {
		return nil, nil
	}
	if cert := s.validCachedCertificate(domainName); cert != nil {
		return cert, nil
	}

	cert, err := s.ca.IssueCertificate(domainName)
	if err != nil {
		log.Error().Err(err).Msg("failed to issue leaf certificate")
		return nil, fmt.Errorf("issue leaf certificate for %q: %w", domainName, err)
	}
	if !s.isDomainAllowed(ctx, domainName) {
		return nil, nil
	}

	s.cache.Store(domainName, &cachedCert{cert: cert, expiresAt: certificateExpiry(cert, s.leafLifetime())})
	log.Debug().Msg("issued new leaf certificate")
	return cert, nil
}

func certificateExpiry(cert *tls.Certificate, fallbackLifetime time.Duration) time.Time {
	if len(cert.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			return leaf.NotAfter
		}
	}
	return time.Now().Add(fallbackLifetime)
}

// SetAdditionalDomains atomically replaces non-route domains authorized for
// on-demand certificates and evicts certificates that are no longer allowed.
func (s *Service) SetAdditionalDomains(domains []string) {
	next := canonicalDomainSet(domains)

	s.allowedMu.Lock()
	previous := s.allowedDomains
	s.allowedDomains = next
	s.allowedMu.Unlock()

	for name := range previous {
		if _, ok := next[name]; !ok {
			s.cache.Delete(name)
		}
	}
}

func (s *Service) isDomainAllowed(ctx context.Context, domainName string) bool {
	s.allowedMu.RLock()
	_, additionallyAllowed := s.allowedDomains[domainName]
	s.allowedMu.RUnlock()
	if additionallyAllowed {
		return true
	}
	for _, r := range s.routes.GetRoutes(ctx) {
		if r.Domain == domainName {
			return true
		}
	}
	if _, ok := s.routes.GetExternalRoutes()[domainName]; ok {
		return true
	}
	return false
}

func canonicalDomainSet(domains []string) map[string]struct{} {
	canonical := make(map[string]struct{}, len(domains))
	for _, name := range domains {
		name = strings.TrimSuffix(strings.TrimSpace(name), ".")
		if name, ok := domain.CanonicalRouteDomain(name); ok {
			canonical[name] = struct{}{}
		}
	}
	return canonical
}

func (s *Service) leafLifetime() time.Duration {
	return s.ca.LeafLifetime()
}

func (s *Service) maintenanceLoop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.renewIntermediateIfNeeded()
			s.sweepExpiredCerts(ctx)
		}
	}
}

func (s *Service) renewIntermediateIfNeeded() {
	expiresAt := s.ca.IntermediateExpiresAt()
	remaining := time.Until(expiresAt)
	lifetime := s.ca.IntermediateLifetime()
	window := time.Duration(float64(lifetime) * renewalWindowRatio)
	if remaining < window {
		s.log.Info().Time("expires", expiresAt).Msg("renewing intermediate CA")
		if err := s.ca.RenewIntermediate(); err != nil {
			s.log.Error().Err(err).Msg("intermediate CA renewal failed")
		}
	}
}

func (s *Service) sweepExpiredCerts(ctx context.Context) {
	now := time.Now()

	// Fetch routes once for all cache entries.
	routes := s.routes.GetRoutes(ctx)
	extRoutes := s.routes.GetExternalRoutes()

	s.allowedMu.RLock()
	allowed := make(map[string]struct{}, len(routes)+len(extRoutes)+len(s.allowedDomains))
	for domain := range s.allowedDomains {
		allowed[domain] = struct{}{}
	}
	s.allowedMu.RUnlock()
	for _, r := range routes {
		allowed[r.Domain] = struct{}{}
	}
	for d := range extRoutes {
		allowed[d] = struct{}{}
	}

	swept := 0
	s.cache.Range(func(key, value any) bool {
		domain := key.(string)
		cached := value.(*cachedCert)
		_, ok := allowed[domain]
		if now.After(cached.expiresAt) || !ok {
			s.cache.Delete(key)
			swept++
		}
		return true
	})
	if swept > 0 {
		s.log.Debug().Int("swept", swept).Msg("cleaned expired/unauthorized leaf certs from cache")
	}
}
