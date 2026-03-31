package pki

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"golang.org/x/sync/singleflight"

	"github.com/bnema/gordon/internal/boundaries/out"
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
	ca     out.CertificateAuthority
	routes out.RouteChecker
	log    zerowrap.Logger

	cache  sync.Map // map[string]*cachedCert
	flight singleflight.Group
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService creates a PKI service and starts background maintenance goroutines.
// It performs an initial intermediate renewal check synchronously so the first
// TLS handshakes never use a nearly-expired intermediate.
func NewService(ctx context.Context, ca out.CertificateAuthority, routes out.RouteChecker, log zerowrap.Logger) *Service {
	ctx, cancel := context.WithCancel(ctx)
	svc := &Service{
		ca:     ca,
		routes: routes,
		log:    log,
		cancel: cancel,
		done:   make(chan struct{}),
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
	domain := hello.ServerName
	if domain == "" {
		return nil, nil
	}

	if entry, ok := s.cache.Load(domain); ok {
		if cached, ok := entry.(*cachedCert); ok {
			if time.Now().Before(cached.expiresAt) {
				if !s.isDomainAllowed(hello.Context(), domain) {
					s.cache.Delete(domain)
					return nil, nil
				}
				return cached.cert, nil
			}
		}
		s.cache.Delete(domain)
	}

	if !s.isDomainAllowed(hello.Context(), domain) {
		return nil, nil
	}

	result, err, _ := s.flight.Do(domain, func() (any, error) {
		// Re-check cache; another goroutine in the same flight may have populated it.
		if entry, ok := s.cache.Load(domain); ok {
			if cached, ok := entry.(*cachedCert); ok {
				if time.Now().Before(cached.expiresAt) {
					return cached.cert, nil
				}
			}
			s.cache.Delete(domain)
		}

		cert, err := s.ca.IssueCertificate(domain)
		if err != nil {
			s.log.Error().Err(err).Str("domain", domain).Msg("failed to issue leaf cert")
			return nil, err
		}

		expiresAt := time.Now().Add(s.leafLifetime())
		if cert.Leaf != nil {
			expiresAt = cert.Leaf.NotAfter
		}
		s.cache.Store(domain, &cachedCert{
			cert:      cert,
			expiresAt: expiresAt,
		})

		s.log.Debug().Str("domain", domain).Msg("issued new leaf certificate")
		return cert, nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return result.(*tls.Certificate), nil
}

func (s *Service) isDomainAllowed(ctx context.Context, domain string) bool {
	for _, r := range s.routes.GetRoutes(ctx) {
		if r.Domain == domain {
			return true
		}
	}
	if _, ok := s.routes.GetExternalRoutes()[domain]; ok {
		return true
	}
	return false
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

	allowed := make(map[string]struct{}, len(routes)+len(extRoutes))
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
