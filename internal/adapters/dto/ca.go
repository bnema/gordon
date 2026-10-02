package dto

import "time"

// CAResponse describes the internal root CA for the admin API.
type CAResponse struct {
	RootCN             string    `json:"root_cn"`
	Fingerprint        string    `json:"fingerprint"`
	IntermediateExpiry time.Time `json:"intermediate_expiry"`
	RootPEM            string    `json:"root_pem"`
}
