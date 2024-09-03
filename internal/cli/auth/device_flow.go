package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bnema/gordon/internal/common"
)

func DeviceFlowAuth(config *common.Config) error {
	fmt.Println("Starting device flow authentication...")

	// Request device code
	deviceCode, err := requestDeviceCode(config)
	if err != nil {
		return fmt.Errorf("error requesting device code: %w", err)
	}

	fmt.Printf("Please go to %v and enter the code: %v\n", deviceCode["verification_uri"], deviceCode["user_code"])

	// Poll for access token
	interval := 5 // Default to 5 seconds if not provided
	if i, ok := deviceCode["interval"].(float64); ok {
		interval = int(i)
	}

	token, err := pollForAccessToken(config, deviceCode["device_code"].(string), interval)
	if err != nil {
		return fmt.Errorf("error polling for access token: %w", err)
	}

	// Save the token
	config.SetToken(token)

	fmt.Println("Authentication successful!")
	return nil
}

func requestDeviceCode(config *common.Config) (map[string]interface{}, error) {
	resp, err := http.PostForm(config.GetBackendURL()+"/api/device/code", url.Values{})
	if err != nil {
		return nil, fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned non-200 status code: %d. Body: %s", resp.StatusCode, string(body))
	}

	var deviceCode map[string]interface{}
	if err := json.Unmarshal(body, &deviceCode); err != nil {
		return nil, fmt.Errorf("error parsing JSON response: %w", err)
	}

	// Check for required fields
	requiredFields := []string{"verification_uri", "user_code", "device_code", "interval"}
	for _, field := range requiredFields {
		if _, ok := deviceCode[field]; !ok {
			return nil, fmt.Errorf("missing required field in response: %s", field)
		}
	}

	return deviceCode, nil
}

func pollForAccessToken(config *common.Config, deviceCode string, interval int) (string, error) {
	maxAttempts := 60 // Maximum number of attempts (5 minutes with 5-second intervals)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(time.Duration(interval) * time.Second)

		resp, err := http.PostForm(config.GetBackendURL()+"/api/device/token", url.Values{
			"device_code": {deviceCode},
		})
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		var tokenResp map[string]interface{}
		if err := json.Unmarshal(body, &tokenResp); err != nil {
			continue
		}

		if accessToken, ok := tokenResp["access_token"].(string); ok && accessToken != "" {
			return accessToken, nil
		}

		if tokenResp["error"] == "authorization_pending" {
			// Optionally, you can print a dot to show progress without cluttering the output
			fmt.Print(".")
			continue
		}

		// If we get here, there's an unexpected response but we can continue to try
		// fmt.Printf("Unexpected response from server (attempt %d): %v\n", attempt+1, tokenResp)
	}

	return "", fmt.Errorf("max attempts reached, failed to obtain access token")
}
