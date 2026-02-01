package dto

// RepositoryTagsResponse represents a repository tags listing response.
type RepositoryTagsResponse struct {
	Repository string   `json:"repository"`
	Tags       []string `json:"tags"`
}
