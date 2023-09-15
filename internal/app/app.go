package app

import (
	"database/sql"
	"io/fs"

	"github.com/bnema/gordon/internal/gotemplate"
	"github.com/bnema/gordon/internal/webui"
)

const (
	BuildVersion = "0.0.2"
	BuildDir     = "tmp"
	// Those configs should be read from a config file
	HttpPort   = 1323
	TopDomain  = "example.com"
	SubDomain  = "gordon"
	AdminPath  = "/admin"
	DockerSock = "/var/run/docker.sock"
	// If enabled replace all docker commands by podman
	PodmanEnable     = false
	PodmanSock       = "/run/user/1000/podman/podman.sock"
	OauthCallbackURL = "http://localhost:1323/oauth/callback"
)

type App struct {
	TemplateFS       fs.FS
	PublicFS         fs.FS
	BuildVersion     string
	BuildDir         string
	DBDir            string
	DBFilename       string
	DBPath           string
	InitialChecksum  string
	DB               *sql.DB
	TopDomain        string
	SubDomain        string
	AdminPath        string
	DockerSock       string
	PodmanEnable     bool
	PodmanSock       string
	OauthCallbackURL string
	HttpPort         int16
}

func NewApp() *App {
	return &App{
		TemplateFS:       gotemplate.TemplateFS,
		PublicFS:         webui.PublicFS,
		BuildVersion:     BuildVersion,
		BuildDir:         BuildDir,
		DBDir:            DBDir,
		DBFilename:       DBFilename,
		TopDomain:        TopDomain,
		SubDomain:        SubDomain,
		AdminPath:        AdminPath,
		OauthCallbackURL: OauthCallbackURL,
		HttpPort:         HttpPort,
	}
}

func NewDockerClient() *App {
	return &App{
		DockerSock:   DockerSock,
		PodmanEnable: PodmanEnable,
		PodmanSock:   PodmanSock,
	}
}
