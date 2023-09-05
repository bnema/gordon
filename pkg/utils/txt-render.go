package utils

import (
	"bytes"
	"errors"
	"io/fs"
	"text/template"
)

type TXTRenderer struct {
	Models     *template.Template
	Format     string
	ParseError error
	Logger     *Logger
}

// Render function renders the template with the given data
func (r *TXTRenderer) TXTRender(data interface{}) (string, error) {
	if r.ParseError != nil {
		r.Logger.Error().Err(r.ParseError).Msg("Parse error")
		return "", r.ParseError
	}

	if r.Models == nil {
		err := errors.New("invalid or nil template")
		r.Logger.Error().Err(r.ParseError).Msg("Parse error")

		return "", err
	}

	buf := new(bytes.Buffer)
	err := r.Models.Execute(buf, data)
	if err != nil {
		r.Logger.Error().Err(err).Msg("Render error")
		return "", err
	}

	return buf.String(), nil
}

// GetTXTRenderer function returns a new Renderer instance
func GetTXTRenderer(filename string, fs fs.FS, logger *Logger) (*TXTRenderer, error) {
	// Check if the file exists in the provided fs.FS using fs.Open
	file, err := fs.Open(filename)
	if err != nil {
		logger.Error().Err(err).Msg("Template or model '%s' not found" + filename)
		return nil, err
	}
	file.Close() // Close the file after checking

	mdls, err := template.New(filename).ParseFS(fs, filename)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to parse template")
		return nil, err
	}
	return &TXTRenderer{
		Models: mdls,
		Logger: logger,
	}, nil
}
