package dto

import (
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// TLSStatusResponse represents the public TLS/ACME status for the admin API.
type TLSStatusResponse struct {
	ACMEEnabled     bool                  `json:"acme_enabled"`
	ConfiguredMode  string                `json:"configured_mode"`
	EffectiveMode   string                `json:"effective_mode"`
	SelectionReason string                `json:"selection_reason"`
	TokenSource     string                `json:"token_source"`
	Certificates    []TLSCertificateEntry `json:"certificates"`
	Routes          []TLSRouteCoverage    `json:"routes"`
	Errors          []string              `json:"errors,omitempty"`
}

// TLSCertificateEntry represents a managed certificate entry in the TLS status.
type TLSCertificateEntry struct {
	ID             string    `json:"id"`
	Names          []string  `json:"names"`
	Challenge      string    `json:"challenge"`
	Status         string    `json:"status"`
	NotAfter       time.Time `json:"not_after"`
	LastError      string    `json:"last_error,omitempty"`
	RenewalPending bool      `json:"renewal_pending"`
}

// TLSRouteCoverage represents a route's TLS coverage status.
type TLSRouteCoverage struct {
	Domain       string `json:"domain"`
	Covered      bool   `json:"covered"`
	CoveredBy    string `json:"covered_by,omitempty"`
	RequiredACME bool   `json:"required_acme"`
	Error        string `json:"error,omitempty"`
}

// TLSStatusFromDomain converts a public TLS domain status to its transport DTO.
func TLSStatusFromDomain(s domain.PublicTLSStatus) TLSStatusResponse {
	certs := make([]TLSCertificateEntry, 0, len(s.Certificates))
	for _, c := range s.Certificates {
		certs = append(certs, TLSCertificateEntry{
			ID:             c.ID,
			Names:          c.Names,
			Challenge:      string(c.Challenge),
			Status:         string(c.Status),
			NotAfter:       c.NotAfter,
			LastError:      c.LastError,
			RenewalPending: c.RenewalPending,
		})
	}

	routes := make([]TLSRouteCoverage, 0, len(s.Routes))
	for _, r := range s.Routes {
		routes = append(routes, TLSRouteCoverage{
			Domain:       r.Domain,
			Covered:      r.Covered,
			CoveredBy:    r.CoveredBy,
			RequiredACME: r.RequiredACME,
			Error:        r.Error,
		})
	}

	return TLSStatusResponse{
		ACMEEnabled:     s.ACMEEnabled,
		ConfiguredMode:  string(s.ConfiguredMode),
		EffectiveMode:   string(s.EffectiveMode),
		SelectionReason: s.SelectionReason,
		TokenSource:     string(s.TokenSource),
		Certificates:    certs,
		Routes:          routes,
		Errors:          s.Errors,
	}
}
