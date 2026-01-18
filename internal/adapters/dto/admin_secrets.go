package dto

// AttachmentSecretsResponse represents secrets for an attachment container.
type AttachmentSecretsResponse struct {
	Service string   `json:"service"`
	Keys    []string `json:"keys"`
}

// SecretsListResponse represents a list of secret keys for a domain.
type SecretsListResponse struct {
	Domain      string                      `json:"domain"`
	Keys        []string                    `json:"keys"`
	Attachments []AttachmentSecretsResponse `json:"attachments,omitempty"`
}

// SecretsStatusResponse represents a secrets write response.
type SecretsStatusResponse struct {
	Status string `json:"status"`
}
