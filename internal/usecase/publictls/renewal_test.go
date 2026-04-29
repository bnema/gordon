package publictls

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// ---------------------------------------------------------------------------
// ShouldRenew
// ---------------------------------------------------------------------------

func TestShouldRenew(t *testing.T) {
	// Fixed "now" for deterministic tests.
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		cert out.StoredCertificate
		want bool
	}{
		{
			name: "cert with NotAfter 45 days from now should not renew",
			cert: out.StoredCertificate{NotAfter: now.Add(45 * 24 * time.Hour)},
			want: false,
		},
		{
			name: "cert with NotAfter 10 days from now should renew",
			cert: out.StoredCertificate{NotAfter: now.Add(10 * 24 * time.Hour)},
			want: true,
		},
		{
			name: "expired cert should renew",
			cert: out.StoredCertificate{NotAfter: now.Add(-1 * time.Hour)},
			want: true,
		},
		{
			name: "cert with zero NotAfter should renew",
			cert: out.StoredCertificate{},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRenew(tt.cert, now)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRenewDueCertificatesRequiresDependencies(t *testing.T) {
	cfg := Config{Enabled: true}
	svc := NewService(cfg, ServiceDeps{})

	err := svc.renewDueCertificates(context.Background(), time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrCertificateStoreRequired)

	svc.deps.Store, _ = newMockCertificateStore(t)
	err = svc.renewDueCertificates(context.Background(), time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrCertificateIssuerRequired)
}

// ---------------------------------------------------------------------------
// RenewalLoopStops
// ---------------------------------------------------------------------------

func TestRenewalLoopStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "http-01",
		HTTPPort:  8088,
		TLSPort:   8443,
	}

	issuer, _ := newMockPublicCertificateIssuer(t, nil, nil)
	store, _ := newMockCertificateStore(t)

	svc := NewService(cfg, ServiceDeps{
		Config:     cfg,
		Routes:     &fakeRoutes{},
		Issuer:     issuer,
		Store:      store,
		Challenges: NewHTTP01Challenges(),
		Effective: EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeHTTP01,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "http-01 challenge selected",
		},
	})

	done := svc.StartRenewalLoop(ctx, 10*time.Millisecond)
	cancel()

	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond, "renewal loop should stop after context cancel")
}

// ---------------------------------------------------------------------------
// RenewDueCertificates
// ---------------------------------------------------------------------------

func TestRenewDueCertificatesSavesAndUpdatesCache(t *testing.T) {
	ctx := context.Background()

	certID := "http01-app.example.com"
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	notAfterSoon := now.Add(10 * 24 * time.Hour)    // 10 days from now — due for renewal
	notAfterRenewed := now.Add(90 * 24 * time.Hour) // renewed cert

	// Generate a test certificate.
	certPEM, keyPEM, err := generateTestCertPEM([]string{"app.example.com"})
	require.NoError(t, err)
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	store, storeState := newMockCertificateStore(t, out.StoredCertificate{
		ID:            certID,
		Names:         []string{"app.example.com"},
		Challenge:     domain.ACMEChallengeHTTP01,
		Certificate:   tlsCert,
		FullchainPEM:  certPEM,
		PrivateKeyPEM: keyPEM,
		NotAfter:      notAfterSoon,
	})

	issuer, _ := newMockPublicCertificateIssuer(t, nil, func(_ context.Context, cert out.StoredCertificate) (*out.StoredCertificate, error) {
		renewed := cert
		renewed.NotAfter = notAfterRenewed
		return &renewed, nil
	})

	cfg := Config{
		Enabled:   true,
		Email:     "admin@example.com",
		Challenge: "http-01",
		HTTPPort:  8088,
		TLSPort:   8443,
	}

	svc := NewService(cfg, ServiceDeps{
		Config:     cfg,
		Routes:     &fakeRoutes{},
		Issuer:     issuer,
		Store:      store,
		Challenges: NewHTTP01Challenges(),
		Effective: EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeHTTP01,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "http-01 challenge selected",
		},
	})

	// Load certs into the cache from the store.
	err = svc.Load(ctx)
	require.NoError(t, err)

	// Verify cert is in cache with original NotAfter via Status.
	status := svc.Status(ctx)
	require.Len(t, status.Certificates, 1, "expected 1 managed certificate after Load")
	assert.True(t, status.Certificates[0].NotAfter.Equal(notAfterSoon),
		"cache should have original NotAfter (got %v, want %v)",
		status.Certificates[0].NotAfter, notAfterSoon)

	// Run renewDueCertificates.
	err = svc.renewDueCertificates(ctx, now)
	require.NoError(t, err)

	// Verify the store has the updated certificate.
	all := storeState.All()
	require.Len(t, all, 1)
	assert.True(t, all[0].NotAfter.Equal(notAfterRenewed), "store should have renewed NotAfter")

	// Verify the cache has the updated certificate via Status.
	status = svc.Status(ctx)
	require.Len(t, status.Certificates, 1, "expected 1 managed certificate after renewal")
	assert.True(t, status.Certificates[0].NotAfter.Equal(notAfterRenewed),
		"cache should have renewed NotAfter (got %v, want %v)",
		status.Certificates[0].NotAfter, notAfterRenewed)

	// Verify lastErr is empty for the renewed certificate.
	assert.Empty(t, status.Certificates[0].LastError,
		"LastError should be empty for renewed cert")
}
