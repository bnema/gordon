package templating

import (
	"bytes"
	"html/template"
	"io/fs"

	"gogs.bnema.dev/gordon-echo/internal/app"
)

type Renderer struct {
	Template   *template.Template
	ParseError error
}

// Render function renders the template with the given data
func (r *Renderer) Render(data interface{}, a *app.App) (string, error) {
	if r.ParseError != nil {
		a.AppLogger.Error().Err(r.ParseError).Msg("Failed to parse template")
		return "", r.ParseError
	}
	if r.Template == nil {
		a.AppLogger.Error().Msg("Template is nil")
		return "", r.ParseError
	}

	buf := new(bytes.Buffer)
	err := r.Template.Execute(buf, data)
	if err != nil {
		a.AppLogger.Error().Err(err).Msg("Failed to execute template")
		return "", err
	}

	return buf.String(), nil
}

// GetRenderer function returns a new Renderer instance
func GetHTMLRenderer(filename string, fs fs.FS, a *app.App) (*Renderer, error) {
	// Check if the file exists in the provided fs.FS using fs.Open
	file, err := fs.Open(filename)
	if err != nil {
		a.AppLogger.Error().Err(err).Msg("Failed to open file")
		return nil, err
	}
	file.Close() // Close the file after checking
	tmpl, err := template.New(filename).ParseFS(fs, filename)
	if err != nil {
		a.AppLogger.Error().Err(err).Msg("Failed to parse template")
		return nil, err
	}
	return &Renderer{
		Template: tmpl,
	}, nil
}
