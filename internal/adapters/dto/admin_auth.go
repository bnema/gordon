package dto

// AuthVerifyResponse represents response from /admin/auth/verify.
type AuthVerifyResponse struct {
	Version   string   `json:"version,omitempty"`
	Commit    string   `json:"commit,omitempty"`
	BuildDate string   `json:"build_date,omitempty"`
	Dirty     string   `json:"dirty,omitempty"`
	Valid     bool     `json:"valid"`
	Subject   string   `json:"subject,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	ExpiresAt int64    `json:"expires_at,omitempty"`
	IssuedAt  int64    `json:"issued_at,omitempty"`
	// RegistryDomain is the Gordon registry host that push clients target.
	RegistryDomain string `json:"registry_domain,omitempty"`
}
