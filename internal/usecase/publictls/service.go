package publictls

import (
	"context"
	"crypto/tls"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// RouteSource provides routes from which certificate targets are derived.
type RouteSource interface {
	GetRoutes(ctx context.Context) []domain.Route
	GetExternalRoutes() map[string]string
}

// ServiceDeps contains the dependencies for the Service.
type ServiceDeps struct {
	Config       Config
	Routes       RouteSource
	Issuer       out.PublicCertificateIssuer
	Store        out.CertificateStore
	ZoneResolver out.CloudflareZoneResolver
	Challenges   *HTTP01Challenges
	Effective    EffectiveChallenge
	Log          zerowrap.Logger
}

const defaultObtainBatchSize = 1

// Service manages public TLS certificates via ACME.
type Service struct {
	mu       sync.RWMutex
	cfg      Config
	deps     ServiceDeps
	log      zerowrap.Logger
	certs    map[string]*out.StoredCertificate // indexed by cert ID
	lastErr  map[string]string                 // indexed by cert ID
	routeErr map[string]string                 // indexed by host

	// requiredHosts is the set of hosts that must be covered by ACME certs.
	requiredHosts map[string]struct{}

	// cancel cancels the renewal loop context.
	cancel context.CancelFunc
	// done is closed when the renewal loop exits.
	done chan struct{}
}

// NewService creates a new public TLS Service.
func NewService(cfg Config, deps ServiceDeps) *Service {
	if deps.Challenges == nil {
		deps.Challenges = NewHTTP01Challenges()
	}
	if reflect.ValueOf(deps.Log).IsZero() {
		deps.Log = zerowrap.Default()
	}
	return &Service{
		cfg:           cfg,
		deps:          deps,
		log:           deps.Log,
		certs:         make(map[string]*out.StoredCertificate),
		lastErr:       make(map[string]string),
		routeErr:      make(map[string]string),
		requiredHosts: make(map[string]struct{}),
	}
}

// Load loads all stored certificates from the store into the internal cache.
// If the store is nil, this is a no-op.
func (s *Service) Load(ctx context.Context) error {
	if s.deps.Store == nil {
		return nil
	}

	stored, err := s.deps.Store.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load stored certificates: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.certs = make(map[string]*out.StoredCertificate, len(stored))
	s.lastErr = make(map[string]string)
	s.requiredHosts = make(map[string]struct{})

	for i := range stored {
		cert := &stored[i]
		if parseErr := populateStoredCertificate(cert); parseErr != nil {
			s.lastErr[cert.ID] = parseErr.Error()
			continue
		}
		s.certs[cert.ID] = cert
		addNamesToRequiredHosts(s.requiredHosts, cert.Names)
		if cert.LastError != "" {
			s.lastErr[cert.ID] = cert.LastError
		}
	}

	return nil
}

// Reconcile ensures all desired certificates are obtained and cached.
// If ACME is disabled, it is a no-op.
func (s *Service) Reconcile(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Reconcile",
	})
	log := zerowrap.FromCtx(ctx)

	if !s.cfg.Enabled {
		log.Debug().Msg("acme disabled, skipping reconcile")
		return nil
	}
	log.Debug().Msg("starting certificate reconciliation")

	if s.deps.Store == nil {
		log.Warn().Msg("certificate store is nil, cannot reconcile")
		return fmt.Errorf("%w: certificate store is nil", domain.ErrCertificateStoreRequired)
	}
	if s.deps.Issuer == nil {
		log.Warn().Msg("certificate issuer is nil, cannot reconcile")
		return fmt.Errorf("%w: certificate issuer is nil", domain.ErrCertificateIssuerRequired)
	}
	if s.deps.Routes == nil {
		log.Warn().Msg("route source is nil, cannot reconcile")
		return fmt.Errorf("%w: route source is nil", domain.ErrRouteSourceRequired)
	}

	// Determine effective challenge mode.
	effective := s.deps.Effective
	if effective.Mode == "" {
		resolved, err := ResolveEffectiveChallenge(ctx, s.cfg, nil)
		if err != nil {
			return fmt.Errorf("resolve effective challenge: %w", err)
		}
		effective = resolved
	}

	// Get route hosts early to build required hosts set before target derivation.
	// This ensures GetCertificate returns ErrTLSRouteNotCovered even if
	// DeriveCertificateTargets fails (e.g. broken DNS-01 zone resolver).
	routes := s.deps.Routes.GetRoutes(ctx)
	external := s.deps.Routes.GetExternalRoutes()
	hosts := routeHosts(routes, external)

	// Build required hosts set from route hosts (before target derivation).
	required := canonicalHostSet(hosts)

	// Set requiredHosts before DeriveCertificateTargets so GetCertificate
	// returns ErrTLSRouteNotCovered if target derivation/issuer fails.
	s.mu.Lock()
	s.requiredHosts = required
	clearRouteErrorsLocked(s.routeErr, required)
	s.mu.Unlock()

	// Derive desired targets.
	targets, err := DeriveCertificateTargets(ctx, effective.Mode,
		routes, external,
		s.deps.ZoneResolver,
	)
	if err != nil {
		s.mu.Lock()
		setRouteErrorsLocked(s.routeErr, required, fmt.Sprintf("derive certificate targets: %v", err))
		s.mu.Unlock()
		return fmt.Errorf("derive certificate targets: %w", err)
	}

	// Merge target names into required hosts set.
	// For DNS-01 wildcard targets, this adds wildcard entries as well;
	// the exact route hosts already present suffice for isRequiredHostLocked.
	addTargetNames(required, targets)

	// Under mu: update requiredHosts (with target names) and compute missing targets.
	s.mu.Lock()
	s.requiredHosts = required

	missing := make([]CertificateTarget, 0)
	for _, target := range targets {
		if s.certificateExistsLocked(target) {
			continue
		}
		missing = append(missing, target)
	}
	s.mu.Unlock()

	// Process missing targets without holding s.mu or the store lock so
	// GetCertificate/Status are not blocked during network I/O (Obtain).
	pendingCount := len(missing)
	missing = limitMissingTargets(missing, s.cfg.ObtainBatchSize)
	if remaining := pendingCount - len(missing); remaining > 0 {
		log.Info().
			Int("obtain_batch_size", len(missing)).
			Int("remaining_count", remaining).
			Msg("ACME obtain batch limit reached; remaining certificates will be reconciled later")
	}
	s.reconcileMissingTargets(ctx, missing, required)

	log.Debug().Int("missing_count", len(missing)).Int("pending_count", pendingCount).Msg("reconciled missing targets")
	return nil
}

