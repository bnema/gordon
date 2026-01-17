package dto

// SecretsListResponse represents a list of secret keys for a domain.
type SecretsListResponse struct {
	Domain string   `json:"domain"`
	Keys   []string `json:"keys"`
}

// SecretsStatusResponse represents a secrets write response.
type SecretsStatusResponse struct {
	Status string `json:"status"`
}
