package dto

// RegistryErrorItem represents an individual registry error.
type RegistryErrorItem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RegistryErrorResponse represents registry errors.
type RegistryErrorResponse struct {
	Errors []RegistryErrorItem `json:"errors"`
}
