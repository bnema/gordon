package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACMEChallengeModeParse(t *testing.T) {
	tests := []struct {
		input string
		want  ACMEChallengeMode
		ok    bool
	}{
		{"", ACMEChallengeAuto, true},
		{"auto", ACMEChallengeAuto, true},
		{"http-01", ACMEChallengeHTTP01, true},
		{"cloudflare-dns-01", ACMEChallengeCloudflareDNS01, true},
		{"dns", "", false},
	}

	for _, tt := range tests {
		got, err := ParseACMEChallengeMode(tt.input)
		if tt.ok {
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		} else {
			require.Error(t, err)
		}
	}
}

func TestManagedCertificateCovers(t *testing.T) {
	apexCert := ManagedCertificate{Names: []string{"example.com", "*.example.com"}}
	prodCert := ManagedCertificate{Names: []string{"prod.example.com", "*.prod.example.com"}}

	assert.True(t, apexCert.Covers("example.com"))
	assert.True(t, apexCert.Covers("app.example.com"))
	assert.False(t, apexCert.Covers("api.prod.example.com"), "*.example.com must not cover two labels")
	assert.True(t, prodCert.Covers("api.prod.example.com"))
	assert.False(t, apexCert.Covers("other.test"))
}

func TestManagedCertificateHealth(t *testing.T) {
	// Fixed reference time for deterministic tests.
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		cert ManagedCertificate
		want TLSCertificateStatus
	}{
		{
			name: "valid certificate far from expiry",
			cert: ManagedCertificate{NotAfter: now.Add(60 * 24 * time.Hour)},
			want: TLSCertificateStatusValid,
		},
		{
			name: "expired certificate",
			cert: ManagedCertificate{NotAfter: now.Add(-time.Hour)},
			want: TLSCertificateStatusExpired,
		},
		{
			name: "warning when less than 30 days remain",
			cert: ManagedCertificate{NotAfter: now.Add(15 * 24 * time.Hour)},
			want: TLSCertificateStatusWarning,
		},
		{
			name: "warning exactly at 30 day boundary in the past",
			cert: ManagedCertificate{NotAfter: now.Add(29 * 24 * time.Hour)},
			want: TLSCertificateStatusWarning,
		},
		{
			name: "missing when NotAfter is zero",
			cert: ManagedCertificate{},
			want: TLSCertificateStatusMissing,
		},
		{
			name: "error when LastError is set",
			cert: ManagedCertificate{
				NotAfter:  now.Add(60 * 24 * time.Hour),
				LastError: "issuer unavailable",
			},
			want: TLSCertificateStatusError,
		},
		{
			name: "error takes priority over expired",
			cert: ManagedCertificate{
				NotAfter:  now.Add(-time.Hour),
				LastError: "something went wrong",
			},
			want: TLSCertificateStatusError,
		},
		{
			name: "error takes priority over zero NotAfter",
			cert: ManagedCertificate{
				LastError: "something went wrong",
			},
			want: TLSCertificateStatusError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cert.Health(now)
			assert.Equal(t, tt.want, got)
		})
	}
}
