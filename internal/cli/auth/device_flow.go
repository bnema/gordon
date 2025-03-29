// internal/cli/auth/device_flow.go
package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bnema/gordon/internal/cli"
)

func DeviceFlowAuth(a *cli.App) (token string, err error) {
	fmt.Println("Starting device flow authentication...")

	// Request device code
	deviceCode, err := requestDeviceCode(a)
	if err != nil {
		return "", fmt.Errorf("error requesting device code: %w", err)
	}

	fmt.Printf("Please go to %v and enter the code: %v\n", deviceCode["verification_uri"], deviceCode["user_code"])

	// Poll for access token
	interval := 5 // Default to 5 seconds if not provided
	if i, ok := deviceCode["interval"].(float64); ok {
		interval = int(i)
	}

	token, err = pollForAccessToken(a, deviceCode["device_code"].(string), interval)
	if err != nil {
		return "", fmt.Errorf("error polling for access token: %w", err)
	}

	// Save the obtained JWT into the JWTSecret field in the config
	a.Config.General.JwtToken = token
	err = a.Config.SaveConfig() // Save the updated config
	if err != nil {
		// Log the error but don't necessarily fail the whole flow,
		// as the token might still be usable in memory for the current session.
		fmt.Printf("\nWarning: Failed to save token to config file: %v\n", err)
		fmt.Println("Authentication successful, but token might not persist.")
	} else {
		fmt.Println("Authentication successful! Token saved to configuration.")
	}

	return token, nil
}

func requestDeviceCode(a *cli.App) (map[string]interface{}, error) {
	resp, err := http.PostForm(a.Config.GetBackendURL()+"/api/device/code", url.Values{})
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

func pollForAccessToken(a *cli.App, deviceCode string, interval int) (string, error) {
	maxAttempts := 60 // Maximum number of attempts (5 minutes with 5-second intervals)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(time.Duration(interval) * time.Second)

		resp, err := http.PostForm(a.Config.GetBackendURL()+"/api/device/token", url.Values{
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

		// Expect "jwt_token" instead of "access_token"
		if jwtToken, ok := tokenResp["jwt_token"].(string); ok && jwtToken != "" {
			fmt.Println() // Print a newline after successful polling dots
			return jwtToken, nil
		}

		if errorMsg, ok := tokenResp["error"].(string); ok && errorMsg == "authorization_pending" {
			// Optionally, you can print a dot to show progress without cluttering the output
			fmt.Print(".")
			continue
		}

		// If we get here, there's an unexpected response but we can continue to try
		// fmt.Printf("Unexpected response from server (attempt %d): %v\n", attempt+1, tokenResp)
	}

	return "", fmt.Errorf("max attempts reached, failed to obtain access token")
}
