package render

import (
	"fmt"

	"github.com/bnema/gordon/internal/server"
	"gopkg.in/yaml.v3"
)

type Localizations map[string]map[string]map[string]interface{}

// GetLocalization function returns the localization for the given language
func GetLocalization(lang string, a *server.App) (map[string]interface{}, error) {
	// 1. Check if the language is supported
	supportedLanguages := []string{"en", "fr"} // Add more languages here
	if !contains(supportedLanguages, lang) {
		return nil, fmt.Errorf("language %s is not supported", lang)
	}

	fmt.Println("Language:", lang)

	// 2. Get the localizations from the strings.yml file
	localizations, err := getLocalizations(a)
	if err != nil {
		return nil, fmt.Errorf("failed to get localizations: %w", err)
	}

	// 3. Return the localizations for the given language
	if localization, ok := localizations[lang]; ok {
		result := make(map[string]interface{})
		for k, v := range localization {
			result[k] = v
		}
		return result, nil
	}

	return nil, fmt.Errorf("language %s is not available", lang)
}

// getLocalizations function returns the localizations from the strings.yml file
func getLocalizations(a *server.App) (Localizations, error) {
	var loc Localizations

	err := yaml.Unmarshal(a.LocYML, &loc)
	if err != nil {
		return loc, fmt.Errorf("failed to unmarshal strings.yml: %w", err)
	}

	return loc, nil
}

// Helper function to check if a slice contains a string
func contains(slice []string, str string) bool {
	for _, v := range slice {
		if v == str {
			return true
		}
	}
	return false
}
