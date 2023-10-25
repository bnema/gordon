package handler

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
)

// config.Http.BackendURL = readUserInput("Enter the backend URL (e.g. https://gordon.mydomain.com):")
// config.General.Token = readUserInput("Enter the token (check your backend config.yml):")
// check in the config.yml if BackendURL and Token are set
// if not, prompt the user to enter them

func checkIfBackendURLisSet(a *cli.App) (string, error) {
	if a.Config.Http.BackendURL == "" {
		fmt.Println("BackendURL is not set in config.yml")
		a.Config.Http.BackendURL = common.ReadUserInput("Enter the backend URL (e.g. https://gordon.mydomain.com):")
		return a.Config.Http.BackendURL, nil
	}
	return a.Config.Http.BackendURL, nil
}

func checkIfTokenIsSet(a *cli.App) (string, error) {
	if a.Config.General.Token == "" {
		fmt.Println("Token is not set in config.yml")
		a.Config.General.Token = common.ReadUserInput("Enter the token (check your backend config.yml):")
		return a.Config.General.Token, nil
	}
	return a.Config.General.Token, nil
}

func FieldCheck(a *cli.App) error {
	_, err := checkIfBackendURLisSet(a)
	if err != nil {
		return err
	}
	_, err = checkIfTokenIsSet(a)
	if err != nil {
		return err
	}

	// save config
	err = a.Config.SaveConfig()
	if err != nil {
		return err
	}

	return nil
}
