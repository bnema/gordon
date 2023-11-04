package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/gorilla/sessions"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

var urlToken string

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

// RenderLoginPage renders the login.html template
func RenderLoginPage(c echo.Context, a *server.App) error {
	urlToken = c.QueryParam("token")
	data := map[string]interface{}{
		"Title": "Login",
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
	fmt.Println("Attempting to get an existing session.")
	sess, err := getSession(c)
	if err != nil || sess == nil || sess.Values == nil {
		fmt.Println("Session retrieval failed, initiating new GitHub OAuth flow.")
		return initiateGithubOAuthFlow(c, a)
	}

	fmt.Println("Attempting to assert session values.")
	sessionID, ok1 := sess.Values["sessionID"].(string)
	accountID, ok2 := sess.Values["accountID"].(string)
	authenticated, ok3 := sess.Values["authenticated"].(bool)

	if !ok1 || !ok2 || !ok3 || !authenticated {
		fmt.Println("Session assertion failed, or user not authenticated. Initiating new OAuth flow.")
		return initiateGithubOAuthFlow(c, a)
	}

	fmt.Println("Checking if session exists in DB.")
	sessionExists, err := queries.CheckDBSessionExists(a, sessionID)
	if err != nil {
		fmt.Printf("Error checking if session exists: %v\n", err)
		return fmt.Errorf("could not check if session exists: %w", err)
	}

	if !sessionExists {
		fmt.Println("Session does not exist in DB, initiating new OAuth flow.")
		return initiateGithubOAuthFlow(c, a)
	}

	fmt.Println("Getting the expiration time of the session.")
	currentTime := time.Now()
	sessionExpiration, err := queries.GetSessionExpiration(a, accountID, sessionID, currentTime)
	if err != nil {
		fmt.Printf("Error getting session expiration: %v\n", err)
		return fmt.Errorf("could not get session expiration: %w", err)
	}

	if currentTime.After(sessionExpiration) {
		fmt.Println("Current time is after session expiration, initiating new OAuth flow.")
		return initiateGithubOAuthFlow(c, a)
	}

	fmt.Println("Extending session expiration time by 30 minutes.")
	newExpirationTime := currentTime.Add(30 * time.Minute)
	err = queries.ExtendSessionExpiration(a, accountID, sessionID, newExpirationTime)
	if err != nil {
		fmt.Printf("Error extending session expiration: %v\n", err)
		return fmt.Errorf("could not extend session expiration: %w", err)
	}

	fmt.Println("Session is valid and extended. Redirecting to admin panel.")
	return c.Redirect(http.StatusFound, a.Config.Admin.Path)
}

// initiateGithubOAuthFlow starts a new GitHub OAuth flow
func initiateGithubOAuthFlow(c echo.Context, a *server.App) error {
	clientID := os.Getenv("GITHUB_APP_ID")
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
	userInfo, err := githubGetUserDetails(c, a)
	if err != nil {
		return err
	}

	user, err := handleUser(c, a, accessToken, browserInfo, userInfo)
	if err != nil {
		return err
	}

	err = setSessionValues(c, sess, user, a.DBTables.Account.ID, a.DBTables.Sessions.Expires, a.DBTables.Sessions.ID)
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
			return nil, echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
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
func setSessionValues(c echo.Context, sess *sessions.Session, user *db.User, accountID string, expires string, sessionID string) error {
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
func githubGetUserDetails(c echo.Context, a *server.App) (userInfo *queries.GithubUserInfo, err error) {
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
