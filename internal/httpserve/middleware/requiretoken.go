package middleware

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// GithubUserInfo is an alias for the type defined in the queries package
type GithubUserInfo = queries.GithubUserInfo

// RequireToken is a middleware that checks for a valid GitHub token in the request
func RequireToken(a *server.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			log.Debug("Starting RequireToken middleware")

			githubUser, err := validateGitHubToken(c)
			if err != nil {
				log.Error("Token validation failed", "error", err)
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid or missing token"})
			}
			log.Debug("GitHub token validated successfully", "user", githubUser.Login)

			// Check if the user exists and matches the GitHub user
			isValid, err := queries.CheckDBUserIsGood(a, githubUser)
			if err != nil {
				log.Error("Error checking user authorization", "error", err)
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Error checking user authorization"})
			}

			if !isValid {
				log.Warn("User not authorized", "user", githubUser.Login)
				return c.JSON(http.StatusForbidden, map[string]string{"error": "User not authorized"})
			}
			log.Debug("User authorization check passed", "user", githubUser.Login)

			// Update or create session
			err = queries.CreateOrUpdateDBSession(a, c.Request().Header.Get("Authorization"), c.Request().UserAgent())
			if err != nil {
				log.Error("Error updating session", "error", err)
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Error updating session"})
			}
			log.Debug("Session created or updated successfully", "user", githubUser.Login)

			// Store the user information in the context for later use
			c.Set("user", githubUser)
			log.Debug("User information stored in context", "user", githubUser.Login)

			return next(c)
		}
	}
}

func validateGitHubToken(c echo.Context) (*GithubUserInfo, error) {
	token := c.Request().Header.Get("Authorization")
	if token == "" {
		log.Warn("Token is missing in the request")
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "Token is missing")
	}

	token = strings.TrimPrefix(token, "Bearer ")
	log.Debug("Validating GitHub token")

	// Validate the token against GitHub API
	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		log.Error("Failed to create request", "error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		log.Error("Failed to send request to GitHub API", "error", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn("Invalid token", "status", resp.Status)
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "Invalid token")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("Failed to read response body", "error", err)
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var githubUser GithubUserInfo
	if err := json.Unmarshal(body, &githubUser); err != nil {
		log.Error("Failed to parse user data", "error", err)
		return nil, fmt.Errorf("failed to parse user data: %w", err)
	}

	log.Debug("GitHub user info fetched successfully", "user", githubUser.Login)

	// Fetch user's email
	emails, err := fetchGitHubUserEmails(token)
	if err != nil {
		log.Error("Failed to fetch user emails", "error", err)
		return nil, fmt.Errorf("failed to fetch user emails: %w", err)
	}
	githubUser.Emails = emails
	log.Debug("GitHub user emails fetched successfully", "user", githubUser.Login, "emailCount", len(emails))

	return &githubUser, nil
}

func fetchGitHubUserEmails(token string) ([]string, error) {
	log.Debug("Fetching GitHub user emails")
	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://api.github.com/user/emails", nil)
	if err != nil {
		log.Error("Failed to create request for emails", "error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		log.Error("Failed to send request for emails", "error", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn("Failed to fetch user emails", "status", resp.Status)
		return nil, fmt.Errorf("failed to fetch user emails: %s", resp.Status)
	}

	var emailsResp []struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emailsResp); err != nil {
		log.Error("Failed to parse email response", "error", err)
		return nil, fmt.Errorf("failed to parse email response: %w", err)
	}

	emails := make([]string, len(emailsResp))
	for i, e := range emailsResp {
		emails[i] = e.Email
	}

	log.Debug("GitHub user emails fetched successfully", "emailCount", len(emails))
	return emails, nil
}
