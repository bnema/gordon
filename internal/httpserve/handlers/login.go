package handlers

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/sessions"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

// --- JWT Claims ---
type JwtCustomClaims struct {
	AccountID string `json:"account_id"`
	jwt.RegisteredClaims
}

// StartOAuthGithub starts the Github OAuth flow.
func StartOAuthGithub(c echo.Context, a *server.App) error {
	logger.Info("Attempting to get an existing session")
	sess, err := getSession(c)
	if err != nil || sess == nil || sess.Values == nil {
		logger.Info("Session retrieval failed, initiating new GitHub OAuth flow")
		return initiateGithubOAuthFlow(c, a)
	}

	logger.Info("Attempting to assert session values")
	sessionID, ok1 := sess.Values["sessionID"].(string)
	accountID, ok2 := sess.Values["accountID"].(string)
	authenticated, ok3 := sess.Values["authenticated"].(bool)

	if !ok1 || !ok2 || !ok3 || !authenticated {
		logger.Info("Session assertion failed, or user not authenticated. Initiating new OAuth flow")
		return initiateGithubOAuthFlow(c, a)
	}

	logger.Info("Checking if session exists in DB")
	sessionExists, err := queries.CheckDBSessionExists(a.DB, sessionID)
	if err != nil {
		logger.Info("Error checking if session exists", "error", err)
		return fmt.Errorf("could not check if session exists: %w", err)
	}

	if !sessionExists {
		logger.Info("Session does not exist in DB, initiating new OAuth flow")
		return initiateGithubOAuthFlow(c, a)
	}

	logger.Info("Getting the expiration time of the session")
	currentTime := time.Now()
	sessionExpiration, err := queries.GetSessionExpiration(a.DB, accountID, sessionID, currentTime)
	if err != nil {
		logger.Info("Error getting session expiration", "error", err)
		return fmt.Errorf("could not get session expiration: %w", err)
	}

	if currentTime.After(sessionExpiration) {
		logger.Info("Current time is after session expiration, initiating new OAuth flow")
		return initiateGithubOAuthFlow(c, a)
	}

	logger.Info("Extending session expiration time by 24 hours")
	newExpirationTime := currentTime.Add(time.Hour * 24)
	err = queries.ExtendSessionExpiration(a.DB, accountID, sessionID, newExpirationTime)
	if err != nil {
		logger.Info("Error extending session expiration", "error", err)
		return fmt.Errorf("could not extend session expiration: %w", err)
	}

	// Explicitly mark the session as online since we've validated it
	logger.Info("Marking existing session as online", "sessionID", sessionID)
	err = queries.MarkSessionActive(a.DB, sessionID)
	if err != nil {
		logger.Error("Failed to mark session as online", "error", err, "sessionID", sessionID)
		return fmt.Errorf("failed to mark session online: %w", err)
	}

	logger.Info("Session is valid and extended. Redirecting to admin panel")
	return c.Redirect(http.StatusFound, a.Config.Admin.Path)
}

// initiateGithubOAuthFlow starts a new GitHub OAuth flow
func initiateGithubOAuthFlow(c echo.Context, a *server.App) error {
	clientID := "" // IDK why it needs that even empty, but it does
	// Use the corrected generation function
	redirectDomain := a.GenerateOauthCallbackURL()

	encodedState := base64.StdEncoding.EncodeToString([]byte("redirectDomain:" + redirectDomain))

	// Redirect to GitHub's OAuth endpoint via the proxy
	proxyURL := a.Config.Build.ProxyURL
	oauthURL := fmt.Sprintf(
		"%s/github/authorize?client_id=%s&redirect_uri=%s&state=%s",
		proxyURL,
		clientID,
		redirectDomain, // Pass the correctly generated public URL to the proxy
		encodedState,
	)
	logger.Debug("Initiating OAuth flow, redirecting to proxy", "proxy_url", oauthURL)
	return c.Redirect(http.StatusFound, oauthURL)
}

type Sessions struct {
	*db.Sessions
}

