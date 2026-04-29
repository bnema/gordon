// Package remote provides an HTTP client for connecting to remote Gordon instances.
package remote

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

// Client is an HTTP client for the Gordon admin API.
type Client struct {
	baseURL     string
	token       string
	httpClient  *http.Client
	insecureTLS bool

	// Ephemeral admin token exchange fields.
	mu           sync.Mutex // protects ephemeral and ephemeralExp
	subject      string     // JWT subject extracted from long-lived token
	ephemeral    string     // cached ephemeral admin token
	ephemeralExp time.Time  // expiry of cached ephemeral token
}

var (
	retryMaxAttempts = 4
	retryBaseDelay   = 250 * time.Millisecond
)

const maxErrorBodySize int64 = 1024 // cap error response reads at 1 KB

// ClientOption configures the Client.
type ClientOption func(*Client)

// NewClient creates a new remote Gordon client.
func NewClient(baseURL string, opts ...ClientOption) *Client {
	// Normalize base URL
	baseURL = strings.TrimSuffix(baseURL, "/")

	c := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	c.applyTLSConfig()

	return c
}

// WithToken sets the authentication token.
// The token must be a valid JWT — the subject claim is extracted for
// ephemeral token exchange. If parsing fails, requests will return
// an error rather than sending the long-lived token directly.
func WithToken(token string) ClientOption {
	return func(c *Client) {
		c.token = token
		c.subject = extractJWTSubject(token)
	}
}

// extractJWTSubject parses a JWT without verification and returns the
// "sub" claim.  Returns "" if the token is not a valid JWT or has no sub.
func extractJWTSubject(tokenStr string) string {
	if tokenStr == "" {
		return ""
	}
	parser := jwt.NewParser()
	claims := jwt.MapClaims{}
	// We only need the claims; signature verification happens server-side.
	_, _, err := parser.ParseUnverified(tokenStr, claims)
	if err != nil {
		return ""
	}
	sub, _ := claims.GetSubject()
	return sub
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

// WithInsecureTLS disables TLS certificate verification for remote admin API requests.
func WithInsecureTLS(insecure bool) ClientOption {
	return func(c *Client) {
		c.insecureTLS = insecure
	}
}

// ephemeralMargin is the safety margin before expiry at which
// the cached ephemeral token is considered stale.
const ephemeralMargin = 30 * time.Second

// ephemeralValid reports whether the cached ephemeral token is still usable.
func (c *Client) ephemeralValid() bool {
	return c.ephemeral != "" && time.Now().Before(c.ephemeralExp.Add(-ephemeralMargin))
}

// exchangeToken exchanges the long-lived token for a short-lived ephemeral
// admin token via /auth/token. Follows the same pattern as ExchangeRegistryToken.
// The caller must hold c.mu.
func (c *Client) exchangeToken(ctx context.Context) error {
	url := c.baseURL + "/auth/token?scope=admin:*:*&service=gordon-registry"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("ephemeral token exchange: %w", err)
	}

	req.SetBasicAuth(c.subject, c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ephemeral token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return fmt.Errorf("ephemeral token exchange: %s: %s", resp.Status, errResp.Error)
		}
		return fmt.Errorf("ephemeral token exchange: %s: %s", resp.Status, string(body))
	}

	var result dto.TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("ephemeral token exchange: decode: %w", err)
	}

	token := result.Token
	if token == "" {
		token = result.AccessToken
	}
	if token == "" {
		return fmt.Errorf("ephemeral token exchange returned empty token")
	}

	if result.ExpiresIn <= 0 {
		return fmt.Errorf("ephemeral token exchange: invalid expires_in value: %d", result.ExpiresIn)
	}

	c.ephemeral = token
	c.ephemeralExp = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	return nil
}

