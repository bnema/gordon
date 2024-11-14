package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"
)

// IsValidToken validates a GitHub token
func IsValidGitHubToken(token string) bool {
	// Use the existing token cache
	tokenCache := GetTokenCache()

	// Check if token exists in cache
	if _, _, found := tokenCache.GetWithUser(token); found {
		log.Debug("token found in cache")
		return true
	}

	// If not in cache, validate with GitHub
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		log.Error("failed to create GitHub API request", "error", err)
		return false
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		log.Error("GitHub API request failed", "error", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn("invalid GitHub token", "status", resp.Status)
		return false
	}

	return true
}

// ExtractAndValidateToken extracts and validates a token from an Authorization header
func ExtractAndValidateToken(authHeader string) (string, bool) {
	if authHeader == "" {
		return "", false
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		return "", false
	}

	if !IsValidGitHubToken(token) {
		return "", false
	}

	return token, true
}
