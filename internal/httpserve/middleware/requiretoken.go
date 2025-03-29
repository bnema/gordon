package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"errors"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

// --- JWT Claims (copied from handlers/login.go for middleware use) ---
// Consider moving this to a shared location if used in more places.
type JwtCustomClaims struct {
	AccountID string `json:"account_id"`
	jwt.RegisteredClaims
}

// RequireToken validates JWT tokens for API requests
func RequireToken(a *server.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				logger.Warn("API request missing Authorization header")
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Missing authorization token"})
			}

			tokenString := strings.TrimPrefix(authHeader, "Bearer ")
			if tokenString == authHeader { // No "Bearer " prefix found
				logger.Warn("API request Authorization header malformed", "header", authHeader)
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Malformed authorization token"})
			}

			if a.Config.General.JwtToken == "" {
				logger.Error("JWT Secret is not configured, cannot validate token")
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Server configuration error"})
			}

			// Parse and validate the token
			token, err := jwt.ParseWithClaims(tokenString, &JwtCustomClaims{}, func(token *jwt.Token) (interface{}, error) {
				// Validate the alg is what we expect:
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
				}
				return []byte(a.Config.General.JwtToken), nil
			})

			if err != nil {
				logger.Warn("Invalid JWT token received", "error", err)
				if errors.Is(err, jwt.ErrTokenMalformed) {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Malformed token"})
				} else if errors.Is(err, jwt.ErrTokenExpired) || errors.Is(err, jwt.ErrTokenNotValidYet) {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Token expired or not yet valid"})
				} else {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid token"})
				}
			}

			// Check if claims are valid and extract AccountID
			if claims, ok := token.Claims.(*JwtCustomClaims); ok && token.Valid {
				if claims.AccountID == "" {
					logger.Warn("JWT token is valid but missing AccountID claim")
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid token claims"})
				}

				// --- Optional: Check if AccountID exists and is active in DB ---
				accountExists, err := queries.CheckDBAccountExists(a.DB, claims.AccountID) // Corrected function name
				if err != nil {
					logger.Error("Database error checking account existence during token validation", "account_id", claims.AccountID, "error", err)
					return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				}
				if !accountExists {
					logger.Warn("JWT token validated for non-existent account", "account_id", claims.AccountID)
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Account not found"})
				}
				// --- End Optional DB Check ---

				logger.Debug("JWT token validated successfully", "account_id", claims.AccountID)
				// Set account ID in context for downstream handlers
				c.Set("accountID", claims.AccountID)
				return next(c)
			}

			logger.Warn("Invalid JWT token claims or token invalid")
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid token"})
		}
	}
}

// Removed unused functions: validateGitHubToken, fetchGitHubUserEmails, shouldUpdateSession, sessionUpdateCache
