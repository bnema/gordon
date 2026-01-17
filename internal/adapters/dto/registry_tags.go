package dto

// TagListResponse represents registry tag list response.
type TagListResponse struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}
