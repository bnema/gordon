package app

import (
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"

	"github.com/bnema/gordon/internal/templates"
	"github.com/bnema/gordon/internal/webui"
	"github.com/bnema/gordon/pkg/docker"
	_ "github.com/joho/godotenv/autoload"
	"gopkg.in/yaml.v3"
)

var (
	OauthCallbackURL string
	config           AppConfig
)

type App struct {
	TemplateFS      fs.FS
	PublicFS        fs.FS
	StringsYML      []byte // strings.yml contains the strings for the current language
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
	RunEnv       string
	BuildDir     string
	BuildVersion string
	StorageDir   string `yaml:"storageDir"`
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
	Network    string `yaml:"network"`
}

func LoadConfig() (AppConfig, error) {
	// Load env elements
	config.General.BuildVersion = os.Getenv("BUILD_VERSION")
	config.General.RunEnv = os.Getenv("RUN_ENV")
	config.General.BuildDir = os.Getenv("BUILD_DIR")

	// if RUN_ENV is not set, assume "prod" and config dir is the current dir
	if config.General.RunEnv == "" {
		config.General.RunEnv = "prod"
		config.General.BuildDir = "."
	}

	fmt.Printf("RUN_ENV: %s\n", config.General.RunEnv)
	fmt.Printf("BUILD_DIR: %s\n", config.General.BuildDir)

	// Load config file
	configFile := fmt.Sprintf("%s/config.yml", config.General.BuildDir)
	configData, err := os.ReadFile(configFile)
	if err != nil {
		return config, err
	}

	err = yaml.Unmarshal(configData, &config)
	if err != nil {
		return config, err
	}

	return config, nil
}

func NewApp() *App {
	// Initialize AppConfig
	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Open the strings.yml file containing the strings for the current language
	file, err := templates.TemplateFS.Open("locstrings.yml")
	if err != nil {
		log.Fatalf("Failed to open strings.yml: %v", err)
	}

	// Read the file content into a byte slice
	bytes, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("Failed to read strings.yml: %v", err)
	}

	file.Close()

	// Initialize App
	a := &App{
		TemplateFS: templates.TemplateFS,
		PublicFS:   webui.PublicFS,
		StringsYML: bytes,
		DBDir:      DBDir,
		DBFilename: DBFilename,
		Config:     config,
	}

	OauthCallbackURL = config.GenerateOauthCallbackURL()
	return a
}

// NewDockerConfig creates and returns a new Docker client configuration
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

// GenerateOauthCallbackURL generates the OAuth callback URL
func (config *AppConfig) GenerateOauthCallbackURL() string {
	var scheme, port string

	if config.General.RunEnv == "dev" {
		scheme = "http"
		port = fmt.Sprintf(":%d", config.Http.Port)
	} else { // Assuming "prod"
		scheme = "https"
		port = ""
	}

	domain := config.Http.TopDomain
	if config.Http.SubDomain != "" {
		domain = fmt.Sprintf("%s.%s", config.Http.SubDomain, config.Http.TopDomain)
	}

	return fmt.Sprintf("%s://%s%s%s/login/oauth/callback", scheme, domain, port, config.Admin.Path)
}
