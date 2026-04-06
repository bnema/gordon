package dto

// DeployErrorResponse represents a structured deployment failure response.
type DeployErrorResponse struct {
	Error string   `json:"error"`
	Cause string   `json:"cause,omitempty"`
	Hint  string   `json:"hint,omitempty"`
	Logs  []string `json:"logs,omitempty"`
}
