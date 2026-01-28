package dto

// AuthVerifyResponse represents response from /admin/auth/verify.
type AuthVerifyResponse struct {
	Valid     bool     `json:"valid"`
	Subject   string   `json:"subject,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	ExpiresAt int64    `json:"expires_at,omitempty"`
	IssuedAt  int64    `json:"issued_at,omitempty"`
}
