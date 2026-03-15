package dto

type AutoRouteAllowedDomainsResponse struct {
	Domains []string `json:"domains"`
}

type AutoRouteAllowedDomainRequest struct {
	Pattern string `json:"pattern"`
}