func limitMissingTargets(targets []CertificateTarget, batchSize int) []CertificateTarget {
	if batchSize <= 0 {
		batchSize = defaultObtainBatchSize
	}
	if len(targets) <= batchSize {
		return targets
	}
	return targets[:batchSize]
}

func populateStoredCertificate(cert *out.StoredCertificate) error {
	if cert == nil {
		return fmt.Errorf("stored certificate is nil")
	}
	if len(cert.Certificate.Certificate) == 0 && len(cert.FullchainPEM) > 0 && len(cert.PrivateKeyPEM) > 0 {
		parsed, err := tls.X509KeyPair(cert.FullchainPEM, cert.PrivateKeyPEM)
		if err != nil {
			return fmt.Errorf("parse stored certificate: %w", err)
		}
		cert.Certificate = parsed
	}
	if len(cert.Certificate.Certificate) == 0 {
		return fmt.Errorf("stored certificate is empty")
	}
	return nil
}

func canonicalHostSet(hosts []string) map[string]struct{} {
	required := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
		if host != "" {
			required[host] = struct{}{}
		}
	}
	return required
}

func addNamesToRequiredHosts(required map[string]struct{}, names []string) {
	for _, name := range names {
		name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
		if name != "" {
			required[name] = struct{}{}
		}
	}
}

func addTargetNames(required map[string]struct{}, targets []CertificateTarget) {
	for _, t := range targets {
		addNamesToRequiredHosts(required, t.Names)
	}
}

func clearRouteErrorsLocked(routeErr map[string]string, hosts map[string]struct{}) {
	for host := range hosts {
		delete(routeErr, host)
	}
}

func setRouteErrorsLocked(routeErr map[string]string, hosts map[string]struct{}, message string) {
	for host := range hosts {
		routeErr[host] = message
	}
}

func setTargetRouteErrorLocked(routeErr map[string]string, required map[string]struct{}, target CertificateTarget, message string) {
	for host := range required {
		if hostMatchesCert(target.Names, host) {
			routeErr[host] = message
		}
	}
}

func clearTargetRouteErrorLocked(routeErr map[string]string, required map[string]struct{}, target CertificateTarget) {
	for host := range required {
		if hostMatchesCert(target.Names, host) {
			delete(routeErr, host)
		}
	}
}

