package in

import "time"

// CAInfoService exposes read-only information about the internal root CA.
type CAInfoService interface {
	// RootCertificate returns the root CA certificate in PEM format.
	RootCertificate() []byte
	// RootCommonName returns the CN of the root CA certificate.
	RootCommonName() string
	// RootFingerprint returns the SHA-256 fingerprint of the root CA certificate.
	RootFingerprint() string
	// IntermediateExpiresAt returns the intermediate CA certificate expiry.
	IntermediateExpiresAt() time.Time
}
