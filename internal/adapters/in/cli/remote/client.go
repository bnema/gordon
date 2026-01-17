// Package remote provides an HTTP client for connecting to remote Gordon instances.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gordon/internal/domain"
)

// Client is an HTTP client for the Gordon admin API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// ClientOption configures the Client.
type ClientOption func(*Client)

// NewClient creates a new remote Gordon client.
func NewClient(baseURL string, opts ...ClientOption) *Client {
	// Normalize base URL
	baseURL = strings.TrimSuffix(baseURL, "/")

	c := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// WithToken sets the authentication token.
func WithToken(token string) ClientOption {
	return func(c *Client) {
		c.token = token
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = client
	}
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.httpClient.Timeout = timeout
	}
}

// request performs an HTTP request to the admin API.
func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	url := c.baseURL + "/admin" + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	return c.httpClient.Do(req)
}

// parseResponse parses a JSON response into the given target.
func parseResponse(resp *http.Response, target any) error {
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return fmt.Errorf("%s: %s", resp.Status, errResp.Error)
		}
		return fmt.Errorf("%s: %s", resp.Status, string(body))
	}

	if target != nil {
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

// Routes API

// ListRoutes returns all configured routes.
func (c *Client) ListRoutes(ctx context.Context) ([]domain.Route, error) {
	resp, err := c.request(ctx, http.MethodGet, "/routes", nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Routes []domain.Route `json:"routes"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Routes, nil
}

// GetRoute returns a specific route by domain.
func (c *Client) GetRoute(ctx context.Context, routeDomain string) (*domain.Route, error) {
	resp, err := c.request(ctx, http.MethodGet, "/routes/"+routeDomain, nil)
	if err != nil {
		return nil, err
	}

	var route domain.Route
	if err := parseResponse(resp, &route); err != nil {
		return nil, err
	}

	return &route, nil
}

// AddRoute adds a new route.
func (c *Client) AddRoute(ctx context.Context, route domain.Route) error {
	resp, err := c.request(ctx, http.MethodPost, "/routes", route)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// UpdateRoute updates an existing route.
func (c *Client) UpdateRoute(ctx context.Context, route domain.Route) error {
	resp, err := c.request(ctx, http.MethodPut, "/routes/"+route.Domain, route)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// RemoveRoute removes a route by domain.
func (c *Client) RemoveRoute(ctx context.Context, routeDomain string) error {
	resp, err := c.request(ctx, http.MethodDelete, "/routes/"+routeDomain, nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// Secrets API

// ListSecrets returns the list of secret keys for a domain.
func (c *Client) ListSecrets(ctx context.Context, secretDomain string) ([]string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/secrets/"+secretDomain, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Domain string   `json:"domain"`
		Keys   []string `json:"keys"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Keys, nil
}

// SetSecrets sets secrets for a domain.
func (c *Client) SetSecrets(ctx context.Context, secretDomain string, secrets map[string]string) error {
	resp, err := c.request(ctx, http.MethodPost, "/secrets/"+secretDomain, secrets)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// DeleteSecret removes a secret from a domain.
func (c *Client) DeleteSecret(ctx context.Context, secretDomain, key string) error {
	resp, err := c.request(ctx, http.MethodDelete, "/secrets/"+secretDomain+"/"+key, nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// Status API

// Status represents the Gordon server status.
type Status struct {
	Routes           int               `json:"routes"`
	RegistryDomain   string            `json:"registry_domain"`
	RegistryPort     int               `json:"registry_port"`
	ServerPort       int               `json:"server_port"`
	AutoRoute        bool              `json:"auto_route"`
	NetworkIsolation bool              `json:"network_isolation"`
	ContainerStatus  map[string]string `json:"container_status"`
}

// GetStatus returns the Gordon server status.
func (c *Client) GetStatus(ctx context.Context) (*Status, error) {
	resp, err := c.request(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		return nil, err
	}

	var status Status
	if err := parseResponse(resp, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

// RouteHealth represents the health status of a route.
type RouteHealth struct {
	ContainerStatus string `json:"container_status"`
	HTTPStatus      int    `json:"http_status"`
	ResponseTimeMs  int64  `json:"response_time_ms"`
	Healthy         bool   `json:"healthy"`
	Error           string `json:"error"`
}

// GetHealth returns health status for all routes with HTTP probing.
func (c *Client) GetHealth(ctx context.Context) (map[string]*RouteHealth, error) {
	resp, err := c.request(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Health map[string]*RouteHealth `json:"health"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Health, nil
}

// Reload triggers a configuration reload.
func (c *Client) Reload(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodPost, "/reload", nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// Config API

// Config represents the Gordon configuration.
type Config struct {
	Server struct {
		Port           int    `json:"port"`
		RegistryPort   int    `json:"registry_port"`
		RegistryDomain string `json:"registry_domain"`
		DataDir        string `json:"data_dir"`
	} `json:"server"`
	AutoRoute struct {
		Enabled bool `json:"enabled"`
	} `json:"auto_route"`
	NetworkIsolation struct {
		Enabled bool   `json:"enabled"`
		Prefix  string `json:"prefix"`
	} `json:"network_isolation"`
	Routes         []domain.Route    `json:"routes"`
	ExternalRoutes map[string]string `json:"external_routes"`
}

// GetConfig returns the Gordon configuration.
func (c *Client) GetConfig(ctx context.Context) (*Config, error) {
	resp, err := c.request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := parseResponse(resp, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// Ping checks if the remote Gordon instance is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.GetStatus(ctx)
	return err
}
