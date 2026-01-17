package dto

// ProcessLogsResponse represents process log response.
type ProcessLogsResponse struct {
	Lines []string `json:"lines"`
}

// ContainerLogsResponse represents container log response.
type ContainerLogsResponse struct {
	Domain string   `json:"domain"`
	Lines  []string `json:"lines"`
}
