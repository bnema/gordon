package internal

import (
	"os"

	"github.com/rs/zerolog"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

// Define the main app struct
type App struct {
	// Define the app logger
	Logger *utils.Logger
	// Define the app config
}

func InitializeAppLogger() zerolog.Logger {
	appLogFile, err := os.OpenFile("logs/app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	appConsoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	appMulti := zerolog.MultiLevelWriter(appLogFile, appConsoleWriter)
	return zerolog.New(appMulti).With().Timestamp().Str("type", "app").Logger()
}

func InitializeHTTPLogger() zerolog.Logger {
	httpLogFile, err := os.OpenFile("logs/http.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	httpConsoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	httpMulti := zerolog.MultiLevelWriter(httpLogFile, httpConsoleWriter)
	return zerolog.New(httpMulti).With().Timestamp().Logger()
}

func SetupLogging() {
	err := utils.CreateLogsDir()
	if err != nil {
		panic(err)
	}
}
