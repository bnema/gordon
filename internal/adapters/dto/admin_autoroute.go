package dto

// AutoRouteAllowedDomainsResponse represents the list of allowed domains.
type AutoRouteAllowedDomainsResponse struct {
	Domains []string `json:"domains"`
}

// AutoRouteAllowedDomainRequest represents a request to add/remove a domain pattern.
type AutoRouteAllowedDomainRequest struct {
	Pattern string `json:"pattern"`
}

// AutoRouteStatusResponse represents a status response for auto-route operations.
type AutoRouteStatusResponse struct {
	Status string `json:"status"`
}
