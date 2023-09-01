package utils

import (
	"bytes"
	"errors"
	"html/template"
)

type Renderer struct {
	Template   *template.Template
	ParseError error
}

// Render executes the template with the specified data as the dot object
// and returns the result as plain string.
func (r *Renderer) Render(data any) (string, error) {
	if r.ParseError != nil {
		return "", r.ParseError
	}

	if r.Template == nil {
		return "", errors.New("invalid or nil template")
	}

	buf := new(bytes.Buffer)

	if err := r.Template.ExecuteTemplate(buf, "index", data); err {
		return "", err
	}

	return buf.String(), nil
}
