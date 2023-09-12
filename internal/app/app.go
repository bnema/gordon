package app

import (
	"fmt"
	"io/fs"

	"github.com/bnema/gordon/internal/gotemplate"
	"github.com/bnema/gordon/internal/webui"
)

const (
	BuildVersion = "0.0.2"
	BuildDir     = "tmp"
)

type App struct {
	TemplateFS   fs.FS
	PublicFS     fs.FS
	BuildVersion string
	BuildDir     string
}

func (*App) GetTemplateFS() fs.FS {
	return gotemplate.TemplateFS
}

func (*App) GetPublicFS() fs.FS {
	return webui.PublicFS
}

func (*App) GetBuildVersion() string {
	return BuildVersion
}

func (*App) GetBuildDir() string {
	return BuildDir
}

func NewApp() *App {
	return &App{
		TemplateFS:   gotemplate.TemplateFS,
		PublicFS:     webui.PublicFS,
		BuildVersion: BuildVersion,
		BuildDir:     BuildDir,
	}
}

func (a *App) Start() {
	fmt.Println("Starting app")

	// LS in the template directory
	entries, err := fs.ReadDir(a.TemplateFS, ".") // List the root directory
	if err != nil {
		fmt.Println("Error reading directory:", err)
		return
	}

	for _, entry := range entries {
		fmt.Println(entry.Name())
	}
}
