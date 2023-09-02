package app

import (
	"html/template"
	"os"

	"github.com/rs/zerolog"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

// Define the main app struct
type App struct {
	// Define the app logger
	APPLogger *utils.Logger
	// Define the HTTP logger
	HTTPLogger *utils.Logger
	// Define the app config
	Template *template.Template
	// Define the app renderer
	Renderer *utils.Renderer
	// Define the app router
}

func NewApp() (*App, error) {
	app := &App{}

	// Initialize the general application logger
	AppLogger, err := initializeAppLogger()
	if err != nil {
		return nil, err
	}
	app.APPLogger = &utils.Logger{Logger: AppLogger}

	// Initialize the HTTP logger
	HttpLogger, err := initializeHTTPLogger()
	if err != nil {
		return nil, err
	}
	app.HTTPLogger = &utils.Logger{Logger: HttpLogger}

	return app, nil
}
func initializeAppLogger() (zerolog.Logger, error) {
	appLogFile, err := os.OpenFile("logs/app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return zerolog.Logger{}, err
	}

	appConsoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	appMulti := zerolog.MultiLevelWriter(appLogFile, appConsoleWriter)
	return zerolog.New(appMulti).With().Timestamp().Str("type", "app").Logger(), nil
}

func initializeHTTPLogger() (zerolog.Logger, error) {
	httpLogFile, err := os.OpenFile("logs/http.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return zerolog.Logger{}, err
	}

	httpConsoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	httpMulti := zerolog.MultiLevelWriter(httpLogFile, httpConsoleWriter)
	return zerolog.New(httpMulti).With().Timestamp().Logger(), nil
}

func SetupLogging() {
	err := utils.CreateLogsDir()
	if err != nil {
		panic(err)
	}
}
