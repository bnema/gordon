package app

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

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
	Config     *Config
}
type Config struct {
	BuildDir   string
	LogDir     string
	TemplateFS fs.FS
	PublicFS   fs.FS
	ModelFS    fs.FS
}

func (c *Config) GetTemplateFS() fs.FS {
	return c.TemplateFS
}
func (c *Config) GetPublicFS() fs.FS {
	return c.PublicFS
}

func (c *Config) GetModelFS() fs.FS {
	return c.ModelFS
}

func defineEnv(config *Config) {
	// Get the environment variable
	env := os.Getenv(EnvVarName)
	// If the environment variable is empty, consider it is dev -> (/tmp)
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
	config.BuildDir = buildDir
	config.LogDir = logDir
}

func NewApp(config *Config) (*App, error) {
	defineEnv(config)
	SetupLogging(config)
	app := &App{
		AppLogger:  utils.NewLogger().SetOutput(initializeLoggerOutput(config, "app.log")),
		HttpLogger: utils.NewLogger().SetOutput(initializeLoggerOutput(config, "http.log")),
		Config:     config,
	}
	return app, nil
}

func initializeLoggerOutput(cfg *Config, logFile string) io.Writer {
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
func SetupLogging(config *Config) {
	// Check if the log directory exists
	if _, err := os.Stat(config.LogDir); os.IsNotExist(err) {
		// If not, create it
		err := os.MkdirAll(config.LogDir, 0755)
		if err != nil {
			panic(fmt.Sprintf("Failed to create log directory at path: %s, error: %s", config.LogDir, err))
		}
	}
}

func GetBuildDir() string {
	env := os.Getenv(EnvVarName)
	buildDir := os.Getenv(BuildDirEnvVarName)
	if buildDir == "" {
		buildDir = DefaultBuildDir
	}
	if env == ProdEnvValue {
		return "."
	}
	return "./" + buildDir
}
