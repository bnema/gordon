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
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

type GithubUserInfo = queries.GithubUserInfo

// middleware/require_token.go
func RequireToken(a *server.App) echo.MiddlewareFunc {
	tokenCache := auth.GetTokenCache()

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token := strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
			if token == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "Token is missing",
				})
			}

			// Check cache first
			if _, cachedUser, found := tokenCache.GetWithUser(token); found {
				c.Set("user", cachedUser)
				return next(c)
			}

			// Validate with GitHub only for new tokens
			githubUser, err := validateGitHubToken(c)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "Invalid token",
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
			log.Debug("skipping session update", "reason", "too_recent")
			return false
		}
	}

	sessionUpdateCache.Store(key, now)
	log.Debug("session update required")
	return true
}

func validateGitHubToken(c echo.Context) (*GithubUserInfo, error) {
	requestID := c.Response().Header().Get(echo.HeaderXRequestID)
	token := c.Request().Header.Get("Authorization")
	if token == "" {
		log.Warn("missing token in validation request",
			"request_id", requestID)
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "Token is missing")
	}

	token = strings.TrimPrefix(token, "Bearer ")
	log.Debug("validating GitHub token",
		"request_id", requestID)

	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		log.Error("failed to create GitHub API request",
			"request_id", requestID,
			"error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		log.Error("GitHub API request failed",
			"request_id", requestID,
			"error", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn("invalid GitHub token",
			"request_id", requestID,
			"status", resp.Status)
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "Invalid token")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("failed to read GitHub response",
			"request_id", requestID,
			"error", err)
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var githubUser GithubUserInfo
	if err := json.Unmarshal(body, &githubUser); err != nil {
		log.Error("failed to parse GitHub user data",
			"request_id", requestID,
			"error", err)
		return nil, fmt.Errorf("failed to parse user data: %w", err)
	}

	log.Debug("GitHub user info fetched",
		"request_id", requestID,
		"user", githubUser.Login)

	emails, err := fetchGitHubUserEmails(token)
	if err != nil {
		log.Error("failed to fetch GitHub user emails",
			"request_id", requestID,
			"user", githubUser.Login,
			"error", err)
		return nil, fmt.Errorf("failed to fetch user emails: %w", err)
	}
	githubUser.Emails = emails

	log.Debug("completed GitHub user validation",
		"request_id", requestID,
		"user", githubUser.Login,
		"email_count", len(emails))

	return &githubUser, nil
}

func fetchGitHubUserEmails(token string) ([]string, error) {
	log.Debug("fetching GitHub user emails")

	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://api.github.com/user/emails", nil)
	if err != nil {
		log.Error("failed to create email request",
			"error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		log.Error("failed to send email request",
			"error", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn("failed to fetch emails",
			"status", resp.Status)
		return nil, fmt.Errorf("failed to fetch user emails: %s", resp.Status)
	}

	var emailsResp []struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emailsResp); err != nil {
		log.Error("failed to parse email response",
			"error", err)
		return nil, fmt.Errorf("failed to parse email response: %w", err)
	}

	emails := make([]string, len(emailsResp))
	for i, e := range emailsResp {
		emails[i] = e.Email
	}

	log.Debug("fetched GitHub emails successfully",
		"email_count", len(emails))
	return emails, nil
}
