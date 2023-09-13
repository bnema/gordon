package app

import (
	"io/fs"

	"github.com/bnema/gordon/internal/gotemplate"
	"github.com/bnema/gordon/internal/webui"
)

const (
	BuildVersion = "0.0.2"
	BuildDir     = "tmp"
	// Those configs should be read from a config file
	HttpPort  = 1323
	TopDomain = "example.com"
	SubDomain = "gordon"
	AdminPath = "/admin"
)

type App struct {
	TemplateFS   fs.FS
	PublicFS     fs.FS
	BuildVersion string
	BuildDir     string
	TopDomain    string
	SubDomain    string
	AdminPath    string
	HttpPort     int16
}

func NewApp() *App {
	return &App{
		TemplateFS:   gotemplate.TemplateFS,
		PublicFS:     webui.PublicFS,
		BuildVersion: BuildVersion,
		BuildDir:     BuildDir,
		TopDomain:    TopDomain,
		SubDomain:    SubDomain,
		AdminPath:    AdminPath,
		HttpPort:     HttpPort,
	}
}
