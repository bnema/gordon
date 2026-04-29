package dto

import "time"

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
