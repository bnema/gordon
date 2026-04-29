package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type ACMEChallengeMode string

const (
	ACMEChallengeAuto            ACMEChallengeMode = "auto"
	ACMEChallengeHTTP01          ACMEChallengeMode = "http-01"
	ACMEChallengeCloudflareDNS01 ACMEChallengeMode = "cloudflare-dns-01"
)

var (
	ErrACMEDisabled              = errors.New("acme disabled")
	ErrACMEEmailRequired         = errors.New("acme email required")
	ErrACMEChallengeInvalid      = errors.New("acme challenge invalid")
	ErrCloudflareTokenMissing    = errors.New("cloudflare api token missing")
	ErrCertificateStoreRequired  = errors.New("certificate store required")
	ErrHTTPChallengeSinkRequired = errors.New("http challenge sink required")
	ErrTLSRouteNotCovered        = errors.New("tls route not covered by public certificate")
)

func ParseACMEChallengeMode(value string) (ACMEChallengeMode, error) {
	switch strings.TrimSpace(value) {
	case "", string(ACMEChallengeAuto):
		return ACMEChallengeAuto, nil
	case string(ACMEChallengeHTTP01):
		return ACMEChallengeHTTP01, nil
	case string(ACMEChallengeCloudflareDNS01):
		return ACMEChallengeCloudflareDNS01, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrACMEChallengeInvalid, value)
	}
}

type ACMETokenSource string

const (
	ACMETokenSourceNone ACMETokenSource = "none"
	ACMETokenSourcePass ACMETokenSource = "pass"
	ACMETokenSourceFile ACMETokenSource = "file"
	ACMETokenSourceEnv  ACMETokenSource = "env"
)

type TLSCertificateStatus string

const (
	TLSCertificateStatusMissing TLSCertificateStatus = "missing"
	TLSCertificateStatusValid   TLSCertificateStatus = "valid"
	TLSCertificateStatusWarning TLSCertificateStatus = "warning"
	TLSCertificateStatusExpired TLSCertificateStatus = "expired"
	TLSCertificateStatusError   TLSCertificateStatus = "error"
)

type ManagedCertificate struct {
	ID             string
	Names          []string
	Challenge      ACMEChallengeMode
	Status         TLSCertificateStatus
	NotAfter       time.Time
	LastError      string
	RenewalPending bool
}

func (c ManagedCertificate) Covers(host string) bool {
	return CertificateNamesCoverHost(c.Names, host)
}

// CertificateNamesCoverHost reports whether any certificate name covers host.
func CertificateNamesCoverHost(names []string, host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	for _, name := range names {
		name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
		if name == host {
			return true
		}
		if suffix, ok := strings.CutPrefix(name, "*."); ok && strings.HasSuffix(host, "."+suffix) {
			left := strings.TrimSuffix(host, "."+suffix)
			if left != "" && !strings.Contains(left, ".") {
				return true
			}
		}
	}
	return false
}

func (c ManagedCertificate) Health(now time.Time) TLSCertificateStatus {
	if c.LastError != "" {
		return TLSCertificateStatusError
	}
	if c.NotAfter.IsZero() {
		return TLSCertificateStatusMissing
	}
	if !now.Before(c.NotAfter) {
		return TLSCertificateStatusExpired
	}
	if !now.Add(30 * 24 * time.Hour).Before(c.NotAfter) {
		return TLSCertificateStatusWarning
	}
	return TLSCertificateStatusValid
}

type TLSRouteCoverage struct {
	Domain       string
	Covered      bool
	CoveredBy    string
	RequiredACME bool
	Error        string
}

type PublicTLSStatus struct {
	ACMEEnabled     bool
	ConfiguredMode  ACMEChallengeMode
	EffectiveMode   ACMEChallengeMode
	SelectionReason string
	TokenSource     ACMETokenSource
	Certificates    []ManagedCertificate
	Routes          []TLSRouteCoverage
	Errors          []string
}
