package handler

import (
	"io/fs"
	"net/http"
	"os"

	"github.com/bnema/gordon/internal/server"
	"github.com/labstack/echo/v4"
)

// NoDirFileSys restricts directory listing
type NoDirFileSys struct {
	fs http.FileSystem
}

func (nfs NoDirFileSys) Open(name string) (http.File, error) {
	f, err := nfs.fs.Open(name)
	if err != nil {
		return nil, err
	}

	return NoDirFile{f}, nil
}

// NoDirFile restricts directory listing
type NoDirFile struct {
	http.File
}

func (f NoDirFile) Readdir(count int) ([]fs.FileInfo, error) {
	// Disable directory listing
	return nil, nil
}

// StaticRoute serves static files from the embedded filesystem
func StaticRoute(c echo.Context, a *server.App) error {
	// Set the cache-control header if we are not in dev mode
	if os.Getenv("RUN_ENV") != "dev" {
		c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		c.Response().Header().Set("Cache-Control", "no-cache")
	}

	// Convert fs.FS to http.FileSystem
	httpFS := http.FS(a.PublicFS)

	// Use custom file system wrapper to disable directory listing
	noDirFS := NoDirFileSys{httpFS}

	// Serve the file from the custom filesystem
	publicFS := http.FileServer(noDirFS)
	publicFS.ServeHTTP(c.Response(), c.Request())
	return nil
}
