package app

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"

	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/bnema/gordon/pkg/utils/parser"
)

var (
	OauthCallbackURL string
	appEnv           string
)

func init() {
	appEnv = os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "prod" // Default to "prod" if APP_ENV is not set
	}
}

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
	RunEnv           string `yaml:"runEnv"`
	BuildVersion     string `yaml:"buildVersion"`
	HttpPort         int    `yaml:"httpPort"`
	TopDomain        string `yaml:"topDomain"`
	SubDomain        string `yaml:"subDomain"`
	AdminPath        string `yaml:"adminPath"`
	DockerSock       string `yaml:"dockerSock"`
	PodmanEnable     bool   `yaml:"podmanEnable"`
	PodmanSock       string `yaml:"podmanSock"`
	OauthCallbackURL string `yaml:"oauthCallbackURL"`
	BuildDir         string `yaml:"buildDir"`
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
	buildDir, configFile := getEnvPaths()
	err := parser.OpenYamlFile(os.DirFS(buildDir), configFile, &config, buildDir) // Replace `nil` with your actual filesystem
	return config, err
}

// NewDockerConfig creates and returns a new Docker client configuration based on AppConfig.
func (config *AppConfig) NewDockerConfig() *docker.Config {
	return &docker.Config{
		DockerSock:   config.DockerSock,
		PodmanEnable: config.PodmanEnable,
		PodmanSock:   config.PodmanSock,
	}
}

func GenerateOauthCallbackURL(config AppConfig) string {
	var scheme, port string

	if config.RunEnv == "dev" {
		scheme = "http"
		port = fmt.Sprintf(":%d", config.HttpPort)
	} else { // Assuming "prod"
		scheme = "https"
		port = "" // Assuming that HTTPS will run on the default port 443
	}

	domain := config.TopDomain
	if config.SubDomain != "" {
		domain = fmt.Sprintf("%s.%s", config.SubDomain, config.TopDomain)
	}

	return fmt.Sprintf("%s://%s%s%s/login/oauth/callback", scheme, domain, port, config.AdminPath)
}

func getEnvPaths() (string, string) {
	if appEnv == "dev" {
		return "tmp/", "config.yml"
	}
	return "./", "config.yml"
}
