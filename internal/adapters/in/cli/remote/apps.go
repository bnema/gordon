package remote

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/google/uuid"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// Apps API: daemon-owned app mutations through the existing V2
// administration transport. Every mutation carries an Idempotency-Key
// header (client-generated ULID); the daemon persists the claim before
// effects and replays recorded results. On ambiguous transport outcome
// the caller gets OutcomeUnknownError and must re-query by key — never
// blindly retry.

// newIdempotencyKey allocates a time-ordered key (uuid v7).
func newIdempotencyKey() string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	var buf [32]byte
	hex.Encode(buf[:], id[:])
	return string(buf[:])
}

// mutationPost sends a POST mutation with an idempotency key.
func (c *Client) mutationPost(ctx context.Context, path string, key string, body any) (*http.Response, error) {
	var jsonBody []byte
	if body != nil {
		var err error
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}
	return c.doMutation(ctx, http.MethodPost, path, key, jsonBody)
}

// doMutation executes one mutation request with the idempotency header.
// Transport ambiguity surfaces as OutcomeUnknownError: the caller must
// re-query GET …/operations/by-key/{key} before any retry, and retries
// reuse the SAME key.
func (c *Client) doMutation(ctx context.Context, method, path, key string, jsonBody []byte) (*http.Response, error) {
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
	req.Header.Set("Idempotency-Key", key)

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &OutcomeUnknownError{Method: method, Path: path, Err: err}
	}
	if isRetryableStatus(resp.StatusCode) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		_ = resp.Body.Close()
		return nil, &OutcomeUnknownError{
			Method: method,
			Path:   path,
			Err:    parseErrorResponse(resp, body),
		}
	}
	return resp, nil
}

// ApplyApp validates and persists desired state (or dry-runs).
func (c *Client) ApplyApp(ctx context.Context, req dto.AppApplyRequest) (*dto.AppApplyResponse, error) {
	resp, err := c.mutationPost(ctx, "/apps/apply", newIdempotencyKey(), req)
	if err != nil {
		return nil, err
	}
	var result dto.AppApplyResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("apply app: %w", err)
	}
	return &result, nil
}

// ListApps returns desired+active summaries.
func (c *Client) ListApps(ctx context.Context) ([]dto.AppSummaryDTO, error) {
	resp, err := c.request(ctx, http.MethodGet, "/apps", nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Apps []dto.AppSummaryDTO `json:"apps"`
	}
	if err := parseResponse(resp, &envelope); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	return envelope.Apps, nil
}

// ShowApp inspects desired + active + intent + op ref.
func (c *Client) ShowApp(ctx context.Context, app string) (*dto.AppShowResponse, error) {
	resp, err := c.request(ctx, http.MethodGet, "/apps/"+url.PathEscape(app), nil)
	if err != nil {
		return nil, err
	}
	var result dto.AppShowResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("show app %s: %w", app, err)
	}
	return &result, nil
}

// DiffApp returns the normalized desired-vs-active diff.
func (c *Client) DiffApp(ctx context.Context, app string) (*dto.AppDiffResponse, error) {
	resp, err := c.request(ctx, http.MethodGet, "/apps/"+url.PathEscape(app)+"/diff", nil)
	if err != nil {
		return nil, err
	}
	var result dto.AppDiffResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("diff app %s: %w", app, err)
	}
	return &result, nil
}

// DeployApp activates a captured revision. It returns the key used so
// ambiguous outcomes can be recovered via OperationByKey.
func (c *Client) DeployApp(ctx context.Context, app string, req dto.AppDeployRequest) (*dto.AppDeployResponse, string, error) {
	key := newIdempotencyKey()
	resp, err := c.mutationPost(ctx, "/apps/"+url.PathEscape(app)+"/deploy", key, req)
	if err != nil {
		return nil, key, err
	}
	result, err := parseDeployResponse(resp, "deploy", app)
	return result, key, err
}

// StopApp persists durable stopped intent and stops exact containers.
func (c *Client) StopApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return c.lifecyclePost(ctx, app, "stop")
}

// StartApp clears stopped intent and ensures running from active.
func (c *Client) StartApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return c.lifecyclePost(ctx, app, "start")
}

