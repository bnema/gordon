package handlers

import (
	"io/fs"
	"net/http"
)

// NoDirFileSys restricts directory listing
type NoDirFileSys struct {
	fs http.FileSystem
}

// NewNoDirFileSys creates a new NoDirFileSys
func NewNoDirFileSys(fs http.FileSystem) NoDirFileSys {
	return NoDirFileSys{fs: fs}
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

// StaticRoute serves static files from the embedded filesystem - REMOVED
