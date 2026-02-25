package dto

// TokenResponse represents the response from the token server.
type TokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token,omitempty"` //nolint:gosec // OCI token response field, required by Docker Registry API v2 spec
	ExpiresIn   int    `json:"expires_in,omitempty"`
	IssuedAt    string `json:"issued_at,omitempty"`
}
