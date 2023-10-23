package app

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/pkg/docker"
	_ "github.com/joho/godotenv/autoload"
	"gopkg.in/yaml.v3"
)

var (
	OauthCallbackURL string
)

type App struct {
	TemplateFS      fs.FS
	PublicFS        fs.FS
	LocYML          []byte // strings.yml contains the strings for the current language
	DBDir           string
	DBFilename      string
	DBPath          string
	InitialChecksum string
	Config          Config
	DB              *sql.DB
	DBTables        DBTables
}
type Config struct {
	General         GeneralConfig         `yaml:"General"`
	Http            HttpConfig            `yaml:"Http"`
	Admin           AdminConfig           `yaml:"Admin"`
	ContainerEngine ContainerEngineConfig `yaml:"ContainerEngine"`
}

type GeneralConfig struct {
	RunEnv       string // come from env
	BuildDir     string // come from env
	BuildVersion string // come from env
	StorageDir   string `yaml:"storageDir"` // = buildir/storage
	GordonToken  string `yaml:"gordonToken"`
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

type DBTables struct {
	User     db.User     `sql:"user"`
	Account  db.Account  `sql:"account"`
	Sessions db.Sessions `sql:"sessions"`
	Provider db.Provider `sql:"provider"`
}

func LoadConfig(config *Config) (*Config, error) {
	// Load env elements
	config.General.BuildVersion = os.Getenv("BUILD_VERSION")
	config.General.RunEnv = os.Getenv("RUN_ENV")
	config.General.BuildDir = os.Getenv("BUILD_DIR")

	// if RUN_ENV is not set, assume "prod" and config dir is the current dir
	if config.General.RunEnv == "" {
		config.General.RunEnv = "prod"
		config.General.BuildDir = "."
	}
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

// NewDockerConfig creates and returns a new Docker client configuration
func (config *Config) NewDockerConfig() *docker.Config {
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
func (config *Config) GenerateOauthCallbackURL() string {
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

func (config *Config) GenerateAPIURL() string {
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

	return fmt.Sprintf("%s://%s%s/api", scheme, domain, port)
}

// UpdateConfig updates the config file
func (config *Config) UpdateConfig() error {
	// Marshal the config struct into YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	// Write the data to the config file (located in the build directory)
	err = os.WriteFile(fmt.Sprintf("%s/config.yml", config.General.BuildDir), data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write to config file: %v", err)
	}

	return nil
}

func (config *Config) GetToken() (string, error) {
	token := config.General.GordonToken
	if token == "" {
		return "", fmt.Errorf("no token found in config.yml")
	}

	return token, nil

}
