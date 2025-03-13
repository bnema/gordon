package middleware

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/cli/auth"
	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo/v4"
)

// GithubUserInfo is an alias to db.GithubUserInfo
type GithubUserInfo = db.GithubUserInfo

// middleware/require_token.go
func RequireToken(a *server.App) echo.MiddlewareFunc {
	tokenCache := auth.GetTokenCache()

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token := strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
			if token == "" {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"success": false,
					"error":   "Token is missing",
				})
			}

			// Check cache first
			if _, cachedUser, found := tokenCache.GetWithUser(token); found {
				// Verify if user exists in database and is authorized
				_, isAuthorized, err := queries.CheckDBUserIsGood(a.DB, cachedUser)
				if err != nil || !isAuthorized {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"success": false,
						"error":   "User not authorized",
					})
				}
				c.Set("user", cachedUser)
				return next(c)
			}

			// Validate with GitHub for new tokens
			githubUser, err := validateGitHubToken(c)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"success": false,
					"error":   "Invalid token",
				})
			}

			// Verify if user exists in database and is authorized
			_, isAuthorized, err := queries.CheckDBUserIsGood(a.DB, githubUser)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{
					"success": false,
					"error":   "Internal server error",
				})
			}

			if !isAuthorized {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"success": false,
					"error":   "User not authorized. Please login through the web interface first.",
				})
			}

			tokenCache.SetWithUser(token, token, githubUser, 15*time.Minute)
			c.Set("user", githubUser)
			return next(c)
		}
	}
}

var sessionUpdateCache sync.Map

func shouldUpdateSession(token string) bool {
	key := token
	now := time.Now()

	if lastUpdate, exists := sessionUpdateCache.Load(key); exists {
		if now.Sub(lastUpdate.(time.Time)) < 5*time.Minute {
			logger.Debug("skipping session update", "reason", "too_recent")
			return false
		}
	}

	sessionUpdateCache.Store(key, now)
	logger.Debug("session update required")
	return true
}

func validateGitHubToken(c echo.Context) (*GithubUserInfo, error) {
	requestID := c.Response().Header().Get(echo.HeaderXRequestID)
	token := c.Request().Header.Get("Authorization")
	if token == "" {
		logger.Warn("missing token in validation request",
			"request_id", requestID)
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "Token is missing")
	}

	token = strings.TrimPrefix(token, "Bearer ")
	logger.Debug("validating GitHub token",
		"request_id", requestID)

	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		logger.Error("failed to create GitHub API request",
			"request_id", requestID,
			"error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		logger.Error("GitHub API request failed",
			"request_id", requestID,
			"error", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warn("invalid GitHub token",
			"request_id", requestID,
			"status", resp.Status)
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "Invalid token")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("failed to read GitHub response",
			"request_id", requestID,
			"error", err)
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var githubUser GithubUserInfo
	if err := json.Unmarshal(body, &githubUser); err != nil {
		logger.Error("failed to parse GitHub user data",
			"request_id", requestID,
			"error", err)
		return nil, fmt.Errorf("failed to parse user data: %w", err)
	}

	logger.Debug("GitHub user info fetched",
		"request_id", requestID,
		"user", githubUser.Login)

	emails, err := fetchGitHubUserEmails(token)
	if err != nil {
		logger.Error("failed to fetch GitHub user emails",
			"request_id", requestID,
			"user", githubUser.Login,
			"error", err)
		return nil, fmt.Errorf("failed to fetch user emails: %w", err)
	}
	githubUser.Emails = emails

	logger.Debug("completed GitHub user validation",
		"request_id", requestID,
		"user", githubUser.Login,
		"email_count", len(emails))

	return &githubUser, nil
}

func fetchGitHubUserEmails(token string) ([]string, error) {
	logger.Debug("fetching GitHub user emails")

	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://api.github.com/user/emails", nil)
	if err != nil {
		logger.Error("failed to create email request",
			"error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		logger.Error("failed to send email request",
			"error", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warn("failed to fetch emails",
			"status", resp.Status)
		return nil, fmt.Errorf("failed to fetch user emails: %s", resp.Status)
	}

	var emailsResp []struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emailsResp); err != nil {
		logger.Error("failed to parse email response",
			"error", err)
		return nil, fmt.Errorf("failed to parse email response: %w", err)
	}

	emails := make([]string, len(emailsResp))
	for i, e := range emailsResp {
		emails[i] = e.Email
	}

	logger.Debug("fetched GitHub emails successfully",
		"email_count", len(emails))
	return emails, nil
}
