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

type requestTransportError struct {
	err error
}

func (e *requestTransportError) Error() string {
	return e.err.Error()
}

func (e *requestTransportError) Unwrap() error {
	return e.err
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
		return nil, &requestTransportError{
			err: fmt.Errorf("%s %s transport request: %w", method, path, err),
		}
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

// OutcomeUnknownError reports that a non-idempotent request may have completed
// even though the client did not receive a definitive response.
type OutcomeUnknownError struct {
	Method string
	Path   string
	Err    error
}

func (e *OutcomeUnknownError) Error() string {
	return fmt.Sprintf(
		"%s %s outcome unknown: request may have completed; inspect current state before retrying: %v",
		e.Method,
		e.Path,
		e.Err,
	)
}

// Unwrap returns the transport or HTTP error that made the outcome ambiguous.
func (e *OutcomeUnknownError) Unwrap() error {
	return e.Err
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
	var transportErr *requestTransportError
	return errors.As(err, &transportErr) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

func isIdempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace,
		http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

func isRetryableStatus(status int) bool {
	// Gateway errors can be transient; requestWithRetry separately ensures the
	// method is safe to replay before attempting another request.
	return status == 502 || status == 503 || status == 504
}

func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return retryBaseDelay
	}
	return retryBaseDelay * time.Duration(1<<(attempt-1))
}

// requestWithRetry performs an HTTP request and retries transient failures
// only when the HTTP method is idempotent. A transient failure from a
// non-idempotent request is returned as OutcomeUnknownError without replaying it.
func (c *Client) requestWithRetry(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var lastErr error
	idempotent := isIdempotentMethod(method)

	for attempt := 1; attempt <= retryMaxAttempts; attempt++ {
		resp, err := c.request(ctx, method, path, body)
		if err != nil {
			if !idempotent {
				var transportErr *requestTransportError
				if errors.As(err, &transportErr) {
					return nil, &OutcomeUnknownError{Method: method, Path: path, Err: err}
				}
				return nil, err
			}
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
			if !idempotent {
				return nil, &OutcomeUnknownError{Method: method, Path: path, Err: lastErr}
			}
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

// Secrets API

// SecretsListResult contains domain secret keys.
type SecretsListResult struct {
	Domain string   `json:"domain"`
	Keys   []string `json:"keys"`
}

// ListSecretsWithAttachments returns domain secret keys. Attachment
// containers are retired: the daemon answers without attachment keys.
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

// Status API

// Status represents the Gordon server status.
type Status struct {
	Apps             int               `json:"apps"`
	RegistryDomain   string            `json:"registry_domain"`
	RegistryPort     int               `json:"registry_port"`
	ServerPort       int               `json:"server_port"`
	NetworkIsolation bool              `json:"network_isolation"`
	ContainerStatus  map[string]string `json:"container_status"`
}

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

// GetTrafficStatus returns the traffic plane status.
func (c *Client) GetTrafficStatus(ctx context.Context) (*dto.TrafficStatusResponse, error) {
	resp, err := c.request(ctx, http.MethodGet, "/traffic/status", nil)
	if err != nil {
		return nil, fmt.Errorf("request traffic status: %w", err)
	}

	var status dto.TrafficStatusResponse
	if err := parseResponse(resp, &status); err != nil {
		return nil, fmt.Errorf("parse traffic status response: %w", err)
	}

	return &status, nil
}

// GetStatus returns the Gordon server status.
func (c *Client) GetStatus(ctx context.Context) (*Status, error) {
	resp, err := c.requestWithRetry(ctx, http.MethodGet, "/status", nil)
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

// ListVolumeBackups returns volume backups globally or for a domain.
func (c *Client) ListVolumeBackups(ctx context.Context, backupDomain string) ([]dto.VolumeBackupJob, error) {
	path := "/backups/volumes"
	if backupDomain != "" {
		path += "/" + url.PathEscape(backupDomain)
	}

	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var result dto.VolumeBackupsResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return result.Backups, nil
}

// VolumeBackupStatus returns aggregate volume backup status.
func (c *Client) VolumeBackupStatus(ctx context.Context) ([]dto.VolumeBackupJob, error) {
	resp, err := c.request(ctx, http.MethodGet, "/backups/volumes/status", nil)
	if err != nil {
		return nil, err
	}

	var result dto.VolumeBackupsResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return result.Backups, nil
}

// RunVolumeBackups triggers volume backups.
func (c *Client) RunVolumeBackups(ctx context.Context, backupDomain, volumeName string) (*dto.VolumeBackupRunResponse, error) {
	path := "/backups/volumes"
	if backupDomain != "" {
		path += "/" + url.PathEscape(backupDomain)
	}

	resp, err := c.request(ctx, http.MethodPost, path, dto.VolumeBackupRunRequest{Volume: volumeName})
	if err != nil {
		return nil, err
	}

	var result dto.VolumeBackupRunResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusPartialContent {
		if result.Error == "" {
			result.Error = "volume backup run partially failed"
		}
		return &result, fmt.Errorf("volume backup run partially failed: %s", result.Error)
	}
	return &result, nil
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
	NetworkIsolation struct {
		Enabled bool   `json:"enabled"`
		Prefix  string `json:"prefix"`
	} `json:"network_isolation"`
	Volumes struct {
		AutoCreate bool   `json:"auto_create"`
		Prefix     string `json:"prefix"`
		Preserve   bool   `json:"preserve"`
	} `json:"volumes"`
	ExternalRoutes []ExternalRoute `json:"external_routes"`
}

// ExternalRoute represents a redacted external route config entry.
type ExternalRoute struct {
	Domain string `json:"domain"`
	Target string `json:"target,omitempty"`
}

// GetConfig returns the Gordon configuration.
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

// ListOrphanedAttachments returns running attachment containers no longer configured.
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
		Transport:     c.httpClient.Transport,
		CheckRedirect: c.httpClient.CheckRedirect,
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
				event, remaining, ok := strings.Cut(data, "\n\n")
				if !ok {
					break
				}

				lineBuffer.Reset()
				lineBuffer.WriteString(remaining)

				// Parse SSE data lines
				for line := range strings.SplitSeq(event, "\n") {
					logLine, ok := strings.CutPrefix(line, "data: ")
					if ok {
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
