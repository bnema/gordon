package utils

import (
	"bytes"
	"errors"
	"html/template"
	"io/fs"
)

type Renderer struct {
	Template   *template.Template
	ParseError error
}

// Render function renders the template with the given data
func (r *Renderer) Render(data interface{}) (string, error) {
	if r.ParseError != nil {
		return "", r.ParseError
	}

	if r.Template == nil {
		return "", errors.New("invalid or nil template")
	}

	buf := new(bytes.Buffer)
	err := r.Template.Execute(buf, data)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

// GetRenderer function returns a new Renderer instance
func GetRenderer(filename string, fs fs.FS) (*Renderer, error) {
	tmpl, err := template.New(filename).ParseFS(fs, filename)
	if err != nil {
		return nil, err
	}
	return &Renderer{
		Template: tmpl,
	}, nil
}
