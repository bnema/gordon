// internal/cli/handler/chunked_client.go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
	"github.com/charmbracelet/log"
	"github.com/google/uuid"
)

type ChunkedClient struct {
	client    *http.Client
	baseURL   string
	token     string
	chunkSize int64
	app       *cli.App
}

type ChunkMetadata common.ChunkMetadata

func NewChunkedClient(app *cli.App) *ChunkedClient {
	baseURL := app.Config.Http.BackendURL + "/api"
	token := app.Config.General.Token

	// If token is empty or invalid, try to authenticate
	if token == "" {
		if err := ReAuthenticate(app); err != nil {
			log.Error("Failed to authenticate", "error", err)
			return nil
		}
		token = app.Config.General.Token
	}

	return &ChunkedClient{
		client: &http.Client{
			Timeout: 30 * time.Minute,
		},
		baseURL:   baseURL,
		token:     token,
		chunkSize: 5 * 1024 * 1024,
		app:       app,
	}
}

func (c *ChunkedClient) SendFile(ctx context.Context, endpoint string, headers http.Header, reader io.Reader, totalSize int64, imageName string) (resp *Response, error error) {
	transferID := uuid.New().String()
	totalChunks := (int(totalSize) + int(c.chunkSize) - 1) / int(c.chunkSize)
	buf := make([]byte, c.chunkSize)
	var lastResponse *Response

	// Determine response type based on endpoint
	responseType := determineResponseType(endpoint)

	for chunkNum := 0; chunkNum < totalChunks; chunkNum++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("upload cancelled")
		default:
			n, err := io.ReadFull(reader, buf)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return nil, fmt.Errorf("failed to read chunk %d: %w", chunkNum, err)
			}

			chunk := buf[:n]
			if n == 0 {
				continue
			}

			metadata := common.ChunkMetadata{
				ChunkNumber: chunkNum,
				TotalChunks: totalChunks,
				ChunkSize:   int64(n),
				TotalSize:   totalSize,
				ImageName:   imageName,
				TransferID:  transferID,
			}

			// Send chunk and get response
			resp, err := c.sendChunkWithRetry(ctx, endpoint, headers, chunk, metadata)
			if err != nil {
				return nil, fmt.Errorf("failed to send chunk %d: %w", chunkNum, err)
			}
			lastResponse = resp

			// Handle chunk response
			if err := handleChunkResponse(resp, responseType); err != nil {
				log.Error("Failed to handle chunk response", "error", err)
			}

			percentage := float64(chunkNum+1) / float64(totalChunks) * 100
			log.Info("Upload progress",
				"completed", chunkNum+1,
				"total", totalChunks,
				"progress", fmt.Sprintf("%.2f%%", percentage))
		}
	}

	// Handle final response
	if lastResponse != nil {
		if err := handleFinalResponse(lastResponse, responseType); err != nil {
			log.Error("Failed to handle final response", "error", err)
		}
	}

	return lastResponse, nil
}

func determineResponseType(endpoint string) string {
	switch {
	case strings.Contains(endpoint, "/deploy"):
		return "deploy"
	case strings.Contains(endpoint, "/push"):
		return "push"
	default:
		return "unknown"
	}
}

func handleChunkResponse(resp *Response, responseType string) error {
	switch responseType {
	case "deploy":
		var deployResp common.DeployResponse
		if err := json.Unmarshal(resp.Body, &deployResp); err != nil {
			return fmt.Errorf("failed to unmarshal deploy response: %w", err)
		}
		log.Info("Deploy chunk response",
			"success", deployResp.Success,
			"message", deployResp.Message,
			"domain", deployResp.Domain,
			"containerID", deployResp.ContainerID,
			"containerName", deployResp.ContainerName)
		return nil

	case "push":
		var pushResp common.PushResponse
		if err := json.Unmarshal(resp.Body, &pushResp); err != nil {
			return fmt.Errorf("failed to unmarshal push response: %w", err)
		}
		log.Info("Push chunk response",
			"success", pushResp.Success,
			"message", pushResp.Message,
			"createContainerURL", pushResp.CreateContainerURL,
			"imageID", pushResp.ImageID)
		return nil

	default:
		return fmt.Errorf("unknown response type: %s", responseType)
	}
}

func handleFinalResponse(resp *Response, responseType string) error {
	switch responseType {
	case "deploy":
		var finalResp common.DeployResponse
		if err := json.Unmarshal(resp.Body, &finalResp); err != nil {
			return fmt.Errorf("failed to unmarshal final deploy response: %w", err)
		}
		log.Info("Final deploy response",
			"success", finalResp.Success,
			"message", finalResp.Message,
			"domain", finalResp.Domain,
			"containerID", finalResp.ContainerID,
			"containerName", finalResp.ContainerName)
		return nil

	case "push":
		var finalResp common.PushResponse
		if err := json.Unmarshal(resp.Body, &finalResp); err != nil {
			return fmt.Errorf("failed to unmarshal final push response: %w", err)
		}
		log.Info("Final push response",
			"success", finalResp.Success,
			"message", finalResp.Message,
			"createContainerURL", finalResp.CreateContainerURL,
			"imageID", finalResp.ImageID)
		return nil

	default:
		return fmt.Errorf("unknown response type: %s", responseType)
	}
}

func (c *ChunkedClient) sendChunkWithRetry(ctx context.Context, endpoint string, headers http.Header, chunk []byte, metadata common.ChunkMetadata) (*Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := c.sendChunk(ctx, endpoint, headers, chunk, metadata)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		log.Warn("Chunk upload failed, retrying",
			"chunk", metadata.ChunkNumber,
			"attempt", attempt+1,
			"error", err)
		time.Sleep(time.Second * time.Duration(attempt+1))
	}
	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

func (c *ChunkedClient) sendChunk(ctx context.Context, endpoint string, headers http.Header, chunk []byte, metadata common.ChunkMetadata) (*Response, error) {
	reauthenticated := false

	for {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()

		metadataJSON, err := json.Marshal(metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}

		// Create the request
		url := c.baseURL + endpoint
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(chunk))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		// Set headers
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Chunk-Metadata", string(metadataJSON))

		for k, v := range headers {
			req.Header[k] = v
		}

		// Send request
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		defer resp.Body.Close()

		// Handle 401 Unauthorized
		if resp.StatusCode == http.StatusUnauthorized && !reauthenticated {
			log.Warn("Token expired, initiating re-authentication")

			err := ReAuthenticate(c.app)
			if err != nil {
				return nil, fmt.Errorf("re-authentication failed: %w", err)
			}

			// Update the client's token with the new one
			c.token = c.app.Config.General.Token
			reauthenticated = true
			continue
		}

		// Handle non-200 status codes
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, &common.DeploymentError{
				StatusCode:  resp.StatusCode,
				RawResponse: string(body),
			}
		}

		// Read response body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		return &Response{
			Http:       resp,
			Body:       body,
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
		}, nil
	}
}
