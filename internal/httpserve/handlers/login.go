package handlers

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/gorilla/sessions"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

var urlToken string

// compareGordonToken compares the token from the URL query parameter with the one from the config.yml
func CompareGordonToken(c echo.Context, a *server.App) error {
	logger.Debug("Starting token comparison")
	configToken, err := a.Config.GetToken()
	if err != nil {
		logger.Error("Error retrieving token from config", "error", err)
		return err
	}

	logger.Debug("Comparing tokens", "url_token_exists", urlToken != "", "config_token_exists", configToken != "")
	if urlToken != configToken {
		logger.Warn("Token mismatch or empty", "url_token_empty", urlToken == "")
		return fmt.Errorf("token is empty or not valid")
	}

	logger.Debug("Token validation successful")
	return nil
}

// HandleTokenSubmission handles the token form submission
func HandleTokenSubmission(c echo.Context, a *server.App) error {
	// Get the token from the form
	formToken := c.FormValue("token")
	if formToken == "" {
		return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login?error=empty_token")
	}

	// Store the token in the urlToken variable for compatibility with existing code
	urlToken = formToken

	// Validate the token
	configToken, err := a.Config.GetToken()
	if err != nil {
		logger.Debug("Error getting token from config", "error", err)
		return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login?error=server_error")
	}

	if urlToken != configToken {
		logger.Debug("Invalid token provided", "token", urlToken)
		return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login?error=invalid_token")
	}

	// Redirect to GitHub OAuth
	return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login/oauth/github")
}

// RenderLoginPage renders the login.html template
func RenderLoginPage(c echo.Context, a *server.App) error {
	// Check for token in query param for backward compatibility
	urlToken = c.QueryParam("token")

	// Check for error message
	errorMsg := c.QueryParam("error")

	data := map[string]interface{}{
		"Title": "Login",
		"Error": errorMsg,
	}

	// Navigate inside the fs.FS to get the template
	path := "html/login"
	rendererData, err := render.GetHTMLRenderer(path, "index.gohtml", a.TemplateFS, a)
	if err != nil {
		return err
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}

	return c.HTML(200, renderedHTML)
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

	logger.Info("Session is valid and extended. Redirecting to admin panel")
	return c.Redirect(http.StatusFound, a.Config.Admin.Path)
}

// initiateGithubOAuthFlow starts a new GitHub OAuth flow
func initiateGithubOAuthFlow(c echo.Context, a *server.App) error {
	clientID := "" // IDK why it needs that even empty, but it does
	redirectDomain := a.GenerateOauthCallbackURL()
	encodedState := base64.StdEncoding.EncodeToString([]byte("redirectDomain:" + redirectDomain))

	// Redirect to GitHub's OAuth endpoint to grab the OAuth access
	proxyURL := a.Config.Build.ProxyURL
	oauthURL := fmt.Sprintf(
		"%s/github/authorize?client_id=%s&redirect_uri=%s&state=%s",
		proxyURL,
		clientID,
		redirectDomain,
		encodedState,
	)
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

	// Compare the state parameter with the redirect domain
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

	user, accountID, session, err := handleUser(c, a, accessToken, browserInfo, userInfo)
	if err != nil {
		logger.Error("Failed to handle user", "error", err)
		return err
	}
	logger.Info("User handled successfully", "user_id", user.ID, "account_id", accountID)

	logger.Debug("Before setting session values", "account_id", accountID, "session", session)
	err = setSessionValues(c, sess, accountID, session)
	if err != nil {
		logger.Error("Failed to set session values", "error", err)
		return err
	}
	logger.Info("Session values set successfully")

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
func handleUser(c echo.Context, a *server.App, accessToken, browserInfo string, userInfo *db.GithubUserInfo) (*db.User, string, *db.Sessions, error) {
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

		// Compare the Gordon token (still need admin token for the *very first* setup)
		err := CompareGordonToken(c, a)
		if err != nil {
			logger.Error("Admin token comparison failed for first login", "error", err)
			return nil, "", nil, echo.NewHTTPError(http.StatusUnauthorized, "Admin token is empty or not valid for initial setup")
		}
		logger.Debug("Admin token comparison successful for first login.")

		// --- Link to Seeded Admin ---
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
		logger.Debug("Existing provider(s) found. Checking if incoming GitHub login matches.")

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
	sess.Values["isOnline"] = session.IsOnline

	if err := sess.Save(c.Request(), c.Response()); err != nil {
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

	return c.JSON(http.StatusOK, tokenResp)
}
