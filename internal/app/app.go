package app

import (
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/bnema/gordon/internal/gotemplate"
	"github.com/bnema/gordon/internal/webui"
	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/bnema/gordon/pkg/utils/parser"
)

var (
	OauthCallbackURL string
	appEnv           string
	BuildVersion     = "0.0.2"
)

type App struct {
	TemplateFS      fs.FS
	PublicFS        fs.FS
	DBDir           string
	DBFilename      string
	DBPath          string
	InitialChecksum string
	DB              *sql.DB
	Config          AppConfig
}
type AppConfig struct {
	General         GeneralConfig         `yaml:"General"`
	Http            HttpConfig            `yaml:"Http"`
	Admin           AdminConfig           `yaml:"Admin"`
	ContainerEngine ContainerEngineConfig `yaml:"ContainerEngine"`
}

type GeneralConfig struct {
	RunEnv       string `yaml:"runEnv"`
	StorageDir   string `yaml:"storageDir"`
	BuildVersion string
}

type HttpConfig struct {
	Port      int    `yaml:"port"`
	TopDomain string `yaml:"topDomain"`
	SubDomain string `yaml:"subDomain"`
}

type AdminConfig struct {
	Path string `yaml:"path"`
}

type ContainerEngineConfig struct {
	Sock       string `yaml:"dockersock"`
	PodmanSock string `yaml:"podmansock"`
	Podman     bool   `yaml:"podman"`
}

func InitializeEnvironment() {
	config, err := LoadConfig()
	if err != nil {
		fmt.Errorf("Error initializing environment: %s", err)
	}
	OauthCallbackURL = GenerateOauthCallbackURL(config)
}

func LoadConfig() (AppConfig, error) {
	var config AppConfig
	configDir, configFile := getConfigFile()
	fsys := os.DirFS(configDir)
	err := parser.OpenYamlFile(fsys, configFile, &config)
	if err != nil {
		return config, err
	}

	// Set the BuildVersion from the global BuildVersion
	config.General.BuildVersion = BuildVersion

	return config, nil
}

func NewApp() *App {
	// Initialize AppConfig
	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize App
	a := &App{
		TemplateFS: gotemplate.TemplateFS,
		PublicFS:   webui.PublicFS,
		DBDir:      DBDir,
		DBFilename: DBFilename,
		Config:     config,
	}

	// If you have any other initializations like generating OAuth URL, do them here
	OauthCallbackURL = GenerateOauthCallbackURL(config)
	return a
}

// NewDockerConfig creates and returns a new Docker client configuration based on AppConfig.
func (config *AppConfig) NewDockerConfig() *docker.Config {
	if config.ContainerEngine.Podman {
		return &docker.Config{
			Sock:         config.ContainerEngine.PodmanSock,
			PodmanEnable: true,
		}
	}
	return &docker.Config{
		Sock:         config.ContainerEngine.Sock,
		PodmanEnable: false,
	}
}

func GenerateOauthCallbackURL(config AppConfig) string {
	var scheme, port string

	if config.General.RunEnv == "dev" {
		scheme = "http"
		port = fmt.Sprintf(":%d", config.Http.Port)
	} else { // Assuming "prod"
		scheme = "https"
		port = "" // Assuming that HTTPS will run on the default port 443
	}

	domain := config.Http.TopDomain
	if config.Http.SubDomain != "" {
		domain = fmt.Sprintf("%s.%s", config.Http.SubDomain, config.Http.TopDomain)
	}

	return fmt.Sprintf("%s://%s%s%s/login/oauth/callback", scheme, domain, port, config.Admin.Path)
}

func getConfigFile() (string, string) {
	return "tmp/", "config.yml" // assuming the file is in the current directory
}