// OAuthCallback handles the callback response from Github OAuth
func OAuthCallback(c echo.Context, a *server.App) error {
	logger.Info("Starting OAuth callback handler")
	redirectPath := a.Config.Admin.Path

	accessToken, encodedState, err := parseQueryParams(c)
	if err != nil {
		logger.Error("Failed to parse query params from OAuth callback", "error", err)
		return c.String(http.StatusBadRequest, err.Error())
	}
	logger.Debug("Parsed access token and state from query params", "token_length", len(accessToken), "encoded_state", encodedState)

	// update the struct with the new access token
	a.DBTables.Sessions.AccessToken = accessToken

	// Compare the state parameter with the correctly generated redirect domain
	redirectDomain := a.GenerateOauthCallbackURL()
	encodedRedirectDomain := base64.StdEncoding.EncodeToString([]byte("redirectDomain:" + redirectDomain))
	if encodedState != encodedRedirectDomain {
		logger.Error("Invalid state parameter", "expected", encodedRedirectDomain, "received", encodedState)
		return c.String(http.StatusBadRequest, "invalid state parameter")
	}
	logger.Debug("State parameter validation successful")

	sess, err := getSession(c)
	if err != nil {
		logger.Error("Failed to get session", "error", err)
		return err
	}
	logger.Debug("Session retrieved successfully")

	browserInfo := c.Request().UserAgent()
	userInfo, err := githubGetUserDetails(c)
	if err != nil {
		logger.Error("Failed to get GitHub user details", "error", err)
		return err
	}
	logger.Debug("Retrieved GitHub user details", "login", userInfo.Login, "email_count", len(userInfo.Emails))

	user, accountID, session, err := handleUser(a, accessToken, browserInfo, userInfo)
	if err != nil {
		logger.Error("Failed to handle user during OAuth callback", "error", err)

		// --- MODIFIED: Redirect to login with specific error based on err type ---
		loginPath := a.Config.Admin.Path + "/login"
		errorParam := "error=unknown_error" // Default error

		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.Code {
			case http.StatusUnauthorized:
				// Check the message to differentiate unauthorized reasons
				if strings.Contains(httpErr.Message.(string), "does not match the allowed email") {
					errorParam = "error=email_mismatch"
				} else if strings.Contains(httpErr.Message.(string), "no public email address") {
					errorParam = "error=no_email"
				} else {
					errorParam = "error=unauthorized"
				}
			case http.StatusInternalServerError:
				// Check for specific internal errors if needed, e.g., config issue
				if strings.Contains(httpErr.Message.(string), "Admin email not set") {
					errorParam = "error=config_email_missing"
				} else {
					errorParam = "error=server_error"
				}
			default:
				errorParam = "error=server_error" // Fallback for other HTTP errors
			}
		} else {
			// Handle non-HTTP errors (like DB connection issues, etc.)
			errorParam = "error=server_error"
		}

		redirectURL := loginPath + "?" + errorParam
		logger.Debug("Redirecting user to login page with error", "url", redirectURL)
		return c.Redirect(http.StatusSeeOther, redirectURL)
		// --- END MODIFICATION ---
	}
	logger.Info("User handled successfully", "user_id", user.ID, "account_id", accountID)

	logger.Debug("Before setting session values", "account_id", accountID, "session", session)
	err = setSessionValues(c, sess, accountID, session)
	if err != nil {
		// Error is already logged within setSessionValues
		// Redirect to login page with a server error message
		return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login?error=server_error")
	}
	// Log success ONLY after setSessionValues returns without error
	logger.Info("Session values set and saved successfully")

	logger.Info("OAuth callback successful, redirecting to", "path", redirectPath)
	return c.Redirect(http.StatusFound, redirectPath)
}

// parseQueryParams parses the query parameters from the callback URL
func parseQueryParams(c echo.Context) (string, string, error) {
	accessToken := c.QueryParam("access_token")
	encodedState := c.QueryParam("state")

	_, err := base64.StdEncoding.DecodeString(encodedState)
	if err != nil {
		return "", "", fmt.Errorf("invalid state parameter")
	}

	return accessToken, encodedState, nil
}

// getSession gets the session from the context
func getSession(c echo.Context) (*sessions.Session, error) {
	sess, err := session.Get("session", c)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "Could not get session")
	}
	return sess, nil
}