// RestartApp restarts from pinned digests (no re-resolution).
func (c *Client) RestartApp(ctx context.Context, app, service string) (*dto.AppDeployResponse, string, error) {
	key := newIdempotencyKey()
	path := "/apps/" + url.PathEscape(app) + "/restart"
	if service != "" {
		path += "?service=" + url.QueryEscape(service)
	}
	resp, err := c.doMutation(ctx, http.MethodPost, path, key, nil)
	if err != nil {
		return nil, key, err
	}
	result, err := parseDeployResponse(resp, "restart", app)
	return result, key, err
}

// RemoveApp withdraws workloads; volumes and secrets are retained.
func (c *Client) RemoveApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return c.lifecyclePost(ctx, app, "remove")
}

// lifecyclePost posts a body-less lifecycle mutation.
func (c *Client) lifecyclePost(ctx context.Context, app, action string) (*dto.AppDeployResponse, string, error) {
	key := newIdempotencyKey()
	resp, err := c.doMutation(ctx, http.MethodPost, "/apps/"+url.PathEscape(app)+"/"+action, key, nil)
	if err != nil {
		return nil, key, err
	}
	result, err := parseDeployResponse(resp, action, app)
	return result, key, err
}

// maxDeployConflictBodySize bounds 409 journal reads: journals carry
// per-service results and steps and can exceed the 1 KB error cap.
const maxDeployConflictBodySize int64 = 1 << 20

// AppOpConflictError reports a 409 deploy/lifecycle outcome while carrying
// the decoded operation journal. Use errors.As to render the journal and
// retain failure semantics; the idempotency key travels alongside the
// returned error from DeployApp/RestartApp/lifecycle calls.
type AppOpConflictError struct {
	StatusCode int
	Status     string
	Response   dto.AppDeployResponse
}

func (e *AppOpConflictError) Error() string {
	return fmt.Sprintf("%s: %s %s outcome %s", e.Status, e.Response.Op, e.Response.App, e.Response.Outcome)
}

// parseDeployResponse decodes a deploy/lifecycle mutation response. A 409
// carries the journaled AppDeployResponse body (per-service results,
// steps, effective/observed state) instead of an error envelope; it is
// decoded and surfaced as AppOpConflictError so the CLI can render the
// journal while retaining failure semantics.
func parseDeployResponse(resp *http.Response, action, app string) (*dto.AppDeployResponse, error) {
	if resp.StatusCode == http.StatusConflict {
		return decodeDeployConflict(resp, action, app)
	}
	var result dto.AppDeployResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("%s app %s: %w", action, app, err)
	}
	return &result, nil
}

// decodeDeployConflict decodes a 409 journal body into AppDeployResponse.
// Bodies that are not journals (preflight AppError envelopes) fall back to
// HTTPError so generic conflicts keep their existing shape.
func decodeDeployConflict(resp *http.Response, action, app string) (*dto.AppDeployResponse, error) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDeployConflictBodySize))
	var result dto.AppDeployResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("%s app %s: %w", action, app, parseErrorResponse(resp, body))
	}
	if result.Op == "" && result.Outcome == "" && len(result.Services) == 0 && len(result.Steps) == 0 {
		return nil, fmt.Errorf("%s app %s: %w", action, app, parseErrorResponse(resp, body))
	}
	conflict := &AppOpConflictError{StatusCode: resp.StatusCode, Status: resp.Status, Response: result}
	return &result, fmt.Errorf("%s app %s: %w", action, app, conflict)
}

// OperationByKey recovers an ambiguous mutation outcome by idempotency key.
func (c *Client) OperationByKey(ctx context.Context, app, key string) (*dto.AppDeployResponse, error) {
	resp, err := c.request(ctx, http.MethodGet, "/apps/"+url.PathEscape(app)+"/operations/by-key/"+url.PathEscape(key), nil)
	if err != nil {
		return nil, err
	}
	var result dto.AppDeployResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, fmt.Errorf("operation by key: %w", err)
	}
	return &result, nil
}

// SetAppSecrets writes secret values for pre-registered names.
func (c *Client) SetAppSecrets(ctx context.Context, app string, req dto.AppSecretSetRequest) error {
	resp, err := c.mutationPost(ctx, "/apps/"+url.PathEscape(app)+"/secrets/set", newIdempotencyKey(), req)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// DeleteAppSecret removes one secret value (refused when referenced).
func (c *Client) DeleteAppSecret(ctx context.Context, app string, req dto.AppSecretDeleteRequest) error {
	resp, err := c.mutationPost(ctx, "/apps/"+url.PathEscape(app)+"/secrets/delete", newIdempotencyKey(), req)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}
