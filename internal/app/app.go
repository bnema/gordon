package app

import (
	"embed"
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
	"gogs.bnema.dev/gordon-echo/config"
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
}
type Config struct {
	BuildDir   string
	LogDir     string
	TemplateFS embed.FS
	PublicFS   embed.FS
	ModelFS    embed.FS
}

func (c *Config) GetTemplateFS() embed.FS {
	return c.TemplateFS
}

func (c *Config) GetPublicFS() embed.FS {
	return c.PublicFS
}

func (c *Config) GetModelFS() embed.FS {
	return c.ModelFS
}

var _ config.Provider = &Config{}

func loadConfig(templateFS, publicFS, modelFS embed.FS) Config {
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
		BuildDir:   buildDir,
		LogDir:     logDir,
		TemplateFS: templateFS,
		PublicFS:   publicFS,
		ModelFS:    modelFS,
	}
}
func NewApp(config *Config) (*App, error) {
	SetupLogging(config)
	app := &App{
		AppLogger:  utils.NewLogger().SetOutput(initializeLoggerOutput(config, "app.log")),
		HttpLogger: utils.NewLogger().SetOutput(initializeLoggerOutput(config, "http.log")),
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

func SetupLogging(cfg *Config) {
	err := utils.CreateLogsDir(cfg.LogDir)
	if err != nil {
		panic(err)
	}
}