// handleUser handles the user creation or update
func handleUser(a *server.App, accessToken, browserInfo string, userInfo *db.GithubUserInfo) (*db.User, string, *db.Sessions, error) {
	logger.Debug("Starting handleUser function", "github_login", userInfo.Login)

	// --- Check provider count ---
	providerCount, err := queries.CountProviders(a.DB)
	if err != nil {
		logger.Error("Error checking provider count", "error", err)
		return nil, "", nil, fmt.Errorf("could not check provider count: %w", err)
	}
	logger.Debug("Provider count check result", "count", providerCount)

	// --- Case 1: First ever GitHub login ---
	if providerCount == 0 {
		logger.Info("First GitHub login detected. Linking to seeded admin account.")

		// --- Link to Seeded Admin ---
		// No token check needed anymore, proceed directly to linking.
		seededUser, seededAccountID, session, err := linkProviderToSeededAdmin(a, accessToken, browserInfo, userInfo)
		if err != nil {
			logger.Error("Failed to link provider to seeded admin", "error", err)
			return nil, "", nil, err
		}
		logger.Info("Successfully linked GitHub provider to seeded admin account", "user_id", seededUser.ID, "account_id", seededAccountID)
		return seededUser, seededAccountID, session, nil
	}

	// --- Case 2: Subsequent GitHub logins ---
	if providerCount > 0 {
		logger.Debug("Existing provider(s) found. Checking if incoming GitHub user matches.")

		// --- ADDED: Check if incoming GitHub user email matches configured email ---
		configuredEmail := a.Config.ReverseProxy.Email
		if configuredEmail == "" {
			logger.Error("Configuration Error: ReverseProxy.Email is not set in config.yml. Cannot validate user email.")
			// Prevent login if the email isn't configured, as it's required for validation.
			return nil, "", nil, echo.NewHTTPError(http.StatusInternalServerError, "Server configuration error: Admin email not set.")
		}

		githubEmail := ""
		if len(userInfo.Emails) > 0 {
			githubEmail = userInfo.Emails[0] // Assuming the first email is primary
		}

		if githubEmail == "" {
			logger.Warn("Unauthorized GitHub login attempt: User has no public email address on GitHub.", "github_login", userInfo.Login)
			return nil, "", nil, echo.NewHTTPError(http.StatusUnauthorized, "GitHub account must have a public primary email address.")
		}

		if !strings.EqualFold(githubEmail, configuredEmail) {
			logger.Warn("Unauthorized GitHub login attempt: Email mismatch.",
				"github_login", userInfo.Login,
				"github_email", githubEmail,
				"configured_email", configuredEmail)
			return nil, "", nil, echo.NewHTTPError(http.StatusUnauthorized, fmt.Sprintf("This GitHub account's email (%s) does not match the allowed email.", githubEmail))
		}
		logger.Debug("GitHub user email matches configured admin email.", "email", githubEmail)
		// --- END: Email Check ---

		// --- Check if incoming GitHub user matches an existing provider ---
		existingUser, existingAccountID, err := queries.GetUserByProviderLogin(a.DB, "github", userInfo.Login)
		if err != nil {
			// Handle sql.ErrNoRows specifically: means this GitHub user is not linked yet.
			if err == sql.ErrNoRows {
				logger.Warn("Unauthorized GitHub user trying to log in (no matching provider found).", "github_login", userInfo.Login)
				// For now, deny access. Future: could allow multiple users if desired.
				return nil, "", nil, echo.NewHTTPError(http.StatusUnauthorized, "This GitHub account is not authorized.")
			}
			// Handle other potential database errors
			logger.Error("Error checking for existing provider by login", "error", err)
			return nil, "", nil, fmt.Errorf("could not check existing provider: %w", err)
		}
		logger.Debug("Incoming GitHub login matches existing user.", "github_login", userInfo.Login, "user_id", existingUser.ID, "account_id", existingAccountID)

		// --- Update Session for Existing User ---
		// The user is valid and matches the existing provider, just update their session.
		session, err := queries.CreateOrUpdateSession(a.DB, existingAccountID, accessToken, browserInfo)
		if err != nil {
			logger.Error("Could not create/update session for existing user", "error", err)
			return nil, "", nil, fmt.Errorf("could not update session: %w", err)
		}
		logger.Info("Successfully created/updated session for existing user", "user_id", existingUser.ID, "account_id", existingAccountID)
		return existingUser, existingAccountID, session, nil
	}

	// Should not be reached if logic is correct
	logger.Error("Could not handle user - reached end of function unexpectedly", "provider_count", providerCount)
	return nil, "", nil, fmt.Errorf("could not handle user")
}

