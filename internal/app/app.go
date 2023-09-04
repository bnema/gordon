package app

import (
	"html/template"
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

var cfg = loadConfig()

const (
	DefaultBuildDir    = "tmp"
	DefaultLogDir      = "logs"
	EnvVarName         = "ENV"
	ProdEnvValue       = "prod"
	BuildDirEnvVarName = "BUILD_DIR"
	LogDirEnvVarName   = "LOG_DIR"
)

type App struct {
	AppLogger  *utils.Logger
	HttpLogger *utils.Logger
	Template   *template.Template
}
type Config struct {
	BuildDir string
	LogDir   string
}

func loadConfig() Config {
	// Get the environment variable
	env := os.Getenv(EnvVarName)
	// If the environment variable is empty, considere it is dev -> (/tmp)
	buildDir := os.Getenv(BuildDirEnvVarName)
	if buildDir == "" {
		buildDir = DefaultBuildDir
	}
	logDir := os.Getenv(LogDirEnvVarName)
	if logDir == "" {
		logDir = DefaultLogDir
	}

	switch env {
	case ProdEnvValue:
		// If it is prod we want everything at the same level of the binary
		buildDir = "."
		logDir = "./" + logDir
	default:
		// If it is dev we want everything in /tmp
		buildDir = "./" + buildDir
		logDir = filepath.Join(buildDir, logDir)
	}

	return Config{
		BuildDir: buildDir,
		LogDir:   logDir,
	}
}
func NewApp() (*App, error) {
	SetupLogging()
	app := &App{}

	// Initialize the general application logger
	app.AppLogger = utils.NewLogger().SetOutput(initializeLoggerOutput("app.log"))
	// Initialize the HTTP logger
	app.HttpLogger = utils.NewLogger().SetOutput(initializeLoggerOutput("http.log"))

	return app, nil
}

func initializeLoggerOutput(logFile string) io.Writer {
	logPath := filepath.Join(cfg.LogDir, logFile)

	dir := filepath.Dir(logPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if mkdirErr := os.MkdirAll(dir, 0755); mkdirErr != nil {
			panic(mkdirErr)
		}
	}

	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
	return zerolog.MultiLevelWriter(file, consoleWriter)
}

func SetupLogging() {
	err := utils.CreateLogsDir(cfg.LogDir)
	if err != nil {
		panic(err)
	}
}
