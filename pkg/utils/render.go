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
	Logger     *Logger
}

// Render function renders the template with the given data
func (r *Renderer) Render(data interface{}) (string, error) {
	if r.ParseError != nil {
		r.Logger.Error().Err(r.ParseError).Msg("Parse error")

		return "", r.ParseError
	}

	if r.Template == nil {
		err := errors.New("invalid or nil template")
		r.Logger.Error().Err(r.ParseError).Msg("Parse error")

		return "", err
	}

	buf := new(bytes.Buffer)
	err := r.Template.Execute(buf, data)
	if err != nil {
		r.Logger.Error().Err(err).Msg("Render error")
		return "", err
	}

	return buf.String(), nil
}

// GetRenderer function returns a new Renderer instance
func GetRenderer(filename string, fs fs.FS, logger *Logger) (*Renderer, error) {
	tmpl, err := template.New(filename).ParseFS(fs, filename)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to parse template")
		return nil, err
	}
	return &Renderer{
		Template: tmpl,
		Logger:   logger,
	}, nil
}
