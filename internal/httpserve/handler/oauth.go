package handler

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/templates/render"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/markbates/goth/gothic"
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

// Check if there is already a user in the db which means that the admin is already setup
func checkAdmin(a *app.App) (bool, error) {
	var count int
	err := a.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	return false, nil
}

// RenderLoginPage renders the login.html template
func RenderLoginPage(c echo.Context, a *app.App) error {

	// Compare the token
	compareGordonToken(c, a)

	// Check if there is already an admin in the db we cannot add another one so we redirect to the login page with a message
	adminExists, err := checkAdmin(a)
	if err != nil {
		return err
	}

	if adminExists {
		return c.HTML(http.StatusOK, "<h1>User already exists</h1>")
	}

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

func OAuthCallback(c echo.Context, a *app.App) error {
	accessToken := c.QueryParam("access_token")
	encodedState := c.QueryParam("state")

	// Decode the state parameter to get the original redirectDomain
	decodedState, err := base64.StdEncoding.DecodeString(encodedState)
	if err != nil {
		return c.String(http.StatusBadRequest, "Invalid state parameter")
	}

	state := string(decodedState)
	parts := strings.SplitN(state, ":", 2)
	if len(parts) != 2 || parts[0] != "redirectDomain" {
		return c.String(http.StatusBadRequest, "Invalid state format")
	}

	// Retrieve the original redirectDomain
	redirectDomain := parts[1]
	fmt.Print(redirectDomain)
	// Now, you can use accessToken for whatever you need in your application

	// Also, you can save the authentication state in the session
	sess, err := session.Get("session", c)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Could not get session")
	}

	// Set the user as authenticated
	sess.Values["authenticated"] = true
	sess.Values["access_token"] = accessToken
	if err = sess.Save(c.Request(), c.Response()); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Could not save session")
	}

	return c.Redirect(http.StatusFound, "/admin")
}

// Logout handles user logout and clears session
func Logout(c echo.Context, a *app.App) error {
	gothic.Logout(c.Response(), c.Request())
	return c.Redirect(http.StatusMovedPermanently, "/")
}
