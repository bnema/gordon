package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/bnema/gordon/internal/app"
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
func SetRequestHeaders(req *http.Request, token string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
}

// SendRequest sends the HTTP request and returns the response
func SendRequest(req *http.Request) (*http.Response, error) {
	client := &http.Client{}
	return client.Do(req)
}

type Response struct {
	Body []byte
}

// SendHTTPRequest sends the HTTP request
func SendHTTPRequest(a *app.App, rp *common.RequestPayload, endpoint string) (*Response, error) {
	apiUrl := a.Config.Http.BackendURL + "/api"
	token := a.Config.General.GordonToken

	// Prepare the entire RequestPayload, not just the inner Payload
	jsonPayload, err := json.Marshal(rp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}
	// Create a new request
	req, err := CreateNewRequest("GET", apiUrl+endpoint, jsonPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to create new request: %w", err)
	}

	// Set the request headers
	SetRequestHeaders(req, token)

	// Send the request
	resp, err := SendRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return &Response{Body: body}, nil
}
