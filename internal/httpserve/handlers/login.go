package handlers

import (
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

// ClLI :
// compareGordonToken compares the token from the URL query parameter with the one from the config.yml
func CompareGordonToken(c echo.Context, a *server.App) error {
	configToken, err := a.Config.GetToken()
	if err != nil {
		return err
	}

	if urlToken != configToken {
		return fmt.Errorf("token is empty or not valid")
	}

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
	sessionExists, err := queries.CheckDBSessionExists(a, sessionID)
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
	sessionExpiration, err := queries.GetSessionExpiration(a, accountID, sessionID, currentTime)
	if err != nil {
		logger.Info("Error getting session expiration", "error", err)
		return fmt.Errorf("could not get session expiration: %w", err)
	}

	if currentTime.After(sessionExpiration) {
		logger.Info("Current time is after session expiration, initiating new OAuth flow")
		return initiateGithubOAuthFlow(c, a)
	}

	logger.Info("Extending session expiration time by 30 minutes")
	newExpirationTime := currentTime.Add(30 * time.Minute)
	err = queries.ExtendSessionExpiration(a, accountID, sessionID, newExpirationTime)
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
	redirectPath := a.Config.Admin.Path

	accessToken, encodedState, err := parseQueryParams(c)
	if err != nil {
		return c.String(http.StatusBadRequest, err.Error())
	}

	// update the struct with the new access token
	a.DBTables.Sessions.AccessToken = accessToken

	// Compare the state parameter with the redirect domain
	redirectDomain := a.GenerateOauthCallbackURL()
	encodedRedirectDomain := base64.StdEncoding.EncodeToString([]byte("redirectDomain:" + redirectDomain))
	if encodedState != encodedRedirectDomain {
		return c.String(http.StatusBadRequest, "invalid state parameter")
	}

	sess, err := getSession(c)
	if err != nil {
		return err
	}

	browserInfo := c.Request().UserAgent()
	userInfo, err := githubGetUserDetails(c)
	if err != nil {
		return err
	}

	_, err = handleUser(c, a, accessToken, browserInfo, userInfo)
	if err != nil {
		return err
	}

	err = setSessionValues(c, sess, a.DBTables.Account.ID, a.DBTables.Sessions.Expires, a.DBTables.Sessions.ID)
	if err != nil {
		return err
	}

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
func handleUser(c echo.Context, a *server.App, accessToken, browserInfo string, userInfo *queries.GithubUserInfo) (*db.User, error) {
	userExists, err := queries.CheckDBUserExists(a)
	if err != nil {
		return nil, fmt.Errorf("could not check if user exists: %w", err)
	}

	if !userExists {
		// if it is a new user creation we compare the gordon token
		err := CompareGordonToken(c, a)
		if err != nil {
			return nil, echo.NewHTTPError(http.StatusUnauthorized, "Token is empty or not valid")
		}

		return createUser(a, accessToken, browserInfo, userInfo)
	}

	if userExists {
		// if it is an existing user we compare the login and email to see if it is the same user
		isGoodUser, err := queries.CheckDBUserIsGood(a, userInfo)
		if err != nil {
			return nil, fmt.Errorf("could not check if user is good: %w", err)
		}

		if !isGoodUser {
			return nil, echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized user")
		}

		if isGoodUser {
			// Update the user
			updatedUser, err := updateUser(a, accessToken, browserInfo, userInfo)
			if err != nil {
				return nil, fmt.Errorf("could not update user: %w", err)
			}
			return updatedUser, nil
		}
	}

	return nil, fmt.Errorf("could not handle user")
}

// createUser creates a new user in the database
func createUser(a *server.App, accessToken, browserInfo string, userInfo *queries.GithubUserInfo) (*db.User, error) {
	err := queries.CreateUser(a, accessToken, browserInfo, userInfo)
	if err != nil {
		return nil, fmt.Errorf("could not create user: %w", err)
	}
	return &a.DBTables.User, nil
}

// updateUser updates an existing user in the database
func updateUser(a *server.App, accessToken, browserInfo string, userInfo *queries.GithubUserInfo) (*db.User, error) {
	user, err := queries.UpdateUser(a, accessToken, browserInfo, userInfo)
	if err != nil {
		return nil, fmt.Errorf("could not update user: %w", err)
	}
	return user, nil
}

// setSessionValues sets the session values for the user
func setSessionValues(c echo.Context, sess *sessions.Session, accountID string, expires string, sessionID string) error {
	sess.Values["authenticated"] = true
	sess.Values["accountID"] = accountID
	sess.Values["expires"] = expires
	sess.Values["sessionID"] = sessionID

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
func githubGetUserDetails(c echo.Context) (userInfo *queries.GithubUserInfo, err error) {
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
