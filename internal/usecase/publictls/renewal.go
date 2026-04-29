package publictls

import (
	"context"
	"fmt"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// renewalWindow is the window before certificate expiry when renewal should begin.
const renewalWindow = 30 * 24 * time.Hour

// ShouldRenew reports whether a certificate should be renewed at the given time.
// A certificate with a zero NotAfter is always considered due for renewal.
func ShouldRenew(cert out.StoredCertificate, now time.Time) bool {
	if cert.NotAfter.IsZero() {
		return true
	}
	return !now.Before(cert.NotAfter.Add(-renewalWindow))
}

// StartRenewalLoop starts a background goroutine that periodically reconciles
// certificates and renews due certificates. Returns a channel that is closed
// when the loop exits (ctx is cancelled).
//
// If interval <= 0, a default of 1 hour is used.
func (s *Service) StartRenewalLoop(ctx context.Context, interval time.Duration) <-chan struct{} {
	if interval <= 0 {
		interval = time.Hour
	}

	done := make(chan struct{})

	// Derive a cancellable context so Stop can terminate the loop.
	loopCtx, cancel := context.WithCancel(ctx)
	loopCtx = zerowrap.CtxWithFields(loopCtx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "RenewDueCertificates",
	})

	// Atomically swap loop state under lock before cancelling the old loop,
	// preventing overlapping loop goroutines and race conditions.
	s.mu.Lock()
	oldCancel := s.cancel
	oldDone := s.done
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	if oldDone != nil {
		<-oldDone
	}

	go func() {
		defer close(done)
		defer cancel()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		log := zerowrap.FromCtx(loopCtx)

		// Startup and reload paths reconcile routes; the periodic loop only renews
		// due certificates to avoid repeated zone lookups and ACME ordering work.
		if err := s.renewDueCertificates(loopCtx, time.Now()); err != nil {
			log.Warn().Err(err).Msg("failed to renew due public TLS certificates")
		}

		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if err := s.renewDueCertificates(loopCtx, time.Now()); err != nil {
					log.Warn().Err(err).Msg("failed to renew due public TLS certificates")
				}
			}
		}
	}()

	return done
}

// renewDueCertificates renews all certificates in the cache that are due
// for renewal according to ShouldRenew.
//
// Per-certificate errors are recorded in lastErr and do not halt the loop.
// Returns nil unless the store or issuer is nil (systemic error).
func (s *Service) renewDueCertificates(ctx context.Context, now time.Time) error {
	log := zerowrap.FromCtx(ctx)
	if !s.cfg.Enabled {
		return nil
	}
	if s.deps.Store == nil {
		return fmt.Errorf("%w: certificate store is nil", domain.ErrCertificateStoreRequired)
	}
	if s.deps.Issuer == nil {
		return fmt.Errorf("%w: certificate issuer is nil", domain.ErrCertificateIssuerRequired)
	}

	// Collect due certs from in-memory cache under s.mu (no store lock needed).
	s.mu.Lock()
	due := make([]out.StoredCertificate, 0, len(s.certs))
	for _, cert := range s.certs {
		if ShouldRenew(*cert, now) {
			due = append(due, *cert)
		}
	}
	s.mu.Unlock()

	for _, cert := range due {
		renewed, err := s.deps.Issuer.Renew(ctx, cert)
		if err != nil {
			s.mu.Lock()
			s.lastErr[cert.ID] = fmt.Sprintf("renew: %v", err)
			s.mu.Unlock()
			continue
		}

		if renewed == nil {
			s.mu.Lock()
			s.lastErr[cert.ID] = "renew returned nil certificate"
			s.mu.Unlock()
			continue
		}
		if err := populateStoredCertificate(renewed); err != nil {
			s.mu.Lock()
			s.lastErr[cert.ID] = err.Error()
			s.mu.Unlock()
			continue
		}
		renewed.LastError = ""

		// Acquire store lock only around Save to avoid holding it across
		// the potentially long-running Issuer.Renew call.
		unlock, err := s.deps.Store.Lock(ctx)
		if err != nil {
			s.mu.Lock()
			s.lastErr[cert.ID] = fmt.Sprintf("acquire store lock: %v", err)
			s.mu.Unlock()
			continue
		}

		saveErr := s.deps.Store.Save(ctx, *renewed)
		if unlockErr := unlock(); unlockErr != nil {
			log.Warn().Err(unlockErr).Msg("failed to release store lock")
		}

		if saveErr != nil {
			s.mu.Lock()
			s.lastErr[cert.ID] = fmt.Sprintf("save renewed certificate: %v", saveErr)
			s.mu.Unlock()
			continue
		}

		s.mu.Lock()
		s.certs[renewed.ID] = renewed
		delete(s.lastErr, renewed.ID)
		s.mu.Unlock()
	}

	return nil
}
