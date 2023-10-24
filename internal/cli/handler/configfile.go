package handler

import "github.com/bnema/gordon/internal/app"

// I need to handle the case of the backend URL not being present in the config file
func IsBackendURLPresent(a *app.App) bool {
	// since config.yml is loaded from newclient.go, I can access the backend URL from the app struct

	if a.Config.Http.BackendURL == "" {
		return false
	}

	return true
}

// I Need to handle the case the token is not present in the config file