// --- NEW FUNCTION: linkProviderToSeededAdmin ---
// Finds the seeded admin, updates its details, creates the provider and session.
func linkProviderToSeededAdmin(a *server.App, accessToken, browserInfo string, userInfo *db.GithubUserInfo) (*db.User, string, *db.Sessions, error) {
	logger.Debug("Starting linkProviderToSeededAdmin", "github_login", userInfo.Login)

	// 1. Get the seeded user and account ID (assuming it's the only one)
	seededUser, seededAccountID, err := queries.GetFirstUserAndAccount(a.DB)
	if err != nil {
		logger.Error("Failed to get seeded user/account", "error", err)
		return nil, "", nil, fmt.Errorf("critical: could not find seeded admin account: %w", err)
	}
	logger.Debug("Found seeded admin", "user_id", seededUser.ID, "account_id", seededAccountID, "original_email", seededUser.Email)

	// 2. Update the seeded user's details with GitHub info
	newEmail := seededUser.Email // Keep original unless GitHub provides one
	if len(userInfo.Emails) > 0 && userInfo.Emails[0] != "" {
		newEmail = userInfo.Emails[0]
	}
	newName := userInfo.Login

	err = queries.UpdateUserDetails(a.DB, seededUser.ID, newName, newEmail)
	if err != nil {
		logger.Error("Failed to update seeded user details", "user_id", seededUser.ID, "error", err)
		return nil, "", nil, fmt.Errorf("could not update seeded user details: %w", err)
	}
	logger.Info("Updated seeded user details", "user_id", seededUser.ID, "new_name", newName, "new_email", newEmail)
	seededUser.Name = newName
	seededUser.Email = newEmail

	// 3. Create the Provider record linked to the seeded account
	err = queries.InsertProviderForAccount(a.DB, seededAccountID, "github", userInfo)
	if err != nil {
		logger.Error("Failed to insert provider record for seeded account", "account_id", seededAccountID, "error", err)
		return nil, "", nil, fmt.Errorf("could not insert provider record: %w", err)
	}
	logger.Info("Inserted provider record", "account_id", seededAccountID, "provider", "github", "login", userInfo.Login)

	// 4. Create/Update the Session record linked to the seeded account
	session, err := queries.CreateOrUpdateSession(a.DB, seededAccountID, accessToken, browserInfo)
	if err != nil {
		logger.Error("Failed to create/update session for seeded account", "account_id", seededAccountID, "error", err)
		return nil, "", nil, fmt.Errorf("could not create/update session: %w", err)
	}
	logger.Info("Created/Updated session record", "account_id", seededAccountID, "session_id", session.ID)

	return seededUser, seededAccountID, session, nil
}

// setSessionValues sets the session values for the user
func setSessionValues(c echo.Context, sess *sessions.Session, accountID string, session *db.Sessions) error {
	sess.Values["authenticated"] = true
	sess.Values["accountID"] = accountID
	sess.Values["expires"] = session.Expires
	sess.Values["sessionID"] = session.ID
	sess.Values["isActive"] = session.IsActive

	if err := sess.Save(c.Request(), c.Response()); err != nil {
		logger.Error("Failed to save session cookie", "error", err, "sessionID", session.ID)
		return fmt.Errorf("could not save session: %w", err)
	}
	return nil
}

// fetchGithubAPI fetches the Github API
func fetchGithubAPI(url string, authHeader string, result interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("got status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	err = json.Unmarshal(body, &result)
	if err != nil {
		return err
	}

	return nil
}

// githubGetUserDetails gets the user details from the Github API using the access token
func githubGetUserDetails(c echo.Context) (userInfo *db.GithubUserInfo, err error) {
	accessToken := c.QueryParam("access_token")

	// Fetch user info
	err = fetchGithubAPI("https://api.github.com/user", "Bearer "+accessToken, &userInfo)
	if err != nil {
		return nil, err
	}
	// Fetch user emails
	var emailObjects []struct {
		Email string `json:"email"`
	}
	err = fetchGithubAPI("https://api.github.com/user/emails", "Bearer "+accessToken, &emailObjects)
	if err != nil {
		return nil, err
	}

	// Extract emails and populate the Emails field in userInfo
	for _, emailObj := range emailObjects {
		userInfo.Emails = append(userInfo.Emails, emailObj.Email)
	}

	return userInfo, nil
}

func DeviceCodeRequest(c echo.Context, a *server.App) error {
	proxyURL := a.Config.Build.ProxyURL
	resp, err := http.Post(proxyURL+"/github/device/code", "application/json", nil)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return c.JSON(resp.StatusCode, map[string]string{"error": string(body)})
	}

	var deviceCode map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&deviceCode); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to decode response"})
	}

	return c.JSON(http.StatusOK, deviceCode)
}

