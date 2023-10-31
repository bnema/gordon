package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
)

// PrepareJSONPayload marshals the payload into JSON
func PrepareJSONPayload(payload common.Payload) ([]byte, error) {
	return json.Marshal(payload)
}

// CreateNewRequest creates a new HTTP request
func CreateNewRequest(method, url string, body []byte) (*http.Request, error) {

	return http.NewRequest(method, url, bytes.NewBuffer(body))
}

// SetRequestHeaders sets the headers for the request
func setAuthRequestHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}

// SendRequest sends the HTTP request and returns the response
func SendRequest(req *http.Request) (*http.Response, error) {
	client := &http.Client{}
	return client.Do(req)
}

type Response struct {
	Body       []byte
	StatusCode int
}

// SendHTTPRequest sends the HTTP request
func SendHTTPRequest(a *cli.App, rp *common.RequestPayload, method string, endpoint string) (*Response, error) {
	apiUrl := a.Config.Http.BackendURL + "/api"
	token := a.Config.General.Token

	var req *http.Request
	var err error

	// Handle push type payload
	if rp.Type == "push" {
		pushPayload, ok := rp.Payload.(common.PushPayload)
		if !ok {
			return nil, fmt.Errorf("invalid payload type for push")
		}

		req, err = http.NewRequest(method, apiUrl+endpoint, pushPayload.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to create new streaming request: %w", err)
		}

		setAuthRequestHeaders(req, token)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Ports", pushPayload.Ports)
		req.Header.Set("X-Target-Domain", pushPayload.TargetDomain)
		req.Header.Set("X-Image-Name", pushPayload.ImageName)

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}
		return &Response{Body: body}, nil
	}

	// Handle other types of payloads
	jsonPayload, err := json.Marshal(rp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err = http.NewRequest(method, apiUrl+endpoint, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to create new JSON request: %w", err)
	}

	setAuthRequestHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return &Response{Body: body}, nil
}
