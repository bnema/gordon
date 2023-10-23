package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/common"
)

// GenerateAPIURL creates the API URL
func GenerateAPIURL(a *app.App) string {
	fmt.Println("Generating API URL:", a.Config.GenerateAPIURL())
	return a.Config.GenerateAPIURL()
}

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

// SendHTTPRequest sends the HTTP request
func SendHTTPRequest(a *app.App, payload common.Payload) {
	apiUrl := GenerateAPIURL(a)
	fmt.Println("Sending request to:", apiUrl)

	token := a.Config.General.GordonToken

	jsonPayload, err := PrepareJSONPayload(payload)
	if err != nil {
		fmt.Println("Error preparing JSON payload:", err)
		return
	}

	req, err := CreateNewRequest("POST", apiUrl, jsonPayload)
	if err != nil {
		fmt.Println("Error creating new HTTP request:", err)
		return
	}

	SetRequestHeaders(req, token)

	resp, err := SendRequest(req)
	if err != nil {
		fmt.Println("Error sending HTTP request:", err)
		return
	}
	defer resp.Body.Close()

	fmt.Println("Response Status:", resp.Status)
}
