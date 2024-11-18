package handler

import (
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/auth"
	"github.com/bnema/gordon/internal/common"
	"github.com/charmbracelet/log"
)

func CheckAndRefreshAuth(a *cli.App) error {
	if a.Config.General.Token == "" {
		log.Info("No authentication token found. Initiating authentication...")
		return ReAuthenticate(a)
	}

	resp, err := SendHTTPRequest(a, &common.RequestPayload{
		Type: "ping",
		Payload: common.PingPayload{
			Message: "ping",
		},
	}, "GET", "/ping")

	if err != nil {
		log.Debug("Token validation request failed", "error", err)
		return ReAuthenticate(a)
	}

	// If we get a 200 status code, and the resp contains message = pong, we're good
	if resp.StatusCode != 200 {
		log.Debug("Token validation failed",
			"status_code", resp.StatusCode,
			"response", resp.Body)
		return ReAuthenticate(a)

	}

	return nil
}

func ReAuthenticate(a *cli.App) error {
	fmt.Println("Re-authenticating...")

	// Small delay for visual clarity
	time.Sleep(100 * time.Millisecond)

	err := auth.DeviceFlowAuth(a)
	if err != nil {
		return fmt.Errorf("device flow authentication failed: %w", err)
	}

	// After successful re-authentication, get the new token
	newToken, err := a.Config.GetToken()
	if err != nil {
		return fmt.Errorf("failed to get new token: %w", err)
	}

	// Update the token in the app config
	a.Config.General.Token = newToken

	// Save the new token to config file
	err = a.Config.SaveConfig()
	if err != nil {
		return fmt.Errorf("failed to save new token to config: %w", err)
	}

	// Clear any remaining auth UI elements
	fmt.Print("\033[2K\r")
	fmt.Println("Re-authentication successful.")

	// Add delay before resuming upload display
	time.Sleep(500 * time.Millisecond)

	return nil
}