// reconcileMissingTargets obtains and saves certificates for each missing
// target. Called without s.mu held so network I/O does not block readers.
func (s *Service) reconcileMissingTargets(ctx context.Context, missing []CertificateTarget, required map[string]struct{}) {
	log := zerowrap.FromCtx(ctx)
	for _, target := range missing {
		order := out.CertificateOrder{
			ID:        target.ID,
			Names:     target.Names,
			Challenge: target.Challenge,
		}

		stored, err := s.deps.Issuer.Obtain(ctx, order)
		if err != nil {
			errorMessage := fmt.Sprintf("obtain certificate: %v", err)
			s.mu.Lock()
			s.lastErr[target.ID] = errorMessage
			setTargetRouteErrorLocked(s.routeErr, required, target, errorMessage)
			s.mu.Unlock()
			continue
		}
		if stored == nil {
			errorMessage := "obtain certificate returned nil"
			s.mu.Lock()
			s.lastErr[target.ID] = errorMessage
			setTargetRouteErrorLocked(s.routeErr, required, target, errorMessage)
			s.mu.Unlock()
			continue
		}
		if err := populateStoredCertificate(stored); err != nil {
			errorMessage := err.Error()
			s.mu.Lock()
			s.lastErr[target.ID] = errorMessage
			setTargetRouteErrorLocked(s.routeErr, required, target, errorMessage)
			s.mu.Unlock()
			continue
		}

		unlock, err := s.deps.Store.Lock(ctx)
		if err != nil {
			errorMessage := fmt.Sprintf("acquire store lock: %v", err)
			s.mu.Lock()
			s.lastErr[target.ID] = errorMessage
			setTargetRouteErrorLocked(s.routeErr, required, target, errorMessage)
			s.mu.Unlock()
			continue
		}
		saveErr := s.deps.Store.Save(ctx, *stored)
		if unlockErr := unlock(); unlockErr != nil {
			log.Warn().Err(unlockErr).Msg("failed to release store lock")
		}
		if saveErr != nil {
			errorMessage := fmt.Sprintf("save certificate: %v", saveErr)
			s.mu.Lock()
			s.lastErr[target.ID] = errorMessage
			setTargetRouteErrorLocked(s.routeErr, required, target, errorMessage)
			s.mu.Unlock()
			continue
		}

		s.mu.Lock()
		s.certs[stored.ID] = stored
		delete(s.lastErr, stored.ID)
		clearTargetRouteErrorLocked(s.routeErr, required, target)
		s.mu.Unlock()
	}
}

// certificateExistsLocked checks if a valid non-expired cached certificate
// covers the target. Must be called with s.mu held.
func (s *Service) certificateExistsLocked(target CertificateTarget) bool {
	now := time.Now()
	for _, cert := range s.certs {
		if cert.NotAfter.IsZero() || now.After(cert.NotAfter) {
			continue // expired or no expiry
		}
		if exactNamesCoverAll(cert.Names, target.Names) {
			return true
		}
	}
	return false
}

// exactNamesCoverAll reports whether names contains every required name exactly, case-insensitively.
func exactNamesCoverAll(names, required []string) bool {
	covered := make(map[string]bool)
	for _, n := range names {
		covered[strings.ToLower(strings.TrimSuffix(n, "."))] = true
	}
	for _, r := range required {
		r = strings.ToLower(strings.TrimSuffix(r, "."))
		if !covered[r] {
			return false
		}
	}
	return true
}

// hostMatchesCert checks if any name in the given list matches host,
// including wildcard matching.
func hostMatchesCert(names []string, host string) bool {
	return domain.CertificateNamesCoverHost(names, host)
}

// GetCertificateForHost returns a TLS certificate for the given SNI host.
//
// If the SNI host does not require ACME coverage, returns nil, nil.
// If ACME is required but no cert covers the host, returns
// ErrTLSRouteNotCovered.
// If a valid cert is found, returns a pointer to it.
func (s *Service) GetCertificateForHost(host string) (*tls.Certificate, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check if this host requires ACME coverage.
	if !s.isRequiredHostLocked(host) {
		return nil, nil
	}

	// Look for a valid non-expired cached certificate covering this host.
	now := time.Now()
	for _, cert := range s.certs {
		if cert.NotAfter.IsZero() || now.After(cert.NotAfter) {
			continue
		}
		if hostMatchesCert(cert.Names, host) {
			return &cert.Certificate, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", domain.ErrTLSRouteNotCovered, host)
}

// isRequiredHostLocked checks if host or any wildcard covering it is in
// the required hosts set. Must be called with s.mu held.
func (s *Service) isRequiredHostLocked(host string) bool {
	if _, ok := s.requiredHosts[host]; ok {
		return true
	}
	// Check wildcard: split at the first dot.
	parts := strings.SplitN(host, ".", 2)
	if len(parts) == 2 {
		if _, ok := s.requiredHosts["*."+parts[1]]; ok {
			return true
		}
	}
	return false
}

// GetHTTP01Challenge delegates to the HTTP-01 challenge store.
func (s *Service) GetHTTP01Challenge(ctx context.Context, token string) (string, bool) {
	return s.deps.Challenges.Get(ctx, token)
}

// getStoredCertificate returns a copy of the stored certificate for the given
// ID, or nil if not found.
func (s *Service) getStoredCertificate(id string) *out.StoredCertificate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cert := s.certs[id]
	if cert == nil {
		return nil
	}
	cpy := *cert
	return &cpy
}

// Stop gracefully stops the service. If a renewal loop is running, it is
// cancelled and Stop waits for it to finish (subject to ctx deadline).
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