func DeviceTokenRequest(c echo.Context, a *server.App) error {
	proxyURL := a.Config.Build.ProxyURL
	deviceCode := c.FormValue("device_code")

	resp, err := http.PostForm(proxyURL+"/github/device/token", url.Values{
		"device_code": {deviceCode},
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return c.JSON(resp.StatusCode, map[string]string{"error": string(body)})
	}

	var tokenResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to decode response"})
	}

	// --- Start JWT Generation ---
	// Extract the access token from the response
	githubAccessToken, ok := tokenResp["access_token"].(string)
	if !ok || githubAccessToken == "" {
		logger.Error("GitHub access token not found or invalid in device token response", "response", tokenResp)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to retrieve GitHub token"})
	}
	logger.Debug("Retrieved GitHub access token via device flow")

	// Get GitHub user details using the access token
	userInfo, err := githubGetUserDetailsWithToken(githubAccessToken)
	if err != nil {
		logger.Error("Failed to get GitHub user details using device flow token", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to fetch GitHub user details"})
	}
	logger.Debug("Retrieved GitHub user details via device flow", "login", userInfo.Login)

	// Find the user in the database based on GitHub login
	_, accountID, err := queries.GetUserByProviderLogin(a.DB, "github", userInfo.Login)
	if err != nil {
		if err == sql.ErrNoRows {
			logger.Warn("Unauthorized GitHub user trying to get CLI token via device flow.", "github_login", userInfo.Login)
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "This GitHub account is not authorized."})
		}
		logger.Error("Error checking for existing provider by login during device flow", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Database error during user lookup"})
	}
	logger.Info("Found matching user for device flow", "account_id", accountID, "github_login", userInfo.Login)

	// Generate JWT
	jwtToken, err := generateJWT(accountID, a.Config.General.JwtToken)
	if err != nil {
		logger.Error("Failed to generate JWT for device flow", "error", err, "account_id", accountID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate authentication token"})
	}
	logger.Info("Generated JWT for CLI device flow", "account_id", accountID)

	// Return the JWT
	return c.JSON(http.StatusOK, map[string]string{"jwt_token": jwtToken})
	// --- End JWT Generation ---
}

// githubGetUserDetailsWithToken gets user details using a provided token
// (Similar to githubGetUserDetails but takes token as argument)
func githubGetUserDetailsWithToken(accessToken string) (*db.GithubUserInfo, error) {
	var userInfo *db.GithubUserInfo // Initialize userInfo

	// Fetch user info
	err := fetchGithubAPI("https://api.github.com/user", "Bearer "+accessToken, &userInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user info: %w", err)
	}
	if userInfo == nil {
		return nil, fmt.Errorf("received nil user info from GitHub API")
	}

	// Fetch user emails
	var emailObjects []struct {
		Email string `json:"email"`
	}
	err = fetchGithubAPI("https://api.github.com/user/emails", "Bearer "+accessToken, &emailObjects)
	if err != nil {
		// Log email fetch error but continue, email might not be needed or primary
		logger.Warn("Failed to fetch user emails from GitHub API", "error", err)
	} else {
		// Extract emails and populate the Emails field in userInfo
		userInfo.Emails = []string{} // Ensure Emails slice is initialized
		for _, emailObj := range emailObjects {
			userInfo.Emails = append(userInfo.Emails, emailObj.Email)
		}
	}

	return userInfo, nil
}

// generateJWT creates a new JWT token for the given account ID
func generateJWT(accountID string, secret string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("JWT secret is not configured")
	}
	// Set custom claims
	claims := &JwtCustomClaims{
		AccountID: accountID,
		RegisteredClaims: jwt.RegisteredClaims{
			// Set token expiration (e.g., 24 hours)
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour * 24 * 30)), // 30 days validity for CLI token
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "gordon",
			Subject:   "cli_auth",
		},
	}

	// Create token with claims
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Generate encoded token and return it
	t, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT token: %w", err)
	}

	return t, nil
}
