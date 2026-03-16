package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// PasswordRequest represents the request body for POST /auth/password.
type PasswordRequest struct {
	Username string `json:"username"`
	Password string `json:"password"` //nolint:gosec // intentional: CLI credential DTO for auth endpoint
}

// PasswordResponse represents the response from POST /auth/password.
type PasswordResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
	IssuedAt  string `json:"issued_at"`
}

// Authenticate calls POST /auth/password and returns a token.
// This method does NOT require an existing token since it's used to obtain one.
func (c *Client) Authenticate(ctx context.Context, username, password string) (*PasswordResponse, error) {
	url := c.baseURL + "/auth/password"

	reqBody := PasswordRequest{
		Username: username,
		Password: password,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("%s: %s", resp.Status, errResp.Error)
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, string(body))
	}

	var result PasswordResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// UpdateRemoteToken updates the token for a named remote.
func UpdateRemoteToken(name, token string) error {
	// Try pass first
	if passAvailable() {
		if err := passWriteToken(name, token); err == nil {
			// Successfully stored in pass. Remove plaintext token from TOML (best-effort).
			config, err := LoadRemotes("")
			if err != nil {
				return nil
			}
			remote, ok := config.Remotes[name]
			if !ok {
				return nil
			}
			remote.Token = ""
			remote.TokenEnv = ""
			config.Remotes[name] = remote
			_ = SaveRemotes("", config)
			return nil
		} else {
			fmt.Fprintf(os.Stderr, "Warning: failed to store token in pass: %v. Falling back to plaintext config.\n", err)
		}
	} else {
		warnPassUnavailable()
	}

	config, err := LoadRemotes("")
	if err != nil {
		return err
	}

	remote, ok := config.Remotes[name]
	if !ok {
		return fmt.Errorf("remote '%s' not found", name)
	}

	remote.Token = token
	remote.TokenEnv = "" // Clear token_env when setting token directly
	config.Remotes[name] = remote

	return SaveRemotes("", config)
}
