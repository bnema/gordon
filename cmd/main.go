package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

// getRenderer returns a new renderer for the given template
func getRenderer(filename string, fs fs.FS) (*utils.Renderer, error) {
	tmpl, err := template.New(filename).ParseFS(fs, filename)
	if err != nil {
		return nil, err
	}
	return &utils.Renderer{
		Template: tmpl,
	}, nil
}

// bindStaticAdminUI serves the static admin UI
func bindStaticAdminUI(e *echo.Echo) error {
	// Load static website data from YAML
	staticData, err := utils.LoadDataFromYAML()
	if err != nil {
		return err
	}

	// Main handler for /admin
	e.GET("/admin/:lang", func(c echo.Context) error {
		lang := c.Param("lang")

		switch lang {
		case "fr":
			staticData.CurrentLang = staticData.FR
		default: // default to English if no match
			staticData.CurrentLang = staticData.EN
		}

		renderer, err := getRenderer("index.html", ui.PublicFS)
		if err != nil {
			return err
		}

		html, err := renderer.Render(staticData)
		if err != nil {
			return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to render template: %v", err))
		}

		return c.HTML(http.StatusOK, html)
	})

	e.GET("/htmx", func(c echo.Context) error {
		fragment := c.Request().Header.Get("X-Fragment")

		if fragment == "hello" {
			// Fetch and return the "hello" fragment
			content, err := utils.GetHTMLFragmentByID("hello")
			fmt.Println("X-Fragment:", c.Request().Header.Get("X-Fragment"))
			if err != nil {
				return c.String(http.StatusInternalServerError, err.Error())
			}
			return c.HTML(http.StatusOK, content)
		}

		// Handle other fragments or default behavior
		return c.String(http.StatusBadRequest, "Invalid fragment")
	})

	// !!! IMPORTANT !!! This must be the last route (except for the 404 route)
	e.GET("/*", func(c echo.Context) error {
		// Set cache headers
		c.Response().Header().Set("Cache-Control", "public, max-age=86400")

		// Proceed with serving the file
		return echo.StaticDirectoryHandler(ui.PublicFS, false)(c)
	})

	// 404 handler
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if he, ok := err.(*echo.HTTPError); ok {
			if he.Code == http.StatusNotFound {
				// 404.html
				renderer, err := getRenderer("404.html", ui.PublicFS)
				if err != nil {
					c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to render template: %v", err))
				}

				data := map[string]interface{}{
					"website": map[string]interface{}{
						"title": "Page Not Found",
					},
				}

				html, err := renderer.Render(data)
				if err != nil {
					c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to render template: %v", err))
				}

				c.HTML(http.StatusNotFound, html)
				return
			}
		}

		e.DefaultHTTPErrorHandler(err, c)
	}

	return nil

}

func main() {
	err := utils.CreateLogsDir()
	if err != nil {
		panic(err)
	}

	// Setup App Logger
	appLogFile, err := os.OpenFile("logs/app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	defer appLogFile.Close()

	appConsoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	appMulti := zerolog.MultiLevelWriter(appLogFile, appConsoleWriter)
	appLogger := zerolog.New(appMulti).With().Timestamp().Str("type", "app").Logger()

	// Setup Echo Logger
	httpLogFile, err := os.OpenFile("logs/http.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	defer httpLogFile.Close()

	httpConsoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	httpMulti := zerolog.MultiLevelWriter(httpLogFile, httpConsoleWriter)
	echoLogger := zerolog.New(httpMulti).With().Timestamp().Logger()

	e := echo.New()

	// Use zerolog for Echo's requests
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:    true,
		LogStatus: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			echoLogger.Info().
				Str("type", "http").
				Str("remote_ip", c.RealIP()).
				Str("method", c.Request().Method).
				Str("URI", v.URI).
				Int("status", v.Status).
				Msg("request")
			return nil
		},
	}))
	bindStaticAdminUI(e)

	// Will crash in case of error
	if err := bindStaticAdminUI(e); err != nil {
		e.Logger.Fatal(err)
	}

	if err := e.Start(":1323"); err != nil {
		appLogger.Error().Err(err).Msg("Failed to start the server")
	}

}
