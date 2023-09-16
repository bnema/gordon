package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/webui"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/markbates/goth/gothic"
)

// RenderLoginPage renders the login.html template
func RenderLoginPage(c echo.Context, a *app.App) error {
	lang := c.Get(middleware.LangKey).(string)
	yamlData := webui.StringsYamlData{}
	err := webui.ReadStringsDataFromYAML(lang, a.TemplateFS, "strings.yml", &yamlData)
	if err != nil {
		return err
	}
	// Navigate inside the fs.FS to get the template
	path := "html/login"
	rendererData, err := render.GetHTMLRenderer(path, "index.gohtml", a.TemplateFS, a)
	if err != nil {
		return err
	}

	// Create a data map to pass to the renderer
	data := map[string]interface{}{
		"CurrentLang": yamlData.CurrentLang,
		// "BuildVersion" will be automatically added in the renderer
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}

	return c.HTML(200, renderedHTML)
}

func StartOAuthGithub(c echo.Context, a *app.App) error {
	clientID := os.Getenv("GITHUB_CLIENTID")
	redirectURI := a.OauthCallbackURL // Make sure this URL is registered in your GitHub OAuth App
	fmt.Print(a.OauthCallbackURL)
	// Define the scopes you need; for example, "repo" and "admin:repo_hook" for repository and webhook actions
	scopes := "repo,admin:repo_hook"

	// Redirect to GitHub's OAuth page
	oauthURL := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=%s",
		clientID,
		redirectURI,
		scopes,
	)
	return c.Redirect(http.StatusFound, oauthURL)
}

func OAuthCallback(c echo.Context, a *app.App) error {
	clientID := os.Getenv("GITHUB_CLIENTID")
	clientSecret := os.Getenv("GITHUB_TOKEN")
	code := c.QueryParam("code")

	if code == "" {
		return c.JSON(http.StatusBadRequest, "Missing code")
	}

	// Create POST request to exchange code for access token
	payload := url.Values{}
	payload.Set("client_id", clientID)
	payload.Set("client_secret", clientSecret)
	payload.Set("code", code)

	resp, err := http.PostForm("https://github.com/login/oauth/access_token", payload)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, "Failed to get access token")
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return c.JSON(http.StatusInternalServerError, "Received non-200 status code")
	}

	// Parse the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, "Failed to read access token")
	}

	// Parse the access token from the response body
	accessToken := string(body)

	parsedQuery, err := url.ParseQuery(accessToken)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, "Failed to parse access token")
	}

	actualToken := parsedQuery.Get("access_token")
	fmt.Println("Received access token:", actualToken)

	// Get the current session from echo-contrib/session
	sess, err := session.Get("session", c)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Could not get session")
	}

	// Set the user as authenticated
	sess.Values["authenticated"] = true
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
