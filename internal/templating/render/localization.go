package render

import (
	"fmt"

	"github.com/bnema/gordon/internal/server"
)

// GetLocalization function returns the localization map for the given language
func GetLocalization(lang string, a *server.App) (map[string]interface{}, error) {
	// 1. Check if the language exists in the pre-parsed map
	langData, langExists := a.Strings[lang]
	if !langExists {
		// Fallback to default language (e.g., "en") if primary is not found
		defaultLang := "en"
		langData, langExists = a.Strings[defaultLang]
		if !langExists {
			return nil, fmt.Errorf("default language '%s' not found in localization data", defaultLang)
		}
		// Use default language if primary was not found
		lang = defaultLang
		fmt.Printf("Warning: Language '%s' not found, falling back to '%s'\n", lang, defaultLang)
	}

	// 2. Assert that the language data is a map
	localizationMap, ok := langData.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("localization data for language '%s' is not in the expected map format", lang)
	}

	// 3. Return the localization map for the requested (or fallback) language
	return localizationMap, nil
}

// Helper function to check if a slice contains a string - No longer needed as we check map keys
// func contains(slice []string, str string) bool { ... }
