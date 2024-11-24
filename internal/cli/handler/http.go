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
	Http       *http.Response
	Header     http.Header
	Body       []byte
	StatusCode int
}

// SendHTTPRequest sends the HTTP request
func SendHTTPRequest(a *cli.App, rp *common.RequestPayload, method string, endpoint string) (*Response, error) {
	apiUrl := a.Config.Http.BackendURL + "/api"
	client := &http.Client{}

	reauthenticated := false

	for {
		req, err := createRequest(apiUrl, endpoint, method, rp, a.Config.General.Token)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			var errorResp struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(body, &errorResp); err == nil {
				if resp.StatusCode == http.StatusUnauthorized && !reauthenticated {
					fmt.Println("Token is invalid or expired. Initiating re-authentication...")
					err := ReAuthenticate(a)
					if err != nil {
						return nil, fmt.Errorf("re-authentication failed: %w", err)
					}

					reauthenticated = true
					continue
				}
				return nil, fmt.Errorf("failed to send request: %s", errorResp.Message)
			}
			return nil, fmt.Errorf("failed to send request: %s", string(body))
		}

		return &Response{Http: resp, Body: body, StatusCode: resp.StatusCode}, nil
	}
}

func createRequest(apiUrl, endpoint, method string, rp *common.RequestPayload, token string) (*http.Request, error) {
	var req *http.Request
	var err error

	switch rp.Type {
	case "deploy":
		deployPayload, ok := rp.Payload.(common.DeployPayload)
		if !ok {
			return nil, fmt.Errorf("invalid payload type for deploy")
		}

		req, err = http.NewRequest(method, apiUrl+endpoint, deployPayload.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to create new streaming request: %w", err)
		}

		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Ports", deployPayload.Port)
		req.Header.Set("X-Target-Domain", deployPayload.TargetDomain)
		req.Header.Set("X-Image-Name", deployPayload.ImageName)

	case "push":
		pushPayload, ok := rp.Payload.(common.PushPayload)
		if !ok {
			return nil, fmt.Errorf("invalid payload type for push")
		}

		req, err = http.NewRequest(method, apiUrl+endpoint, pushPayload.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to create new streaming request: %w", err)
		}

		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Image-Name", pushPayload.ImageName)

	default:
		jsonPayload, err := json.Marshal(rp)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}

		req, err = http.NewRequest(method, apiUrl+endpoint, bytes.NewBuffer(jsonPayload))
		if err != nil {
			return nil, fmt.Errorf("failed to create new JSON request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
	}

	// Set the authorization header for all request types
	setAuthRequestHeaders(req, token)

	return req, nil
}
