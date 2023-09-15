package handler

import (
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/webui"
	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
	"github.com/markbates/goth/providers/github"
	"net/http"
)

var providerNames []string

func init() {
	goth.UseProviders(
		github.New(os.Getenv("GITHUB_KEY"), os.Getenv("GITHUB_SECRET"), app.OauthCallbackURL),
	)
	for _, provider := range goth.GetProviders() {
		// Append the provider's name to the slice
		providerNames = append(providerNames, provider.Name())
	}
}

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

	renderedHTML, err := rendererData.Render(yamlData.CurrentLang, a)
	if err != nil {
		return err
	}

	return c.HTML(200, renderedHTML)
}

// StartOAuth initiates OAuth process
func StartOAuth(c echo.Context, a *app.App) error {
	provider := c.Param("provider")
	if provider == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Missing provider")
	}

	// Get the current session from echo-contrib/session
	sess, _ := session.Get("session", c)

	// Set the provider in the session
	sess.Values["provider"] = provider
	sess.Save(c.Request(), c.Response())

	gothic.BeginAuthHandler(c.Response(), c.Request())
	return nil
}

// OAuthCallback handles OAuth callback from provider
func OAuthCallback(c echo.Context, a *app.App) error {
	_, err := gothic.CompleteUserAuth(c.Response(), c.Request())
	if err != nil {
		return c.JSON(http.StatusUnauthorized, "Authentication failed")
	}
	// TODO: Add your logic here, like creating a session, etc.
	return c.Redirect(http.StatusMovedPermanently, "/success")
}

// Logout handles user logout and clears session
func Logout(c echo.Context, a *app.App) error {
	gothic.Logout(c.Response(), c.Request())
	// TODO: Add your logout logic here, like clearing session, etc.
	return c.Redirect(http.StatusMovedPermanently, "/")
}
