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
	valid := ManagedCertificate{NotAfter: time.Now().Add(60 * 24 * time.Hour)}
	expired := ManagedCertificate{NotAfter: time.Now().Add(-time.Hour)}

	assert.Equal(t, TLSCertificateStatusValid, valid.Health(time.Now()))
	assert.Equal(t, TLSCertificateStatusExpired, expired.Health(time.Now()))
}