// bearerToken returns the token to use in Authorization headers.
// It exchanges the long-lived token for an ephemeral one via /auth/token.
// If subject extraction failed, uses "unknown" — the server validates everything.
func (c *Client) bearerToken(ctx context.Context) (string, error) {
	if c.token == "" {
		return "", nil
	}
	if c.subject == "" {
		c.subject = "unknown"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ephemeralValid() {
		if err := c.exchangeToken(ctx); err != nil {
			return "", err
		}
	}
	return c.ephemeral, nil
}

func (c *Client) applyTLSConfig() {
	if !c.insecureTLS {
		return
	}

	fmt.Fprintf(os.Stderr, "WARNING: TLS certificate verification disabled for %s\n", c.baseURL)

	var transport *http.Transport
	switch t := c.httpClient.Transport.(type) {
	case *http.Transport:
		transport = t.Clone()
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	default:
		// Cannot apply InsecureSkipVerify to a non-*http.Transport.
		// Log a warning and leave the transport intact rather than
		// silently replacing it (which would drop caller-provided behavior).
		fmt.Fprintf(os.Stderr, "WARNING: --insecure requires *http.Transport, got %T; TLS override not applied\n", t)
		return
	}

	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	//nolint:gosec // Explicit CLI opt-in via --insecure for self-signed/private cert deployments.
	transport.TLSClientConfig.InsecureSkipVerify = true
	c.httpClient.Transport = transport
}

// request performs an HTTP request to the admin API.
func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	// Marshal body once so it can be replayed on 401 retry.
	var jsonBody []byte
	if body != nil {
		var err error
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	resp, err := c.doRequest(ctx, method, path, jsonBody)
	if err != nil {
		return nil, err
	}

	// On 401: invalidate ephemeral, re-exchange, retry once.
	if resp.StatusCode == http.StatusUnauthorized && c.subject != "" && c.ephemeral != "" {
		// Drain and close the first response.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodySize))
		resp.Body.Close()

		c.mu.Lock()
		c.ephemeral = ""
		c.ephemeralExp = time.Time{}
		if exErr := c.exchangeToken(ctx); exErr != nil {
			c.mu.Unlock()
			return nil, fmt.Errorf("token re-exchange after 401: %w", exErr)
		}
		c.mu.Unlock()
		return c.doRequest(ctx, method, path, jsonBody)
	}

	return resp, nil
}

// doRequest builds and executes a single HTTP request to the admin API.
func (c *Client) doRequest(ctx context.Context, method, path string, jsonBody []byte) (*http.Response, error) {
	reqURL := c.baseURL + "/admin" + path

	var bodyReader io.Reader
	if jsonBody != nil {
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// parseResponse parses a JSON response into the given target.
func parseResponse(resp *http.Response, target any) error {
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		return parseErrorResponse(resp, body)
	}

	if target != nil {
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

// HTTPError represents an HTTP error response with a status code.
// Use errors.As to check for specific status codes in error handling.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
	Cause      string
	Hint       string
	Logs       []string
	Structured bool
}

func (e *HTTPError) Error() string {
	return e.Status + ": " + e.Body
}

func parseErrorResponse(resp *http.Response, body []byte) error {
	msg := string(body)
	structured := false
	var errResp struct {
		Error string   `json:"error"`
		Cause string   `json:"cause"`
		Hint  string   `json:"hint"`
		Logs  []string `json:"logs"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil {
		structured = errResp.Cause != "" || errResp.Hint != "" || len(errResp.Logs) > 0
		if errResp.Error != "" {
			msg = errResp.Error
		}
	}
	return &HTTPError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       msg,
		Cause:      errResp.Cause,
		Hint:       errResp.Hint,
		Logs:       errResp.Logs,
		Structured: structured,
	}
}

func isRetryableRequestError(err error) bool {
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func isRetryableStatus(status int) bool {
	// Only retry gateway errors; retrying 500 on non-idempotent POSTs
	// (deploy, restart, reload) can cause duplicate state changes.
	return status == 502 || status == 503 || status == 504
}

func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return retryBaseDelay
	}
	return retryBaseDelay * time.Duration(1<<(attempt-1))
}

// requestWithRetry performs an HTTP request and retries transient failures.
// Retries occur on transport errors and gateway errors (502, 503, 504).
func (c *Client) requestWithRetry(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var lastErr error

	for attempt := 1; attempt <= retryMaxAttempts; attempt++ {
		resp, err := c.request(ctx, method, path, body)
		if err != nil {
			lastErr = err
			if attempt == retryMaxAttempts || !isRetryableRequestError(err) {
				return nil, err
			}
		} else {
			if !isRetryableStatus(resp.StatusCode) {
				return resp, nil
			}

			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
			_ = resp.Body.Close()
			lastErr = parseErrorResponse(resp, respBody)
			if attempt == retryMaxAttempts {
				return nil, lastErr
			}
		}

		delay := retryDelay(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("request failed after retries")
}

// Routes API

// Type aliases for API types using shared DTO package.
type RouteInfo = dto.RouteInfo
type Attachment = dto.Attachment

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

// ListRoutesWithDetails returns routes with network and attachment info.
func (c *Client) ListRoutesWithDetails(ctx context.Context) ([]RouteInfo, error) {
	resp, err := c.request(ctx, http.MethodGet, "/routes?detailed=true", nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Routes []RouteInfo `json:"routes"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Routes, nil
}

// ListNetworks returns Gordon-managed networks.
func (c *Client) ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error) {
	resp, err := c.request(ctx, http.MethodGet, "/networks", nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Networks []*domain.NetworkInfo `json:"networks"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Networks, nil
}

// ListAttachments returns attachments for a domain.
func (c *Client) ListAttachments(ctx context.Context, routeDomain string) ([]Attachment, error) {
	if routeDomain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	path := "/routes/" + url.PathEscape(routeDomain) + "/attachments"
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Attachments []Attachment `json:"attachments"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Attachments, nil
}

// GetRoute returns a specific route by domain.
func (c *Client) GetRoute(ctx context.Context, routeDomain string) (*domain.Route, error) {
	resp, err := c.request(ctx, http.MethodGet, "/routes/"+url.PathEscape(routeDomain), nil)
	if err != nil {
		return nil, err
	}

	var route domain.Route
	if err := parseResponse(resp, &route); err != nil {
		return nil, err
	}

	return &route, nil
}

// FindRoutesByImage returns all routes associated with the given image name.
func (c *Client) FindRoutesByImage(ctx context.Context, imageName string) ([]domain.Route, error) {
	resp, err := c.request(ctx, http.MethodGet, "/routes/by-image/"+url.PathEscape(imageName), nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Image  string         `json:"image"`
		Routes []domain.Route `json:"routes"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Routes, nil
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
	resp, err := c.request(ctx, http.MethodPut, "/routes/"+url.PathEscape(route.Domain), route)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// RemoveRoute removes a route by domain.
func (c *Client) RemoveRoute(ctx context.Context, routeDomain string) error {
	resp, err := c.request(ctx, http.MethodDelete, "/routes/"+url.PathEscape(routeDomain), nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

func (c *Client) Bootstrap(ctx context.Context, req dto.BootstrapRequest) (*dto.BootstrapResponse, error) {
	resp, err := c.request(ctx, http.MethodPost, "/bootstrap", req)
	if err != nil {
		return nil, err
	}
	var result dto.BootstrapResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Secrets API

// AttachmentSecrets represents secrets for an attachment container.
type AttachmentSecrets struct {
	Service string   `json:"service"`
	Keys    []string `json:"keys"`
}

// SecretsListResult contains domain secrets and any attachment secrets.
type SecretsListResult struct {
	Domain      string              `json:"domain"`
	Keys        []string            `json:"keys"`
	Attachments []AttachmentSecrets `json:"attachments,omitempty"`
}

// ListSecrets returns the list of secret keys for a domain.
func (c *Client) ListSecrets(ctx context.Context, secretDomain string) ([]string, error) {
	result, err := c.ListSecretsWithAttachments(ctx, secretDomain)
	if err != nil {
		return nil, err
	}
	return result.Keys, nil
}

// ListSecretsWithAttachments returns domain secrets and attachment secrets.
func (c *Client) ListSecretsWithAttachments(ctx context.Context, secretDomain string) (*SecretsListResult, error) {
	resp, err := c.request(ctx, http.MethodGet, "/secrets/"+url.PathEscape(secretDomain), nil)
	if err != nil {
		return nil, err
	}

	var result SecretsListResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// SetSecrets sets secrets for a domain.
func (c *Client) SetSecrets(ctx context.Context, secretDomain string, secrets map[string]string) error {
	resp, err := c.request(ctx, http.MethodPost, "/secrets/"+url.PathEscape(secretDomain), secrets)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// DeleteSecret removes a secret from a domain.
func (c *Client) DeleteSecret(ctx context.Context, secretDomain, key string) error {
	resp, err := c.request(ctx, http.MethodDelete, "/secrets/"+url.PathEscape(secretDomain)+"/"+url.PathEscape(key), nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// SetAttachmentSecrets sets secrets for an attachment container.
func (c *Client) SetAttachmentSecrets(ctx context.Context, domain, service string, secrets map[string]string) error {
	path := "/secrets/" + url.PathEscape(domain) + "/attachments/" + url.PathEscape(service)
	resp, err := c.request(ctx, http.MethodPost, path, secrets)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// DeleteAttachmentSecret removes a secret from an attachment container.
func (c *Client) DeleteAttachmentSecret(ctx context.Context, domain, service, key string) error {
	path := "/secrets/" + url.PathEscape(domain) + "/attachments/" + url.PathEscape(service) + "/" + url.PathEscape(key)
	resp, err := c.request(ctx, http.MethodDelete, path, nil)
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

// GetTLSStatus returns the public TLS/ACME status.
func (c *Client) GetTLSStatus(ctx context.Context) (*dto.TLSStatusResponse, error) {
	resp, err := c.request(ctx, http.MethodGet, "/tls/status", nil)
	if err != nil {
		return nil, err
	}

	var status dto.TLSStatusResponse
	if err := parseResponse(resp, &status); err != nil {
		return nil, err
	}

	return &status, nil
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

// Backups API

// Images API

// ListImages returns runtime images and registry tags from the admin API.
func (c *Client) ListImages(ctx context.Context) ([]dto.Image, error) {
	resp, err := c.request(ctx, http.MethodGet, "/images", nil)
	if err != nil {
		return nil, err
	}

	var result dto.ImagesResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Images, nil
}

// PruneImages prunes runtime and registry images.
func (c *Client) PruneImages(ctx context.Context, req dto.ImagePruneRequest) (*dto.ImagePruneResponse, error) {
	if req.KeepLast != nil && *req.KeepLast < 0 {
		return nil, fmt.Errorf("keep_last must be >= 0")
	}

	resp, err := c.request(ctx, http.MethodPost, "/images/prune", req)
	if err != nil {
		return nil, err
	}

	var result dto.ImagePruneResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// ListBackups returns backups globally or for a domain.
func (c *Client) ListBackups(ctx context.Context, backupDomain string) ([]dto.BackupJob, error) {
	path := "/backups"
	if backupDomain != "" {
		path += "/" + url.PathEscape(backupDomain)
	}

	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var result dto.BackupsResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Backups, nil
}

// BackupStatus returns aggregate backup status.
func (c *Client) BackupStatus(ctx context.Context) ([]dto.BackupJob, error) {
	resp, err := c.request(ctx, http.MethodGet, "/backups/status", nil)
	if err != nil {
		return nil, err
	}

	var result dto.BackupsResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Backups, nil
}

// RunBackup triggers a backup for a domain.
func (c *Client) RunBackup(ctx context.Context, backupDomain, dbName string) (*dto.BackupRunResponse, error) {
	if backupDomain == "" {
		return nil, fmt.Errorf("domain cannot be empty")
	}

	resp, err := c.request(ctx, http.MethodPost, "/backups/"+url.PathEscape(backupDomain), dto.BackupRunRequest{DB: dbName})
	if err != nil {
		return nil, err
	}

	var result dto.BackupRunResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// DetectDatabases detects supported databases for a domain.
func (c *Client) DetectDatabases(ctx context.Context, backupDomain string) ([]dto.DatabaseInfo, error) {
	if backupDomain == "" {
		return nil, fmt.Errorf("domain cannot be empty")
	}

	resp, err := c.request(ctx, http.MethodGet, "/backups/"+url.PathEscape(backupDomain)+"/detect", nil)
	if err != nil {
		return nil, err
	}

	var result dto.BackupDetectResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Databases, nil
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
	resp, err := c.requestWithRetry(ctx, http.MethodPost, "/reload", nil)
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
		DataDir        string `json:"data_dir,omitempty"`
	} `json:"server"`
	AutoRoute struct {
		Enabled bool `json:"enabled"`
	} `json:"auto_route"`
	NetworkIsolation struct {
		Enabled bool   `json:"enabled"`
		Prefix  string `json:"prefix"`
	} `json:"network_isolation"`
	Routes         []domain.Route  `json:"routes"`
	ExternalRoutes []ExternalRoute `json:"external_routes"`
}

// ExternalRoute represents a redacted external route config entry.
type ExternalRoute struct {
	Domain string `json:"domain"`
	Target string `json:"target,omitempty"`
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

// Deploy API

// DeployResult contains the result of a deployment.
type DeployResult struct {
	Status      string `json:"status"`
	ContainerID string `json:"container_id"`
	Domain      string `json:"domain"`
}

// Deploy triggers a deployment for the specified domain.
func (c *Client) Deploy(ctx context.Context, deployDomain string) (*DeployResult, error) {
	resp, err := c.requestWithRetry(ctx, http.MethodPost, "/deploy/"+url.PathEscape(deployDomain), nil)
	if err != nil {
		return nil, err
	}

	var result DeployResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// DeployIntent tells the server that a CLI-managed push is about to happen,
// suppressing event-based deploys for this image.
func (c *Client) DeployIntent(ctx context.Context, imageName string) error {
	imageName = strings.TrimSpace(imageName)
	if imageName == "" {
		return fmt.Errorf("image name cannot be empty")
	}
	resp, err := c.requestWithRetry(ctx, http.MethodPost, "/deploy-intent/"+url.PathEscape(imageName), nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// Restart API

// RestartResult contains the result of a restart.
type RestartResult struct {
	Status string `json:"status"`
	Domain string `json:"domain"`
}

// Restart triggers a container restart for the specified domain.
func (c *Client) Restart(ctx context.Context, restartDomain string, withAttachments bool) (*RestartResult, error) {
	if restartDomain == "" {
		return nil, fmt.Errorf("domain cannot be empty")
	}
	path := "/restart/" + url.PathEscape(restartDomain)
	if withAttachments {
		path += "?attachments=true"
	}
	resp, err := c.requestWithRetry(ctx, http.MethodPost, path, nil)
	if err != nil {
		return nil, err
	}

	var result RestartResult
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// Tags API

// ListTags returns available tags for a repository.
func (c *Client) ListTags(ctx context.Context, repository string) ([]string, error) {
	if repository == "" {
		return nil, fmt.Errorf("repository cannot be empty")
	}
	resp, err := c.request(ctx, http.MethodGet, "/tags/"+url.PathEscape(repository), nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Repository string   `json:"repository"`
		Tags       []string `json:"tags"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return result.Tags, nil
}

// Logs API

// GetProcessLogs returns Gordon process logs.
func (c *Client) GetProcessLogs(ctx context.Context, lines int) ([]string, error) {
	path := fmt.Sprintf("/logs?lines=%d", lines)
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Lines []string `json:"lines"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Lines, nil
}

// GetContainerLogs returns container logs for a specific domain.
func (c *Client) GetContainerLogs(ctx context.Context, logDomain string, lines int) ([]string, error) {
	path := fmt.Sprintf("/logs/%s?lines=%d", url.PathEscape(logDomain), lines)
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Domain string   `json:"domain"`
		Lines  []string `json:"lines"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Lines, nil
}

// StreamProcessLogs returns a channel that streams Gordon process log lines via SSE.
// The caller is responsible for reading from the channel until it's closed.
func (c *Client) StreamProcessLogs(ctx context.Context, lines int) (<-chan string, error) {
	path := fmt.Sprintf("/logs?lines=%d&follow=true", lines)
	return c.streamLogs(ctx, path)
}

// StreamContainerLogs returns a channel that streams container log lines via SSE.
// The caller is responsible for reading from the channel until it's closed.
func (c *Client) StreamContainerLogs(ctx context.Context, logDomain string, lines int) (<-chan string, error) {
	path := fmt.Sprintf("/logs/%s?lines=%d&follow=true", url.PathEscape(logDomain), lines)
	return c.streamLogs(ctx, path)
}

// Attachments Config API

// GetAllAttachmentsConfig returns all configured attachments.
func (c *Client) GetAllAttachmentsConfig(ctx context.Context) (map[string][]string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/attachments", nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Attachments map[string][]string `json:"attachments"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Attachments, nil
}

// GetAttachmentsConfig returns attachments for a specific domain/group from config.
func (c *Client) GetAttachmentsConfig(ctx context.Context, domainOrGroup string) ([]string, error) {
	if domainOrGroup == "" {
		return nil, fmt.Errorf("domain or group is required")
	}
	path := "/attachments/" + url.PathEscape(domainOrGroup)
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Target string   `json:"target"`
		Images []string `json:"images"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Images, nil
}

// FindAttachmentTargetsByImage returns all attachment targets associated with the given image name.
func (c *Client) FindAttachmentTargetsByImage(ctx context.Context, imageName string) ([]string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/attachments/by-image/"+url.PathEscape(imageName), nil)
	if err != nil {
		return nil, err
	}

	var result dto.AttachmentTargetsByImageResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Targets, nil
}

// AddAttachment adds an attachment to a domain/group.
func (c *Client) AddAttachment(ctx context.Context, domainOrGroup, image string) error {
	if domainOrGroup == "" {
		return fmt.Errorf("domain or group is required")
	}
	if image == "" {
		return fmt.Errorf("image is required")
	}
	path := "/attachments/" + url.PathEscape(domainOrGroup)
	resp, err := c.request(ctx, http.MethodPost, path, struct {
		Image string `json:"image"`
	}{Image: image})
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// RemoveAttachment removes an attachment from a domain/group.
func (c *Client) RemoveAttachment(ctx context.Context, domainOrGroup, image string) error {
	if domainOrGroup == "" {
		return fmt.Errorf("domain or group is required")
	}
	if image == "" {
		return fmt.Errorf("image is required")
	}
	path := "/attachments/" + url.PathEscape(domainOrGroup) + "/" + url.PathEscape(image)
	resp, err := c.request(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

func (c *Client) GetAutoRouteAllowedDomains(ctx context.Context) ([]string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/autoroute/allowed-domains", nil)
	if err != nil {
		return nil, err
	}

	var result dto.AutoRouteAllowedDomainsResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Domains, nil
}

func (c *Client) AddAutoRouteAllowedDomain(ctx context.Context, pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("pattern must not be empty")
	}
	resp, err := c.request(ctx, http.MethodPost, "/autoroute/allowed-domains", dto.AutoRouteAllowedDomainRequest{Pattern: pattern})
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

func (c *Client) RemoveAutoRouteAllowedDomain(ctx context.Context, pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("pattern must not be empty")
	}
	resp, err := c.request(ctx, http.MethodDelete, "/autoroute/allowed-domains/"+url.PathEscape(pattern), nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// openSSEStream opens an SSE connection to the given admin path and returns the response.
func (c *Client) openSSEStream(ctx context.Context, path string) (*http.Response, error) {
	streamURL := c.baseURL + "/admin" + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "text/event-stream")
	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	// Use the same transport as the main client (honoring TLS config and
	// custom transports) but without a timeout so streaming doesn't get cut off.
	streamClient := &http.Client{
		Transport: c.httpClient.Transport,
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", resp.Status, string(body))
	}

	return resp, nil
}

// streamLogs handles SSE streaming for log endpoints.
func (c *Client) streamLogs(ctx context.Context, path string) (<-chan string, error) {
	resp, err := c.openSSEStream(ctx, path)
	if err != nil {
		return nil, err
	}

	ch := make(chan string, 100)

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		buf := make([]byte, 4096)
		var lineBuffer strings.Builder

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, err := resp.Body.Read(buf)
			if n > 0 {
				lineBuffer.Write(buf[:n])
			}
			if err != nil {
				return // EOF or context cancellation — clean exit
			}

			// Process complete SSE events
			for {
				data := lineBuffer.String()
				idx := strings.Index(data, "\n\n")
				if idx == -1 {
					break
				}

				event := data[:idx]
				lineBuffer.Reset()
				lineBuffer.WriteString(data[idx+2:])

				// Parse SSE data lines
				for _, line := range strings.Split(event, "\n") {
					if strings.HasPrefix(line, "data: ") {
						logLine := strings.TrimPrefix(line, "data: ")
						select {
						case ch <- logLine:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}
	}()

	return ch, nil
}

// VerifyAuth checks if authentication session is valid.
func (c *Client) VerifyAuth(ctx context.Context) (*dto.AuthVerifyResponse, error) {
	resp, err := c.request(ctx, http.MethodGet, "/auth/verify", nil)
	if err != nil {
		return nil, err
	}

	var result dto.AuthVerifyResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// ListVolumes lists all volumes via the admin API.
func (c *Client) ListVolumes(ctx context.Context) ([]dto.Volume, error) {
	resp, err := c.request(ctx, http.MethodGet, "/volumes", nil)
	if err != nil {
		return nil, err
	}

	var volumes []dto.Volume
	if err := parseResponse(resp, &volumes); err != nil {
		return nil, err
	}

	return volumes, nil
}

// PruneVolumes prunes orphaned volumes via the admin API.
func (c *Client) PruneVolumes(ctx context.Context, req dto.VolumePruneRequest) (*dto.VolumePruneResponse, error) {
	resp, err := c.request(ctx, http.MethodPost, "/volumes/prune", req)
	if err != nil {
		return nil, err
	}

	var result dto.VolumePruneResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}
