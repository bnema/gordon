package dto

// BootstrapRequest represents a bootstrap operation request.
type BootstrapRequest struct {
	Domain        string                       `json:"domain"`
	Image         string                       `json:"image"`
	Attachments   []string                     `json:"attachments,omitempty"`
	Env           map[string]string            `json:"env,omitempty"`
	AttachmentEnv map[string]map[string]string `json:"attachment_env,omitempty"`
}

// BootstrapStep represents the result of one bootstrap step.
type BootstrapStep struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "created", "updated", "noop", "failed"
}

// BootstrapResponse represents the result of a bootstrap operation.
type BootstrapResponse struct {
	Domain   string          `json:"domain"`
	Image    string          `json:"image"`
	Steps    []BootstrapStep `json:"steps"`
	Warnings []string        `json:"warnings,omitempty"`
	Next     string          `json:"next"`
}
