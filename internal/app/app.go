package app

import (
	"html/template"
	"io"
	"os"

	"github.com/rs/zerolog"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

type App struct {
	APPLogger  *utils.Logger
	HTTPLogger *utils.Logger
	Template   *template.Template
	Renderer   *utils.Renderer
}

func NewApp() (*App, error) {
	app := &App{}

	// Initialize the general application logger
	app.APPLogger = utils.NewLogger().SetOutput(initializeAppLoggerOutput())
	// Initialize the HTTP logger
	app.HTTPLogger = utils.NewLogger().SetOutput(initializeHTTPLoggerOutput())

	return app, nil
}

func initializeAppLoggerOutput() io.Writer {
	appLogFile, err := os.OpenFile("logs/app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	appConsoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	return zerolog.MultiLevelWriter(appLogFile, appConsoleWriter)
}

func initializeHTTPLoggerOutput() io.Writer {
	httpLogFile, err := os.OpenFile("logs/http.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	httpConsoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	return zerolog.MultiLevelWriter(httpLogFile, httpConsoleWriter)
}

func SetupLogging() {
	err := utils.CreateLogsDir()
	if err != nil {
		panic(err)
	}
}
