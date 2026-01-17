// Package dto provides shared data transfer objects for API responses.
package dto

// AttachmentsConfigResponse represents all configured attachments.
type AttachmentsConfigResponse struct {
	Attachments map[string][]string `json:"attachments"`
}

// AttachmentConfigResponse represents attachments for a specific target.
type AttachmentConfigResponse struct {
	Target string   `json:"target"`
	Images []string `json:"images"`
}

// AttachmentAddRequest represents a request to add an attachment.
type AttachmentAddRequest struct {
	Image string `json:"image"`
}

// AttachmentStatusResponse represents attachment operation result.
type AttachmentStatusResponse struct {
	Status string `json:"status"`
}
