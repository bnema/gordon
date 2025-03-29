package handler

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/auth"
	"github.com/bnema/gordon/internal/common"
)

// config.Http.BackendURL = readUserInput("Enter the backend URL (e.g. https://gordon.mydomain.com):")
// config.General.Token = readUserInput("Enter the token (check your backend config.yml):")
// check in the config.yml if BackendURL and Token are set
// if not, prompt the user to enter them

// Helper function to check, prompt and set a value
func checkAndSetField(value *string, promptMessage, errorMessage string) (bool, error) {
	wasEmpty := false
	if *value == "" {
		wasEmpty = true
		fmt.Println(errorMessage)
		*value = common.ReadUserInput(promptMessage)
	}
	return wasEmpty, nil
}

func FieldCheck(a *cli.App) error {
	wasBackendURLEmpty, err := checkAndSetField(&a.Config.Http.BackendURL,
		"Enter the backend URL (e.g. https://gordon.mydomain.com):",
		"BackendURL is not set in config.yml")
	if err != nil {
		return err
	}

	if a.Config.General.JwtToken == "" {
		fmt.Println("Token is not set. Starting OAuth device flow...")
		_, err := auth.DeviceFlowAuth(a)
		if err != nil {
			fmt.Printf("Device flow authentication error: %v\n", err)
			return fmt.Errorf("failed to complete device flow authentication: %w", err)
		}
	}

	// Save config if BackendURL was empty or if a new token was set
	if wasBackendURLEmpty || a.Config.General.JwtToken != "" {
		err = a.Config.SaveConfig()
		if err != nil {
			return err
		}
	}

	return nil
}
