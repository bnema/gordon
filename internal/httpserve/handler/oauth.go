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
	"github.com/bnema/gordon/internal/templates/render"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

// compareGordonToken compares the token from the URL query parameter with the one from the config.yml
func compareGordonToken(c echo.Context, a *app.App) error {
	urlToken := c.QueryParam("token")
	configToken := a.Config.General.GordonToken
	if urlToken != configToken {
		return c.Redirect(http.StatusMovedPermanently, "/")
	}
	return nil
}

// RenderLoginPage renders the login.html template
func RenderLoginPage(c echo.Context, a *app.App) error {

	// Compare the token
	compareGordonToken(c, a)

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

func StartOAuthGithub(c echo.Context, a *app.App) error {
	// Clear the session in case it is already set or corrupted
	err := ResetSession(c)
	if err != nil {
		return err
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

func OAuthCallback(c echo.Context, a *app.App) error {

	accessToken := c.QueryParam("access_token")
	encodedState := c.QueryParam("state")

	// Decode the state parameter to validate the original redirectDomain
	_, err := base64.StdEncoding.DecodeString(encodedState)
	if err != nil {
		return c.String(http.StatusBadRequest, "Invalid state parameter")
	}

	sess, err := session.Get("session", c)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Could not get session")
	}
	// Set the user as authenticated
	sess.Values["authenticated"] = true
	sess.Values["access_token"] = accessToken
	sess.Values["expires"] = 604800
	if err = sess.Save(c.Request(), c.Response()); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Could not save session")
	}

	browserInfo := c.Request().UserAgent()
	// print github user info
	userInfo, err := githubGetUserDetails(c, a)
	if err != nil {
		return err
	}

	// Check if the user exists
	// if not, create it
	// if yes, update the access token and create or update the session
	// If the user does not exist, create it
	userExists, err := queries.CheckDBUserExists(a)
	if err != nil {
		return fmt.Errorf("could not check if user exists: %w", err)
	}
	if !userExists {
		err := queries.CreateUser(a, accessToken, browserInfo, userInfo)
		if err != nil {
			return fmt.Errorf("could not create user: %w", err)
		}
	} else {

	}
	return c.Redirect(http.StatusFound, "/admin")
}
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
