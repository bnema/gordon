package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/gorilla/sessions"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

var urlToken string

// compareGordonToken compares the token from the URL query parameter with the one from the config.yml
func compareGordonToken(c echo.Context, a *app.App) error {
	configToken := a.Config.General.GordonToken
	if urlToken != configToken {
		// if token is not present or does not match the one from the config.yml
		return fmt.Errorf("token is not valid")
	}
	return nil
}

// RenderLoginPage renders the login.html template
func RenderLoginPage(c echo.Context, a *app.App) error {
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

// StartOAuthGithub starts the Github OAuth flow
func StartOAuthGithub(c echo.Context, a *app.App) error {
	// Check if the session is already here and valid before going for the oauth flow
	sess, err := getSession(c)
	if err != nil {
		return err
	}

	// Check if the session values are valid
	accountID, ok := sess.Values["accountID"].(string)
	if ok && accountID != "" {
		return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path) // StatusSeeOther is HTTP 303
	}

	//Initiate the Github OAuth flow
	clientID := os.Getenv("GITHUB_APP_ID")
	redirectDomain := a.Config.GenerateOauthCallbackURL()
	encodedState := base64.StdEncoding.EncodeToString([]byte("redirectDomain:" + redirectDomain))
	// Redirect to Gordon's Proxy to grab the oauth access
	oauthURL := fmt.Sprintf(
		"https://gordon.bnema.dev/github-proxy/authorize?client_id=%s&redirect_uri=%s&state=%s",
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
func OAuthCallback(c echo.Context, a *app.App) error {
	redirectPath := a.Config.Admin.Path

	accessToken, encodedState, err := parseQueryParams(c)
	if err != nil {
		return c.String(http.StatusBadRequest, err.Error())
	}

	// update the struct with the new access token
	a.DBTables.Sessions.AccessToken = accessToken

	// Compare the state parameter with the redirect domain
	redirectDomain := a.Config.GenerateOauthCallbackURL()
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
func handleUser(c echo.Context, a *app.App, accessToken, browserInfo string, userInfo *queries.GithubUserInfo) (*db.User, error) {
	userExists, err := queries.CheckDBUserExists(a)
	if err != nil {
		return nil, fmt.Errorf("could not check if user exists: %w", err)
	}

	if !userExists {
		// if it is a new user creation we compare the gordon token
		err := compareGordonToken(c, a)
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
func createUser(a *app.App, accessToken, browserInfo string, userInfo *queries.GithubUserInfo) (*db.User, error) {
	err := queries.CreateUser(a, accessToken, browserInfo, userInfo)
	if err != nil {
		return nil, fmt.Errorf("could not create user: %w", err)
	}
	return &a.DBTables.User, nil
}

// updateUser updates an existing user in the database
func updateUser(a *app.App, accessToken, browserInfo string, userInfo *queries.GithubUserInfo) (*db.User, error) {
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
func githubGetUserDetails(c echo.Context, a *app.App) (userInfo *queries.GithubUserInfo, err error) {
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
